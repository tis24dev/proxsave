// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/version"
)

const (
	// daemonKillGrace is how long a hung child gets between SIGTERM and SIGKILL.
	daemonKillGrace = 30 * time.Second
	// logTailBytes bounds the log excerpt POSTed with a non-success outcome.
	logTailBytes = 8 * 1024
	// defaultMaxRunDuration is the watchdog fallback when MAX_RUN_DURATION is unset.
	defaultMaxRunDuration = 6 * time.Hour
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

	mu           sync.Mutex
	reporter     backupReporter
	fetchWarned  bool // centralized fetch already warned once (throttle recurring WARN)
	updateWarned bool // an update is already known available (WARN once per transition)
	// newBackupCmd builds the child backup command; overridable in tests.
	newBackupCmd func(ctx context.Context) *exec.Cmd

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
	logging.Info("ProxSave daemon starting (run-at=%s max-run=%s healthcheck=%v mode=%s)",
		d.cfg.SchedulerTime, d.maxRunDuration(), d.cfg.HealthcheckEnabled, d.cfg.HealthcheckMode)
	return d.run(rt.ctx)
}

func (d *daemon) run(ctx context.Context) int {
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
	defer func() {
		if err := health.RemoveDaemonPID(d.cfg.BaseDir); err != nil {
			logging.Debug("daemon: remove pid file failed: %v", err)
		}
	}()

	if d.cfg.HealthcheckEnabled {
		if r := d.buildReporter(ctx); r != nil {
			d.setReporter(r)
		}
	}

	var wg sync.WaitGroup
	// The manual-outcome waker runs regardless of the heartbeat/update loops: it must receive
	// SIGUSR1 (so the default terminate action never fires) even when a piece of the healthcheck
	// wiring is off. processManualOutcome is itself a no-op when healthchecks are disabled or
	// nothing was handed off. It returns on ctx.Done() and joins the waitgroup; it does NOT
	// signal.Stop -- that is owned by run()'s defer above so the disposition is reverted only after
	// the pidfile is gone (see the signal.Stop comment).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-usr1:
				d.processManualOutcome(ctx)
			}
		}
	}()

	if d.cfg.HealthcheckEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.heartbeatLoop(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.updateCheckLoop(ctx)
		}()
	}

	d.scheduleLoop(ctx)
	wg.Wait()
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
	if (r == nil || !r.HasBackupURL()) && d.cfg.HealthcheckMode == "centralized" {
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
	d.recordPing(health.KindRunFinished, perr)

	if rmErr := health.RemoveManualOutcome(d.cfg.BaseDir); rmErr != nil {
		logging.Debug("daemon: remove manual outcome failed: %v", rmErr)
	}
}

// scheduleLoop waits for the next daily run time and supervises a backup, until
// the context is cancelled.
func (d *daemon) scheduleLoop(ctx context.Context) {
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
			return
		case <-timer.C:
			d.runOnce(ctx)
		}
	}
}

// runOnce launches ONE supervised backup as a child process under a hard timeout
// and reports the outcome. A child that exceeds the budget is SIGTERM'd, then
// SIGKILL'd, and reported as a hang.
func (d *daemon) runOnce(parentCtx context.Context) {
	if parentCtx.Err() != nil { // shutting down: do not start a run
		return
	}
	// Backups disabled: do NOT exec a child (it would exit 0 without backing up)
	// and do NOT ping an outcome, so the backup-outcome check honestly goes down
	// (no backups) rather than showing a false green. The alive heartbeat is
	// independent and keeps signalling the daemon is up.
	if !d.cfg.BackupEnabled {
		logging.Info("daemon: BACKUP_ENABLED=false; skipping the scheduled run (no outcome ping)")
		return
	}
	r := d.getReporter()
	rid := health.NewRunID()
	d.reportBestEffort("start", func() error { return d.startPing(parentCtx, r, rid) })

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
	cmd.WaitDelay = daemonKillGrace

	logging.Info("daemon: launching backup (rid=%s timeout=%s)", rid, d.maxRunDuration())
	runErr := cmd.Run()

	// Interrupted by shutdown, not a real outcome: stay silent so we don't flip
	// the check on a clean stop (the alive check going quiet signals the stop).
	if parentCtx.Err() != nil {
		return
	}

	logBody := ""
	if tail != nil {
		logBody = tail.String()
	}

	if runCtx.Err() == context.DeadlineExceeded {
		logging.Error("daemon: backup exceeded %s and was killed (hang)", d.maxRunDuration())
		d.reportBestEffort("hang", func() error { return d.hangPing(parentCtx, r, rid, logBody) })
		return
	}

	code := exitCodeFromErr(runErr)
	logging.Info("daemon: backup finished (rid=%s exit=%d)", rid, code)
	d.reportBestEffort("finish", func() error { return d.finishPing(parentCtx, r, rid, code, logBody) })

	// The child reached Phase-7 and wrote its per-channel notify outcomes; ping one
	// healthchecks check per channel it reported (Fase 2B / R4). Strictly after the child
	// exits, so it is naturally the last transmission of the run.
	d.reportNotifyOutcomes(parentCtx, r, rid)
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
func (d *daemon) reportBestEffort(label string, fn func() error) {
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
	// label is already the kind ("start"/"hang"/"finish" == KindRun*).
	d.recordPing(label, err)
}

// recordPing persists one real transmission outcome to the shared status file,
// serialized by statusMu because the heartbeat loop and runOnce write it
// concurrently and health.RecordPing is a read-modify-write. Best effort: a write
// error must not break the daemon, so it is only logged at debug. The ping error
// text is already redacted by the Reporter, so this never leaks a URL or secret.
func (d *daemon) recordPing(kind string, pingErr error) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	if err := health.RecordPing(d.cfg.BaseDir, d.cfg.HealthcheckMode, kind, d.now().Unix(), pingErr == nil, pingErr); err != nil {
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
	r := d.getReporter()
	if (r == nil || !r.HasAliveURL()) && d.cfg.HealthcheckMode == "centralized" {
		if nr := d.buildReporter(ctx); nr != nil && nr.HasAliveURL() {
			d.setReporter(nr)
			r = nr
		}
	}
	done := logging.DebugStart(d.logger, "hc ping", "kind=%s", health.KindHeartbeat)
	// A nil reporter means no alive URL was ever resolved. Surface that as
	// ErrNoAliveURL (instead of returning) so the beat is STILL recorded -- as a
	// liveness trace with reason no_url, not a false success. This is the whole point:
	// the run-side section must be able to tell "daemon up but not provisioned yet"
	// (a heartbeat record exists, OK=false, reason no_url) from "daemon not running at
	// all" (no heartbeat record). A running daemon records its first beat immediately.
	var err error
	if r == nil {
		err = health.ErrNoAliveURL
	} else {
		err = r.Heartbeat(ctx)
	}
	done(err)
	if errors.Is(err, health.ErrNoAliveURL) {
		logging.Debug("daemon: heartbeat has no url yet (recording liveness, reason=no_url)")
	} else if err != nil {
		logging.Debug("daemon: heartbeat ping failed: %v", err)
	}
	// Always record: even a no-url beat proves the daemon is alive this tick.
	d.recordPing(health.KindHeartbeat, err)
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
	if (r == nil || !r.HasUpdatesURL()) && d.cfg.HealthcheckMode == "centralized" {
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
	if d.cfg.HealthcheckMode == "self" {
		alive, backup, checks := d.selfURLs()
		if alive == "" && backup == "" && len(checks) == 0 {
			logging.Warning("daemon: healthcheck self mode enabled but no ping URLs configured")
			return nil
		}
		d.registerReporterSecrets(alive, backup, checks)
		return health.NewReporter(health.Config{AliveURL: alive, BackupURL: backup, Checks: checks, SendLog: d.cfg.HealthcheckSendLog})
	}

	// centralized
	alive, backup, checks, err := d.fetchCentralized(ctx)
	if err != nil {
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
func (d *daemon) fetchCentralized(ctx context.Context) (string, string, map[string]string, error) {
	secret, _ := identity.LoadNotifySecret(d.cfg.BaseDir)
	if strings.TrimSpace(secret) == "" {
		return "", "", nil, fmt.Errorf("no relay secret on disk (pair Telegram first)")
	}
	// Send the authoritative enabled-notification set so the server provisions one check per
	// enabled channel (Fase 2C). Always non-nil in centralized mode (empty -> "none" sentinel).
	channels := enabledNotifyChannels(d.cfg)
	cfg, err := health.FetchCentralizedConfigWithChannels(ctx, nil, d.cfg.ServerAPIHost, d.cfg.ServerID, secret, false, channels)
	if err != nil {
		return "", "", nil, err
	}
	return cfg.AliveURL, cfg.BackupURL, cfg.Checks, nil
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
	if d.cfg.HealthcheckAliveURL != "" || d.cfg.HealthcheckBackupURL != "" {
		return d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL, checks
	}
	return build(d.cfg.HealthcheckAliveID), build(d.cfg.HealthcheckBackupID), checks
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
	if d.cfg.HealthcheckMode == "centralized" && needsNotifyResolve(r, nr.Results) {
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
	for _, name := range names {
		suffix, down, skip := severityToSuffix(nr.Results[name])
		if skip {
			continue // "disabled"/unknown: the child did not really send this channel
		}
		key := health.CheckKeyNotify(name)
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

// exitCodeFromErr extracts a process exit code: 0 on success, the child's code on
// a normal non-zero exit, and 1 when the child could not be started/run at all
// (which is a real failure worth alerting on).
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
