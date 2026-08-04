// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/types"
)

// sigtermProofCmd builds a child that IGNORES SIGTERM, standing in for one parked in
// uninterruptible sleep. A real D-state task needs a wedged kernel path (a dead NFS mount),
// which no test can conjure in userspace, and SIGKILL always works on anything a test can
// spawn -- so the reap DEADLINE is what the tests drive, which is exactly the mechanism that
// was missing. From the daemon's side the two are indistinguishable: still there after the
// grace, with no exit status to report.
//
// The script must outlive the assertions but not the test binary; it exits on its own a few
// seconds later, so nothing is left running.
func sigtermProofCmd(seconds string) func(ctx context.Context) *exec.Cmd {
	return shCmd(`trap "" TERM; sleep ` + seconds)
}

// newAbandonDaemon builds a daemon whose child cannot be reaped inside the reap deadline.
// The kill grace is left at the PRODUCTION 30s on purpose: os/exec's own SIGKILL must not be
// able to land inside the test and reap the child, so what the test exercises is the daemon
// giving up, not the kernel rescuing it.
func newAbandonDaemon(t *testing.T, rep backupReporter, maxRun time.Duration) *daemon {
	t.Helper()
	d := newTestDaemon(t, rep, sigtermProofCmd("3"), maxRun)
	d.reapWaitOverride = 200 * time.Millisecond
	return d
}

// TestRunOnceAbandonsUnreapableChild is the whole point of the fix. os/exec's Cmd.Wait calls
// Process.Wait (wait4) BEFORE it reads the cancellation result, so a child that never dies
// leaves cmd.Run() blocked forever: the scheduler wedges, SIGTERM cannot stop the daemon, and
// the hang report sitting downstream of cmd.Run() is unreachable for exactly the case it
// documents. runOnce must instead give up, report the backup check DOWN and the alive check
// DOWN, and ask the caller to take the daemon down for a systemd restart.
func TestRunOnceAbandonsUnreapableChild(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	start := time.Now()
	abandoned := d.runOnce(context.Background())
	elapsed := time.Since(start)

	if !abandoned {
		t.Fatal("a child still unreaped past the deadline must be abandoned so the daemon can restart")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("runOnce stayed blocked on the unreapable child for %s", elapsed)
	}
	s := rep.snapshot()
	if s.hung != 1 || s.finished != 0 {
		t.Fatalf("abandoned run: hung=%d finished=%d, want 1/0", s.hung, s.finished)
	}
	if s.aliveDown != 1 {
		t.Fatalf("abandoning must drive the alive check DOWN exactly once, got %d (alive must not stay green while backups are dead)", s.aliveDown)
	}
}

// TestRunOnceAbandonRecordsTheBackupOutcomeOnly pins what the LOCAL status file may and may
// not say after an abandon. The backup outcome is recorded DOWN, as for any hang. The alive
// side is NOT recorded, and that is deliberate: PingRecord.Down is read only for the backup
// and notify kinds (health.SensorRows), so a heartbeat record carrying it would still render
// green -- while its fresh TS would keep health.Diagnose answering DaemonUp=true, and push
// the stale transition a whole heartbeat interval into the future, for a daemon that is
// exiting. Writing nothing lets the last real beat age into TxStale, which is the truth.
func TestRunOnceAbandonRecordsTheBackupOutcomeOnly(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if hang := st.Record(health.KindRunHang); hang == nil || !hang.Down {
		t.Fatalf("the backup-outcome record must be DOWN, got %+v", hang)
	}
	if hb := st.Record(health.KindHeartbeat); hb != nil {
		t.Fatalf("the abandon must not write a liveness record for a daemon that is exiting, got %+v", hb)
	}
}

// TestAbandonWithNoReporterWritesNoPhantomHeartbeat guards the same rule where it does real
// damage. With healthchecks disabled the heartbeat loop never runs, so the status file holds
// NO heartbeat record at all and health.Diagnose correctly answers TxNoHeartbeat / "the
// daemon is not running". A record written by the abandon path for a ping that never left the
// process would be the first one the file ever held, flipping that verdict to
// TxNotProvisioned / DaemonUp=true at the exact moment the daemon dies -- and it would break
// the same no-phantom-ping invariant reportBestEffort already enforces for the outcome pings.
func TestAbandonWithNoReporterWritesNoPhantomHeartbeat(t *testing.T) {
	d := newAbandonDaemon(t, nil, 150*time.Millisecond)
	d.cfg.HealthcheckEnabled = false

	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if hb := st.Record(health.KindHeartbeat); hb != nil {
		t.Fatalf("a ping that never left the process must not be persisted, got %+v", hb)
	}
	if dg := health.Diagnose(st, 5*time.Minute, time.Now()); dg.DaemonUp {
		t.Fatalf("the abandon must not fabricate liveness, got DaemonUp=true state=%v", dg.State)
	}
}

// TestRunOnceDoesNotAbandonAChildThatOnlyDiesOnSIGKILL is the other half of the contract, and
// the reason the reap deadline is anchored on the run context being done rather than on the
// watchdog budget. This child ignores SIGTERM and dies only when os/exec's WaitDelay SIGKILL
// lands -- late, but inside the window. It is reaped, so it is an ORDINARY hang: report it,
// leave the alive check alone, and keep the daemon running.
func TestRunOnceDoesNotAbandonAChildThatOnlyDiesOnSIGKILL(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newTestDaemon(t, rep, sigtermProofCmd("5"), 100*time.Millisecond)
	d.killGraceOverride = 200 * time.Millisecond // SIGKILL lands at ~300ms
	d.reapWaitOverride = 3 * time.Second         // ...well inside the give-up window

	if d.runOnce(context.Background()) {
		t.Fatal("a child that died late but DIED must not be abandoned (no daemon exit)")
	}
	s := rep.snapshot()
	if s.hung != 1 || s.finished != 0 {
		t.Fatalf("late-dying run: hung=%d finished=%d, want 1/0", s.hung, s.finished)
	}
	if s.aliveDown != 0 {
		t.Fatalf("an ordinary hang must not degrade the alive check, got aliveDown=%d", s.aliveDown)
	}
}

// TestRunOnceOrdinaryHangDoesNotAbandon guards the blast radius on the common path, with the
// PRODUCTION graces: a child that overruns its budget and dies on the SIGTERM is still just a
// hang. It must not degrade the alive check and must not take the daemon down with it.
func TestRunOnceOrdinaryHangDoesNotAbandon(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newTestDaemon(t, rep, shCmd("sleep 5"), 150*time.Millisecond)

	if d.runOnce(context.Background()) {
		t.Fatal("a hung child that actually died must not tear the daemon down")
	}
	s := rep.snapshot()
	if s.hung != 1 || s.aliveDown != 0 {
		t.Fatalf("ordinary hang: hung=%d aliveDown=%d, want 1/0", s.hung, s.aliveDown)
	}
}

// TestRunOnceAbandonDuringShutdownStaysSilent pins the asymmetry: when the abandon happens
// because we are already stopping, the existing silence rule wins -- no outcome ping may flip
// a check on a clean stop -- and no restart is requested. runOnce must still RETURN, which
// today it would not: it would sit in cmd.Wait until systemd's TimeoutStopSec SIGKILLed the
// daemon.
func TestRunOnceAbandonDuringShutdownStaysSilent(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, time.Hour) // an hour-long budget: only the cancel can end the run
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	if d.runOnce(ctx) {
		t.Fatal("a shutdown-time abandon must not request a restart; the daemon is already exiting")
	}
	// The give-up is ~300ms after the cancel. The bound is deliberately tight: a runOnce that
	// only returns when the child happens to die anyway is the pre-fix behaviour, and there it
	// is systemd's TimeoutStopSec, not the daemon, that ends the stop.
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("runOnce did not give up on the unreapable child during shutdown (%s); the stop is at systemd's mercy", elapsed)
	}
	s := rep.snapshot()
	if s.started != 1 {
		t.Fatalf("the start ping fires before the child runs, got started=%d", s.started)
	}
	if s.hung != 0 || s.finished != 0 || s.aliveDown != 0 {
		t.Fatalf("a stop must flip no check, got hung=%d finished=%d aliveDown=%d", s.hung, s.finished, s.aliveDown)
	}
}

// TestShutdownAbandonStillHandsTheDegradeToTheNextProcess covers the path an operator
// actually takes. Someone who notices a wedged daemon runs `systemctl restart`: the context is
// cancelled, the reap deadline expires, and the abandon happens on the SHUTDOWN branch -- which
// pings nothing, correctly, because no check may be flipped on a clean stop. But the marker is
// not a ping. It transmits nothing during the stop; it is the only way the process systemd
// starts ten seconds later learns that an unreapable orphan is still holding the backup lock.
// Without it that successor comes up fully green over dead backups, sends a SUCCESS heartbeat
// immediately, and every scheduled run exits ExitBackupSkipped without pinging anything -- the
// exact false green this whole change exists to remove, reached by its most common route.
func TestShutdownAbandonStillHandsTheDegradeToTheNextProcess(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, time.Hour) // only the cancel can end this run
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)

	if d.runOnce(ctx) {
		t.Fatal("a shutdown-time abandon must not request a restart")
	}
	if s := rep.snapshot(); s.hung != 0 || s.finished != 0 || s.aliveDown != 0 {
		t.Fatalf("a stop must still flip no check, got hung=%d finished=%d aliveDown=%d", s.hung, s.finished, s.aliveDown)
	}

	rec, err := health.ReadAbandon(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("ReadAbandon: %v", err)
	}
	if rec == nil {
		t.Fatal("a shutdown-time abandon left no marker; the daemon systemd restarts comes up green over an orphan that still holds the backup lock")
	}
	if rec.PID <= 0 {
		t.Fatalf("the marker must name the orphan an operator has to hunt for, got %+v", rec)
	}

	next := &fakeReporter{alive: true, backupURL: true}
	restarted := restartedDaemon(t, d, next)
	if !restarted.aliveDegraded.Load() {
		t.Fatal("the restarted daemon did not inherit the shutdown-time abandon")
	}
	restarted.beat(context.Background())
	if s := next.snapshot(); s.beats != 0 || s.aliveDown != 1 {
		t.Fatalf("the successor must keep the alive check DOWN, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
}

// TestAbandonPingsTheBackupCheckBeforeWritingTheMarker pins the ordering the marker write must
// never invert. That write is os.MkdirAll + os.WriteFile + os.Rename against BaseDir, and this
// path only ever runs on a host with I/O the kernel will not let go of -- a BaseDir on that
// same dead mount blocks there exactly as the child did. Ordered first, it would cost both
// mandated signals; ordered after the /fail, the primary report is already on the wire.
func TestAbandonPingsTheBackupCheckBeforeWritingTheMarker(t *testing.T) {
	rep := &blockingHangReporter{entered: make(chan struct{}), release: make(chan struct{})}
	rep.alive, rep.backupURL = true, true
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	done := make(chan bool, 1)
	go func() { done <- d.runOnce(context.Background()) }()

	select {
	case <-rep.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the hang ping was never attempted")
	}
	// The /fail is on the wire and has not returned. Nothing local may have run before it.
	if rec, err := health.ReadAbandon(d.cfg.BaseDir); err != nil || rec != nil {
		t.Fatalf("the marker was written before the backup /fail was sent (rec=%+v err=%v); a wedged BaseDir would swallow the primary signal", rec, err)
	}
	close(rep.release)

	select {
	case abandoned := <-done:
		if !abandoned {
			t.Fatal("expected the run to be abandoned")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("runOnce never returned")
	}
	if rec, err := health.ReadAbandon(d.cfg.BaseDir); err != nil || rec == nil {
		t.Fatalf("the marker must still be written after the ping (rec=%+v err=%v)", rec, err)
	}
}

// blockingHangReporter parks the backup /fail inside its transmission until the test releases
// it, which is what makes "the ping is not gated on local disk" observable.
type blockingHangReporter struct {
	fakeReporter
	entered chan struct{}
	release chan struct{}
}

func (b *blockingHangReporter) RunHang(ctx context.Context, rid string, timeout time.Duration, tail string) error {
	close(b.entered)
	<-b.release
	return b.fakeReporter.RunHang(ctx, rid, timeout, tail)
}

// TestRunWithinAbandonsACallThatNeverReturns is the unit-level guarantee behind that ordering:
// a local write that never comes back must not be able to stop the abandon path. Nothing in
// userspace can cancel a syscall parked in the kernel, so the call is abandoned, not
// interrupted -- acceptable only because every caller is moments from exiting the process.
func TestRunWithinAbandonsACallThatNeverReturns(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	if runWithin(150*time.Millisecond, func() { <-release }) {
		t.Fatal("runWithin claimed a call that never returned had finished")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("runWithin waited %s on a wedged call; the deadline is not a deadline", elapsed)
	}
	if !runWithin(5*time.Second, func() {}) {
		t.Fatal("runWithin must report a call that did finish")
	}
}

// TestBeatIsSuppressedWhileTheDaemonIsExiting pins the silence latch: heartbeatLoop is a live
// goroutine throughout the abandon, and one success beat from it would re-green the alive
// check before the process even exits.
func TestBeatIsSuppressedWhileTheDaemonIsExiting(t *testing.T) {
	rep := &fakeReporter{alive: true}
	d := newTestDaemon(t, rep, nil, time.Hour)
	d.cfg.HealthcheckMode = "self" // no centralized rebuild, no network
	d.aliveSilenced.Store(true)

	d.beat(context.Background())

	if s := rep.snapshot(); s.beats != 0 || s.aliveDown != 0 {
		t.Fatalf("an exiting daemon must transmit nothing more on the alive check, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if rec := st.Record(health.KindHeartbeat); rec != nil {
		t.Fatalf("a suppressed beat must not record over the degraded one, got %+v", rec)
	}
}

// blockingBeatReporter parks the FIRST heartbeat inside its transmission until the test
// releases it, and logs the order in which the alive check's transmissions actually complete.
// It is what makes the latch's race observable: a bool read once at the top of beat() cannot
// order two concurrent POSTs, and the losing order is the one that re-greens a dead host.
type blockingBeatReporter struct {
	fakeReporter
	once    sync.Once
	entered chan struct{} // closed once a beat is inside Heartbeat, past the latch check
	release chan struct{} // closed by the test to let that beat finish transmitting

	orderMu sync.Mutex
	order   []string
}

func (b *blockingBeatReporter) Heartbeat(ctx context.Context) error {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	b.note("beat")
	return b.fakeReporter.Heartbeat(ctx)
}

func (b *blockingBeatReporter) AliveDegraded(ctx context.Context, reason string) error {
	b.note("degrade")
	return b.fakeReporter.AliveDegraded(ctx, reason)
}

func (b *blockingBeatReporter) note(what string) {
	b.orderMu.Lock()
	defer b.orderMu.Unlock()
	b.order = append(b.order, what)
}

func (b *blockingBeatReporter) transmissions() []string {
	b.orderMu.Lock()
	defer b.orderMu.Unlock()
	return append([]string(nil), b.order...)
}

// TestAbandonWaitsForAnInFlightBeat is the regression test for the race a bare latch cannot
// close. heartbeatLoop is live for the whole abandon, and beat() reads the latch ONCE and then
// spends up to a full ping timeout inside the transmission. A beat that entered before the
// latch closed must not be allowed to deliver its SUCCESS ping after the degrade's /fail: on
// the monitor that ping re-greens the alive check, and a green service over dead backups is
// the exact misreport the degrade exists to prevent. So the abandon has to WAIT for the
// in-flight beat and transmit last.
//
// The wait is on the ALIVE degrade only, and it is deadline-capped; the backup /fail has
// already gone out by this point (TestAbandonPingsTheBackupCheckBeforeTouchingTheAliveInterlock)
// and runOnce returns even if the lock never comes free
// (TestAbandonReportsEvenWhenTheHeartbeatInterlockIsStuck).
func TestAbandonWaitsForAnInFlightBeat(t *testing.T) {
	rep := &blockingBeatReporter{entered: make(chan struct{}), release: make(chan struct{})}
	rep.alive, rep.backupURL = true, true
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	beatDone := make(chan struct{})
	go func() { defer close(beatDone); d.beat(context.Background()) }()
	<-rep.entered // the beat is now INSIDE the transmission, past the latch check

	abandonDone := make(chan bool, 1)
	go func() { abandonDone <- d.runOnce(context.Background()) }()

	// runOnce reaches the abandon in ~350ms (budget + reap deadline). It must still not have
	// reported: the beat it would be overtaken by is on the wire.
	select {
	case <-abandonDone:
		t.Fatal("the abandon reported while a heartbeat was still in flight; that beat lands after the /fail and re-greens the alive check")
	case <-time.After(900 * time.Millisecond):
	}

	close(rep.release)
	<-beatDone
	select {
	case abandoned := <-abandonDone:
		if !abandoned {
			t.Fatal("expected the run to be abandoned")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the abandon never completed after the beat was released")
	}

	got := rep.transmissions()
	if len(got) == 0 || got[len(got)-1] != "degrade" {
		t.Fatalf("the alive check's LAST transmission must be the degrade /fail, got %v", got)
	}
	if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 1 {
		t.Fatalf("beats=%d aliveDown=%d, want 1/1", s.beats, s.aliveDown)
	}
	// And the beat that was already on the wire must not have left a record behind either:
	// the abandon writes no liveness record, so whatever the beat wrote is the last local
	// word. It is a real beat, so it is allowed to stand -- but it must not be DOWN-flavoured
	// noise from a suppressed tick.
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if hb := st.Record(health.KindHeartbeat); hb == nil || hb.Down {
		t.Fatalf("the in-flight beat's own record must stand unmodified, got %+v", hb)
	}
}

// TestBeatAfterDegradeIsDroppedWhole pairs with the test above: once the latch is closed under
// aliveMu, every later beat is dropped entirely -- no ping, and no record that would refresh
// the liveness timestamp of a daemon on its way out.
func TestBeatAfterDegradeIsDroppedWhole(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}
	d.beat(context.Background())

	if s := rep.snapshot(); s.beats != 0 {
		t.Fatalf("a beat after the degrade must not re-green the alive check, got %d beats", s.beats)
	}
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if hb := st.Record(health.KindHeartbeat); hb != nil {
		t.Fatalf("a suppressed beat must record nothing, got %+v", hb)
	}
}

// restartedDaemon is the daemon systemd brings back ten seconds after the abandoning one
// exited: a FRESH struct (runDaemon builds one per process, so every in-memory latch starts
// false) over the SAME BaseDir. It is the only way to observe what the second process
// actually reports, which is where the degrade either survives or is silently undone.
func restartedDaemon(t *testing.T, prev *daemon, rep backupReporter) *daemon {
	t.Helper()
	d := newTestDaemon(t, rep, nil, time.Hour)
	d.cfg.BaseDir = prev.cfg.BaseDir
	d.cfg.HealthcheckMode = "self" // no centralized rebuild, no network
	// The orphan is still wedged -- the premise of every test that uses this helper. Pinned
	// instead of left to the real kill(2) probe because the stand-in child a test can spawn
	// exits on its own a few seconds in, which would otherwise make these assertions depend on
	// how long the preceding lines happened to take.
	d.pidAliveOverride = func(int) bool { return true }
	d.loadAbandonMarker() // what run() does before any loop starts
	return d
}

// TestAbandonSurvivesTheRestart is the regression test for the window the in-memory latch
// cannot cover. The abandon exits the process on purpose (Restart=always / RestartSec=10), and
// heartbeatLoop beats IMMEDIATELY on start, before its ticker -- so an in-memory-only degrade
// is reversed about ten seconds after it was sent, flipping the alive check back UP and firing
// a "recovered" alert while the orphan still holds the backup lock and every scheduled run
// exits ExitBackupSkipped without pinging. The restarted daemon must inherit the degrade and
// keep reporting the alive check DOWN.
func TestAbandonSurvivesTheRestart(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	second := &fakeReporter{alive: true, backupURL: true}
	restarted := restartedDaemon(t, d, second)
	if !restarted.aliveDegraded.Load() {
		t.Fatal("the restarted daemon did not inherit the abandon; its first beat re-greens a host whose backups are dead")
	}
	restarted.beat(context.Background())

	s := second.snapshot()
	if s.beats != 0 {
		t.Fatalf("the restarted daemon sent %d success heartbeat(s); alive must not go green while backups are dead", s.beats)
	}
	if s.aliveDown != 1 {
		t.Fatalf("the restarted daemon must keep reporting the alive check DOWN, got aliveDown=%d", s.aliveDown)
	}
	// ...and it must still record its OWN liveness locally: this daemon really is running, and
	// the run-side panel / health.Diagnose read that record. Only the REMOTE signal is
	// inverted; suppressing the record too would make a live daemon look dead locally.
	st, err := health.LoadStatus(restarted.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if hb := st.Record(health.KindHeartbeat); hb == nil || !hb.OK {
		t.Fatalf("a degraded but running daemon must still record liveness, got %+v", hb)
	}
	if dg := health.Diagnose(st, 5*time.Minute, time.Now()); !dg.DaemonUp {
		t.Fatalf("the restarted daemon is up and must diagnose as up, got state=%v", dg.State)
	}
}

// TestCompletedRunClearsTheInheritedDegrade is the other half: the degrade must not be
// permanent, or the alive check is red forever on a host the operator has already fixed. A run
// that reaches an exit code proves the orphan no longer holds the backup lock and the daemon
// can supervise a child again, so it lifts the degrade -- marker and all, so a further restart
// does not resurrect it. A SKIP does not, because a skip is the signature of the orphan still
// holding that lock.
func TestCompletedRunClearsTheInheritedDegrade(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	skipped := restartedDaemon(t, d, &fakeReporter{alive: true, backupURL: true})
	// The orphan died after this daemon inherited the degrade and before any beat could review
	// it, so the probe now says "gone" and the skip rule is the ONLY thing left standing between
	// this run and a lift. Without that, the assertion below passes on the probe alone and stops
	// testing the rule it names.
	skipped.pidAliveOverride = func(int) bool { return false }
	skipped.newBackupCmd = shCmd("exit " + strconv.Itoa(types.ExitBackupSkipped.Int()))
	skipped.runOnce(context.Background())
	if !skipped.aliveDegraded.Load() {
		t.Fatal("a skipped run performed no backup and proves nothing; only a run that reached the lock may lift the degrade")
	}

	rep := &fakeReporter{alive: true, backupURL: true}
	recovered := restartedDaemon(t, d, rep)
	// The host healed while this daemon was up: the mount came back and the orphan finally
	// died. Only then can a completed run prove the wedge is over -- the exit code alone does
	// not, because the backup lock is checked after the pre-flight gates a dead mount fails.
	// See clearAbandonMarkerOnCompletedRun.
	recovered.pidAliveOverride = func(int) bool { return false }
	recovered.newBackupCmd = shCmd("exit 0")
	recovered.runOnce(context.Background())

	if recovered.aliveDegraded.Load() {
		t.Fatal("a completed backup must lift the degrade, or the alive check stays red on a healed host")
	}
	if rec, err := health.ReadAbandon(recovered.cfg.BaseDir); err != nil || rec != nil {
		t.Fatalf("the marker must be gone so a later restart does not resurrect the degrade (rec=%+v err=%v)", rec, err)
	}
	recovered.beat(context.Background())
	if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 0 {
		t.Fatalf("after recovery the beat must be a plain success ping, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
}

// TestStandaloneBackupLiftsTheInheritedDegrade covers the operator's most natural remediation.
// After fixing the host they PROVE it with `proxsave --backup`, whose outcome is handed off by
// SIGUSR1 and pinged by processManualOutcome -- the same finish machinery a supervised run
// uses. If that path does not lift the degrade, the backup check goes GREEN off the handoff
// while the alive check keeps sending /fail, and the monitor reads "service dead, backups
// fine" until the next scheduled run, up to a full scheduling period away.
func TestStandaloneBackupLiftsTheInheritedDegrade(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	rep := &fakeReporter{alive: true, backupURL: true}
	restarted := restartedDaemon(t, d, rep)
	restarted.cfg.HealthcheckEnabled = true
	if !restarted.aliveDegraded.Load() {
		t.Fatal("the restarted daemon must inherit the degrade")
	}

	// The operator fixed the host first: the orphan is gone by the time they prove it with a
	// backup. Without that, a non-zero exit from a pre-lock gate would be enough to lift the
	// degrade -- see TestCompletedRunOverALiveOrphanKeepsTheDegrade.
	restarted.pidAliveOverride = func(int) bool { return false }

	if err := health.WriteManualOutcome(restarted.cfg.BaseDir, "rid-manual", time.Now().Unix(), 0); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	restarted.processManualOutcome(context.Background())

	if restarted.aliveDegraded.Load() {
		t.Fatal("a completed standalone backup must lift the degrade; otherwise the monitor says 'service dead, backups fine'")
	}
	if rec, err := health.ReadAbandon(restarted.cfg.BaseDir); err != nil || rec != nil {
		t.Fatalf("the marker must be gone so a later restart does not resurrect the degrade (rec=%+v err=%v)", rec, err)
	}
	restarted.beat(context.Background())
	if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 0 {
		t.Fatalf("after the handoff the beat must be a plain success ping, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
}

// TestCompletedRunOverALiveOrphanKeepsTheDegrade is the counterweight to
// TestCompletedRunClearsTheInheritedDegrade, and it covers the gap three rounds of review
// walked past.
//
// A run reaching a real exit code does NOT prove it took the backup lock. The lock is checked
// LAST (internal/orchestrator/orchestrator.go, "4. Check lock file LAST"), after the
// directory, temp-dir, disk-space and permission gates -- and a dead NFS/CIFS mount, the very
// fault that parks a child in D state, is what fails those gates first. So the operator's
// `proxsave --backup`, run precisely to see what is wrong, dies on the disk-space check with a
// non-zero code without ever reaching the orphan's lock. If that lifted the degrade, the alive
// check would go GREEN over an orphan that has not moved -- the "recovered" alert this whole
// mechanism exists to prevent.
func TestCompletedRunOverALiveOrphanKeepsTheDegrade(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	// Supervised half. restartedDaemon pins the orphan as still wedged, which is the premise.
	rep := &fakeReporter{alive: true, backupURL: true}
	restarted := restartedDaemon(t, d, rep)
	restarted.newBackupCmd = shCmd("exit " + strconv.Itoa(types.ExitBackupError.Int()))
	restarted.runOnce(context.Background())

	if !restarted.aliveDegraded.Load() {
		t.Fatal("a run that failed before the lock check proves nothing about the orphan; the degrade must stand")
	}
	if rec, err := health.ReadAbandon(restarted.cfg.BaseDir); err != nil || rec == nil {
		t.Fatalf("the marker must survive so a later restart still inherits the degrade (rec=%+v err=%v)", rec, err)
	}

	// Standalone half: the same rule through the SIGUSR1 handoff.
	manualRep := &fakeReporter{alive: true, backupURL: true}
	manual := restartedDaemon(t, d, manualRep)
	manual.cfg.HealthcheckEnabled = true
	if err := health.WriteManualOutcome(manual.cfg.BaseDir, "rid-early-fail", time.Now().Unix(), types.ExitBackupError.Int()); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	manual.processManualOutcome(context.Background())

	if !manual.aliveDegraded.Load() {
		t.Fatal("a standalone run that failed before the lock check must not lift the degrade either")
	}
}

// TestTheKeptDegradeStopsClaimingNoBackupHasCompleted covers the BODY of the signal the test
// above pins the existence of.
//
// Keeping the degrade over a live orphan is right, and it is also the one state in which the
// note written at startup ages into a falsehood: it says "no backup has completed since", the
// beat repeats it verbatim on every heartbeat, and by then a supervised run has completed. The
// operator reading that alert is sent hunting a backup failure that has already stopped
// happening instead of the D-state task that is actually holding the check down. The degrade
// survives here for the orphan's sake, so the orphan is what it must say.
func TestTheKeptDegradeStopsClaimingNoBackupHasCompleted(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	rep := &fakeReporter{alive: true, backupURL: true}
	restarted := restartedDaemon(t, d, rep)
	restarted.cfg.HealthcheckEnabled = true

	restarted.beat(context.Background())
	if s := rep.snapshot(); !strings.Contains(s.lastAliveReason, "no backup has completed since") {
		t.Fatalf("before any run the inherited note is the honest one, got %q", s.lastAliveReason)
	}

	// A backup now completes end to end while the orphan is still parked in D state -- the
	// mount healed, the task did not. The degrade stands (the orphan may still hold its lock),
	// but the claim that no backup has completed does not.
	restarted.newBackupCmd = shCmd("exit 0")
	restarted.runOnce(context.Background())
	if !restarted.aliveDegraded.Load() {
		t.Fatal("the orphan is still on the host; the degrade must stand")
	}

	restarted.beat(context.Background())
	s := rep.snapshot()
	if strings.Contains(s.lastAliveReason, "no backup has completed since") {
		t.Fatalf("the alive /fail still tells the operator no backup has completed, after one did: %q", s.lastAliveReason)
	}
	if !strings.Contains(s.lastAliveReason, "still on this host") {
		t.Fatalf("the body must name what is actually still true -- the unreapable orphan -- got %q", s.lastAliveReason)
	}
}

// TestMarkerFromBeforeThisBootIsDiscarded covers what the boot-generation check is worth once
// the (pid, starttime) probe answers the recycled-number case on its own: ORDERING. A record
// from a previous boot must not be kept "for when backups are re-enabled", because re-enabling
// backups cannot make a process that no longer exists relevant again -- and the BACKUP_ENABLED
// branch, which sits below this one, would keep it on a daemon that is otherwise healthy.
//
// It runs the REAL probe against a pid the test already reaped, so btime is only ever
// CONFIRMING what the identity check independently says. A btime that DISAGREED with a live
// orphan may not retire anything; see TestAForwardClockStepMayNotRetireALiveOrphansMarker.
func TestMarkerFromBeforeThisBootIsDiscarded(t *testing.T) {
	base := t.TempDir()
	reaped := exec.Command("/bin/sh", "-c", "exit 0")
	if err := reaped.Run(); err != nil {
		t.Fatalf("run the throwaway child: %v", err)
	}
	dead := reaped.Process.Pid // exited AND waited on: this pid is gone

	if err := health.WriteAbandon(base, health.AbandonRecord{PID: dead, RID: "rid-old", TS: time.Now().Unix()}); err != nil {
		t.Fatalf("WriteAbandon: %v", err)
	}
	rep := &fakeReporter{alive: true, backupURL: true}
	rebooted := newTestDaemon(t, rep, nil, time.Hour)
	rebooted.cfg.BaseDir = base
	rebooted.cfg.HealthcheckMode = "self"
	// Backups were turned off after the abandon -- the reaction the ERROR that path prints
	// invites -- so nothing below the boot check would ever retire this record.
	rebooted.cfg.BackupEnabled = false
	rebooted.bootUnixOverride = func() int64 { return time.Now().Add(time.Hour).Unix() }
	rebooted.loadAbandonMarker()

	if rebooted.aliveDegraded.Load() {
		t.Fatal("a marker written before the current boot names a process that cannot still exist; it must not degrade")
	}
	if rec, err := health.ReadAbandon(base); err != nil || rec != nil {
		t.Fatalf("the stale marker must be removed, not left to be re-read forever (rec=%+v err=%v)", rec, err)
	}
}

// TestAForwardClockStepMayNotRetireALiveOrphansMarker is the counterweight to the test above,
// and it covers the false GREEN a wall-clock oracle creates on its own.
//
// /proc/stat btime is not a stamp the kernel recorded at boot: it derives it from the CURRENT
// realtime offset (getboottime64 = offs_real - offs_boot), so every forward step of the wall
// clock moves btime forward by exactly the same amount. A host that booted with a dead RTC,
// abandoned a wedged child, and was then stepped forward by chrony therefore has a btime LATER
// than a marker written minutes earlier during that very boot. Retiring it there re-greens the
// service-alive check over an orphan that is still holding the backup lock -- and the marker's
// own (pid, starttime) pair, which no clock step can move, says so at the same moment.
//
// This runs the real probe, over a pid that is unquestionably alive and whose start time was
// recorded honestly: the test process itself.
func TestAForwardClockStepMayNotRetireALiveOrphansMarker(t *testing.T) {
	base := t.TempDir()
	self, ok := procStartTicks(os.Getpid())
	if !ok {
		t.Fatalf("procStartTicks(self) failed; the identity probe cannot be exercised at all")
	}
	if err := health.WriteAbandon(base, health.AbandonRecord{
		PID: os.Getpid(), Start: self, RID: "rid-old", TS: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("WriteAbandon: %v", err)
	}

	rep := &fakeReporter{alive: true, backupURL: true}
	d := newTestDaemon(t, rep, nil, time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	// The clock was stepped forward after the marker was written, so btime now looks later than
	// a record from this same boot. No pidAliveOverride: the point is that the real probe's
	// answer must win.
	d.bootUnixOverride = func() int64 { return time.Now().Add(time.Hour).Unix() }
	d.loadAbandonMarker()

	if !d.aliveDegraded.Load() {
		t.Fatal("the marker's own (pid, starttime) pair still names a live process on this host; a wall-clock comparison must not overrule it and re-green the alive check over a live orphan")
	}
	if rec, err := health.ReadAbandon(base); err != nil || rec == nil {
		t.Fatalf("the marker must survive so the successor still inherits the degrade (rec=%+v err=%v)", rec, err)
	}
	d.beat(context.Background())
	if s := rep.snapshot(); s.beats != 0 || s.aliveDown != 1 {
		t.Fatalf("the beat must keep reporting the alive check DOWN, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
}

// TestTheOrphanProbeRecognisesALiveProcess is the positive control for the one branch that
// enforces the whole mechanism: "the orphan is STILL THERE".
//
// Every other test that needs a live orphan pins pidAliveOverride, which replaces the probe
// entirely, and both tests that reach the real code assert the GONE answer -- so a defect in the
// field-22 parse or in the identity gate would turn every live orphan into "gone", lift the
// degrade, delete the marker and re-green proxsave-alive over a wedged child, with the suite
// still passing. The subject is a pid that is unquestionably alive and whose start time was read
// honestly: the test process itself.
func TestTheOrphanProbeRecognisesALiveProcess(t *testing.T) {
	self, ok := procStartTicks(os.Getpid())
	if !ok || self == 0 {
		t.Fatalf("procStartTicks(self) = %d ok=%v; /proc/<pid>/stat field 22 is not being read", self, ok)
	}
	// Field 22 is monotonic in start order and the fields around it are not, so bracketing the
	// test process between pid 1 and a child started right now catches an off-by-one that lands
	// on a plausible-looking number in either direction.
	if init, iok := procStartTicks(1); iok && self <= init {
		t.Fatalf("procStartTicks: self=%d is not later than pid 1's %d; the wrong /proc/<pid>/stat field is being read", self, init)
	}
	later := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := later.Start(); err != nil {
		t.Fatalf("start the younger child: %v", err)
	}
	defer func() {
		_ = later.Process.Kill()
		_ = later.Wait()
	}()
	child, ok := procStartTicks(later.Process.Pid)
	if !ok || child <= self {
		t.Fatalf("procStartTicks: a child started just now reads %d (ok=%v), not later than this process's %d; the wrong /proc/<pid>/stat field is being read", child, ok, self)
	}

	d := newTestDaemon(t, nil, nil, time.Hour)
	// The real probe over a live process that is emphatically not the test binary.
	if d.abandonedChildGone(later.Process.Pid, child) {
		t.Fatal("a child this test just started is on the host; reporting it GONE lifts every degrade over a wedged orphan")
	}
	if d.abandonedChildGone(os.Getpid(), self) {
		t.Fatal("a LIVE pid whose recorded start time matches must be reported STILL THERE, or every degrade is lifted over a child that never moved")
	}
	if !d.abandonedChildGone(os.Getpid(), self+1) {
		t.Fatal("a live pid whose start time does NOT match is a different process; calling it our child pins the alive check DOWN on a healed host")
	}
}

// TestAbandonRecordsTheOrphansRealStartTime pins the round trip the successor's identity check
// is built on. A marker that carries a zero start time still works, but only through the weaker
// cmdline fallback -- so a silent regression in the read at abandon time downgrades every later
// check without failing anything.
func TestAbandonRecordsTheOrphansRealStartTime(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	rec, err := health.ReadAbandon(d.cfg.BaseDir)
	if err != nil || rec == nil {
		t.Fatalf("abandonChild must leave a marker (rec=%+v err=%v)", rec, err)
	}
	if rec.PID <= 0 {
		t.Fatalf("the marker must name the orphan's pid, got %d", rec.PID)
	}
	if rec.Start == 0 {
		t.Fatal("the marker carries no start time: every later check falls back to the cmdline match, and a pid the kernel recycles into another proxsave --backup then pins the alive check DOWN")
	}
	if cur, ok := procStartTicks(rec.PID); ok && cur != rec.Start {
		t.Fatalf("the recorded start time %d is not the orphan's own %d", rec.Start, cur)
	}
}

// TestAStalledOrphanProbeIsNeverReissued guards the resource cost of re-validating a degrade.
//
// probeWithin bounds the WAIT, not the read: the goroutine it gives up on is abandoned, not
// cancelled, and it holds an open /proc file descriptor. Every other caller of that helper is on
// its way out of the process; this one is not -- reviewAbandonDegrade runs it once per heartbeat
// for as long as the degrade stands. Re-issuing a read that has already proved it can block
// would strand a goroutine and an fd on every beat, indefinitely, on the one host an operator is
// actively investigating.
func TestAStalledOrphanProbeIsNeverReissued(t *testing.T) {
	d := newTestDaemon(t, nil, nil, time.Hour)
	release := make(chan struct{})
	defer close(release)
	var reads atomic.Int32
	d.procIdentityIO = func(int, uint64) bool {
		reads.Add(1)
		<-release // a /proc read the kernel never lets go of
		return true
	}

	for i := 0; i < 4; i++ {
		if !d.pidIsAbandonedChild(4242, 7) {
			t.Fatal("a probe that cannot answer must count as the orphan still being there")
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d /proc reads are parked in the kernel, one per call; at one call per heartbeat that is a goroutine and an fd stranded every beat, for the life of the daemon", n)
	}
}

// TestSkippedStandaloneBackupKeepsTheDegrade is the other half, and mirrors runOnce's rule
// exactly: ExitBackupSkipped means no backup was performed -- the orphan still holds the lock,
// or backups are off -- so it proves nothing and lifts nothing.
func TestSkippedStandaloneBackupKeepsTheDegrade(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	restarted := restartedDaemon(t, d, &fakeReporter{alive: true, backupURL: true})
	restarted.cfg.HealthcheckEnabled = true
	// As in the supervised half: the orphan is gone, so every other gate on the lift now agrees,
	// and only the skip rule can keep the degrade standing.
	restarted.pidAliveOverride = func(int) bool { return false }
	if err := health.WriteManualOutcome(restarted.cfg.BaseDir, "rid-skip", time.Now().Unix(), types.ExitBackupSkipped.Int()); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	restarted.processManualOutcome(context.Background())

	if !restarted.aliveDegraded.Load() {
		t.Fatal("a skipped standalone run performed no backup; the degrade must stand")
	}
}

// TestDisabledBackupsDoNotInheritTheDegrade closes the trap the degrade would otherwise become.
// The ERROR this path logs tells the operator backups cannot run until the host is cleared, and
// the standard reaction is to turn them off. With BACKUP_ENABLED=false nothing can ever clear
// the marker again -- runOnce returns at its guard before the clear, and a standalone backup
// refuses with ExitBackupSkipped -- so believing the marker here pins the check that pages
// people DOWN forever on a daemon that is perfectly healthy. The backup check is already down
// on its own merits (no run pings anything), which is the honest half of the report.
func TestDisabledBackupsDoNotInheritTheDegrade(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	rep := &fakeReporter{alive: true, backupURL: true}
	off := newTestDaemon(t, rep, nil, time.Hour)
	off.cfg.BaseDir = d.cfg.BaseDir
	off.cfg.HealthcheckMode = "self"
	off.cfg.BackupEnabled = false
	off.pidAliveOverride = func(int) bool { return true } // the orphan is still there
	off.loadAbandonMarker()

	if off.aliveDegraded.Load() {
		t.Fatal("with backups administratively off the alive check must not be pinned DOWN by a degrade nothing can ever lift")
	}
	off.beat(context.Background())
	if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 0 {
		t.Fatalf("a healthy daemon with backups off must beat normally, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
	// The marker is KEPT: re-enabling backups makes it relevant again, and only a completed run
	// (or a vanished orphan) should retire it.
	if rec, err := health.ReadAbandon(off.cfg.BaseDir); err != nil || rec == nil {
		t.Fatalf("the marker must survive so re-enabling backups restores the degrade (rec=%+v err=%v)", rec, err)
	}
	back := restartedDaemon(t, d, &fakeReporter{alive: true, backupURL: true})
	if !back.aliveDegraded.Load() {
		t.Fatal("re-enabling backups with the orphan still wedged must restore the degrade")
	}
}

// TestVanishedOrphanDoesNotInheritTheDegrade bounds the degrade by the condition it describes
// instead of by a clock. The marker's claim is "pid N is unreapable and holds the backup lock".
// A reboot -- the one action that reliably clears a D-state task -- makes that claim false while
// leaving the file behind, and the marker lives in the identity dir, not /run. Re-validating it
// can only ever CLEAR (a pid that does not exist cannot be our child), never invent, a degrade.
// This exercises the real kill(2) probe, not the test seam.
func TestVanishedOrphanDoesNotInheritTheDegrade(t *testing.T) {
	base := t.TempDir()
	reaped := exec.Command("/bin/sh", "-c", "exit 0")
	if err := reaped.Run(); err != nil {
		t.Fatalf("run the throwaway child: %v", err)
	}
	dead := reaped.Process.Pid // exited AND waited on: this pid is gone

	if err := health.WriteAbandon(base, health.AbandonRecord{PID: dead, RID: "rid-old", TS: time.Now().Unix()}); err != nil {
		t.Fatalf("WriteAbandon: %v", err)
	}
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newTestDaemon(t, rep, nil, time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.loadAbandonMarker()

	if d.aliveDegraded.Load() {
		t.Fatal("the abandoned child is gone; keeping the alive check DOWN is a false RED on a healed host")
	}
	if rec, err := health.ReadAbandon(base); err != nil || rec != nil {
		t.Fatalf("a marker whose orphan is gone must be retired (rec=%+v err=%v)", rec, err)
	}
	d.beat(context.Background())
	if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 0 {
		t.Fatalf("want a plain success beat once the orphan is gone, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
}

// TestBeatLiftsTheDegradeWhenTheOrphanDies covers the host that heals WITHOUT a restart: the
// NFS server comes back, the task finally leaves D state and dies. Nothing else on the daemon
// would notice until the next scheduled run, up to a whole scheduling period later, and until
// then the alive check is red on a host that is fine -- the mirror image of the false green the
// degrade exists to remove.
func TestBeatLiftsTheDegradeWhenTheOrphanDies(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	rep := &fakeReporter{alive: true, backupURL: true}
	restarted := restartedDaemon(t, d, rep)
	restarted.beat(context.Background())
	if s := rep.snapshot(); s.aliveDown != 1 {
		t.Fatalf("while the orphan is wedged the beat must report DOWN, got aliveDown=%d", s.aliveDown)
	}

	restarted.pidAliveOverride = func(int) bool { return false } // the orphan finally died
	restarted.beat(context.Background())

	if restarted.aliveDegraded.Load() {
		t.Fatal("the degrade outlived the orphan it names")
	}
	if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 1 {
		t.Fatalf("the next beat must be a plain success ping, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
	}
	if rec, err := health.ReadAbandon(restarted.cfg.BaseDir); err != nil || rec != nil {
		t.Fatalf("the marker must be retired with the degrade (rec=%+v err=%v)", rec, err)
	}
}

// TestUnreadableMarkerIsStillRetiredByACompletedRun pins the cleanup a degrade-gated clear
// used to skip. A marker this process could not READ still degrades nothing -- we refuse to
// guess -- but it is still a file, and leaving it there means the next process that can read it
// resurrects a degrade for a wedge that ended long ago.
func TestUnreadableMarkerIsStillRetiredByACompletedRun(t *testing.T) {
	base := t.TempDir()
	// A directory where the marker file belongs: os.ReadFile fails with EISDIR, which is
	// ReadAbandon's genuine-read-fault branch rather than its tolerated corrupt-contents one.
	if err := os.MkdirAll(health.AbandonPath(base), 0o750); err != nil {
		t.Fatalf("stage the unreadable marker: %v", err)
	}
	d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, shCmd("exit 0"), time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.loadAbandonMarker()

	if d.aliveDegraded.Load() {
		t.Fatal("an unreadable marker is not evidence of an abandon; the daemon must not degrade on a guess")
	}
	d.runOnce(context.Background())

	if _, err := os.Stat(health.AbandonPath(base)); !os.IsNotExist(err) {
		t.Fatalf("a completed run must retire the marker it could not read (stat err=%v)", err)
	}
}

// TestAbandonReportsEvenWhenTheHeartbeatInterlockIsStuck is the regression test for the
// critical defect a naive interlock reintroduces: aliveMu sits on the abandon path, so any
// holder that never lets go would wedge runOnce again -- the same failure this whole change
// removes, one layer down. The ordering the lock buys is a nicety; runOnce returning is the
// invariant. Both pings must still go out.
func TestAbandonReportsEvenWhenTheHeartbeatInterlockIsStuck(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	// The production wait is 15s of pure wall clock in a package suite; what this test pins is
	// that the abandon gives up on the lock AT ALL, which the seam preserves exactly.
	d.aliveInterlockWaitOverride = 200 * time.Millisecond

	d.aliveMu.Lock() // a beat parked forever, e.g. inside the cross-process relay-secret flock
	defer d.aliveMu.Unlock()

	done := make(chan bool, 1)
	go func() { done <- d.runOnce(context.Background()) }()
	select {
	case abandoned := <-done:
		if !abandoned {
			t.Fatal("expected the run to be abandoned")
		}
	case <-time.After(d.aliveInterlockWait() + 20*time.Second):
		t.Fatal("runOnce never returned: the alive interlock can still wedge the scheduler")
	}
	if s := rep.snapshot(); s.hung != 1 || s.aliveDown != 1 {
		t.Fatalf("both checks must still be driven DOWN, got hung=%d aliveDown=%d", s.hung, s.aliveDown)
	}
}

// TestAbandonPingsTheBackupCheckBeforeTouchingTheAliveInterlock pins the ordering rule that
// keeps the primary signal free of an unrelated lock: the backup /fail -- the report this
// whole change exists to make reachable -- must be on the wire before aliveMu is contended
// for, so a stuck heartbeat can never delay it.
func TestAbandonPingsTheBackupCheckBeforeTouchingTheAliveInterlock(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)

	d.aliveMu.Lock()
	done := make(chan bool, 1)
	go func() { done <- d.runOnce(context.Background()) }()

	reported := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !reported {
		reported = rep.snapshot().hung == 1
		time.Sleep(10 * time.Millisecond)
	}
	d.aliveMu.Unlock()
	<-done // join before the temp BaseDir is torn down

	if !reported {
		t.Fatal("the backup hang /fail was gated on the alive interlock; it must be sent before it")
	}
}

// TestScheduleLoopUnwindsOnAbandon proves the wedge is gone at the level the report described:
// runOnce is called synchronously from the loop, so today the scheduler never comes back and
// no further backup is ever scheduled. The clock is frozen just before the scheduled time so
// the loop arms one short timer; it never reaches a second iteration, so the frozen clock
// cannot spin.
func TestScheduleLoopUnwindsOnAbandon(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)
	d.cfg.SchedulerTime = "03:00"
	d.now = func() time.Time { return time.Date(2026, 1, 2, 2, 59, 59, 750*int(time.Millisecond), time.UTC) }

	done := make(chan bool, 1)
	go func() { done <- d.scheduleLoop(context.Background()) }()
	select {
	case abandoned := <-done:
		if !abandoned {
			t.Fatal("scheduleLoop must report the abandoned run up to run()")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scheduleLoop never returned: the scheduler is still wedged behind the child")
	}
}

// TestRunExitsAfterAbandoningUnreapableChild pins the whole unwind, and is the regression test
// for the deadlock a partial fix would introduce: scheduleLoop propagates the abandon, run()
// cancels its OWN derived context so the SIGUSR1 waker (started unconditionally and joined by
// wg.Wait()) actually returns, and the daemon exits non-zero so systemd restarts it. Without
// that derived cancel the caller's context is still live and wg.Wait() blocks forever -- one
// hang traded for another.
//
// HealthcheckEnabled stays false, so no heartbeat/update loop starts and nothing touches the
// network; the waker alone is enough to deadlock wg.Wait().
func TestRunExitsAfterAbandoningUnreapableChild(t *testing.T) {
	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)
	d.cfg.SchedulerTime = "03:00"
	d.now = func() time.Time { return time.Date(2026, 1, 2, 2, 59, 59, 750*int(time.Millisecond), time.UTC) }

	code := make(chan int, 1)
	go func() { code <- d.run(context.Background()) }()
	select {
	case got := <-code:
		if got != types.ExitBackupError.Int() {
			t.Fatalf("run() exit = %d, want %d (abandoning is not a clean stop, and exit 1 is documented as benign)",
				got, types.ExitBackupError.Int())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run() did not return after abandoning the child: the daemon can never be restarted")
	}
}

// TestReapWaitCoversTheKillGrace pins the production ordering the overridden tests cannot
// exercise: the daemon must never declare a child unreapable before os/exec has actually sent
// the SIGKILL. Anything less would abandon children that a kill would still have collected.
func TestReapWaitCoversTheKillGrace(t *testing.T) {
	d := &daemon{}
	if d.killGrace() != daemonKillGrace {
		t.Fatalf("killGrace() = %s, want the production %s", d.killGrace(), daemonKillGrace)
	}
	if d.reapWait() <= d.killGrace() {
		t.Fatalf("reapWait() = %s must exceed the kill grace %s, or a child is abandoned before the SIGKILL is even sent",
			d.reapWait(), d.killGrace())
	}
	if d.reapWait() != daemonKillGrace+daemonReapSlack {
		t.Fatalf("reapWait() = %s, want killGrace+slack = %s", d.reapWait(), daemonKillGrace+daemonReapSlack)
	}
}

// corruptMarker stages the case the health package deliberately tolerates: a marker whose
// PRESENCE is the signal but whose contents are unreadable, so ReadAbandon yields the zero
// record (pid 0, no rid). WriteAbandon does not fsync and these hosts are typically hard-reset
// by the operator, so a truncated file is the ordinary way this happens -- not a curiosity.
func corruptMarker(t *testing.T, base string) {
	t.Helper()
	if err := health.WriteAbandon(base, health.AbandonRecord{PID: 1}); err != nil {
		t.Fatalf("WriteAbandon: %v", err)
	}
	if err := os.WriteFile(health.AbandonPath(base), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt the marker: %v", err)
	}
}

// TestARecycledPidDoesNotPinTheDegradeForever is the mirror of
// TestCompletedRunOverALiveOrphanKeepsTheDegrade, and covers the false RED that gating every
// lift path on a bare liveness probe creates.
//
// "Is pid N alive" does not identify a PROCESS: the kernel recycles pid numbers within a boot.
// The mount heals, the orphan finally dies while nothing is watching -- the daemon was stopped
// for the repair, or healthchecks are off so no beat ever reviews the marker -- and the number
// is handed to the next long-lived process the host starts. From then on the probe answers
// "still there" forever: startup re-degrades, the beat never lifts, and a completed run does
// not lift either, so the check that pages people is RED on a host whose backups are running
// perfectly, until somebody deletes the marker by hand. The boot-generation check cannot help
// -- the reuse happened WITHIN this boot.
//
// This runs the real probe, not the test seam, and names a pid that is unquestionably alive
// and unquestionably not a backup child: the test process itself.
func TestARecycledPidDoesNotPinTheDegradeForever(t *testing.T) {
	cases := []struct {
		name  string
		start uint64
	}{
		// A tick count no process on this host can have (~348 years of uptime at 100 Hz), so
		// the exact identity test must reject it.
		{"start time recorded", 1 << 40},
		// A marker written before that field existed: the cmdline fallback must reject it too.
		{"legacy marker with no start time", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			if err := health.WriteAbandon(base, health.AbandonRecord{
				PID: os.Getpid(), Start: tc.start, RID: "rid-old", TS: time.Now().Unix(),
			}); err != nil {
				t.Fatalf("WriteAbandon: %v", err)
			}
			rep := &fakeReporter{alive: true, backupURL: true}
			d := newTestDaemon(t, rep, nil, time.Hour)
			d.cfg.BaseDir = base
			d.cfg.HealthcheckMode = "self"
			d.loadAbandonMarker()

			if d.aliveDegraded.Load() {
				t.Fatal("the pid was recycled by an unrelated process; keeping the alive check DOWN is a false RED nothing can ever lift")
			}
			if rec, err := health.ReadAbandon(base); err != nil || rec != nil {
				t.Fatalf("a marker whose pid is no longer our child must be retired (rec=%+v err=%v)", rec, err)
			}
			d.beat(context.Background())
			if s := rep.snapshot(); s.beats != 1 || s.aliveDown != 0 {
				t.Fatalf("want a plain success beat over a recycled pid, got beats=%d aliveDown=%d", s.beats, s.aliveDown)
			}
		})
	}
}

// TestACorruptMarkerIsNotLiftedByAFailedRun closes the false GREEN the pid-based carve-out left
// open on exactly the host that is hardest to reason about.
//
// A corrupt marker degrades (presence is the signal), but it names no pid, so the orphan probe
// has nothing to ask. Treating that as "there is no orphan to outlive" hands the lift to the
// exit code alone -- and the exit code is what the whole clearAbandonMarkerOnCompletedRun
// contract says proves nothing, because the backup lock is checked LAST, after the very gates a
// dead mount fails. The operator's `proxsave --backup`, run to see what is wrong, dies on the
// disk-space check and re-greens the alive check over an orphan that has not moved.
//
// A run that actually SUCCEEDED is different in kind: it passed the lock check, so a backup
// completed end to end and backups are demonstrably not dead. That is the escape, so the
// degrade is still bounded rather than permanent.
func TestACorruptMarkerIsNotLiftedByAFailedRun(t *testing.T) {
	base := t.TempDir()
	corruptMarker(t, base)

	d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true},
		shCmd("exit "+strconv.Itoa(types.ExitBackupError.Int())), time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.loadAbandonMarker()
	if !d.aliveDegraded.Load() {
		t.Fatal("a marker whose contents are unreadable is still an abandon; it must degrade")
	}

	d.runOnce(context.Background())
	if !d.aliveDegraded.Load() {
		t.Fatal("a run that failed before the lock check proves nothing; the degrade must stand even when the marker names no pid")
	}
	if rec, err := health.ReadAbandon(base); err != nil || rec == nil {
		t.Fatalf("the marker must survive so a later restart still inherits the degrade (rec=%+v err=%v)", rec, err)
	}

	// The escape: a backup that actually succeeded took the lock the orphan would be holding.
	d.newBackupCmd = shCmd("exit 0")
	d.runOnce(context.Background())
	if d.aliveDegraded.Load() {
		t.Fatal("a successful backup passed the lock check; nothing is left for the degrade to mean")
	}
	if rec, err := health.ReadAbandon(base); err != nil || rec != nil {
		t.Fatalf("the marker must be retired with the degrade (rec=%+v err=%v)", rec, err)
	}
}

// TestACorruptMarkerIsLiftedByARunThatOnlyWarned closes the mirror defect of the test above: a
// service-alive DOWN on a healthy host that NOTHING can lift.
//
// A corrupt marker names no pid, so the probe has nothing to ask and every probe-based lift path
// is closed; it carries no timestamp either, so the boot-generation check cannot date it and a
// reboot does not retire it. The run's own exit code is therefore the only escape there is --
// and exit 1 is not a failure. applyIssueExitCode promotes a CLEAN run to it when the run logged
// warnings (a run with real errors becomes 4 instead), and docs/TROUBLESHOOTING.md documents a
// routine state in which every run on a perfectly healthy host exits 1: unacknowledged release
// notes after an upgrade. Refusing it there leaves the check that pages people RED until somebody
// deletes the file by hand.
func TestACorruptMarkerIsLiftedByARunThatOnlyWarned(t *testing.T) {
	base := t.TempDir()
	corruptMarker(t, base)

	// First: a reboot really is no escape for this marker, so the exit code has to be one.
	rebooted := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, nil, time.Hour)
	rebooted.cfg.BaseDir = base
	rebooted.cfg.HealthcheckMode = "self"
	rebooted.bootUnixOverride = func() int64 { return time.Now().Add(time.Hour).Unix() }
	rebooted.loadAbandonMarker()
	if !rebooted.aliveDegraded.Load() {
		t.Fatal("a marker with no pid and no timestamp cannot be dated; the boot check must not pretend otherwise")
	}

	d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true},
		shCmd("exit "+strconv.Itoa(types.ExitGenericError.Int())), time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.loadAbandonMarker()
	if !d.aliveDegraded.Load() {
		t.Fatal("a marker whose contents are unreadable is still an abandon; it must degrade")
	}

	d.runOnce(context.Background())
	if d.aliveDegraded.Load() {
		t.Fatal("a backup that finished with warnings passed the lock check; keeping the alive check DOWN is a false RED no run, no restart and no reboot can lift")
	}
	if rec, err := health.ReadAbandon(base); err != nil || rec != nil {
		t.Fatalf("the marker must be retired with the degrade (rec=%+v err=%v)", rec, err)
	}
}

// TestAKeptMarkerSurvivesAStandaloneRunThatFailedBeforeTheLock covers the quieter half of the
// same rule: the clear paths that run when THIS process never degraded at all.
//
// With BACKUP_ENABLED=false the marker is deliberately kept for a later re-enable, and nothing
// is degraded. A `proxsave --backup` that dies on a pre-flight gate is exactly the evidence
// clearAbandonMarkerOnCompletedRun exists to disbelieve, and deleting the marker on it costs
// nothing visible in this process -- the whole price lands on the successor, which comes up
// fully green over an orphan that still holds the backup lock while every scheduled run exits
// ExitBackupSkipped and pings nothing.
func TestAKeptMarkerSurvivesAStandaloneRunThatFailedBeforeTheLock(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	off := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, nil, time.Hour)
	off.cfg.BaseDir = d.cfg.BaseDir
	off.cfg.HealthcheckMode = "self"
	off.cfg.HealthcheckEnabled = true
	off.cfg.BackupEnabled = false
	off.pidAliveOverride = func(int) bool { return true } // the orphan is still wedged
	off.loadAbandonMarker()
	if off.aliveDegraded.Load() {
		t.Fatal("with backups administratively off nothing may be degraded")
	}

	// The operator put BACKUP_ENABLED back in the config and proved it by hand before
	// restarting the daemon, and the run died on the disk-space gate -- long before the lock.
	if err := health.WriteManualOutcome(off.cfg.BaseDir, "rid-early-fail", time.Now().Unix(), types.ExitBackupError.Int()); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	off.processManualOutcome(context.Background())

	if rec, err := health.ReadAbandon(off.cfg.BaseDir); err != nil || rec == nil {
		t.Fatalf("a run that failed before the lock check deleted a marker that was kept on purpose (rec=%+v err=%v)", rec, err)
	}
	back := restartedDaemon(t, d, &fakeReporter{alive: true, backupURL: true})
	if !back.aliveDegraded.Load() {
		t.Fatal("the successor inherited nothing and comes up green over an orphan that still holds the backup lock")
	}
}

// TestAnUnreadableMarkerSurvivesARunThatFailedBeforeTheLock is the other un-degraded state, and
// the more likely of the two: ReadAbandon's genuine-read-fault branch is reached on precisely
// the host whose BaseDir I/O is wedged -- the same fault that parks a backup child in D state.
// This process never even saw the record, so it knows nothing about the orphan it names, and a
// pre-lock failure tells it nothing either. The cleanup it does owe the identity dir is real,
// but it waits for evidence.
func TestAnUnreadableMarkerSurvivesARunThatFailedBeforeTheLock(t *testing.T) {
	base := t.TempDir()
	// A directory where the marker file belongs: os.ReadFile fails with EISDIR, which is
	// ReadAbandon's genuine-read-fault branch rather than its tolerated corrupt-contents one.
	if err := os.MkdirAll(health.AbandonPath(base), 0o750); err != nil {
		t.Fatalf("stage the unreadable marker: %v", err)
	}
	d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true},
		shCmd("exit "+strconv.Itoa(types.ExitBackupError.Int())), time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.loadAbandonMarker()
	if d.aliveDegraded.Load() {
		t.Fatal("an unreadable marker is not evidence of an abandon; the daemon must not degrade on a guess")
	}

	d.runOnce(context.Background())
	if _, err := os.Stat(health.AbandonPath(base)); err != nil {
		t.Fatalf("a run that failed before the lock check deleted a marker this daemon never read; the successor comes up green over an orphan that may still hold the lock (stat err=%v)", err)
	}

	// ...and the cleanup is not lost: a run that DID reach the lock still retires it.
	d.newBackupCmd = shCmd("exit 0")
	d.runOnce(context.Background())
	if _, err := os.Stat(health.AbandonPath(base)); !os.IsNotExist(err) {
		t.Fatalf("a completed run must still retire the marker it could not read (stat err=%v)", err)
	}
}

// unstartableCmd builds a child that can never be FORKED: the path does not exist, so
// cmd.Start fails and no process is ever created. superviseChild deliberately folds that into
// reaped=true (there is nothing to abandon and no pid to leak), and exitCodeFromErr then
// SYNTHESISES exit code 1 for it -- TestExitCodeFromErr pins that mapping. A fork/exec failure
// is ordinary on the host class this whole path exists for: EAGAIN/ENOMEM on a box already
// accumulating D-state tasks, a BASE_DIR mount that swallowed the binary, or a path that
// vanished under a package upgrade.
func unstartableCmd() func(ctx context.Context) *exec.Cmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/proxsave/binary/xyz", "--backup")
	}
}

// TestAChildThatNeverStartedProvesNothingAboutTheLock closes the false GREEN that the exit
// code alone opens on the two branches with no pid to probe.
//
// The daemon-side 1 and the child's own 1 are different facts wearing the same number.
// exitProvesLockWasTaken reasons about the CHILD's exit codes -- there 1 is a clean run that
// only warned, which is why it must qualify (see TestACorruptMarkerIsLiftedByARunThatOnlyWarned)
// -- but exitCodeFromErr also synthesises 1 for an error that carries no wait status at all,
// above all a cmd.Start failure. Nothing was forked, so nothing reached the backup lock, and
// nothing may retire the marker.
//
// Both un-probeable states are covered because the exit code is the ONLY evidence in each:
// the corrupt marker degrades but names no pid, and the unreadable one never degraded and is
// reached on precisely the host whose BaseDir I/O is wedged. Deleting on either hands the
// successor nothing, and it beats green over an orphan that may still hold the lock.
func TestAChildThatNeverStartedProvesNothingAboutTheLock(t *testing.T) {
	t.Run("corrupt marker", func(t *testing.T) {
		base := t.TempDir()
		corruptMarker(t, base)

		d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, unstartableCmd(), time.Hour)
		d.cfg.BaseDir = base
		d.cfg.HealthcheckMode = "self"
		d.loadAbandonMarker()
		if !d.aliveDegraded.Load() {
			t.Fatal("a marker whose contents are unreadable is still an abandon; it must degrade")
		}

		d.runOnce(context.Background())
		if !d.aliveDegraded.Load() {
			t.Fatal("no child was ever forked, so nothing reached the backup lock; the degrade must stand")
		}
		if rec, err := health.ReadAbandon(base); err != nil || rec == nil {
			t.Fatalf("a run whose child could not even be started deleted the marker (rec=%+v err=%v)", rec, err)
		}
	})

	t.Run("unreadable marker", func(t *testing.T) {
		base := t.TempDir()
		// ReadAbandon's genuine-read-fault branch: os.ReadFile fails with EISDIR, so this
		// process never degrades and only owes the identity dir a cleanup once a run proves
		// the wedge is over.
		if err := os.MkdirAll(health.AbandonPath(base), 0o750); err != nil {
			t.Fatalf("stage the unreadable marker: %v", err)
		}
		d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, unstartableCmd(), time.Hour)
		d.cfg.BaseDir = base
		d.cfg.HealthcheckMode = "self"
		d.loadAbandonMarker()
		if d.aliveDegraded.Load() {
			t.Fatal("an unreadable marker is not evidence of an abandon; the daemon must not degrade on a guess")
		}

		d.runOnce(context.Background())
		if _, err := os.Stat(health.AbandonPath(base)); err != nil {
			t.Fatalf("a run whose child could not even be started retired a marker this daemon never read; the successor comes up green over an orphan that may still hold the lock (stat err=%v)", err)
		}

		// ...and the cleanup is not lost: a child that really RAN and only warned still
		// retires it, so this is not a blanket refusal of exit 1.
		d.newBackupCmd = shCmd("exit " + strconv.Itoa(types.ExitGenericError.Int()))
		d.runOnce(context.Background())
		if _, err := os.Stat(health.AbandonPath(base)); !os.IsNotExist(err) {
			t.Fatalf("a run that really reached the lock must still retire the marker (stat err=%v)", err)
		}
	})
}

// TestAFailedMarkerRemovalIsRetriedByALaterRun pins the gate that decides whether anything ever
// touches the file again. abandonMarkerOnDisk is dropped before the unlink is known to have
// happened, and the unlink's error is only Debug-logged, so a removal that FAILS leaves a marker
// no later caller in this process will retry -- and the next daemon reads it and degrades for a
// wedge that ended long ago.
func TestAFailedMarkerRemovalIsRetriedByALaterRun(t *testing.T) {
	base := t.TempDir()
	// A marker path that cannot be unlinked: a NON-EMPTY directory where the file belongs.
	// os.ReadFile fails with EISDIR and os.Remove with ENOTEMPTY, which is the shape of any
	// removal fault on an identity dir the daemon cannot fully write.
	if err := os.MkdirAll(health.AbandonPath(base), 0o750); err != nil {
		t.Fatalf("stage the marker: %v", err)
	}
	blocker := filepath.Join(health.AbandonPath(base), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("stage the blocker: %v", err)
	}

	d := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, shCmd("exit 0"), time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.loadAbandonMarker()

	d.runOnce(context.Background()) // the removal is attempted and fails
	if _, err := os.Stat(health.AbandonPath(base)); err != nil {
		t.Fatalf("the removal was supposed to fail, leaving the path in place: %v", err)
	}

	// The identity dir recovers, and the next completed run must try again.
	if err := os.Remove(blocker); err != nil {
		t.Fatalf("unblock the removal: %v", err)
	}
	d.runOnce(context.Background())
	if _, err := os.Stat(health.AbandonPath(base)); !os.IsNotExist(err) {
		t.Fatalf("a removal that failed closed the gate for good, so no later run retries it and the marker survives to re-degrade the next daemon (stat err=%v)", err)
	}
}

// TestAStragglingMarkerRemovalCannotEatAFreshAbandonMarker covers the one interleaving abandonMu
// cannot close, on the one host class this path exists for.
//
// clearAbandonMarker unlinks inside runWithin, and runWithin gives up WAITING without cancelling
// anything -- nothing in userspace can recall a syscall the kernel is holding. On a wedged
// BaseDir the lock is therefore released with the removal still queued: abandonChild then takes
// it, latches, and writes the marker for a NEW orphan, and the straggler finally lands on top of
// that fresh file. The successor daemon inherits nothing and beats green over a live orphan --
// the exact outcome the barrier was chosen to prevent, reached silently.
func TestAStragglingMarkerRemovalCannotEatAFreshAbandonMarker(t *testing.T) {
	base := t.TempDir()
	// The precondition: this process inherited a marker, so its clear paths are armed.
	corruptMarker(t, base)

	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.aliveInterlockWaitOverride = 100 * time.Millisecond

	entered, release, unlinked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.clearAbandonMarkerIO = func() error {
		close(entered)
		<-release // an unlink parked in the kernel, long past daemonAbandonIOWait
		err := health.ClearAbandon(base)
		close(unlinked)
		return err
	}
	d.loadAbandonMarker()

	clearDone := make(chan struct{})
	go func() {
		defer close(clearDone)
		d.clearAbandonMarkerOnCompletedRun("a supervised backup completed", 0)
	}()
	<-entered

	// ...and while that removal is stuck, the daemon wedges on a NEW child and abandons it.
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}
	<-clearDone
	fresh, err := health.ReadAbandon(base)
	if err != nil || fresh == nil || fresh.PID <= 0 {
		t.Fatalf("abandonChild must leave a marker naming the new orphan (rec=%+v err=%v)", fresh, err)
	}

	close(release) // the parked unlink finally lands, on top of the fresh marker
	<-unlinked

	deadline := time.Now().Add(5 * time.Second)
	var rec *health.AbandonRecord
	for time.Now().Before(deadline) {
		if rec, err = health.ReadAbandon(base); err == nil && rec != nil && rec.PID == fresh.PID {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec == nil || rec.PID != fresh.PID {
		t.Fatalf("a removal that outlived the barrier deleted the marker written for orphan pid=%d (rec=%+v); the next daemon comes up green over a live orphan", fresh.PID, rec)
	}
}

// TestAFreshAbandonMarkerSurvivesAStandaloneHandoff pins the barrier that keeps the marker
// written for THIS process's orphan out of reach of the clear paths reasoning about the
// INHERITED one.
//
// The two are the same file, so whoever writes last wins. abandonChild persists its marker and
// then spends up to the whole alive interlock wait plus a ping before returning, and the SIGUSR1
// waker goroutine stays live for all of it (run() only stops the loops after scheduleLoop
// returns). A `proxsave --backup` handing off in that window -- precisely what an operator runs
// when they notice the wedge -- takes processManualOutcome straight to the clear, and the
// successor comes up fully green over a live orphan that still holds the backup lock. Guarding
// one caller does not fix this; the barrier belongs where the file is touched.
func TestAFreshAbandonMarkerSurvivesAStandaloneHandoff(t *testing.T) {
	base := t.TempDir()
	// The precondition: this process inherited a marker, so its clear paths are armed. Corrupt
	// is the easiest way there and also the nastiest -- it names no pid to re-check.
	corruptMarker(t, base)

	rep := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, rep, 150*time.Millisecond)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "self"
	d.cfg.HealthcheckEnabled = true
	d.aliveInterlockWaitOverride = 200 * time.Millisecond
	d.loadAbandonMarker()

	// ...and then it wedges on a NEW child and abandons it.
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}
	fresh, err := health.ReadAbandon(base)
	if err != nil || fresh == nil || fresh.PID <= 0 {
		t.Fatalf("abandonChild must leave a marker naming the new orphan (rec=%+v err=%v)", fresh, err)
	}

	// The handoff lands while the abandon is still unwinding.
	if err := health.WriteManualOutcome(base, "rid-manual", time.Now().Unix(), 0); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	d.processManualOutcome(context.Background())

	rec, err := health.ReadAbandon(base)
	if err != nil {
		t.Fatalf("ReadAbandon: %v", err)
	}
	if rec == nil || rec.PID != fresh.PID {
		t.Fatalf("the marker written for orphan pid=%d was deleted (rec=%+v); the next daemon comes up green over a live orphan", fresh.PID, rec)
	}
}

// TestARetiredMarkerLeavesNoPidBehindToProbe covers the operator-facing half of the
// boot-generation discard. Retiring the marker is only half the job: a pid left behind in the
// daemon's fields is probed by every completed run for the rest of the process's life, and
// after a reboot that number belongs to something else, so each successful backup announces
// that the service-alive check is being held DOWN -- while it is green. The healed host is
// exactly the host the discard exists to serve.
func TestARetiredMarkerLeavesNoPidBehindToProbe(t *testing.T) {
	first := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, first, 150*time.Millisecond)
	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}

	rebooted := newTestDaemon(t, &fakeReporter{alive: true, backupURL: true}, shCmd("exit 0"), time.Hour)
	rebooted.cfg.BaseDir = d.cfg.BaseDir
	rebooted.cfg.HealthcheckMode = "self"
	rebooted.pidAliveOverride = func(int) bool { return false } // the reboot freed the orphan
	rebooted.bootUnixOverride = func() int64 { return time.Now().Add(time.Hour).Unix() }
	rebooted.loadAbandonMarker()

	if rebooted.aliveDegraded.Load() {
		t.Fatal("a marker written before the current boot must not degrade")
	}
	if rebooted.abandonPID != 0 {
		t.Fatalf("the retired marker left pid=%d behind; every completed run now probes an unrelated process and reports it is holding DOWN a check that is green", rebooted.abandonPID)
	}
	rebooted.runOnce(context.Background())
	if rebooted.aliveDegraded.Load() {
		t.Fatal("nothing may re-raise a degrade the daemon retired at startup")
	}
}

// TestAbandonReportsThroughAReporterInstalledMidRun pins the re-read the two MANDATED signals
// depend on. runOnce captures the reporter once, at the top of a run that may last
// MAX_RUN_DURATION; in centralized mode a daemon that started unpaired resolves its URLs on a
// later beat and installs one behind the run's back. Without re-reading it, the daemon that
// most needs to report -- one whose monitor only just became reachable -- drops BOTH the backup
// hang and the alive degrade and exits silently.
func TestAbandonReportsThroughAReporterInstalledMidRun(t *testing.T) {
	late := &fakeReporter{alive: true, backupURL: true}
	d := newAbandonDaemon(t, nil, 150*time.Millisecond) // no URLs resolved when the run starts
	child := sigtermProofCmd("3")
	d.newBackupCmd = func(ctx context.Context) *exec.Cmd {
		d.setReporter(late) // the lazy centralized re-resolve, landing mid-run
		return child(ctx)
	}

	if !d.runOnce(context.Background()) {
		t.Fatal("expected the run to be abandoned")
	}
	if s := late.snapshot(); s.hung != 1 || s.aliveDown != 1 {
		t.Fatalf("the abandon must report through the reporter resolved during the run, got hung=%d aliveDown=%d", s.hung, s.aliveDown)
	}
}

// TestDaemonFileCleanupCannotHoldTheExit guards the last five lines of the abandon path. The
// daemon abandons an unreapable child in order to EXIT and be restarted by systemd, and
// everything on the way out is deadline-bounded for one reason: BaseDir may be on the very
// filesystem that parked the child in D state. run()'s final defer removes the pid file and
// .daemon_info.json from that same directory, so an unbounded unlink there strands the process
// after all the bounded work -- alive, unable to die, and never restarted.
func TestDaemonFileCleanupCannotHoldTheExit(t *testing.T) {
	d := newTestDaemon(t, nil, nil, time.Hour)
	release := make(chan struct{})
	defer close(release)
	d.removeDaemonFilesIO = func() { <-release } // an unlink that never returns

	done := make(chan struct{})
	go func() { defer close(done); d.removeDaemonFiles() }()
	select {
	case <-done:
	case <-time.After(daemonAbandonIOWait + 10*time.Second):
		t.Fatal("the exit is hostage to the identity dir: on a wedged BaseDir the daemon never returns from run() and systemd never restarts it")
	}
}
