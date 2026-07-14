// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
)

// backupReporter is the healthchecks surface the daemon uses; *health.Reporter
// implements it. An interface so the scheduler/watchdog is testable with a fake.
type backupReporter interface {
	Heartbeat(ctx context.Context) error
	RunStarted(ctx context.Context, rid string) error
	RunFinished(ctx context.Context, rid string, exitCode int, logTail string) error
	RunHang(ctx context.Context, rid string, timeout time.Duration, logTail string) error
	HasAliveURL() bool
	HasBackupURL() bool
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

	mu          sync.Mutex
	reporter    backupReporter
	fetchWarned bool // centralized fetch already warned once (throttle recurring WARN)
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
	if d.cfg.HealthcheckEnabled {
		if r := d.buildReporter(ctx); r != nil {
			d.setReporter(r)
		}
	}

	var wg sync.WaitGroup
	if d.cfg.HealthcheckEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.heartbeatLoop(ctx)
		}()
	}

	d.scheduleLoop(ctx)
	wg.Wait()
	logging.Info("ProxSave daemon stopped")
	return types.ExitSuccess.Int()
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
	cmd := d.buildBackupCmd(runCtx, tail)
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

// buildReporter resolves the two ping URLs (centralized fetch from the server, or
// self-mode assembly) and returns a Reporter, or nil if nothing is reportable.
func (d *daemon) buildReporter(ctx context.Context) *health.Reporter {
	if !d.cfg.HealthcheckEnabled {
		return nil
	}
	if d.cfg.HealthcheckMode == "self" {
		alive, backup := d.selfURLs()
		if alive == "" && backup == "" {
			logging.Warning("daemon: healthcheck self mode enabled but no ping URLs configured")
			return nil
		}
		d.registerSecrets(alive, backup)
		return health.NewReporter(health.Config{AliveURL: alive, BackupURL: backup, SendLog: d.cfg.HealthcheckSendLog})
	}

	// centralized
	alive, backup, err := d.fetchCentralized(ctx)
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
		alive, backup = d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL
	} else {
		d.mu.Lock()
		d.fetchWarned = false // recovered: allow a future failure to warn again
		d.mu.Unlock()
	}
	if alive == "" && backup == "" {
		return nil
	}
	d.registerSecrets(alive, backup)
	return health.NewReporter(health.Config{AliveURL: alive, BackupURL: backup, SendLog: d.cfg.HealthcheckSendLog})
}

// fetchCentralized asks the proxsave_server for this client's ping URLs, reusing
// the same identity/secret as /api/notify.
func (d *daemon) fetchCentralized(ctx context.Context) (string, string, error) {
	secret, _ := identity.LoadNotifySecret(d.cfg.BaseDir)
	if strings.TrimSpace(secret) == "" {
		return "", "", fmt.Errorf("no relay secret on disk (pair Telegram first)")
	}
	cfg, err := health.FetchCentralizedConfig(ctx, nil, d.cfg.ServerAPIHost, d.cfg.ServerID, secret, false)
	if err != nil {
		return "", "", err
	}
	return cfg.AliveURL, cfg.BackupURL, nil
}

// selfURLs resolves the two ping URLs from self-mode config: full URLs if given,
// otherwise assembled from the ping endpoint (+ optional ping key) and check IDs.
func (d *daemon) selfURLs() (string, string) {
	if d.cfg.HealthcheckAliveURL != "" || d.cfg.HealthcheckBackupURL != "" {
		return d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL
	}
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
	return build(d.cfg.HealthcheckAliveID), build(d.cfg.HealthcheckBackupID)
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
func (d *daemon) buildBackupCmd(ctx context.Context, tail *tailBuffer) *exec.Cmd {
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
