// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/types"
)

// fakeReporter records the daemon's healthchecks calls for assertions.
type fakeReporter struct {
	mu               sync.Mutex
	started          int
	finished         int
	hung             int
	beats            int
	lastCode         int
	lastRid          string
	lastTail         string
	alive, backupURL bool
	// Fase 1 updates sensor.
	updates         bool
	updatesReported int
	lastAvailable   bool
	// Fase 2 generic per-check Ping + dynamic (notify-*) check resolution.
	checks map[string]bool // HasCheck(name) for dynamic keys (alive/backup/updates use the flags above)
	pings  []fakePing      // recorded generic Ping calls
}

// fakePing records one generic Reporter.Ping call for assertions.
type fakePing struct{ name, suffix, rid string }

func (f *fakeReporter) Heartbeat(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats++
	return nil
}
func (f *fakeReporter) RunStarted(ctx context.Context, rid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.lastRid = rid
	return nil
}
func (f *fakeReporter) RunFinished(ctx context.Context, rid string, code int, tail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished++
	f.lastCode = code
	f.lastTail = tail
	return nil
}
func (f *fakeReporter) RunHang(ctx context.Context, rid string, timeout time.Duration, tail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hung++
	f.lastTail = tail
	return nil
}
func (f *fakeReporter) HasAliveURL() bool  { return f.alive }
func (f *fakeReporter) HasBackupURL() bool { return f.backupURL }

func (f *fakeReporter) ReportUpdate(ctx context.Context, available bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.updates { // no updates URL resolved: mirror health.Reporter's ErrNoUpdatesURL
		return health.ErrNoUpdatesURL
	}
	f.updatesReported++
	f.lastAvailable = available
	return nil
}
func (f *fakeReporter) HasUpdatesURL() bool { return f.updates }

func (f *fakeReporter) HasCheck(name string) bool {
	switch name {
	case health.CheckKeyAlive:
		return f.alive
	case health.CheckKeyBackup:
		return f.backupURL
	case health.CheckKeyUpdates:
		return f.updates
	}
	return f.checks[name]
}

func (f *fakeReporter) Ping(ctx context.Context, name, suffix, rid, body, label string) error {
	if !f.HasCheck(name) {
		return health.ErrNoPingURL
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings = append(f.pings, fakePing{name: name, suffix: suffix, rid: rid})
	return nil
}

func (f *fakeReporter) snapshot() fakeReporter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeReporter{started: f.started, finished: f.finished, hung: f.hung, beats: f.beats, lastCode: f.lastCode, lastRid: f.lastRid, lastTail: f.lastTail, updates: f.updates, updatesReported: f.updatesReported, lastAvailable: f.lastAvailable, pings: append([]fakePing(nil), f.pings...)}
}

func newTestDaemon(t *testing.T, rep backupReporter, cmdFn func(ctx context.Context) *exec.Cmd, maxRun time.Duration) *daemon {
	t.Helper()
	return &daemon{
		// A temp BaseDir keeps recordPing's status writes out of the source tree
		// (StatusPath("") would resolve to a cwd-relative identity/ dir).
		cfg:          &config.Config{BaseDir: t.TempDir(), MaxRunDuration: maxRun, HealthcheckSendLog: false, BackupEnabled: true},
		reporter:     rep,
		newBackupCmd: cmdFn,
		now:          time.Now,
	}
}

func shCmd(script string) func(ctx context.Context) *exec.Cmd {
	return func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script)
	}
}

func TestExitCodeFromErr(t *testing.T) {
	if got := exitCodeFromErr(nil); got != 0 {
		t.Errorf("nil err -> %d, want 0", got)
	}
	err := exec.Command("/bin/sh", "-c", "exit 3").Run()
	if got := exitCodeFromErr(err); got != 3 {
		t.Errorf("exit 3 -> %d, want 3", got)
	}
	startErr := exec.Command("/nonexistent/proxsave/binary/xyz").Run()
	if got := exitCodeFromErr(startErr); got != 1 {
		t.Errorf("start failure -> %d, want 1", got)
	}
}

func TestRunOnceReportsExitCode(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, shCmd("exit 4"), time.Hour)
	d.runOnce(context.Background())
	s := rep.snapshot()
	if s.started != 1 || s.finished != 1 || s.hung != 0 {
		t.Fatalf("calls started=%d finished=%d hung=%d, want 1/1/0", s.started, s.finished, s.hung)
	}
	if s.lastCode != 4 {
		t.Fatalf("finished code = %d, want 4", s.lastCode)
	}
	if s.lastRid == "" {
		t.Fatalf("start ping should carry a run id")
	}
}

func TestRunOnceReportsSuccess(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, shCmd("exit 0"), time.Hour)
	d.runOnce(context.Background())
	s := rep.snapshot()
	if s.finished != 1 || s.lastCode != 0 || s.hung != 0 {
		t.Fatalf("success run: finished=%d code=%d hung=%d, want 1/0/0", s.finished, s.lastCode, s.hung)
	}
}

func TestRunOnceCapturesLogTail(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, shCmd("echo HELLO_TAIL_MARKER; exit 4"), time.Hour)
	d.cfg.HealthcheckSendLog = true // enable capture of the child's output
	d.runOnce(context.Background())
	s := rep.snapshot()
	if s.finished != 1 || s.lastCode != 4 {
		t.Fatalf("finished=%d code=%d, want 1/4", s.finished, s.lastCode)
	}
	if !strings.Contains(s.lastTail, "HELLO_TAIL_MARKER") {
		t.Fatalf("log tail did not capture the child's output, got %q", s.lastTail)
	}
}

func TestRunOnceNoLogTailWhenSendLogOff(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, shCmd("echo SHOULD_NOT_APPEAR; exit 4"), time.Hour) // SendLog=false
	d.runOnce(context.Background())
	if s := rep.snapshot(); s.lastTail != "" {
		t.Fatalf("SendLog off must POST no body, got %q", s.lastTail)
	}
}

func TestRunOnceReportsHang(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	// Tiny watchdog budget + a slow child -> timeout -> SIGTERM -> hang.
	d := newTestDaemon(t, rep, shCmd("sleep 5"), 150*time.Millisecond)
	start := time.Now()
	d.runOnce(context.Background())
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("runOnce did not kill the hung child promptly (%s)", elapsed)
	}
	s := rep.snapshot()
	if s.hung != 1 || s.finished != 0 {
		t.Fatalf("hang run: hung=%d finished=%d, want 1/0", s.hung, s.finished)
	}
}

// TestRunOnceSuppressesFinishPingOnBackupSkipped pins F09-03: a supervised child that exits
// ExitBackupSkipped (16) because another backup was already running did NOT back up, so runOnce
// must NOT ping a finish (which would be a false green on the backup-outcome check). The start
// ping already fired before the child ran; only finish/hang must be suppressed.
func TestRunOnceSuppressesFinishPingOnBackupSkipped(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, shCmd(fmt.Sprintf("exit %d", types.ExitBackupSkipped.Int())), time.Hour)
	d.runOnce(context.Background())
	s := rep.snapshot()
	if s.finished != 0 || s.hung != 0 {
		t.Fatalf("a skipped backup (exit %d) must NOT ping finish/hang, got finished=%d hung=%d", types.ExitBackupSkipped.Int(), s.finished, s.hung)
	}
}

func TestRunOnceSkipsWhenBackupDisabled(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	called := false
	d := newTestDaemon(t, rep, func(ctx context.Context) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}, time.Hour)
	d.cfg.BackupEnabled = false
	d.runOnce(context.Background())
	if called {
		t.Fatal("BACKUP_ENABLED=false must not exec a child")
	}
	s := rep.snapshot()
	if s.started != 0 || s.finished != 0 || s.hung != 0 {
		t.Fatalf("disabled run must report nothing (no false green), got started=%d finished=%d hung=%d", s.started, s.finished, s.hung)
	}
}

func TestRunOnceNoReporterRecordsNoPhantomPing(t *testing.T) {
	// No reporter resolved (unpaired centralized, or the server was down at startup with
	// no cached backup.env URLs) means NOTHING can be transmitted. A scheduled backup
	// still runs, but the outcome pings must be swallowed as "no url configured" and NOT
	// persisted: recording a RunFinished{OK:true} for a ping that never left the process
	// would let the run-side section print a false green "transmitting to the monitor".
	base := t.TempDir()
	called := false
	d := newTestDaemon(t, nil, func(ctx context.Context) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}, time.Hour)
	d.cfg.BaseDir = base
	d.cfg.HealthcheckMode = "centralized"

	d.runOnce(context.Background())

	if !called {
		t.Fatal("the backup child should still run even without a reporter")
	}
	st, err := health.LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if st.Record(health.KindRunStarted) != nil || st.Record(health.KindRunFinished) != nil || st.Record(health.KindRunHang) != nil {
		t.Fatalf("a nil reporter must record no outcome ping, got started=%v finished=%v hang=%v",
			st.Record(health.KindRunStarted), st.Record(health.KindRunFinished), st.Record(health.KindRunHang))
	}
}

func TestBeatRecordsLivenessWhenNoURL(t *testing.T) {
	// A running daemon that cannot resolve a ping URL (self mode with no URLs, or
	// centralized not provisioned) must STILL record a heartbeat - as a liveness trace
	// with reason no_url, OK=false - so the run-side section can tell "daemon up but not
	// provisioned yet" from "daemon not running at all" (no heartbeat record).
	d := newTestDaemon(t, nil, nil, time.Hour)
	d.cfg.HealthcheckMode = "self" // self + no URLs: no centralized rebuild, no network

	d.beat(context.Background())

	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if st.Record(health.KindHeartbeat) == nil {
		t.Fatal("a running daemon with no url must still record a heartbeat (liveness), got nil")
	}
	if st.Record(health.KindHeartbeat).OK {
		t.Fatalf("a no-url beat must record OK=false, got %+v", st.Record(health.KindHeartbeat))
	}
	if st.Record(health.KindHeartbeat).Reason != health.ReasonNoURL {
		t.Fatalf("Reason=%q want %q", st.Record(health.KindHeartbeat).Reason, health.ReasonNoURL)
	}
}

func TestRunOnceSkipsOnShutdown(t *testing.T) {
	rep := &fakeReporter{backupURL: true}
	d := newTestDaemon(t, rep, shCmd("exit 0"), time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already shutting down
	d.runOnce(ctx)
	s := rep.snapshot()
	if s.started != 0 || s.finished != 0 || s.hung != 0 {
		t.Fatalf("shutdown run must report nothing, got started=%d finished=%d hung=%d", s.started, s.finished, s.hung)
	}
}

func TestSelfURLs(t *testing.T) {
	tests := []struct {
		name                       string
		cfg                        config.Config
		wantAlive, wantBk, wantUpd string
	}{
		{
			name:      "uuid urls from endpoint + ids",
			cfg:       config.Config{HealthcheckPingEndpoint: "https://hc-ping.com", HealthcheckAliveID: "a", HealthcheckBackupID: "b"},
			wantAlive: "https://hc-ping.com/a", wantBk: "https://hc-ping.com/b",
		},
		{
			name:      "slug urls with ping key",
			cfg:       config.Config{HealthcheckPingEndpoint: "https://hc.example/", HealthcheckPingKey: "pk", HealthcheckAliveID: "alive", HealthcheckBackupID: "backup"},
			wantAlive: "https://hc.example/pk/alive", wantBk: "https://hc.example/pk/backup",
		},
		{
			name:      "explicit full urls win",
			cfg:       config.Config{HealthcheckAliveURL: "https://x/ping/1", HealthcheckBackupURL: "https://x/ping/2", HealthcheckPingEndpoint: "https://hc-ping.com", HealthcheckAliveID: "ignored"},
			wantAlive: "https://x/ping/1", wantBk: "https://x/ping/2",
		},
		{
			name:      "updates url assembled from its check id",
			cfg:       config.Config{HealthcheckPingEndpoint: "https://hc-ping.com", HealthcheckAliveID: "a", HealthcheckBackupID: "b", HealthcheckUpdatesID: "u"},
			wantAlive: "https://hc-ping.com/a", wantBk: "https://hc-ping.com/b", wantUpd: "https://hc-ping.com/u",
		},
		{
			name:      "explicit updates full url wins over id",
			cfg:       config.Config{HealthcheckPingEndpoint: "https://hc-ping.com", HealthcheckAliveID: "a", HealthcheckBackupID: "b", HealthcheckUpdatesURL: "https://x/ping/u", HealthcheckUpdatesID: "ignored"},
			wantAlive: "https://hc-ping.com/a", wantBk: "https://hc-ping.com/b", wantUpd: "https://x/ping/u",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &daemon{cfg: &tc.cfg}
			alive, backup, checks := d.selfURLs()
			if alive != tc.wantAlive || backup != tc.wantBk || checks[health.CheckKeyUpdates] != tc.wantUpd {
				t.Fatalf("selfURLs = %q / %q / %q, want %q / %q / %q", alive, backup, checks[health.CheckKeyUpdates], tc.wantAlive, tc.wantBk, tc.wantUpd)
			}
		})
	}
}

func TestMaxRunDurationFallback(t *testing.T) {
	d := &daemon{cfg: &config.Config{}}
	if d.maxRunDuration() != defaultMaxRunDuration {
		t.Fatalf("maxRunDuration() = %s, want %s", d.maxRunDuration(), defaultMaxRunDuration)
	}
	d.cfg.MaxRunDuration = 2 * time.Hour
	if d.maxRunDuration() != 2*time.Hour {
		t.Fatalf("maxRunDuration() = %s, want 2h", d.maxRunDuration())
	}
}
