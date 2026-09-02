// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/version"
)

const (
	// daemonKillGrace is how long a hung child gets between SIGTERM and SIGKILL.
	daemonKillGrace = 30 * time.Second
	// daemonReapSlack is how much longer, AFTER that SIGKILL, the daemon still waits for the
	// child to be REAPED before declaring it unreapable and abandoning it. A process SIGKILL
	// can actually kill is reaped within microseconds of the signal, so this margin is only
	// ever spent on a child that is already lost: a task parked in TASK_UNINTERRUPTIBLE (D
	// state -- a dead NFS/CIFS mount, a wedged block device) never dequeues the signal and
	// never will be reaped. It is deliberately generous enough that a merely slow teardown on
	// a loaded host is still reaped normally rather than misjudged, and deliberately short
	// enough that abandoning during a shutdown (killGrace + this = 45s) stays inside a stock
	// host's DefaultTimeoutStopSec of 90s. The unit does NOT pin that value (buildDaemonUnit
	// leaves the kill directives at their defaults on purpose), so a host that lowered it
	// below 45s simply has systemd SIGKILL the daemon mid-teardown -- which is exactly what
	// happened on every host before this path existed. See superviseChild.
	daemonReapSlack = 15 * time.Second
	// daemonAliveInterlockWait caps how long the abandon path waits for aliveMu before it
	// reports anyway. An in-flight beat holds that lock for one ping (pingTimeout, 10s) plus a
	// small status-file write, so this is comfortably longer than any legitimate hold. It
	// exists because the ordering the lock buys -- the degrade /fail transmitting last -- is a
	// nicety, while runOnce returning is the invariant: no lock holder, however it got stuck,
	// may be able to wedge the scheduler again.
	daemonAliveInterlockWait = 15 * time.Second
	// daemonAbandonIOWait bounds the LOCAL filesystem writes the abandon path performs: the
	// abandoned-child marker, and the status-file record of the hang ping. Everything else on
	// that path is a network ping the Reporter already caps at pingTimeout. It exists because
	// the abandon path runs, by definition, on a host with wedged I/O -- a dead NFS/CIFS mount,
	// a hung block device. If BaseDir sits on that same filesystem, an unbounded
	// os.MkdirAll/os.WriteFile/os.Rename blocks exactly like the wait4 that started all this,
	// and the wedge is straight back one layer out, after having already cost the two
	// monitoring signals it was ordered ahead of. Generous next to a healthy write
	// (microseconds) and short next to the pings around it.
	daemonAbandonIOWait = 5 * time.Second
	// daemonProcProbeWait bounds the /proc read that identifies an abandoned child (see
	// abandonedChildGone). /proc is a pseudo-filesystem and never lives on the dead mount, but
	// the file it serves is rendered from the very task that is parked in the kernel, and
	// /proc/<pid>/cmdline in particular is read under that task's mmap lock -- which a page
	// fault on the wedged mount can be holding. That probe runs on the SCHEDULER's goroutine
	// (clearAbandonMarkerOnCompletedRun) as well as on the beat, so an unbounded one would put
	// an unbounded wait back on the path this whole change exists to bound. Expiry is not an
	// error: it means the task is demonstrably still in the kernel, which is the same answer
	// "still there" the probe would have given.
	daemonProcProbeWait = 2 * time.Second
	// daemonLoopDrainWait caps the join on the background loops when the daemon is unwinding
	// after an abandon. They return immediately on their context being cancelled, EXCEPT a
	// beat already inside buildReporter, which can be parked on the cross-process relay-secret
	// flock (identity.LockNotifySecret takes LOCK_EX with no deadline and no context). The
	// abandon pings have already been sent by then and the process is about to die, so an
	// undrainable loop must not be able to hold the exit -- and with it the systemd restart.
	daemonLoopDrainWait = 15 * time.Second
	// logTailBytes bounds the log excerpt POSTed with a non-success outcome.
	logTailBytes = 8 * 1024
	// defaultMaxRunDuration is the watchdog fallback when MAX_RUN_DURATION is unset. A
	// config backup (config files, not VM data) completes well under this; raise
	// MAX_RUN_DURATION for an unusually large archive over a slow cloud upload.
	defaultMaxRunDuration = 1 * time.Hour
	// defaultHeartbeatInterval is the alive-ping fallback.
	defaultHeartbeatInterval = 5 * time.Minute
	// defaultUpdateInterval is the updates-check fallback cadence.
	defaultUpdateInterval = 5 * time.Minute
	// manualOutcomeStaleWindow bounds how old a handed-off standalone-backup outcome may be
	// before the daemon refuses to ping it. A wake that arrives long after the run (e.g. the
	// daemon was down when the standalone backup finished and only started later) must NOT flip
	// the backup-outcome check for a stale run; 15 minutes is comfortably longer than any real
	// handoff-to-signal latency while still discarding a genuinely stale outcome.
	manualOutcomeStaleWindow = 15 * time.Minute
)

// backupReporter is the healthchecks surface the daemon uses; *health.Reporter
// implements it. An interface so the scheduler/watchdog is testable with a fake.
type backupReporter interface {
	Heartbeat(ctx context.Context) error
	// AliveDegraded drives the service-alive check DOWN. Sent once, from the abandon
	// path, so the monitor never shows a green service behind dead backups.
	AliveDegraded(ctx context.Context, reason string) error
	RunStarted(ctx context.Context, rid string) error
	RunFinished(ctx context.Context, rid string, exitCode int, logTail string) error
	RunHang(ctx context.Context, rid string, timeout time.Duration, logTail string) error
	ReportUpdate(ctx context.Context, available bool) error
	Ping(ctx context.Context, name, suffix, rid, body, label string) error
	HasAliveURL() bool
	HasBackupURL() bool
	HasUpdatesURL() bool
	HasCheck(name string) bool
}

// dispatchDaemonMode runs the resident daemon when --daemon is set. It blocks
// until the run context is cancelled (SIGTERM from systemd), then returns.
func dispatchDaemonMode(rt *appRuntime) modeResult {
	if !rt.args.Daemon {
		return modeResult{exitCode: types.ExitSuccess.Int()}
	}
	return modeResult{exitCode: runDaemon(rt), handled: true}
}

type daemon struct {
	cfg        *config.Config
	logger     *logging.Logger
	execPath   string
	configPath string
	now        func() time.Time

	mu               sync.Mutex
	reporter         backupReporter
	fetchWarned      bool      // centralized fetch already warned once (throttle recurring WARN)
	updateWarned     bool      // an update is already known available (WARN once per transition)
	provisionRetryAt time.Time // next relay-secret self-heal attempt; guarded by mu
	// aliveMu orders the TRANSMISSIONS to the service-alive check: one beat's ping+record, or
	// the one abandon degrade, never both at once. A bare latch cannot do this. beat() reads
	// it, then spends up to a full pingTimeout inside r.Heartbeat, so a beat that entered
	// before the latch closed would still deliver its SUCCESS ping AFTER the degrade's /fail
	// and re-green the check remotely -- exactly the failure the latch was written to prevent.
	// The mutex makes the abandon wait for that in-flight beat instead, so the /fail is the
	// last word on the check.
	//
	// What it must NEVER cover is URL RESOLUTION. buildReporter can reach a cross-process
	// flock (fetchCentralized -> maybeProvisionRelaySecret -> identity.LockNotifySecret ->
	// syscall.Flock LOCK_EX, which honours neither a deadline nor the context), and an
	// unbounded hold here would put an unbounded wait right back on the abandon path -- the
	// same wedge this whole change exists to remove, one layer down. So beat resolves the
	// reporter BEFORE taking the lock, and the only things under it are bounded: one ping
	// (the reporter caps it at pingTimeout) and one status-file write. abandonChild's own
	// acquisition is additionally deadline-capped (see lockAliveWithin), so no lock holder can
	// ever stop runOnce from returning.
	//
	// Lock order: aliveMu is outermost (code under it takes statusMu); nothing takes it while
	// holding mu or statusMu.
	aliveMu sync.Mutex
	// aliveSilenced latches when the daemon has driven the service-alive check DOWN on its way
	// OUT (abandonChild). Every later beat is then dropped WHOLE -- no ping, so nothing
	// re-greens the check between that /fail and the exit, and no local record, so a daemon
	// that is at that moment dying does not refresh its own liveness timestamp. Atomic rather
	// than aliveMu-guarded so the abandon can close it even if the interlock deadline expires.
	aliveSilenced atomic.Bool
	// aliveDegraded mirrors the on-disk abandon marker (health.AbandonRecord) read at startup:
	// a PREVIOUS process abandoned an unreapable child, and the orphan is still holding the
	// backup lock. This daemon is alive and must keep saying so LOCALLY (it records every beat
	// as usual, so the run-side panel and health.Diagnose stay honest), but each beat pings
	// /fail instead of success, so the remote alive check stays DOWN for as long as backups
	// are dead. Without it the restarted daemon's immediate first beat would flip the check
	// back UP about ten seconds after the /fail, and fire a "recovered" alert over a host on
	// which no backup can run.
	//
	// It is cleared -- marker and all -- the moment anything proves backups are no longer dead:
	// a supervised run that reaches a real exit code (runOnce), a standalone run that hands one
	// off (processManualOutcome), or the orphan simply being gone from the host
	// (reviewAbandonDegrade, checked at startup and on every beat). It is never SET at all when
	// backups are administratively off, because then nothing could ever clear it. A degrade that
	// cannot be lifted is not a safety net, it is a false RED on the check that pages people --
	// the exact mirror of the false green it exists to remove.
	aliveDegraded atomic.Bool
	// abandonMarkerOnDisk records that a marker file may be sitting in the identity dir -- this
	// process either read one at startup or could not tell (an unreadable marker is still a
	// marker). It is what makes the removal in clearAbandonMarker unconditional on the degrade:
	// a process that hit ReadAbandon's error branch never degrades, and must still delete the
	// file once a run proves the wedge is over, or it lingers to resurrect the degrade the
	// moment it becomes readable again. False on the overwhelmingly common path, so a normal
	// run pays one atomic load.
	abandonMarkerOnDisk atomic.Bool
	// abandonNote explains that degrade in the ping body (the orphan's pid and run id). Set in
	// run() before any loop starts, and REPLACED once a supervised or standalone run has
	// completed over an orphan that is still on this host -- a state the degrade deliberately
	// survives (clearAbandonMarkerOnCompletedRun), and one in which the original wording ("no
	// backup has completed since") is a false statement transmitted on every beat for as long
	// as the degrade stands. The whole point of the degrade is that the monitor is told the
	// truth; a body that ages into a lie is the same defect in miniature.
	//
	// An atomic pointer because the replacement happens on the scheduler (runOnce) or the
	// SIGUSR1 waker (processManualOutcome) while beat reads it on the heartbeat goroutine. It
	// takes no lock, so it cannot interact with aliveMu/abandonMu ordering. nil means no
	// degrade was ever raised and nothing reads it.
	abandonNote atomic.Pointer[string]
	// abandonPID / abandonStart identify the orphan named by that marker: its pid and its
	// start time in clock ticks (0 when the marker carried neither, or carried a pid this
	// process is not going to act on). Both are set ONLY on the branch that actually raises
	// the degrade, and are read-only afterwards; they are what lets the degrade be
	// re-validated against the host instead of being believed forever. A marker this process
	// retires at startup leaves them at 0 on purpose: after a reboot that number belongs to
	// something else entirely, and a daemon that kept probing it would report, after every
	// successful backup, that it is holding DOWN a check that is in fact green.
	abandonPID   int
	abandonStart uint64
	// abandonMu serializes the two operations that decide what the SUCCESSOR daemon inherits:
	// writing the marker for a child this process just abandoned, and removing an inherited
	// one because the wedge is over. Without it the two race on the beat/scheduler boundary --
	// a clear that has already passed its guard, and is parked in the unlink, lands on the
	// marker abandonChild wrote moments later, and the next daemon comes up green over a live
	// orphan. Both sides are deadline-bounded underneath it (runWithin), so the hold is
	// bounded too and the abandon path can never be stalled on it.
	//
	// Lock order: innermost. Nothing is taken while it is held.
	abandonMu sync.Mutex
	// abandonMarkerWritten latches once THIS process has persisted a marker for a child of its
	// OWN. From that instant the file in the identity dir describes the orphan the successor
	// must inherit -- not the inherited one any clear path in this process is reasoning about
	// -- so no clear may remove it. A latch rather than a comparison because there is nothing
	// to compare: both markers live at the same path.
	abandonMarkerWritten atomic.Bool
	// abandonRec is the record behind that latch, published by persistAbandonMarker. It exists
	// for one reader: a marker REMOVAL that stayed parked in the kernel past its deadline, was
	// therefore no longer covered by abandonMu when it finally landed, and may have deleted the
	// successor's inheritance on its way out. That straggler puts this record back. An atomic
	// pointer rather than a mutex-guarded field so the straggler -- which runs on a goroutine
	// its own caller spawned while HOLDING abandonMu -- can never deadlock against it.
	abandonRec atomic.Pointer[health.AbandonRecord]
	// probeInFlight latches while an orphan-identity /proc read is still parked in the kernel
	// (see pidIsAbandonedChild). The read has a deadline but no cancellation -- nothing in
	// userspace can cancel a syscall the kernel is holding -- so a probe that expires leaves its
	// goroutine, and the open /proc file descriptor inside it, behind. reviewAbandonDegrade runs
	// that probe once per HEARTBEAT for the entire life of a degraded daemon, so without this
	// latch a host whose orphan blocks the read accumulates one stranded goroutine and one
	// stranded fd every beat, forever, on the one host an operator is actively investigating.
	// With it at most ONE read is ever outstanding: while it is set the answer is taken from the
	// latch itself ("still there", the same answer an expiry gives), and probing resumes by
	// itself the moment the parked read finally returns.
	probeInFlight atomic.Bool
	// newBackupCmd builds the child backup command; overridable in tests.
	newBackupCmd func(ctx context.Context) *exec.Cmd
	// killGraceOverride / reapWaitOverride replace daemonKillGrace and the reap deadline in
	// tests; zero means the production values. Fields rather than config knobs: a shorter
	// grace is never the right answer in production, but a test cannot spend a 45s wall clock
	// proving the abandon path exists. Set at construction and never written afterwards.
	killGraceOverride time.Duration
	reapWaitOverride  time.Duration
	// pidAliveOverride replaces the WHOLE orphan probe -- kill(2) liveness and the /proc
	// identity check behind it -- for an abandoned child's pid in tests; nil means the real
	// one. true means "the orphan is still there". A test can spawn a process that ignores
	// SIGTERM, but it cannot spawn one the kernel refuses to reap, so the pid an abandon
	// records there is alive only for as long as the stand-in child lives -- a wall-clock race
	// no assertion should depend on. Set at construction and never written afterwards.
	pidAliveOverride func(pid int) bool
	// bootUnixOverride replaces the /proc/stat boot-time read in tests; nil means the real
	// one. A test cannot reboot the host, so this is the only way to exercise the
	// boot-generation check in loadAbandonMarker. Set at construction, never written after.
	bootUnixOverride func() int64
	// aliveInterlockWaitOverride replaces daemonAliveInterlockWait in tests; zero means the
	// production value. Without it, a test that deliberately wedges the interlock spends the
	// whole production wait in wall clock.
	aliveInterlockWaitOverride time.Duration
	// removeDaemonFilesIO replaces the pid/info removal in removeDaemonFiles; nil means the
	// real one. It is the only way to stand in for a BaseDir whose unlink never returns, which
	// is what the deadline there exists for. Set at construction, never written after.
	removeDaemonFilesIO func()
	// procIdentityIO replaces the /proc read behind the orphan-identity probe; nil means the
	// real one. It stands in for a read that stays parked in the kernel -- the case
	// probeInFlight exists for, and one no test can produce against a real /proc file. Set at
	// construction, never written after.
	procIdentityIO func(pid int, start uint64) bool
	// clearAbandonMarkerIO replaces the marker unlink in clearAbandonMarker; nil means the real
	// one. Same purpose as removeDaemonFilesIO: only a seam can stand in for a removal that
	// stays parked in the kernel past its deadline and lands after the abandon path has written
	// the successor's marker. Set at construction, never written after.
	clearAbandonMarkerIO func() error

	// statusMu serializes writes to the shared healthcheck status file: the
	// heartbeat loop and runOnce record ping outcomes concurrently, and
	// health.RecordPing is a read-modify-write, so it needs its own lock. Kept
	// separate from mu (which guards reporter/fetchWarned across buildReporter) so
	// a status write never contends with reporter resolution.
	statusMu sync.Mutex
}

func runDaemon(rt *appRuntime) int {
	d := &daemon{
		cfg:        rt.cfg,
		logger:     rt.logger,
		execPath:   daemonSelfExecPath(),
		configPath: rt.args.ConfigPath,
		now:        time.Now,
	}
	// The status file is written only by this daemon; surface a corrupt-file
	// self-heal (health quarantines the unreadable bytes and resets) as a debug
	// line, since the health package is deliberately logger-free.
	health.SetCorruptStatusHook(func(quarantinedPath string) {
		logging.Debug("daemon: healthcheck status file was corrupt, quarantined to %s and reset", quarantinedPath)
	})
	logging.Info("ProxSave daemon starting (run-at=%s max-run=%s healthcheck=%v mode=%s)",
		d.cfg.SchedulerTime, d.maxRunDuration(), d.cfg.HealthcheckEnabled, d.cfg.HealthcheckMode)
	return d.run(rt.ctx)
}

func (d *daemon) run(ctx context.Context) int {
	// The trusted-path gate for the operator scripts, once, before any tick can start
	// one: a refused path is blanked here with a loud reason (validatePersonalScripts),
	// so the silent starters below never see it.
	validatePersonalScripts(d.cfg)

	// CRITICAL: install the SIGUSR1 handler BEFORE publishing the pidfile below. Go's DEFAULT action
	// for SIGUSR1 is to TERMINATE the process, and the pidfile is exactly what a standalone backup
	// run uses to discover us and send SIGUSR1 to hand off its outcome. If the pid became
	// discoverable first, a concurrent standalone handoff in that window could deliver SIGUSR1 while
	// Go still held the default-terminate disposition and KILL the just-started daemon. signal.Notify
	// replaces that disposition; only after it returns is it safe for the pid to become
	// discoverable/signallable. SIGUSR1 wakes the daemon to ping a standalone run's handed-off outcome
	// (processManualOutcome). Buffered(1) so a wake is not lost while we are mid-process.
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	// Revert SIGUSR1 to its default (terminate) disposition on shutdown -- but only AFTER the pidfile
	// has been removed. run() owns BOTH lifecycles so their order is guaranteed: this defer is
	// declared BEFORE the RemoveDaemonPID defer below, so by LIFO the pid is removed FIRST and
	// signal.Stop runs LAST. That keeps the startup invariant symmetric on shutdown -- the handler
	// stays installed for as long as the pid is discoverable, so a standalone handoff can never
	// deliver SIGUSR1 under the default-terminate action to the exiting daemon. A SIGUSR1 that
	// arrives after the waker has returned but before this Stop lands in the buffered(1) channel and
	// is harmlessly dropped (never terminate).
	defer signal.Stop(usr1)

	// Record our PID so a STANDALONE backup run can find us to hand off its outcome, and clear it
	// on shutdown. Best-effort: a pid-file hiccup must not stop the daemon. Published AFTER the
	// SIGUSR1 handler above so the pid is never signallable while the default-terminate action holds.
	if err := health.WriteDaemonPID(d.cfg.BaseDir, os.Getpid()); err != nil {
		logging.Debug("daemon: write pid file failed: %v", err)
	}
	// Alongside the bare pid (the SIGUSR1 handoff contract), record the daemon's IDENTITY -- the
	// binary it booted from, version/commit, start time -- in the companion .daemon_info.json, so a
	// later reader can display the running version and gate restart-verify freshness. Staleness ("is
	// the running binary behind the file on disk?") is answered separately and hash-free via
	// /proc/<pid>/exe (see daemon_state.go), so nothing is hashed here. Best-effort: a write hiccup is
	// only Debug-logged and must not fail startup.
	if err := health.WriteDaemonInfo(d.cfg.BaseDir, health.DaemonInfo{
		PID:      os.Getpid(),
		ExecPath: d.execPath,
		Version:  version.String(),
		Commit:   version.Commit,
		StartTS:  d.now().Unix(),
	}); err != nil {
		logging.Debug("daemon: write daemon info failed: %v", err)
	}
	defer d.removeDaemonFiles()

	// Inherit an abandon from a PREVIOUS process before anything can ping. The daemon exits
	// after abandoning an unreapable child and systemd restarts it ten seconds later, so
	// without this the very first beat of the new process -- heartbeatLoop beats once
	// immediately, before its ticker -- would send a SUCCESS ping, flip the alive check back
	// UP and fire a "recovered" alert, while the orphan still holds the backup lock and every
	// scheduled run exits ExitBackupSkipped without pinging anything. Read regardless of
	// HealthcheckEnabled so the state is carried (and cleanly cleared) even on a host that
	// turns healthchecks on later.
	d.loadAbandonMarker()

	if d.cfg.HealthcheckEnabled {
		if r := d.buildReporter(ctx); r != nil {
			d.setReporter(r)
		}
	}

	// loopCtx is how an ABANDONED run stops the background loops. They all return ONLY on
	// their context being done, and on the abandon path the caller's context is still live --
	// no SIGTERM arrived, we are the ones deciding to die -- so without a cancel of our own
	// the wg.Wait() below would block forever and the daemon would hang on the way OUT,
	// trading one wedge for another. It also stops the heartbeat loop from pinging while we
	// exit. Deferred as well as called explicitly so a panic unwind and go vet's lostcancel
	// are both satisfied.
	loopCtx, stopLoops := context.WithCancel(ctx)
	defer stopLoops()

	var wg sync.WaitGroup
	// The manual-outcome waker runs regardless of the heartbeat/update loops: it must receive
	// SIGUSR1 (so the default terminate action never fires) even when a piece of the healthcheck
	// wiring is off. processManualOutcome is itself a no-op when healthchecks are disabled or
	// nothing was handed off. It returns on loopCtx.Done() and joins the waitgroup; it does NOT
	// signal.Stop -- that is owned by run()'s defer above so the disposition is reverted only after
	// the pidfile is gone (see the signal.Stop comment).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-usr1:
				d.processManualOutcome(loopCtx)
			}
		}
	}()

	if d.cfg.HealthcheckEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.heartbeatLoop(loopCtx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.updateCheckLoop(loopCtx)
		}()
	}

	abandoned := d.scheduleLoop(loopCtx)
	// Release the sibling loops BEFORE joining them: on the abandon path nothing else ever
	// will. The abandon pings have already been sent by this point, under the still-live
	// context, so cancelling here can never cut them short.
	stopLoops()
	if abandoned {
		// A CAPPED join, unlike the clean stop below. The loops return as soon as their
		// context is done, with one exception: a beat already inside buildReporter can be
		// parked on the cross-process relay-secret flock, which honours neither a deadline nor
		// the context. Everything worth transmitting is already transmitted, so a loop that
		// cannot drain must not be allowed to hold the exit -- that would put the wedge back
		// one step further out, with the daemon unable to die and systemd unable to restart
		// it. The goroutines die with the process moments later.
		if !waitGroupWithin(&wg, daemonLoopDrainWait) {
			logging.Warning("daemon: background loops did not stop within %s; exiting anyway", daemonLoopDrainWait)
		}
	} else {
		wg.Wait()
	}
	// Both deferred cleanups still run after either return below, in the same LIFO order: the
	// pid file and .daemon_info.json go FIRST, signal.Stop LAST. So a restarting daemon never
	// inherits a stale pid, and no standalone handoff can deliver SIGUSR1 to a process that
	// has already reverted to the default-terminate disposition.
	if abandoned {
		// Restart=always + RestartSec=10 in the unit (buildDaemonUnit) brings a clean daemon
		// back: no goroutine/fd accumulation behind a wedged child, and a live scheduler
		// again. The orphan stays in D state until the kernel releases it; nothing in
		// userspace can reap it. It is still in this unit's cgroup, so systemd's stop job
		// will sit through its TimeoutStopSec phases waiting for a cgroup that cannot drain
		// before the restart lands: the gap is minutes, not the nominal RestartSec=10. That is
		// the accepted cost of keeping the default KillMode -- irrelevant for a once-daily
		// scheduler, and the cgroup-wide kill is the only thing that collects the abandoned
		// child's own descendants (see buildDaemonUnit). The restarted daemon picks the
		// degrade back up from the marker abandonChild left on disk, so the alive check stays
		// DOWN across the gap instead of re-greening.
		//
		// NOT ExitSuccess: an exit 0 is indistinguishable from an operator-requested stop.
		// NOT ExitGenericError either -- docs/CLI_REFERENCE.md and docs/TROUBLESHOOTING.md
		// publish 1 as one of the BENIGN non-zero codes (a run that succeeded with warnings),
		// so a monitoring wrapper written from the documented contract would deliberately not
		// alert on it. ExitBackupError is the documented "the backup operation failed" code,
		// which is exactly what happened: no backup ran and none will until the host is
		// cleared. No new constant is minted -- exit_codes_doc_drift_test.go requires a docs/
		// table row for every constant in internal/types/exit_codes.go.
		logging.Error("ProxSave daemon exiting %d after abandoning an unreapable backup child; systemd will restart it",
			types.ExitBackupError.Int())
		return types.ExitBackupError.Int()
	}
	logging.Info("ProxSave daemon stopped")
	return types.ExitSuccess.Int()
}

// processManualOutcome pings + records the backup-outcome check for a STANDALONE backup run that
// handed off its outcome and woke the daemon with SIGUSR1. The daemon is the SOLE pinger: a
// standalone run never builds a Reporter nor writes the status file; it drops a handoff file and
// signals here. This runs the outcome through the SAME finish machinery a daemon-supervised run
// uses -- finishPing (nil-guarded like beat) then recordPing under KindRunFinished, serialized by
// statusMu -- so the backup-outcome check updates identically. A stale handoff (older than
// manualOutcomeStaleWindow) is dropped WITHOUT pinging (never flip the check for a long-past run).
// A nil/unresolved reporter records a no_url liveness trace (like beat's no-url path) instead of a
// phantom success. The handoff is removed after processing (processed-once; also guards a
// duplicate signal). Best-effort throughout.
func (d *daemon) processManualOutcome(ctx context.Context) {
	if !d.cfg.HealthcheckEnabled {
		return
	}
	mo, err := health.LoadManualOutcome(d.cfg.BaseDir)
	if err != nil {
		logging.Debug("daemon: read manual outcome failed: %v", err)
		return
	}
	if mo.RID == "" { // nothing handed off (missing/empty file)
		return
	}
	// Staleness guard: never ping a run whose handoff is older than the window (e.g. the daemon
	// was down when the standalone backup finished and only received the wake much later).
	if age := d.now().Unix() - mo.TS; age > int64(manualOutcomeStaleWindow/time.Second) {
		logging.Debug("daemon: manual outcome rid=%s is stale (%ds old); dropping without ping", mo.RID, age)
		if rmErr := health.RemoveManualOutcome(d.cfg.BaseDir); rmErr != nil {
			logging.Debug("daemon: remove stale manual outcome failed: %v", rmErr)
		}
		return
	}

	r := d.getReporter()
	// Centralized lazy re-resolve (mirrors beat/updateTick's single re-resolve): if the backup URL
	// is not resolved yet, try ONE rebuild so a daemon paired after startup can still ping.
	if (r == nil || !r.HasBackupURL()) && d.cfg.HealthcheckMode == config.HealthcheckModeCentralized {
		if nr := d.buildReporter(ctx); nr != nil && nr.HasBackupURL() {
			d.setReporter(nr)
			r = nr
		}
	}

	logging.Info("daemon: pinging handed-off standalone backup outcome (rid=%s exit=%d)", mo.RID, mo.ExitCode)
	// Same finish path as a supervised run: finishPing returns ErrNoBackupURL on a nil/unresolved
	// reporter. Unlike reportBestEffort (which swallows a no-url finish for a supervised run whose
	// start ping was ALSO skipped), we ALWAYS record here -- exactly like beat -- so a standalone
	// run against an unprovisioned daemon leaves a no_url trace the section can render.
	done := logging.DebugStart(d.logger, "hc ping", "kind=%s", health.KindRunFinished)
	perr := d.finishPing(ctx, r, mo.RID, mo.ExitCode, "")
	done(perr)
	if health.IsNoURLErr(perr) {
		logging.Debug("daemon: manual outcome finish has no url yet (recording, reason=no_url)")
	} else if perr != nil {
		logging.Debug("daemon: manual outcome finish ping failed: %v", perr)
	}
	d.recordOutcomePing(health.KindRunFinished, mo.ExitCode != 0, perr)

	// A handed-off run that reached a real exit code is the same proof a supervised one is: a
	// backup process took the lock the orphan used to hold and ran to completion. So it lifts an
	// inherited abandon degrade for exactly the reason runOnce's does -- and this is the path
	// that matters most, because `proxsave --backup` is how an operator PROVES the host they
	// just fixed is working. Without it the backup check goes green off this handoff while the
	// alive check keeps sending /fail, and the monitor reads "service dead, backups fine" until
	// the next scheduled run. A SKIP proves the opposite (the lock is still held, or backups are
	// off) and lifts nothing, mirroring runOnce exactly.
	if mo.ExitCode != types.ExitBackupSkipped.Int() {
		d.clearAbandonMarkerOnCompletedRun(fmt.Sprintf("a standalone backup completed (rid=%s exit=%d)", mo.RID, mo.ExitCode), mo.ExitCode)
	}

	if rmErr := health.RemoveManualOutcome(d.cfg.BaseDir); rmErr != nil {
		logging.Debug("daemon: remove manual outcome failed: %v", rmErr)
	}
}

// scheduleLoop waits for the next daily run time and supervises a backup, until the context
// is cancelled. It returns true when a run had to ABANDON a child the kernel will not let us
// reap: there is nothing useful to schedule behind such a child (it still holds the backup
// lock, so tomorrow's run would only exit ExitBackupSkipped), so the loop unwinds and lets
// run() exit for a systemd restart instead.
func (d *daemon) scheduleLoop(ctx context.Context) bool {
	for {
		next, err := cron.NextDaily(d.now(), d.cfg.SchedulerTime)
		if err != nil {
			logging.Error("daemon: invalid SCHEDULER_TIME %q (%v); using %s", d.cfg.SchedulerTime, err, cron.DefaultTime)
			next, _ = cron.NextDaily(d.now(), cron.DefaultTime)
		}
		wait := next.Sub(d.now())
		if wait < 0 {
			wait = 0
		}
		logging.Info("daemon: next backup at %s (in %s)", next.Format("2006-01-02 15:04"), wait.Round(time.Second))

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			if d.runOnce(ctx) {
				return true
			}
		}
	}
}

// runOnce launches ONE supervised backup as a child process under a hard timeout
// and reports the outcome. A child that exceeds the budget is SIGTERM'd, then
// SIGKILL'd, and reported as a hang.
//
// It returns true ONLY when the child could not be REAPED even after that SIGKILL and had to
// be abandoned (see abandonChild): the caller must then unwind so the daemon exits and
// systemd restarts it. Every ordinary outcome -- success, failure, skip, shutdown, and an
// ordinary hang whose child actually died -- returns false and leaves the scheduler running.
func (d *daemon) runOnce(parentCtx context.Context) bool {
	if parentCtx.Err() != nil { // shutting down: do not start a run
		return false
	}
	// Backups disabled: do NOT exec a child (it would exit 0 without backing up)
	// and do NOT ping an outcome, so the backup-outcome check honestly goes down
	// (no backups) rather than showing a false green. The alive heartbeat is
	// independent and keeps signalling the daemon is up.
	if !d.cfg.BackupEnabled {
		logging.Info("daemon: BACKUP_ENABLED=false; skipping the scheduled run (no outcome ping)")
		return false
	}
	// The two operator scripts bracket the run, and this is the only place in the daemon from
	// which they are ever started. ABOVE the run id, the start ping and the watchdog context
	// below on purpose: context.WithTimeout(parentCtx, d.maxRunDuration()) starts the
	// MAX_RUN_DURATION clock, so a pre script started any lower would spend the backup's own
	// budget and could turn a healthy backup into a reported hang, and one started after the
	// start ping would silently add its own time to the run duration the remote monitor
	// measures from that ping. BELOW the two guards above on purpose: those two returns mean
	// no run happens at all, a tick that arrived during shutdown and backups administratively
	// off, and neither script belongs to a run that does not exist.
	//
	// The post is a defer for the one reason a defer exists: every return BELOW this line is
	// a run that happened and each of them must reach it, the abandon, the shutdown, the
	// hang, the skip, the ordinary exit, a cmd.Start that never forked (superviseChild
	// reports that as reaped=true), and a panic unwind. It is registered before the
	// defer cancel() below, so LIFO cancels the run context first and the post is never
	// handed a live one; it is handed no context of the run's at all.
	//
	// The post is waited for on every path but two, where the daemon is on its way out and a
	// wait would be actively harmful. The first is the abandoned-child unwind (postWaits): the
	// daemon exits so systemd can restart it, and every other step there is bounded to 2 to 15
	// seconds. The second is any shutdown (parentCtx cancelled): the whole teardown budget is
	// a stock TimeoutStopSec of 90 seconds, so a waited script would be SIGKILLed along with
	// the daemon, leaving the pid and info files behind and no clean-stop line. On both the
	// script is started and left to the unit's cgroup.
	//
	// Neither call logs anything, at any level, on any outcome. That is deliberate and is not
	// an omission to be repaired: these scripts are the operator's, not ours.
	// Both waited calls carry parentCtx.Done() as their stop: a shutdown landing
	// MID-WAIT abandons the wait (never the script) instead of holding this
	// goroutine through the 90-second teardown budget - the same harm the
	// detached branch below documents avoiding for a shutdown that has already
	// happened when the post fires.
	postWaits := true
	runPersonalScript(d.cfg.PersonalScriptPreRun, parentCtx.Done())
	defer func() {
		if postWaits && parentCtx.Err() == nil {
			runPersonalScript(d.cfg.PersonalScriptPostRun, parentCtx.Done())
			return
		}
		startPersonalScriptDetached(d.cfg.PersonalScriptPostRun)
	}()

	r := d.getReporter()
	rid := health.NewRunID()
	d.reportBestEffort("start", false, func() error { return d.startPing(parentCtx, r, rid) })

	runCtx, cancel := context.WithTimeout(parentCtx, d.maxRunDuration())
	defer cancel()

	// Capture the child's combined output (bounded) so a non-success outcome can
	// POST a real log tail; the output still streams to journald via os.Std*.
	var tail *tailBuffer
	if d.cfg.HealthcheckSendLog {
		tail = &tailBuffer{max: logTailBytes}
	}
	cmd := d.buildBackupCmd(runCtx, tail, rid)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = d.killGrace()

	logging.Info("daemon: launching backup (rid=%s timeout=%s)", rid, d.maxRunDuration())
	reaped, runErr := d.superviseChild(runCtx, cmd)

	// Read the captured tail ONCE, before branching: the abandon path wants it too. tailBuffer
	// is mutex-guarded, so this is well defined even in the pathological case where an
	// abandoned child's copy goroutine is still appending (os/exec has in fact already closed
	// the parent pipe ends and drained those goroutines by then -- watchCtx does that when
	// WaitDelay elapses, strictly before the reap deadline runs out).
	logBody := ""
	if tail != nil {
		logBody = tail.String()
	}

	// The child is still running and can NEVER be waited on. This is the only branch that
	// reports on a process that is not dead, and the only one that asks the daemon to exit.
	// It must come first: every branch below assumes a finished process, and runErr is
	// meaningless when the wait never completed.
	if !reaped {
		postWaits = false // do not delay the restart this unwind exists to trigger
		return d.abandonChild(parentCtx, r, cmd, rid, logBody)
	}

	// Interrupted by shutdown, not a real outcome: stay silent so we don't flip
	// the check on a clean stop (the alive check going quiet signals the stop).
	if parentCtx.Err() != nil {
		return false
	}

	if runCtx.Err() == context.DeadlineExceeded {
		logging.Error("daemon: backup exceeded %s and was killed (hang)", d.maxRunDuration())
		d.reportBestEffort("hang", true, func() error { return d.hangPing(parentCtx, r, rid, logBody) })
		return false
	}

	code := exitCodeFromErr(runErr)
	// A child that exits ExitBackupSkipped did NOT back up (another backup was already running,
	// or BACKUP_ENABLED was re-read as false): there is no outcome to ping. Stay silent, exactly
	// like the honest disabled skip above, so the backup-outcome check does not go a false green
	// (F09-03). The real backup that holds the lock reports its own outcome.
	if code == types.ExitBackupSkipped.Int() {
		logging.Info("daemon: scheduled run skipped, no backup performed (rid=%s, no outcome ping)", rid)
		return false
	}
	logging.Info("daemon: backup finished (rid=%s exit=%d)", rid, code)
	// A run whose child actually RAN can lift an inherited abandon degrade: the daemon launched
	// a child, it took the backup lock the orphan used to hold, and it was reaped. That is what
	// the marker's claim is checked against -- not a skip (ExitBackupSkipped, handled above, is
	// the signature of the orphan STILL holding the lock) and not a mere restart. Whether the
	// backup itself succeeded is the backup check's business, not the alive check's.
	//
	// The gate is childReachedItsOwnExit and NOT the code, because `code` here is not always
	// the child's: exitCodeFromErr synthesises 1 for an error that carries no wait status, and
	// the loudest such error is a cmd.Start failure, which superviseChild returns as reaped=true
	// (there is no child to abandon and no pid to leak). That daemon-side 1 is indistinguishable
	// from the child's own "clean run, warnings only" 1 that exitProvesLockWasTaken must accept,
	// so on the two branches with no pid to probe a fork/exec that never happened would delete
	// the marker and hand the successor nothing -- the exact false GREEN this path exists to
	// remove. A run with no child never reached the lock, so it answers nothing.
	if childReachedItsOwnExit(runErr) {
		d.clearAbandonMarkerOnCompletedRun("a supervised backup completed", code)
	} else {
		logging.Debug("daemon: the backup child never reached an exit status of its own (%v), so the run proves nothing about the backup lock; any abandoned-child marker is left alone", runErr)
	}
	d.reportBestEffort("finish", code != 0, func() error { return d.finishPing(parentCtx, r, rid, code, logBody) })

	// The child reached Phase-7 and wrote its per-channel notify outcomes; ping one
	// healthchecks check per channel it reported (Fase 2B / R4). Strictly after the child
	// exits, so it is naturally the last transmission of the run.
	d.reportNotifyOutcomes(parentCtx, r, rid)
	return false
}

// superviseChild starts cmd, waits for it, and gives up if the child cannot be reaped.
// It reports whether the wait completed at all and, when it did, the child's run error.
//
// It exists because cmd.Run() is NOT interruptible. os/exec's Cmd.Wait does
// "state, err := c.Process.Wait()" -- a wait4(2) -- BEFORE it ever reads the cancellation
// result, so cmd.Cancel and cmd.WaitDelay cannot unblock it: a child parked in
// TASK_UNINTERRUPTIBLE (D state -- a dead NFS/CIFS mount, a stuck device) never dequeues the
// SIGKILL os/exec sends after WaitDelay, is never reaped, and wait4 never returns. Waiting
// for Run() inline is therefore an UNBOUNDED wait on the scheduler's own goroutine: it wedges
// scheduleLoop so no later backup is ever scheduled, it keeps run() from reaching wg.Wait()
// so SIGTERM cannot stop the daemon, and it makes every branch downstream -- including the
// hang report written for exactly this case -- unreachable while the heartbeat loop keeps
// reporting the host green.
//
// So Wait runs on its own goroutine and this function bounds it. Start stays on the caller's
// goroutine: it is not the blocking part, and keeping it here means cmd.Process is published
// before the waiter exists, so the abandon path can log the orphan's pid without racing.
//
// Phase one is the normal case and is byte-for-byte today's behaviour, a Start failure
// included (reaped=true carrying the start error, which exitCodeFromErr still maps to 1).
// reaped=true therefore means "there is no orphan", NOT "a child ran": a caller that needs
// the second fact -- the abandon evidence rule does -- must ask childReachedItsOwnExit about
// the error, because the 1 alone cannot be told from a child's own warning exit.
// Phase two begins only once runCtx is done -- the exact instant os/exec begins the
// SIGTERM -> (WaitDelay) -> SIGKILL sequence, whether the watchdog budget expired or a
// shutdown cancelled the parent -- and gives the child reapWait() to actually be reaped. That
// anchor is what separates "did not die" from "died late but died": a child that ignored
// SIGTERM and only fell to the SIGKILL, or one whose pipes a grandchild held open for the
// full WaitDelay, is reaped inside that window and reported exactly as before. reaped=false
// means the kill grace AND the slack both elapsed and wait4 still has not returned: the
// child is abandoned, and the caller must tear the daemon down rather than pretend its slot
// is free.
//
// waitCh is buffered so the orphaned goroutine can never block on its send if the child is
// somehow reaped much later (the NFS server came back). That goroutine is deliberately dumb:
// its only statement is that send. It touches no daemon field, no reporter and no status
// file, so nothing it can reach is read after we give up and it cannot race the exit.
func (d *daemon) superviseChild(runCtx context.Context, cmd *exec.Cmd) (reaped bool, runErr error) {
	if err := cmd.Start(); err != nil {
		return true, err // never started: no child to abandon, no pid to leak
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case err := <-waitCh:
		return true, err
	case <-runCtx.Done():
	}

	timer := time.NewTimer(d.reapWait())
	defer timer.Stop()
	select {
	case err := <-waitCh:
		return true, err
	case <-timer.C:
		return false, nil
	}
}

// abandonChild gives up on a supervised child that survived SIGTERM followed by SIGKILL and
// that the kernel will not let os/exec reap, and reports whether the daemon must now exit.
// There is nothing left to wait for: the child cannot be killed, cannot be reaped, and holds
// its process slot (and the backup lock) until the kernel releases it. The pid stays behind
// in D state -- nothing in userspace can clear that -- but the daemon stops being hostage to
// it.
//
// The ordering is what the operator sees, and it is deliberate: the diagnosis lands in
// journald first; then the backup-outcome check goes DOWN -- this is the hang report that was
// structurally unreachable while cmd.Run() owned the wait -- then the marker is persisted so
// the state survives the exit, and only then the alive check goes DOWN. A monitor shown only
// one of those two checks lies about the host.
//
// The two TRANSMISSIONS are ordered ahead of the marker write on purpose. Persisting first
// reads better -- the state outlives everything after it -- but the marker write is local
// filesystem I/O against BaseDir, and the premise of this entire path is a host with wedged
// I/O. A BaseDir on that same dead mount would block those syscalls exactly as they blocked
// the child, and marker-first ordering would then cost BOTH monitoring signals (and runOnce's
// return with them). The pings are network calls the Reporter caps at pingTimeout; the marker
// write is bounded by persistAbandonMarker. Losing the marker costs the successor its
// inherited degrade -- bad, and logged -- while losing the pings costs the operator the
// outage itself.
//
// The backup /fail is also sent BEFORE aliveMu is touched. It is the primary signal of this
// whole change and has nothing to do with the alive check's ordering, so it must not be gated
// on another goroutine's lock. The alive silence latch then closes -- atomically, before the
// lock, so a beat that has not yet entered its critical section is dropped whatever happens
// next -- and only the degrade transmission itself takes aliveMu, with a deadline, purely to
// let a beat already on the wire land FIRST so the /fail is the last word on that check.
//
// A shutdown-time abandon returns false and PINGS nothing: the same rule as any other run
// interrupted by a stop (never flip a check on a clean stop), and the stop was requested, so
// there is no restart to ask for. It still writes the marker; see that branch.
func (d *daemon) abandonChild(parentCtx context.Context, r backupReporter, cmd *exec.Cmd, rid, logTail string) bool {
	pid := -1
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	// Read the child's start time NOW, while it is still unambiguously ours. It is what turns
	// the pid in the marker into an identifier the successor can verify instead of a number the
	// kernel may hand to something else (see abandonedChildGone). Bounded like every other read
	// on this path, and best-effort: a 0 costs the successor the exact check, not the marker.
	start := uint64(0)
	if pid > 0 {
		if v, answered := probeWithin(daemonProcProbeWait, func() uint64 {
			ticks, ok := procStartTicks(pid)
			if !ok {
				return 0
			}
			return ticks
		}); answered {
			start = v
		}
		if start == 0 {
			logging.Debug("daemon: could not record the start time of abandoned child pid=%d; its successor will fall back to a cmdline match", pid)
		}
	}
	note := fmt.Sprintf("backup child pid=%d (rid=%s) is unreapable; backups cannot run until the host clears the stuck I/O", pid, rid)

	if parentCtx.Err() != nil {
		logging.Error("daemon: backup child pid=%d (rid=%s) survived SIGTERM + %s and cannot be reaped; leaving it behind and continuing the shutdown",
			pid, rid, d.killGrace())
		// The silence rule is about TRANSMISSIONS -- no check may be flipped on a clean stop --
		// and the marker is not one: it is local state that changes nothing during this stop and
		// only makes the NEXT process honest. Writing it here is what keeps `systemctl restart`
		// (the reflex of any operator who notices a wedged daemon, and so the dominant way this
		// branch is reached) from bringing the daemon back fully green over an orphan that still
		// holds the backup lock, with every scheduled run exiting ExitBackupSkipped and pinging
		// nothing. We already know the child is unreapable here; that fact is exactly what the
		// successor needs.
		d.persistAbandonMarker(pid, start, rid)
		return false
	}

	logging.Error("daemon: backup child pid=%d (rid=%s) exceeded %s, then survived SIGTERM and SIGKILL for %s: it is stuck in uninterruptible sleep and can never be reaped (look for a dead NFS/CIFS mount or a hung device)",
		pid, rid, d.maxRunDuration(), d.reapWait())
	logging.Error("daemon: abandoning the run; the process stays behind in D state and keeps holding the backup lock until the host clears the stuck I/O")

	// The reporter runOnce captured may be stale by now: it is read once at the top of a run
	// that may last MAX_RUN_DURATION, and in centralized mode beat's lazy re-resolve can
	// install one behind our back (setReporter) at any point during it. Re-read it, or a
	// daemon that started unpaired and got its URLs mid-run would silently drop BOTH mandated
	// signals. No buildReporter fetch here -- see degradeAlive: this path must not block on the
	// network, most likely against the same unreachable host that wedged the child.
	if cur := d.getReporter(); cur != nil {
		r = cur
	}

	// The backup /fail goes out first, outside aliveMu and ahead of every local write: it is
	// the report this whole path exists to make reachable, and neither another goroutine's lock
	// nor a possibly-wedged disk may stand in front of it. Its own status-file record is
	// deadline-bounded for that second reason (the ping itself has already been transmitted by
	// the time that write is attempted).
	d.reportBestEffortBounded("hang", true, daemonAbandonIOWait, func() error { return d.hangPing(parentCtx, r, rid, logTail) })

	// Close the silence latch BEFORE writing the marker. It drops every beat that has not yet
	// entered its critical section, and -- the reason it comes first -- it also stops
	// reviewAbandonDegrade, which runs on that same beat, from deleting the marker written
	// just below. A daemon that inherited a degrade whose old orphan has since died, and that
	// then wedges on a NEW child, would otherwise have its fresh marker removed by a beat that
	// passed the liveness probe moments earlier, and the successor would come up green over a
	// live orphan.
	d.aliveSilenced.Store(true)

	// Now persist, under a deadline. This is the only part that outlives the process, and it is
	// what stops the daemon systemd is about to restart from re-greening the alive check with
	// its immediate first beat. Best-effort: a write fault -- or a write that never returns --
	// must not cost us the degrade below.
	d.persistAbandonMarker(pid, start, rid)

	// The lock only drains a beat ALREADY inside its transmission, so that its success ping
	// cannot land behind the /fail below and re-green the check; that is worth a bounded wait
	// and nothing more, hence the deadline. Failing to get it in time is logged and does not
	// stop us: a runOnce that returns is worth more than a perfectly ordered pair of pings.
	if d.lockAliveWithin(d.aliveInterlockWait()) {
		defer d.aliveMu.Unlock()
	} else {
		logging.Warning("daemon: heartbeat interlock still held after %s; reporting the alive degrade anyway", d.aliveInterlockWait())
	}
	d.degradeAlive(parentCtx, r, note)
	return true
}

// lockAliveWithin acquires aliveMu, giving up after limit. It reports whether the lock was taken
// (only then may the caller unlock). The wall clock is read directly, NOT through d.now: the
// tests freeze that clock, and a deadline that never advances is not a deadline.
func (d *daemon) lockAliveWithin(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		if d.aliveMu.TryLock() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// persistAbandonMarker records the abandon for the daemon systemd is about to start, under a
// deadline.
//
// The deadline is the point. health.WriteAbandon is os.MkdirAll + os.WriteFile + os.Rename
// against BaseDir, with no context and no timeout of its own (it is a stdlib-only sibling of
// the pid/status files by design), and the only reason this function is ever called is that
// the host has I/O the kernel will not let go of. A BaseDir on that filesystem -- a NAS-hosted
// BASE_DIR, or simply a wedged local disk -- turns those three syscalls into the same
// uninterruptible block that started all this, and an unbounded one here would stop runOnce
// from returning: the original wedge, reproduced one layer out, by the code written to remove
// it. runOnce RETURNING is the invariant; the marker is a best-effort nicety for the next
// process. So the write runs on its own goroutine and this waits a bounded time for it.
//
// A goroutine left behind on expiry is acceptable for the same reason it is in waitGroupWithin
// and superviseChild: every caller is on the abandon path, moments from exiting the process,
// and the goroutine touches nothing that is read afterwards.
// The start ticks come from the caller because they must be read while the child is still
// OURS -- see abandonChild. A 0 is honest and handled (the successor falls back to the cmdline
// identity test); a value read later, after the pid could already have been recycled, would
// not be.
//
// It takes abandonMu and latches abandonMarkerWritten BEFORE the write, so a concurrent clear
// either already ran (and this overwrites it) or sees the latch and leaves this file alone.
// Either order is correct; without the lock only one of them is.
func (d *daemon) persistAbandonMarker(pid int, start uint64, rid string) {
	rec := health.AbandonRecord{PID: pid, Start: start, RID: rid, TS: d.now().Unix()}
	d.abandonMu.Lock()
	defer d.abandonMu.Unlock()
	d.abandonMarkerWritten.Store(true)
	// Publish the record with the latch, for the one reader that needs it: a removal that
	// escaped abandonMu by staying parked in the kernel past its deadline and can still land on
	// top of the write below. See restoreAbandonMarkerIfSuperseded.
	d.abandonRec.Store(&rec)
	if !runWithin(daemonAbandonIOWait, func() {
		if err := health.WriteAbandon(d.cfg.BaseDir, rec); err != nil {
			logging.Warning("daemon: could not persist the abandoned-child marker (%v); the alive check will re-green after the restart", err)
		}
	}) {
		logging.Warning("daemon: writing the abandoned-child marker to %s did not complete within %s (is BASE_DIR on the wedged filesystem too?); continuing without it, so the alive check will re-green after the restart",
			health.AbandonPath(d.cfg.BaseDir), daemonAbandonIOWait)
	}
}

// removeDaemonFiles clears the pid file and .daemon_info.json on the way out, under a
// deadline.
//
// The deadline is not decoration. This runs in run()'s LAST defer, so it is the final thing
// between the daemon and its exit -- and on the ABANDON path the exit is the whole point:
// maintainer decision, systemd Restart=always brings a clean daemon back. Both removals are
// plain os.Remove against BaseDir, which on that path may be the very filesystem that parked
// the child in D state; persistAbandonMarker goes to considerable lengths to bound its own
// writes for exactly this reason, and it would all be for nothing if the process then blocked
// here forever, unable to die and so never restarted. A removal that times out is left to the
// straggler goroutine (the process is about to be gone anyway) and the next daemon overwrites
// both files at startup regardless.
//
// removeDaemonFilesIO is the seam that lets a test wedge those two syscalls; nil means the
// real ones.
func (d *daemon) removeDaemonFiles() {
	io := d.removeDaemonFilesIO
	if io == nil {
		io = func() {
			if err := health.RemoveDaemonPID(d.cfg.BaseDir); err != nil {
				logging.Debug("daemon: remove pid file failed: %v", err)
			}
			if err := health.RemoveDaemonInfo(d.cfg.BaseDir); err != nil {
				logging.Debug("daemon: remove daemon info failed: %v", err)
			}
		}
	}
	if !runWithin(daemonAbandonIOWait, io) {
		logging.Warning("daemon: clearing the pid file in %s did not complete within %s (is BASE_DIR on a wedged filesystem?); exiting anyway",
			d.cfg.BaseDir, daemonAbandonIOWait)
	}
}

// runWithin runs fn on its own goroutine and waits up to limit for it to return, reporting
// whether it did. It bounds a call that has no context of its own -- local filesystem I/O on
// the abandon path -- and its callers must treat a false as "this may still be running": the
// goroutine is abandoned, not cancelled, because nothing in userspace can cancel a syscall
// parked in the kernel. Only ever used where the process is about to exit anyway.
func runWithin(limit time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// waitGroupWithin joins wg, giving up after limit, and reports whether it drained. The helper
// goroutine outlives a timeout, which is only ever acceptable because the sole caller is on
// its way out of the process; it touches nothing else.
func waitGroupWithin(wg *sync.WaitGroup, limit time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// loadAbandonMarker inherits an abandon left by a previous process (see abandonChild). It
// runs in run() before any loop starts, so the flags it sets are published to every reader.
//
// A marker is a CLAIM, not a verdict: "pid N is unreapable and is still holding the backup
// lock". This is the one place that claim can be checked against the host before a whole
// process lifetime is spent acting on it, so the two ways it can already be false are checked
// here -- backups administratively off, and the named orphan no longer on the host. Believing
// it unconditionally is how the fix for a false GREEN turns into a false RED on the check that
// pages people.
func (d *daemon) loadAbandonMarker() {
	rec, err := health.ReadAbandon(d.cfg.BaseDir)
	if err != nil {
		// We could not read it, so we do not know whether an abandon happened: do NOT degrade on
		// a guess. But a file we failed to read is still probably there, so remember that this
		// process owes the identity dir a cleanup once a run proves the wedge is over -- see
		// abandonMarkerOnDisk.
		d.abandonMarkerOnDisk.Store(true)
		logging.Debug("daemon: read abandoned-child marker failed: %v", err)
		return
	}
	if rec == nil {
		return
	}
	d.abandonMarkerOnDisk.Store(true)

	// The marker looks older than this boot, so the pid it names probably belongs to a process
	// from a previous boot and cannot be our orphan: the pid space was recycled at boot. Retire
	// it ahead of the BACKUP_ENABLED branch below -- a record from a previous boot must not be
	// kept "for when backups are re-enabled" either, since re-enabling them cannot make a
	// process that no longer exists relevant again.
	//
	// The comparison MAY NOT decide this on its own, and the gate on the identity probe is not
	// belt and braces: /proc/stat btime is not a stamp the kernel recorded at boot, it is
	// derived from the CURRENT realtime offset (getboottime64 = offs_real - offs_boot), so every
	// forward step of the wall clock moves it forward by the same amount. A host that booted
	// with a dead RTC, abandoned a child, and was then stepped forward by chrony has a btime
	// later than a marker written minutes earlier during THIS boot -- and discarding it there
	// re-greens the service-alive check over an orphan that is still wedged, which is the exact
	// false GREEN this whole mechanism exists to remove. The same argument is why the identity
	// half of the pid is a tick count and not a timestamp; see pidIsAbandonedChild and
	// health.AbandonRecord.Start.
	//
	// So btime may only ever CONFIRM what the probe already says, never override it. What the
	// branch is still worth is the ordering: it retires a pre-boot marker that the branch below
	// would otherwise keep for a re-enable that can never make it true again. A boot time we
	// cannot read (0) decides nothing.
	if boot := d.bootUnix(); boot > 0 && rec.TS > 0 && rec.TS < boot && d.abandonedChildGone(rec.PID, rec.Start) {
		logging.Info("daemon: discarding an abandoned-child marker for pid=%d (rid=%s) written before the current boot; that process is gone", rec.PID, rec.RID)
		d.clearAbandonMarker("")
		return
	}

	// Backups are administratively OFF. The degrade says "backups are dead because an orphan
	// holds the lock", but with BACKUP_ENABLED=false they are dead by operator decision, the
	// backup-outcome check already goes honestly down on its own (runOnce pings nothing), and
	// the alive check has nothing left to add. It also could never be lifted: runOnce returns
	// at the BACKUP_ENABLED guard before any completed run can clear the marker, and a
	// standalone backup refuses for the same reason (ExitBackupSkipped), so degrading here
	// would pin the alive check DOWN forever on a daemon that is perfectly healthy -- and turning
	// backups off is precisely what an operator does after reading the ERROR this path prints.
	// BACKUP_ENABLED is read once at startup and the daemon restarts to pick up a config change,
	// so this decision holds for the whole process lifetime. The marker is deliberately LEFT on
	// disk: re-enabling backups makes it relevant again, and the first completed run clears it.
	if !d.cfg.BackupEnabled {
		logging.Info("daemon: a previous run abandoned backup child pid=%d (rid=%s), but BACKUP_ENABLED=false; not degrading the service-alive check (the backup check is down on its own). The marker is kept for when backups are re-enabled.", rec.PID, rec.RID)
		return
	}

	// The orphan is provably gone -- the host was rebooted, or the dead mount came back and the
	// task finally died. kill(2) can only answer this in one direction (a pid that does not
	// exist cannot be our unreapable child; a pid that does exist may be an unrelated process
	// that reused the number after a reboot), so this only ever CLEARS, never invents, a
	// degrade. Nothing here claims a backup succeeded: the backup-outcome check stays DOWN until
	// a run actually reports one.
	if d.abandonedChildGone(rec.PID, rec.Start) {
		logging.Info("daemon: a previous run abandoned backup child pid=%d (rid=%s), but that process is gone; clearing the marker and reporting the service-alive check normally (the backup check stays down until a run reports one)", rec.PID, rec.RID)
		d.clearAbandonMarker("")
		return
	}

	// Only NOW is the orphan's identity worth carrying: this is the one branch that acts on it
	// for the rest of the process's life. The branches above RETIRED the marker, and a pid
	// left in these fields by one of them would be probed by every completed run from here on
	// -- after a reboot, against whatever unrelated process inherited the number -- and logged
	// as "keeping the service-alive check DOWN" while the check is in fact green.
	d.abandonPID = rec.PID
	d.abandonStart = rec.Start
	d.aliveDegraded.Store(true)
	note := fmt.Sprintf("a previous run abandoned backup child pid=%d (rid=%s); no backup has completed since", rec.PID, rec.RID)
	d.abandonNote.Store(&note)
	logging.Error("daemon: %s. The service-alive check is reported DOWN until a supervised backup completes. Check for a process stuck in D state (ps -eo pid,stat,wchan,cmd | grep ' D'), a dead NFS/CIFS mount, and for leftover backup helpers (tar/pigz/rclone) still writing to it.", note)
}

// abandonedChildGone reports whether the child a previous process abandoned is no longer on
// this host. It answers about a PROCESS, not about a number: liveness alone is not an answer,
// because the kernel recycles pid numbers within a boot.
//
// That distinction is the whole safety of the mechanism in the OTHER direction. Every lift
// path -- the startup re-validation, the per-beat review, and a completed run -- is gated on
// this function, so a "still there" it gets wrong is not a transient annoyance: it pins the
// service-alive check DOWN on a host whose backups are running perfectly, with no run, no
// restart and no reboot able to lift it, until somebody deletes the marker by hand. And the
// coincidence is ordinary, not exotic: the mount heals, the orphan finally dies while nothing
// is watching (the daemon was stopped for the repair, or healthchecks are off so no beat ever
// reviews it), and the number is handed to the next long-lived process the host starts. The
// repo already refuses this oracle where the consequence was a misdirected signal; see
// probeProxsaveDaemonAlive ("the cmdline match is the SAFETY gate").
//
// So: ESRCH from signal 0 proves the number is unused and the orphan is gone. Otherwise the
// number is in use by SOMETHING, and the identity check decides whether that something is
// still our child. Anything unreadable counts as GONE -- the process we are asking about is
// one we could observe in full detail when we abandoned it, so losing sight of it is evidence
// of a different process, and the direction that keeps the degrade liftable.
//
// A pid the marker never carried (0, or the -1 abandonChild records for a child that was
// never published) is unanswerable and counts as still there -- the conservative direction,
// which keeps the degrade. That case has its own lift rule; see
// clearAbandonMarkerOnCompletedRun.
func (d *daemon) abandonedChildGone(pid int, start uint64) bool {
	if pid <= 0 { // never signal 0 or -1: those mean "my process group" and "every process"
		return false
	}
	if d.pidAliveOverride != nil {
		return !d.pidAliveOverride(pid)
	}
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true // the number is not in use at all
	}
	return !d.pidIsAbandonedChild(pid, start)
}

// pidIsAbandonedChild reports whether the LIVE pid is still the process the marker named,
// under a deadline (see daemonProcProbeWait: expiry means the task is still parked in the
// kernel, which is itself "still there").
//
// The exact test is the start time: (pid, starttime) is unique for the life of a boot, so a
// recycled number can never match, and a tick count cannot be moved by a clock step the way
// /proc/stat btime can. Markers written before that field existed carry 0, and fall back to
// the same cmdline test probeProxsaveDaemonAlive uses -- weaker (another proxsave --backup
// that happened to inherit the number would pass) but self-healing, because that process
// exits and the next beat lifts the degrade.
//
// At most ONE read is ever outstanding, and that is a requirement rather than an optimisation.
// probeWithin bounds the WAIT, not the read: a goroutine it gives up on is abandoned, not
// cancelled, and it is holding an open /proc file descriptor. Unlike every other caller of that
// helper this one is not on its way out of the process -- reviewAbandonDegrade runs it once per
// heartbeat for as long as the degrade stands -- so re-issuing a read that has already proved it
// can block would strand a goroutine and an fd every beat, indefinitely, on the one host an
// operator is actively investigating. While a read is still parked the answer is the same one
// its expiry gives ("still there"), and the next beat after it returns probes again.
func (d *daemon) pidIsAbandonedChild(pid int, start uint64) bool {
	if !d.probeInFlight.CompareAndSwap(false, true) {
		logging.Debug("daemon: an earlier identity probe for the abandoned child pid=%d is still parked in the kernel; treating it as still there without issuing another read", pid)
		return true
	}
	ours, answered := probeWithin(daemonProcProbeWait, func() bool {
		defer d.probeInFlight.Store(false)
		if d.procIdentityIO != nil {
			return d.procIdentityIO(pid, start)
		}
		if start > 0 {
			cur, ok := procStartTicks(pid)
			return ok && cur == start
		}
		return procIsBackupChild(pid)
	})
	if !answered {
		logging.Debug("daemon: identifying the abandoned child pid=%d did not complete within %s; treating it as still there", pid, daemonProcProbeWait)
		return true
	}
	return ours
}

// probeWithin runs fn on its own goroutine and waits up to limit for its answer, reporting
// whether one arrived. Same contract as runWithin -- a goroutine left behind on expiry is
// abandoned, not cancelled -- but it carries a value back, over a buffered channel so the
// straggler can never block on its send and never writes anything the caller reads. That last
// part is why fn RETURNS its result instead of assigning to a captured variable: an assignment
// from a goroutine that outlived the deadline would be a data race with the caller.
func probeWithin[T any](limit time.Duration, fn func() T) (result T, answered bool) {
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-timer.C:
		var zero T
		return zero, false
	}
}

// procStartTicks returns pid's start time in clock ticks since boot (/proc/<pid>/stat field
// 22) and whether it could be read. The comm field (2) is wrapped in parentheses and may
// itself contain spaces AND parentheses, so the fields are counted from the LAST ')' -- the
// documented way to parse this file.
func procStartTicks(pid int) (uint64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, false
	}
	// Fields after the comm start at field 3 (state), so field 22 is index 19 here.
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 {
		return 0, false
	}
	v, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// procIsBackupChild reports whether pid's /proc/<pid>/cmdline identifies a proxsave backup
// child: a "proxsave" token AND the exact "--backup" arg (buildBackupCmd's argv). Same shape
// as probeProxsaveDaemonAlive's daemon test, and used for the same reason -- a pid number on
// its own identifies nothing. Unreadable counts as NOT ours; see abandonedChildGone.
func procIsBackupChild(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	// /proc/<pid>/cmdline is NUL-separated argv; split to whole args so the match is on a real
	// argument, not a coincidental "proxsave" substring buried inside an unrelated path.
	args := strings.Split(string(data), "\x00")
	var hasProxsave, hasBackup bool
	for _, a := range args {
		if strings.Contains(a, "proxsave") {
			hasProxsave = true
		}
		if a == "--backup" {
			hasBackup = true
		}
	}
	return hasProxsave && hasBackup
}

// reviewAbandonDegrade re-validates an inherited degrade against the host and lifts it once
// the orphan it names is gone. Called from beat, so a host that healed WITHOUT a restart (the
// NFS server came back, the D-state task finally died) stops reporting the alive check DOWN
// within one heartbeat interval instead of waiting up to a whole scheduling period for the
// next run to prove it -- the same false-RED-for-a-day the marker exists to avoid the inverse
// of. It costs one kill(2) plus one bounded /proc read per beat on a degraded daemon, and
// nothing at all otherwise.
func (d *daemon) reviewAbandonDegrade() {
	// Cheap pre-check for the abandon path: once the latch is closed this process is on its
	// way out after abandoning a child of its OWN, and the marker in the identity dir is the
	// fresh one abandonChild just wrote for the successor -- not the inherited one this review
	// is about. This is an optimisation, NOT the barrier: the latch and this load are separate
	// atomics, so a beat that read it a moment too early is still on its way to the clear. The
	// barrier that actually holds is in clearAbandonMarker (abandonMu + abandonMarkerWritten).
	if d.aliveSilenced.Load() {
		return
	}
	if !d.aliveDegraded.Load() || !d.abandonedChildGone(d.abandonPID, d.abandonStart) {
		return
	}
	d.clearAbandonMarker(fmt.Sprintf("the abandoned backup child pid=%d is gone", d.abandonPID))
}

// clearAbandonMarkerOnCompletedRun retires the marker when a backup run reached a real exit
// code -- but only once the orphan the marker names is also gone.
//
// A completed run is NOT by itself proof that the wedge is over. The backup lock is checked
// LAST (internal/orchestrator/orchestrator.go, "4. Check lock file LAST"), after the
// directory, temp-dir, disk-space and permission gates, so a run can reach a real exit code
// without ever having reached the lock the orphan holds. Worse, the fault that wedges a child
// in D state -- a dead NFS/CIFS mount under the backup path -- is exactly the fault that fails
// those earlier gates. Believing the exit code alone therefore lifts the degrade precisely on
// the host that still needs it: the operator runs `proxsave --backup` to see what is wrong, it
// dies on the disk-space check with a non-zero code, and the alive check goes green over an
// orphan that has not moved.
//
// So a completed run only TRIGGERS the question, and the orphan probe answers it. Three cases,
// and which one applies is decided by whether a DEGRADE was ever raised -- not by the pid
// value, which says nothing on its own:
//
//   - Degraded, and the marker named a pid. The probe decides, and only "the orphan is gone"
//     lifts it. This is the case the paragraph above is about.
//   - Degraded with NO usable pid -- a corrupt or truncated marker, which the health package
//     deliberately reads as a real abandon (WriteAbandon does not fsync, and these hosts get
//     hard-reset). There is nothing to probe, so the run's own evidence is all there is and
//     only a code that proves the run REACHED the lock qualifies; see exitProvesLockWasTaken.
//   - Nothing was degraded, but a file is still owed to the identity dir: one this process
//     could not READ at startup (ReadAbandon's error branch), or one it deliberately KEPT
//     because backups are administratively off. Neither is a free pass, and the SAME evidence
//     rule applies. Both of those states are reached with a marker that may name a LIVE orphan
//     -- the unreadable one most likely of all, since the host whose BaseDir I/O is wedged is
//     exactly the host that wedges children -- and this process, having read nothing, knows
//     even less about it than a degraded one does. Deleting on a pre-lock failure here is the
//     same false GREEN as deleting on one above, only quieter: nothing is degraded in THIS
//     process, so the whole cost lands on the successor, which inherits nothing and beats green
//     over an orphan that still holds the backup lock.
//
// exitCode must be one a backup PROCESS actually reported. Callers hand it over unchanged
// (mo.ExitCode from the standalone handoff) or after checking that a child really exited
// (runOnce, via childReachedItsOwnExit): a code this daemon synthesised for a child that never
// ran is not evidence about anything, least of all about a lock that process never reached.
func (d *daemon) clearAbandonMarkerOnCompletedRun(reason string, exitCode int) {
	if !d.aliveDegraded.Load() {
		if !exitProvesLockWasTaken(exitCode) {
			logging.Debug("daemon: %s, but the run did not reach the backup lock, so nothing proves the abandoned-child marker is stale; leaving it alone", reason)
			return
		}
		d.clearAbandonMarker(reason)
		return
	}
	if d.abandonPID > 0 {
		if !d.abandonedChildGone(d.abandonPID, d.abandonStart) {
			logging.Info("daemon: %s, but the abandoned backup child pid=%d is still on this host, so it never took the backup lock; keeping the service-alive check DOWN", reason, d.abandonPID)
			// The degrade stands, but the reason it transmits must not: the note written at
			// startup says "no backup has completed since", and one just did. Every beat from
			// here on would repeat that as fact. Restate what is actually still true -- the
			// orphan is on the host -- so the operator is told to hunt the D-state task rather
			// than a backup failure that has already stopped happening.
			d.setAbandonNote(fmt.Sprintf("a previous run abandoned backup child pid=%d, and that process is still on this host; backups have run since, but the orphan cannot be reaped", d.abandonPID))
			return
		}
		d.clearAbandonMarker(reason)
		return
	}
	if !exitProvesLockWasTaken(exitCode) {
		logging.Info("daemon: %s, but the marker names no pid this daemon can check and the run did not reach the backup lock, so nothing proves it was taken; keeping the service-alive check DOWN", reason)
		// Same correction the pid branch above makes, for the same reason: the startup note
		// says "no backup has completed since", a run just did, and every beat from here on
		// would transmit that as fact. This branch can say less -- with no checkable pid there
		// is nothing to point the operator at -- but it must not keep asserting something it
		// now knows to be false.
		d.setAbandonNote("a previous run abandoned a backup child and the marker names no pid this daemon can check; backups have run since, but none of them proved it reached the backup lock")
		return
	}
	d.clearAbandonMarker(reason)
}

// setAbandonNote replaces the body the degraded beat transmits. See the abandonNote field.
func (d *daemon) setAbandonNote(note string) {
	d.abandonNote.Store(&note)
}

// abandonNoteNow returns that body. A degrade always has one, but a nil is answered with a
// bare statement rather than an empty ping body: the beat must never transmit less than the
// fact that the check is down on purpose.
func (d *daemon) abandonNoteNow() string {
	if n := d.abandonNote.Load(); n != nil {
		return *n
	}
	return "a previous run abandoned an unreapable backup child"
}

// exitProvesLockWasTaken reports whether a completed run's exit code is evidence that the run
// got as far as the BACKUP LOCK -- the LAST of the orchestrator's pre-flight gates -- and so
// that the orphan named by a marker is no longer holding it.
//
// Exactly the two documented NON-FAILURE codes qualify. 0 is a clean run. 1 is the same run
// with warnings: applyIssueExitCode (internal/orchestrator/extensions.go) promotes a clean run
// to ExitGenericError when it logged warnings or notify issues, and a run that raised real
// ERRORS becomes ExitBackupError instead, so a 1 is never a failure that was demoted into this
// set. docs/TROUBLESHOOTING.md documents both (the code table, and the "every backup reports
// warnings" section) -- including a routine state in which EVERY run on a perfectly healthy
// host exits 1, unacknowledged release notes after an upgrade. Refusing 1 here is therefore not
// caution: on such a host it is a service-alive DOWN that no run, no restart and no reboot can
// lift, which is the same unliftable false RED the pid identity check exists to prevent.
//
// The per-phase failure codes qualify for the opposite reason: they can ONLY be minted by
// RunGoBackup's phases (internal/orchestrator/backup_run_phases.go builds a BackupError with
// them, and cmd/proxsave/backup_execution.go returns backupErr.Code), and that runs strictly
// after RunPreBackupChecks succeeded -- which means the lock gate passed. A run that died in
// collection or encryption is a failed backup, but it is proof the orphan was not holding the
// lock. Refusing them would strand the degrade on a host whose backups fail for an unrelated
// reason: an unliftable false RED, the same failure the pid identity check exists to prevent.
//
// Everything else proves nothing. A pre-flight gate failure returns ExitBackupError, and a
// dead mount under the backup path is exactly what fails those gates before the lock is ever
// reached. ExitBackupSkipped never arrives here: both callers filter it first.
//
// The set is only sound because no PRE-lock path reports one of these codes. That is a real
// constraint on the rest of the tree, not an observation: the encryption-setup abort in
// cmd/proxsave/backup_mode.go returned ExitGenericError until it was found to be exactly such
// a path, and now returns ExitConfigError. Anything added here must be checked the same way.
func exitProvesLockWasTaken(exitCode int) bool {
	switch exitCode {
	case types.ExitSuccess.Int(), types.ExitGenericError.Int(),
		types.ExitStorageError.Int(), types.ExitVerificationError.Int(),
		types.ExitCollectionError.Int(), types.ExitArchiveError.Int(),
		types.ExitCompressionError.Int(), types.ExitEncryptionError.Int():
		return true
	default:
		return false
	}
}

// hostBootUnix reads the host's boot time from /proc/stat "btime", in Unix seconds. It
// returns 0 when the value cannot be read or parsed, which every caller must treat as
// "unknown" rather than as a boundary.
func hostBootUnix() int64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "btime ")
		if !ok {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return 0
}

// bootUnix is hostBootUnix with the test seam applied.
func (d *daemon) bootUnix() int64 {
	if d.bootUnixOverride != nil {
		return d.bootUnixOverride()
	}
	return hostBootUnix()
}

// clearAbandonMarker lifts an inherited degrade and removes the marker that carries it across
// restarts. reason names what proved the wedge was over and is logged only when a degrade was
// actually lifted (an empty reason logs nothing -- for callers that print their own line).
//
// The gate is abandonMarkerOnDisk, NOT the degrade: a process that could not read the marker
// never degraded but still has a file to delete, and leaving it would resurrect the degrade in
// the next process the moment the file became readable again. The CompareAndSwap keeps this a
// no-op, and silent, on the overwhelmingly common path where nothing was ever abandoned, so a
// normal run pays one atomic load and no syscall.
//
// The abandonMu / abandonMarkerWritten pair is the BARRIER against the abandon path, and it
// lives here rather than in any one caller because every caller needs it: the beat's review,
// runOnce's completed run, and processManualOutcome's SIGUSR1 handoff all reach this function,
// and the handoff's window is the widest of the three (abandonChild spends the whole interlock
// wait plus a ping after writing its marker, while the waker goroutine stays live until run()
// stops the loops). Once this process has written a marker for an orphan of its OWN, that file
// is the successor's inheritance and nothing here may delete it -- not even a caller that
// checked a latch a microsecond before abandonChild closed it and is only now arriving. The
// removal is deadline-bounded for the same reason the marker WRITE is: BaseDir may be on the
// filesystem that wedged the child, and an unbounded unlink under this lock would stall the
// abandon path that is waiting to persist.
//
// That deadline is also the one hole the lock cannot plug, and the removal closes it itself:
// runWithin gives up WAITING, it does not cancel the unlink, so on a wedged BaseDir -- the one
// host class this path exists for -- the lock is released with the syscall still queued, and it
// can land after abandonChild has persisted the successor's marker. See
// restoreAbandonMarkerIfSuperseded. For the same reason abandonMarkerOnDisk is only allowed to
// STAY down once the file is known to be gone: a removal that failed or timed out leaves a file
// nobody would ever retry, and it re-degrades the next daemon.
func (d *daemon) clearAbandonMarker(reason string) {
	d.abandonMu.Lock()
	defer d.abandonMu.Unlock()
	if d.abandonMarkerWritten.Load() {
		return
	}
	if !d.abandonMarkerOnDisk.CompareAndSwap(true, false) {
		return
	}
	var cleared atomic.Bool
	if !runWithin(daemonAbandonIOWait, func() {
		if err := d.clearAbandonIO(); err != nil {
			logging.Debug("daemon: clear abandoned-child marker failed: %v", err)
		} else {
			cleared.Store(true)
		}
		d.restoreAbandonMarkerIfSuperseded()
	}) {
		logging.Warning("daemon: removing the abandoned-child marker %s did not complete within %s (is BASE_DIR on a wedged filesystem?); the degrade is lifted in this process anyway",
			health.AbandonPath(d.cfg.BaseDir), daemonAbandonIOWait)
	}
	if !cleared.Load() {
		// The file may well still be there. Re-arm the gate so a later completed run retries the
		// unlink instead of leaving a marker no caller in this process will ever touch again --
		// which the NEXT daemon reads, and degrades on, for a wedge that ended long ago.
		d.abandonMarkerOnDisk.Store(true)
	}
	if d.aliveDegraded.CompareAndSwap(true, false) && reason != "" {
		logging.Info("daemon: %s; the abandoned-child degrade is cleared and the alive check recovers on the next heartbeat", reason)
	}
}

// clearAbandonIO removes the marker file, with the test seam applied.
func (d *daemon) clearAbandonIO() error {
	if d.clearAbandonMarkerIO != nil {
		return d.clearAbandonMarkerIO()
	}
	return health.ClearAbandon(d.cfg.BaseDir)
}

// restoreAbandonMarkerIfSuperseded puts back the marker abandonChild persisted for THIS
// process's own orphan, when the removal it runs at the tail of may have deleted it.
//
// It runs on clearAbandonMarker's bounded goroutine, and matters only when that goroutine
// outlived its deadline: the caller then returned and released abandonMu with the unlink still
// queued in the kernel, abandonChild acquired the lock and wrote the successor's marker, and
// this removal finally landed on top of it. The barrier cannot see that -- it guards the two
// WAITS, and nothing in userspace can recall a syscall the kernel is holding -- so the only
// remaining move is to notice afterwards and rewrite what was taken. Without it the successor
// daemon inherits nothing and beats green over a live orphan that still holds the backup lock.
//
// The record comes from an atomic pointer, not from under abandonMu, and that is deliberate: on
// the ordinary fast path this runs while its own caller still HOLDS that lock, so any acquire
// here would deadlock. A nil pointer -- no marker of our own was ever written -- is the fast
// path and the common case, and returns without touching anything.
func (d *daemon) restoreAbandonMarkerIfSuperseded() {
	rec := d.abandonRec.Load()
	if rec == nil {
		return
	}
	if err := health.WriteAbandon(d.cfg.BaseDir, *rec); err != nil {
		logging.Warning("daemon: an abandoned-child marker removal completed after the marker for pid=%d had been written, and restoring it failed (%v); the alive check will re-green after the restart", rec.PID, err)
		return
	}
	logging.Warning("daemon: an abandoned-child marker removal completed after the marker for pid=%d had been written; the marker has been restored so the next daemon still inherits the degrade", rec.PID)
}

// degradeAlive drives the SERVICE-ALIVE check DOWN on the way out of the process.
//
// It deliberately writes NOTHING to the local status file, unlike beat. The status file has
// exactly one field per kind that a reader consults for the alive sensor -- the KindHeartbeat
// record's TS and OK (health.sensorLevel, health.Diagnose) -- and a record written here would
// corrupt both: PingRecord.Down is read only for the backup and notify kinds, so a
// {OK:true, Down:true} heartbeat renders "ok"/green anyway, while its FRESH timestamp pushes
// the stale transition out by a whole heartbeat interval and keeps Diagnose answering
// DaemonUp=true -- for a daemon that is at that moment exiting. Worse, on a host with
// healthchecks disabled (heartbeatLoop never starts, so no heartbeat record has ever existed)
// it would fabricate the FIRST liveness record the file has ever held, flipping Diagnose from
// TxNoHeartbeat ("not running at all") to TxNotProvisioned ("up, not provisioned"). Writing
// nothing leaves the last real beat to age into TxStale, which is what actually happened.
// The DOWN signal rides the remote /fail, which is where the operator's alerting lives.
//
// There is no lazy centralized re-resolve here, unlike beat: the daemon is moments from
// exiting and must not block on a server fetch -- against, most likely, the same unreachable
// host -- ahead of the ping that matters. That is also why it must never be called with
// aliveMu held across a resolve; see the aliveMu field comment.
func (d *daemon) degradeAlive(ctx context.Context, r backupReporter, reason string) {
	done := logging.DebugStart(d.logger, "hc ping", "kind=%s", health.KindHeartbeat)
	var err error
	if r == nil {
		err = health.ErrNoAliveURL
	} else {
		err = r.AliveDegraded(ctx, reason+"; the daemon is abandoning it and restarting")
	}
	done(err)
	if health.IsNoURLErr(err) {
		logging.Debug("daemon: alive-degraded ping skipped (no url configured)")
	} else if err != nil {
		// err is already redacted by the Reporter (redactURLErr strips the url).
		logging.Debug("daemon: alive-degraded ping failed: %v", err)
	}
}

// killGrace is how long a child gets between SIGTERM and SIGKILL (os/exec's Cmd.WaitDelay).
func (d *daemon) killGrace() time.Duration {
	if d.killGraceOverride > 0 {
		return d.killGraceOverride
	}
	return daemonKillGrace
}

// reapWait is how long the daemon waits, from the moment the run context is done, for the
// child to be reaped before abandoning it. It covers the WHOLE SIGTERM -> SIGKILL grace plus
// daemonReapSlack, so it can never expire before the kill sequence it is waiting on has run
// its course; a child is only ever declared unreapable after the SIGKILL was actually sent.
func (d *daemon) reapWait() time.Duration {
	if d.reapWaitOverride > 0 {
		return d.reapWaitOverride
	}
	return d.killGrace() + daemonReapSlack
}

// aliveInterlockWait is daemonAliveInterlockWait with the test seam applied.
func (d *daemon) aliveInterlockWait() time.Duration {
	if d.aliveInterlockWaitOverride > 0 {
		return d.aliveInterlockWaitOverride
	}
	return daemonAliveInterlockWait
}

// A nil reporter means no ping URL was ever resolved (unpaired/centralized, or the
// server was down at startup with no cached backup.env URLs), so NOTHING can be
// transmitted. The helpers report that as ErrNoBackupURL (symmetric with beat's
// r==nil guard) so reportBestEffort's swallow-and-skip path excludes it and does NOT
// record a phantom RunFinished{OK:true} for a ping that never left the process.
func (d *daemon) startPing(ctx context.Context, r backupReporter, rid string) error {
	if r == nil {
		return health.ErrNoBackupURL
	}
	return r.RunStarted(ctx, rid)
}

func (d *daemon) finishPing(ctx context.Context, r backupReporter, rid string, code int, logTail string) error {
	if r == nil {
		return health.ErrNoBackupURL
	}
	return r.RunFinished(ctx, rid, code, logTail)
}

func (d *daemon) hangPing(ctx context.Context, r backupReporter, rid, logTail string) error {
	if r == nil {
		return health.ErrNoBackupURL
	}
	return r.RunHang(ctx, rid, d.maxRunDuration(), logTail)
}

// reportBestEffort runs one outcome ping (start/hang/finish), records its real
// transmission result to the shared status file, and never lets a down monitor
// break the daemon. An ErrNo*URL means no URL was resolved so nothing was
// transmitted: it is swallowed and NOT recorded (recording it would misreport a
// failed ping). Every other result, success included, is a genuine transmission
// attempt worth persisting so the run-side section can report the real state.
func (d *daemon) reportBestEffort(label string, failed bool, fn func() error) {
	d.reportBestEffortBounded(label, failed, 0, fn)
}

// reportBestEffortBounded is reportBestEffort with a deadline on the local status-file WRITE
// only. recordLimit <= 0 means unbounded, which is what every ordinary run wants: the write is
// a few hundred microseconds, and a goroutine left parked on statusMu would accumulate on a
// daemon that stays up for months. The abandon path passes a limit because it is the one
// caller running on a host whose I/O is known to be wedged, and it must reach the alive
// degrade (and return) even if BaseDir is on the same dead mount as the child. The ping itself
// has already been transmitted by then, so a timeout here costs only the local record.
func (d *daemon) reportBestEffortBounded(label string, failed bool, recordLimit time.Duration, fn func() error) {
	done := logging.DebugStart(d.logger, "hc ping", "kind=%s", label)
	err := fn()
	done(err)
	if errors.Is(err, health.ErrNoBackupURL) || errors.Is(err, health.ErrNoAliveURL) {
		logging.Debug("daemon: %s ping skipped (no url configured)", label)
		return
	}
	if err != nil {
		// err is already redacted by the Reporter (redactURLErr strips the url).
		logging.Debug("daemon: %s ping failed: %v", label, err)
	}
	// label is already the kind ("start"/"hang"/"finish" == KindRun*). failed is the OUTCOME
	// signal (a failed finish / any hang) so the local sensor renders red, not green (F09-02).
	if recordLimit <= 0 {
		d.recordOutcomePing(label, failed, err)
		return
	}
	if !runWithin(recordLimit, func() { d.recordOutcomePing(label, failed, err) }) {
		logging.Warning("daemon: recording the %s ping outcome did not complete within %s (is BASE_DIR on the wedged filesystem too?); the ping itself was sent",
			label, recordLimit)
	}
}

// recordPing persists one real transmission outcome to the shared status file,
// serialized by statusMu because the heartbeat loop and runOnce write it
// concurrently and health.RecordPing is a read-modify-write. Best effort: a write
// error must not break the daemon, so it is only logged at debug. The ping error
// text is already redacted by the Reporter, so this never leaks a URL or secret.
func (d *daemon) recordOutcomePing(kind string, failed bool, pingErr error) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	if err := health.RecordOutcomePing(d.cfg.BaseDir, d.cfg.HealthcheckMode, kind, d.now().Unix(), pingErr == nil, failed, pingErr); err != nil {
		logging.Debug("daemon: record %s ping status failed: %v", kind, err)
	}
}

// heartbeatLoop pings the service-alive check on a fixed interval (and once
// immediately). In centralized mode it lazily (re)resolves the ping URLs so a
// daemon that started while the server was down eventually reports liveness.
func (d *daemon) heartbeatLoop(ctx context.Context) {
	interval := d.cfg.HealthcheckHeartbeatInterval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	d.beat(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.beat(ctx)
		}
	}
}

func (d *daemon) beat(ctx context.Context) {
	// Silenced by an abandon in progress: the daemon is exiting and has already had the last
	// word on this check. Drop the tick whole -- no ping, and no record either, so nothing
	// here refreshes the liveness timestamp of a daemon that is dying.
	if d.aliveSilenced.Load() {
		return
	}
	// An inherited degrade is only as true as its premise. Re-check it before reporting it, so a
	// host that healed without a restart recovers on this beat rather than on the next scheduled
	// run. Outside aliveMu (it is a syscall on process state, not a transmission) and free on a
	// daemon that inherited nothing.
	d.reviewAbandonDegrade()
	// Resolve the URLs BEFORE taking aliveMu. buildReporter can block indefinitely on the
	// cross-process relay-secret flock, and aliveMu is on the abandon path: holding it across
	// this would let an unrelated process wedge runOnce, which is the exact failure mode this
	// whole change removes. Only the transmission below needs the lock.
	r := d.getReporter()
	if (r == nil || !r.HasAliveURL()) && d.cfg.HealthcheckMode == config.HealthcheckModeCentralized {
		if nr := d.buildReporter(ctx); nr != nil && nr.HasAliveURL() {
			d.setReporter(nr)
			r = nr
		}
	}
	// From here the hold is bounded: one ping (the reporter caps it at pingTimeout) plus one
	// status-file write. It exists so an abandon cannot interleave its /fail with a success
	// ping already on the wire and be re-greened by it; abandonChild waits for us, then
	// transmits last.
	d.aliveMu.Lock()
	defer d.aliveMu.Unlock()
	// Re-check under the lock: the abandon may have landed while we were resolving.
	if d.aliveSilenced.Load() {
		return
	}
	done := logging.DebugStart(d.logger, "hc ping", "kind=%s", health.KindHeartbeat)
	// A nil reporter means no alive URL was ever resolved. Surface that as
	// ErrNoAliveURL (instead of returning) so the beat is STILL recorded -- as a
	// liveness trace with reason no_url, not a false success. This is the whole point:
	// the run-side section must be able to tell "daemon up but not provisioned yet"
	// (a heartbeat record exists, OK=false, reason no_url) from "daemon not running at
	// all" (no heartbeat record). A running daemon records its first beat immediately.
	var err error
	switch {
	case r == nil:
		err = health.ErrNoAliveURL
	case d.aliveDegraded.Load():
		// An abandon inherited from a previous process is still outstanding, so this beat
		// reports the alive check DOWN instead of up: backups are dead and the check must say
		// so until one completes. It is still RECORDED like any other beat below -- this
		// daemon really is alive, and the local panel and health.Diagnose must keep saying so.
		// Only the remote signal is inverted.
		err = r.AliveDegraded(ctx, d.abandonNoteNow())
	default:
		err = r.Heartbeat(ctx)
	}
	done(err)
	if errors.Is(err, health.ErrNoAliveURL) {
		logging.Debug("daemon: heartbeat has no url yet (recording liveness, reason=no_url)")
	} else if err != nil {
		logging.Debug("daemon: heartbeat ping failed: %v", err)
	}
	// Always record: even a no-url beat proves the daemon is alive this tick. The heartbeat has
	// no outcome, so failed is always false.
	d.recordOutcomePing(health.KindHeartbeat, false, err)
}

// daemonEvaluateUpdate is the update-check seam: production uses checkForUpdates (a live
// GitHub fetch); tests override it to drive updateTick through the up-to-date / available /
// inconclusive transitions deterministically without network access.
var daemonEvaluateUpdate = checkForUpdates

// updateCheckLoop checks for a newer release on a fixed interval (and once immediately)
// and reports it to the "updates" check: /0 when up to date (green) or /1 when an update
// is available (the check goes DOWN so the user's alerts fire). It mirrors heartbeatLoop
// (immediate first tick + ticker) and, in centralized mode, lazily (re)resolves the
// reporter so a daemon paired after startup eventually reports.
func (d *daemon) updateCheckLoop(ctx context.Context) {
	interval := d.cfg.HealthcheckUpdateInterval
	if interval <= 0 {
		interval = defaultUpdateInterval
	}
	d.updateTick(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.updateTick(ctx)
		}
	}
}

// updateTick runs one update check, reports the /0-vs-/1 signal, and records the real
// transmission outcome. The operator-facing WARNING is throttled to once per transition
// into "available" (mirrors fetchWarned): checkForUpdates would WARN on every call, so it
// is handed a silenced logger (the same idiom the dashboard upgrade check uses) while the
// loop emits a single throttled WARNING, instead of spamming journald every ~5m.
func (d *daemon) updateTick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	info := daemonEvaluateUpdate(ctx, quietUpdateLogger(), version.String())
	available := info != nil && info.NewVersion
	latest := ""
	if info != nil {
		latest = strings.TrimSpace(info.Latest)
	}

	// checkForUpdates collapses "GitHub unreachable", rate-limit, and empty-latest into an
	// UpdateInfo with NewVersion:false and no Latest (main_update.go). A genuine "up to date"
	// result ALWAYS carries a non-empty Latest, so an empty Latest here means the check was
	// INCONCLUSIVE. Do not let a transient error flip a live /1 (update available) to /0
	// (green): that clears the monitor's DOWN state and flaps the alert until the operator
	// upgrades. Re-affirm the last persisted verdict instead, or skip if there is none yet.
	if latest == "" {
		prev, _ := health.LoadStatus(d.cfg.BaseDir)
		if prev.Update == nil {
			logging.Debug("daemon: update check inconclusive and no prior verdict; skipping ping")
			return
		}
		available = prev.Update.Available
		latest = prev.Update.Latest
	}

	// Throttle: warn the first tick an update becomes available, stay quiet while it
	// remains available, and reset when it clears so a later update warns again.
	d.mu.Lock()
	firstAvail := available && !d.updateWarned
	d.updateWarned = available
	d.mu.Unlock()
	switch {
	case firstAvail:
		logging.Warning("daemon: a newer ProxSave version is available (%s); run 'proxsave --upgrade' to install", orUnknownVersion(latest))
	case available:
		logging.Debug("daemon: update still available (%s)", orUnknownVersion(latest))
	default:
		logging.Debug("daemon: ProxSave is up to date")
	}

	r := d.getReporter()
	// In centralized mode, lazily (re)resolve until the updates URL is present, so a daemon
	// paired (or a server that adds the updates check) after startup eventually reports it.
	if (r == nil || !r.HasUpdatesURL()) && d.cfg.HealthcheckMode == config.HealthcheckModeCentralized {
		if nr := d.buildReporter(ctx); nr != nil && nr.HasUpdatesURL() {
			d.setReporter(nr)
			r = nr
		}
	}
	var perr error
	if r == nil {
		perr = health.ErrNoUpdatesURL
	} else {
		perr = r.ReportUpdate(ctx, available)
	}
	if errors.Is(perr, health.ErrNoUpdatesURL) {
		logging.Debug("daemon: updates ping has no url yet (recording, reason=no_url)")
	} else if perr != nil {
		logging.Debug("daemon: updates ping failed: %v", perr)
	}
	d.recordUpdate(available, latest, perr)
}

// recordUpdate persists one update-report outcome to the shared status file, serialized by
// statusMu (like recordPing) because the update loop and the heartbeat/run loops write the
// same file concurrently and health.RecordUpdate is a read-modify-write. Best effort: a
// write error must not break the daemon.
func (d *daemon) recordUpdate(available bool, latest string, pingErr error) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	if err := health.RecordUpdate(d.cfg.BaseDir, d.cfg.HealthcheckMode, d.now().Unix(), available, latest, pingErr == nil, pingErr); err != nil {
		logging.Debug("daemon: record updates ping status failed: %v", err)
	}
}

// quietUpdateLogger builds a discard logger for the periodic update check so
// checkForUpdates' own per-tick "new version available" WARNING (main_update.go) does not
// spam journald every ~5m; the loop emits ONE throttled warning itself. This is the same
// idiom the dashboard upgrade check uses (dashboard_upgrade.go) to silence checkForUpdates.
func quietUpdateLogger() *logging.Logger {
	lg := logging.New(types.LogLevelError, false)
	lg.SetOutput(io.Discard)
	return lg
}

// orUnknownVersion renders an empty version string as "unknown" for the update WARNING.
func orUnknownVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unknown"
	}
	return v
}

// buildReporter resolves the two ping URLs (centralized fetch from the server, or
// self-mode assembly) and returns a Reporter, or nil if nothing is reportable.
func (d *daemon) buildReporter(ctx context.Context) *health.Reporter {
	if !d.cfg.HealthcheckEnabled {
		return nil
	}
	if d.cfg.HealthcheckMode == config.HealthcheckModeSelf {
		alive, backup, checks := d.selfURLs()
		if alive == "" && backup == "" && len(checks) == 0 {
			logging.Warning("daemon: healthcheck self mode enabled but no ping URLs configured")
			return nil
		}
		d.registerReporterSecrets(alive, backup, checks)
		return health.NewReporter(health.Config{AliveURL: alive, BackupURL: backup, Checks: checks, SendLog: d.cfg.HealthcheckSendLog})
	}

	// centralized
	alive, backup, checks, secretUsed, err := d.fetchCentralized(ctx)
	if err != nil {
		// A DEFINITIVE rejection means the on-disk relay secret is no longer usable:
		//   ErrHCAuth   - the secret no longer matches the server's stored hash (a
		//                 server DB restore/rollback, or a lost double-issuance race);
		//   ErrHCParked - the server purged this host's row (its unused account was
		//                 parked, design 11.2), so the token authenticates nothing.
		// Both clear the secret so the next throttled maybeProvisionRelaySecret mints a
		// fresh one (a parked host is re-admitted as a recreation) - restoring the
		// self-heal. ONLY these two: a transient / unreachable / not-ready / unknown
		// error must NOT churn a possibly-good secret. The clear is value-guarded under
		// LockNotifySecret against the EXACT secret fetchCentralized used (secretUsed),
		// so a concurrent hook that persisted+confirmed a fresh secret is never
		// clobbered (which would strand the host).
		if errors.Is(err, health.ErrHCAuth) || errors.Is(err, health.ErrHCParked) {
			if cleared, rmErr := identity.RemoveNotifySecretIfMatches(d.cfg.BaseDir, secretUsed); rmErr != nil {
				logging.Debug("daemon: clear rejected relay secret failed: %v", rmErr)
			} else if cleared {
				logging.Debug("daemon: relay secret rejected by server (auth/parked); cleared for re-provisioning")
			} else {
				logging.Debug("daemon: relay secret rejected by server (auth/parked) but on-disk secret changed concurrently; keeping it")
			}
		}
		// The heartbeat loop retries this every interval; warn ONCE (so the
		// operator sees healthchecks isn't working, e.g. Telegram not paired yet),
		// then drop to Debug to avoid a recurring WARN every few minutes.
		d.mu.Lock()
		firstFail := !d.fetchWarned
		d.fetchWarned = true
		d.mu.Unlock()
		if firstFail {
			logging.Warning("daemon: healthcheck centralized fetch failed: %v", err)
		} else {
			logging.Debug("daemon: healthcheck centralized fetch failed: %v", err)
		}
		// Fall back to any URLs cached in backup.env so a transient server outage
		// still lets us report.
		alive, backup, checks = d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL, nil
	} else {
		d.mu.Lock()
		d.fetchWarned = false // recovered: allow a future failure to warn again
		d.mu.Unlock()
	}
	if alive == "" && backup == "" && len(checks) == 0 {
		return nil
	}
	d.registerReporterSecrets(alive, backup, checks)
	return health.NewReporter(health.Config{AliveURL: alive, BackupURL: backup, Checks: checks, SendLog: d.cfg.HealthcheckSendLog})
}

// registerReporterSecrets registers the alive/backup URLs plus every dynamic check URL as
// log secrets so a ping URL (which embeds the check UUID) never leaks into a log line.
func (d *daemon) registerReporterSecrets(alive, backup string, checks map[string]string) {
	secrets := []string{alive, backup}
	for _, u := range checks {
		secrets = append(secrets, u)
	}
	d.registerSecrets(secrets...)
}

// fetchCentralized asks the proxsave_server for this client's ping URLs, reusing
// the same identity/secret as /api/notify. The optional updates URL rides in the additive
// Checks map (absent on old servers -> "").
func (d *daemon) fetchCentralized(ctx context.Context) (alive, backup string, checks map[string]string, secretUsed string, err error) {
	secret, _ := identity.LoadNotifySecret(d.cfg.BaseDir)
	if strings.TrimSpace(secret) == "" {
		// No relay secret yet. Attempt a THROTTLED, Telegram-independent provisioning (hook b):
		// the server now issues the relay secret for a chat-less known ServerID. On success,
		// continue with the fresh secret; otherwise degrade gracefully (buildReporter warns once,
		// the heartbeat loop retries, and beats still record no_url).
		secret = d.maybeProvisionRelaySecret(ctx)
		if strings.TrimSpace(secret) == "" {
			return "", "", nil, "", fmt.Errorf("no relay secret on disk (centralized provisioning pending)")
		}
	}
	// Send the authoritative enabled-notification set so the server provisions one check per
	// enabled channel (Fase 2C). Always non-nil in centralized mode (empty -> "none" sentinel).
	channels := enabledNotifyChannels(d.cfg)
	// Return the exact secret sent to the server as secretUsed so buildReporter can
	// value-guard an ErrHCAuth secret removal against precisely this comparand.
	cfg, ferr := health.FetchCentralizedConfigWithChannels(ctx, nil, d.cfg.ServerAPIHost, d.cfg.ServerID, secret, false, channels)
	if ferr != nil {
		return "", "", nil, secret, ferr
	}
	return cfg.AliveURL, cfg.BackupURL, cfg.Checks, secret, nil
}

const (
	// daemonProvisionRetryInterval is the local retry floor: a persistent failure
	// must not hit the relay on every heartbeat interval.
	daemonProvisionRetryInterval = 15 * time.Minute
	// daemonProvisionMaxRetryJitter spreads clients released from the same global
	// admission window without materially extending the server's Retry-After.
	daemonProvisionMaxRetryJitter = 5 * time.Minute
)

// daemonProvisionRetryJitter is deterministic per server_id, so no shared random
// source or persisted state is needed. It uses up to 10% of Retry-After, capped at
// five minutes. A stable spread is sufficient to avoid a synchronized retry herd.
func daemonProvisionRetryJitter(serverID string, retryAfter time.Duration) time.Duration {
	window := retryAfter / 10
	if window > daemonProvisionMaxRetryJitter {
		window = daemonProvisionMaxRetryJitter
	}
	if window <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(serverID))
	// Mask to 63 bits so the sum is a valid non-negative int64 (the conversion cannot
	// overflow), then reduce it modulo the window in signed space. window is in
	// (0, 5min], so int64(window)+1 is a small positive divisor and the remainder is a
	// bounded Duration.
	return time.Duration(int64(h.Sum64()&math.MaxInt64) % (int64(window) + 1))
}

// provisionRelaySecretFn is the relay-secret provisioner seam (stubbed in tests so the
// self-heal logic is exercised without touching the network).
var provisionRelaySecretFn = notify.ProvisionRelaySecret

// provisionRelaySecretAttempt performs the best-effort attempt and also returns
// server-directed backpressure. It never propagates an error: Retry-After is the
// only semantic detail the daemon needs from a handled 429.
func provisionRelaySecretAttempt(
	ctx context.Context, cfg *config.Config, logger *logging.Logger,
) (string, time.Duration) {
	if cfg == nil || !cfg.HealthcheckEnabled || cfg.HealthcheckMode != config.HealthcheckModeCentralized {
		return "", 0
	}
	baseDir := strings.TrimSpace(cfg.BaseDir)
	if baseDir == "" {
		return "", 0
	}
	if s, _ := identity.LoadNotifySecret(baseDir); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s), 0 // already provisioned; do not churn
	}
	serverID := strings.TrimSpace(cfg.ServerID)
	if serverID == "" {
		return "", 0
	}
	if _, err := provisionRelaySecretFn(ctx, cfg.ServerAPIHost, serverID, baseDir, logger); err != nil {
		var limited *notify.RelayProvisionRateLimitError
		if errors.As(err, &limited) {
			logging.Debug(
				"daemon: relay-secret provisioning rate limited (server backoff honored)")
			return "", limited.RetryAfter
		}
		logging.Debug("daemon: relay-secret provisioning attempt failed (will retry later): %v", err)
		return "", 0
	}
	// Reload regardless of the provisioned flag: ProvisionRelaySecret returns false when it
	// adopts a secret a concurrent provisioner persisted under the cross-process lock, so a
	// usable secret may be on disk even then; LoadNotifySecret yields "" when there is
	// genuinely none, degrading gracefully.
	s, _ := identity.LoadNotifySecret(baseDir)
	return strings.TrimSpace(s), 0
}

// provisionRelaySecretBestEffort keeps the one-shot setup callers' established
// string-only contract. They run once and therefore do not need Retry-After.
func provisionRelaySecretBestEffort(
	ctx context.Context, cfg *config.Config, logger *logging.Logger,
) string {
	secret, _ := provisionRelaySecretAttempt(ctx, cfg, logger)
	return secret
}

// maybeProvisionRelaySecret is the daemon's throttled relay-secret self-heal (hook b). It
// returns a freshly persisted secret, or "" when throttled / not applicable / failed. The
// local retry floor is installed BEFORE the network call so concurrent heartbeat
// paths cannot duplicate an attempt. A longer server Retry-After extends that
// deadline and adds bounded deterministic jitter.
func (d *daemon) maybeProvisionRelaySecret(ctx context.Context) string {
	now := d.now()
	d.mu.Lock()
	if !d.provisionRetryAt.IsZero() && now.Before(d.provisionRetryAt) {
		d.mu.Unlock()
		return ""
	}
	d.provisionRetryAt = now.Add(daemonProvisionRetryInterval)
	d.mu.Unlock()

	secret, retryAfter := provisionRelaySecretAttempt(ctx, d.cfg, d.logger)
	if retryAfter > daemonProvisionRetryInterval {
		retryAt := d.now().Add(
			retryAfter + daemonProvisionRetryJitter(d.cfg.ServerID, retryAfter))
		d.mu.Lock()
		if retryAt.After(d.provisionRetryAt) {
			d.provisionRetryAt = retryAt
		}
		d.mu.Unlock()
	}
	return secret
}

// provisionRelaySecretOnDaemonSetup is the one-shot relay-secret self-heal (hook a) run during
// --daemon-setup / the upgrade migration, right after HEALTHCHECK_ENABLED=true is written. It
// re-reads the just-written config (the caller's cfg predates the write, so its
// HealthcheckEnabled is stale), resolves the ServerID from the identity file when backup.env
// carries none, and provisions the relay secret so a retrofitted centralized host obtains it
// WITHOUT Telegram pairing. Best-effort and non-blocking.
func provisionRelaySecretOnDaemonSetup(ctx context.Context, configPath, baseDir string) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return
	}
	cfg, err := config.LoadConfigWithBaseDir(configPath, baseDir)
	if err != nil || cfg == nil {
		logging.Debug("daemon-setup: relay-secret provisioning skipped: config reload failed: %v", err)
		return
	}
	if strings.TrimSpace(cfg.ServerID) == "" {
		// identity.DetectWithContext has a write side effect (creates .server_identity if
		// absent) - acceptable in this setup/write context.
		if info, derr := identity.DetectWithContext(ctx, baseDir, nil); derr == nil && info != nil {
			cfg.ServerID = strings.TrimSpace(info.ServerID)
		}
	}
	if provisionRelaySecretBestEffort(ctx, cfg, nil) != "" {
		logging.Info("Centralized monitoring: relay secret provisioned for this host.")
	}
}

// enabledNotifyChannels returns the lowercased notification-channel names enabled in cfg,
// sorted, for the ?channels provisioning hint. Metrics/Prometheus is a sink, not a
// notification channel, and is excluded. A non-nil (possibly empty) slice is always returned
// so the daemon sends an authoritative set (empty -> the server pauses all notify checks).
func enabledNotifyChannels(cfg *config.Config) []string {
	out := []string{}
	if cfg == nil {
		return out
	}
	if cfg.EmailEnabled {
		out = append(out, "email")
	}
	if cfg.TelegramEnabled {
		out = append(out, "telegram")
	}
	if cfg.GotifyEnabled {
		out = append(out, "gotify")
	}
	if cfg.WebhookEnabled {
		out = append(out, "webhook")
	}
	sort.Strings(out)
	return out
}

// selfURLs resolves the ping URLs from self-mode config: full URLs if given, otherwise
// assembled from the ping endpoint (+ optional ping key) and check IDs. The updates URL
// prefers an explicit full URL, else assembles from its own check ID.
func (d *daemon) selfURLs() (string, string, map[string]string) {
	base := strings.TrimRight(strings.TrimSpace(d.cfg.HealthcheckPingEndpoint), "/")
	build := func(id string) string {
		id = strings.TrimSpace(id)
		if base == "" || id == "" {
			return ""
		}
		if d.cfg.HealthcheckPingKey != "" {
			return base + "/" + d.cfg.HealthcheckPingKey + "/" + id
		}
		return base + "/" + id
	}
	checks := map[string]string{}
	updates := strings.TrimSpace(d.cfg.HealthcheckUpdatesURL)
	if updates == "" {
		updates = build(d.cfg.HealthcheckUpdatesID)
	}
	if updates != "" {
		checks[health.CheckKeyUpdates] = updates
	}
	// Per-notification-channel checks (self mode): full URL or assembled from a check ID.
	addNotify := func(ch, fullURL, id string) {
		u := strings.TrimSpace(fullURL)
		if u == "" {
			u = build(id)
		}
		if u != "" {
			checks[health.CheckKeyNotify(ch)] = u
		}
	}
	addNotify("email", d.cfg.HealthcheckNotifyEmailURL, d.cfg.HealthcheckNotifyEmailID)
	addNotify("telegram", d.cfg.HealthcheckNotifyTelegramURL, d.cfg.HealthcheckNotifyTelegramID)
	addNotify("gotify", d.cfg.HealthcheckNotifyGotifyURL, d.cfg.HealthcheckNotifyGotifyID)
	addNotify("webhook", d.cfg.HealthcheckNotifyWebhookURL, d.cfg.HealthcheckNotifyWebhookID)
	alive := strings.TrimSpace(d.cfg.HealthcheckAliveURL)
	if alive == "" {
		alive = build(d.cfg.HealthcheckAliveID)
	}
	backup := strings.TrimSpace(d.cfg.HealthcheckBackupURL)
	if backup == "" {
		backup = build(d.cfg.HealthcheckBackupID)
	}
	return alive, backup, checks
}

func (d *daemon) registerSecrets(urls ...string) {
	if d.logger == nil {
		return
	}
	for _, u := range urls {
		if strings.TrimSpace(u) != "" {
			d.logger.RegisterSecret(u)
		}
	}
}

// buildBackupCmd builds the supervised child: `proxsave --backup [--config ...]`.
// When tail is non-nil the child's combined output is mirrored into it (bounded)
// while still streaming to os.Std* (journald).
func (d *daemon) buildBackupCmd(ctx context.Context, tail *tailBuffer, rid string) *exec.Cmd {
	var cmd *exec.Cmd
	if d.newBackupCmd != nil {
		cmd = d.newBackupCmd(ctx)
	} else {
		args := []string{"--backup"}
		if strings.TrimSpace(d.configPath) != "" {
			args = append(args, "--config", d.configPath)
		}
		// #nosec G204 -- execPath is the running proxsave binary (os.Executable), args
		// are fixed literals; not user-controlled. safeexec's allowlist is for external
		// tools, not for re-executing self.
		cmd = exec.CommandContext(ctx, d.execPath, args...)
	}
	// Correlate the child's per-channel notify-results handoff with THIS run: the child
	// writes <baseDir>/identity/.notify_results.json tagged with this rid, and the daemon
	// rejects any file whose rid does not match. Preserve the inherited environment (PATH,
	// etc.) via os.Environ() so the child still finds its tools.
	if rid != "" {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(base, health.EnvRunID+"="+rid)
	}
	if tail != nil {
		cmd.Stdout = io.MultiWriter(os.Stdout, tail)
		cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	} else {
		if cmd.Stdout == nil {
			cmd.Stdout = os.Stdout
		}
		if cmd.Stderr == nil {
			cmd.Stderr = os.Stderr
		}
	}
	return cmd
}

// reportNotifyOutcomes pings one healthchecks check per notification channel the CHILD
// reported in its per-run handoff file, then records each outcome. It is driven by the FILE's
// channel set (what the child actually attempted), NOT the daemon's cached config, so a
// channel toggled off without a daemon restart never produces a false DOWN. A results file
// whose rid does not match this run (stale, or a child that crashed before Phase-7) is
// rejected: no pings, no flap. Best-effort throughout.
func (d *daemon) reportNotifyOutcomes(ctx context.Context, r backupReporter, rid string) {
	if !d.cfg.HealthcheckEnabled {
		return
	}
	nr, err := health.LoadNotifyResults(d.cfg.BaseDir)
	if err != nil {
		logging.Debug("daemon: read notify results failed: %v", err)
		return
	}
	if nr.RID != rid || len(nr.Results) == 0 {
		return // stale/missing file, or the child recorded nothing to report
	}

	// Resolve the reporter at most ONCE for the whole run: in centralized mode, if any
	// channel still lacks a resolved check URL, try a single rebuild (mirrors beat's single
	// re-resolve; never a per-channel re-fetch storm).
	if d.cfg.HealthcheckMode == config.HealthcheckModeCentralized && needsNotifyResolve(r, nr.Results) {
		if newR := d.buildReporter(ctx); newR != nil {
			d.setReporter(newR)
			r = newR
		}
	}

	names := make([]string, 0, len(nr.Results))
	for name := range nr.Results {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic ping/record order
	keep := make([]string, 0, len(names))
	for _, name := range names {
		suffix, down, skip := severityToSuffix(nr.Results[name])
		if skip {
			continue // "disabled"/unknown: the child did not really send this channel
		}
		key := health.CheckKeyNotify(name)
		keep = append(keep, key)
		var perr error
		if r == nil || !r.HasCheck(key) {
			perr = health.ErrNoPingURL // not provisioned yet (self/old server) -> no_url trace
		} else {
			perr = r.Ping(ctx, key, suffix, rid, "", key)
		}
		if perr != nil && !health.IsNoURLErr(perr) {
			logging.Debug("daemon: notify ping %s failed: %v", key, perr)
		}
		d.recordNotifyPing(key, down, perr)
	}
	// Prune notify rows for channels the child did NOT attempt this run (disabled/removed),
	// so a stale channel stops showing a phantom row. Only reached after the rid guard, so a
	// crashed/mismatched child never wipes a still-valid panel (F09-07).
	d.pruneNotifyRecords(keep)
}

// needsNotifyResolve reports whether any pingable channel in results lacks a resolved check
// URL, so reportNotifyOutcomes rebuilds the reporter at most once per run.
func needsNotifyResolve(r backupReporter, results map[string]string) bool {
	for name := range results {
		if _, _, skip := severityToSuffix(results[name]); skip {
			continue
		}
		if r == nil || !r.HasCheck(health.CheckKeyNotify(name)) {
			return true
		}
	}
	return false
}

// severityToSuffix maps a channel send severity to the ping suffix + Down signal. Per the
// chosen policy ANY imperfection (warning = a fallback was used, or error = the send failed)
// makes the check go DOWN (/1) so the user is alerted; only a clean "ok" is /0 (green).
// "disabled" and an unrecognized/empty severity are skipped (the child did not really send).
func severityToSuffix(severity string) (suffix string, down, skip bool) {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "ok":
		return "/0", false, false
	case "warning", "error":
		return "/1", true, false
	default: // "disabled", "", or anything unknown
		return "", false, true
	}
}

// recordNotifyPing persists one per-channel ping outcome, serialized by statusMu like the
// other status writers (the run/heartbeat/update loops share the file). Best-effort.
func (d *daemon) recordNotifyPing(key string, down bool, pingErr error) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	if err := health.RecordNotifyPing(d.cfg.BaseDir, d.cfg.HealthcheckMode, key, d.now().Unix(), pingErr == nil, down, pingErr); err != nil {
		logging.Debug("daemon: record notify ping status failed: %v", err)
	}
}

func (d *daemon) pruneNotifyRecords(keep []string) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	if err := health.PruneNotifyRecords(d.cfg.BaseDir, d.cfg.HealthcheckMode, keep); err != nil {
		logging.Debug("daemon: prune notify records failed: %v", err)
	}
}

func (d *daemon) maxRunDuration() time.Duration {
	if d.cfg.MaxRunDuration > 0 {
		return d.cfg.MaxRunDuration
	}
	return defaultMaxRunDuration
}

// tailBuffer is a bounded io.Writer keeping only the last max bytes written, used
// to capture the tail of a supervised backup's output for the outcome POST body.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func (d *daemon) getReporter() backupReporter {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reporter
}

func (d *daemon) setReporter(r backupReporter) {
	d.mu.Lock()
	d.reporter = r
	d.mu.Unlock()
}

// childReachedItsOwnExit reports whether a supervised run's error describes a child that
// really ran and reached a wait status, so the code exitCodeFromErr derives from it is the
// CHILD's and not one this process invented.
//
// It exists because exitCodeFromErr is lossy in the one direction that matters to the abandon
// mechanism: it maps every error carrying no wait status to 1, which is also the child's own
// "succeeded with warnings" code. That is right for ALERTING -- a child that could not be
// forked is a real failure -- and wrong as EVIDENCE, because exitProvesLockWasTaken reads a 1
// as "the run got past the backup lock". Only the caller still holds the error, so only the
// caller can tell the two apart; see runOnce.
//
// nil is the plain success. An *exec.ExitError is a child that ran and exited, whatever the
// code. Everything else -- a cmd.Start failure above all, but equally a pipe-copy fault that
// os/exec returns in place of the wait status -- means this process never saw the child reach
// an exit of its own, and answers nothing about the lock. That is the conservative direction:
// it keeps a marker, it never invents one.
func childReachedItsOwnExit(runErr error) bool {
	if runErr == nil {
		return true
	}
	var ee *exec.ExitError
	return errors.As(runErr, &ee)
}

// exitCodeFromErr extracts a process exit code: 0 on success, the child's code on
// a normal non-zero exit, and 1 when the child could not be started/run at all
// (which is a real failure worth alerting on). A caller that needs to know WHICH of those
// two a 1 is -- alerting does not, evidence does -- must ask childReachedItsOwnExit.
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// daemonSelfExecPath resolves the running binary to re-exec as the backup child.
func daemonSelfExecPath() string {
	if info := getExecInfo(); strings.TrimSpace(info.ExecPath) != "" {
		return info.ExecPath
	}
	if p, err := os.Executable(); err == nil && strings.TrimSpace(p) != "" {
		return p
	}
	return daemonExecPath
}
