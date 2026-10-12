package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

type installRuntimeFixture struct {
	p          *installRuntimePreparation
	events     []string
	active     bool
	installed  bool
	cron       bool
	installErr error
	restart    RestartVerifyResult
	resident   string
}

func newInstallRuntimeFixture(t *testing.T) *installRuntimeFixture {
	t.Helper()
	dir := t.TempDir()
	executable := filepath.Join(dir, "binary")
	if err := os.WriteFile(executable, []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(cfgPath, []byte("BASE_DIR="+dir+"\nSCHEDULER_MODE=daemon\nHEALTHCHECK_ENABLED=false\nLOCK_PATH="+dir+"/new-lock\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &installRuntimeFixture{cron: true}
	p := newInstallRuntimePreparation(cfgPath, dir, ExecInfo{ExecPath: executable}, nil)
	f.p = p
	p.canonicalPath = filepath.Join(dir, "proxsave")
	p.ops.entrypoints = func(ctx context.Context) error {
		f.events = append(f.events, "entrypoints")
		return os.Symlink(executable, p.canonicalPath)
	}
	p.ops.inspect = func(context.Context) (bool, health.DaemonPresence, error) {
		return f.installed, health.DaemonPresence{Probed: true, Installed: f.installed, Active: f.active}, nil
	}
	p.ops.install = func(context.Context) error {
		f.events = append(f.events, "install")
		f.installed = true
		if f.installErr == nil {
			f.active = true
		}
		return f.installErr
	}
	p.ops.snapshot = func(context.Context, *config.Config) RestartVerifyResult {
		return RestartVerifyResult{ProcessAlive: f.active, Aligned: f.active, State: health.DaemonState{ProcessAlive: f.active, Aligned: f.active, AlignChecked: true, HaveInfo: true}}
	}
	p.ops.verify = func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
		f.events = append(f.events, "verify")
		return p.ops.snapshot(ctx, cfg)
	}
	p.ops.residentConfig = func(health.DaemonState, string) (string, bool) { return f.resident, f.resident != "" }
	p.ops.restart = func(ctx context.Context, base, lock string, known bool, interval time.Duration) RestartVerifyResult {
		f.events = append(f.events, "restart:"+lock)
		return f.restart
	}
	p.ops.removeCron = func(context.Context) (cronRemovalOutcome, error) {
		f.events = append(f.events, "remove-cron")
		f.cron = false
		return cronRemovalOutcome{Removed: 1, Verified: true}, nil
	}
	p.ops.warnCron = func(context.Context) int { return 0 }
	p.ops.cronPresent = func(context.Context) (bool, bool) { return f.cron, true }
	p.ops.configureCron = func(context.Context, string) error {
		f.events = append(f.events, "configure-cron")
		f.cron = true
		return nil
	}
	return f
}

func TestInstallRuntimeFreshStartsOnceBeforeFinalization(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	if err := f.p.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"entrypoints", "install", "verify", "remove-cron"}) {
		t.Fatalf("preparation=%v", f.events)
	}
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"entrypoints", "install", "verify", "remove-cron"}) {
		t.Fatalf("finalization repeated mutations: %v", f.events)
	}
	if !f.p.result.ActivationConfirmed || !f.p.result.CronRemoval.Verified {
		t.Fatalf("result=%+v", f.p.result)
	}
}

func TestInstallRuntimeWrongEntrypointNeverStartsDaemon(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	old := filepath.Join(t.TempDir(), "old-binary")
	if err := os.WriteFile(old, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	f.p.ops.entrypoints = func(context.Context) error { return os.Symlink(old, f.p.canonicalPath) }
	if err := f.p.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.p.result.Err == nil || f.installed || !f.cron {
		t.Fatalf("wrong executable accepted: %+v events=%v", f.p.result, f.events)
	}
}

func TestInstallRuntimeServiceInspectionErrorPreventsMutation(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.p.ops.inspect = func(context.Context) (bool, health.DaemonPresence, error) {
		return false, health.DaemonPresence{}, os.ErrPermission
	}
	_ = f.p.Prepare(context.Background())
	if len(f.events) != 0 || !errors.Is(f.p.result.Err, os.ErrPermission) {
		t.Fatalf("events=%v error=%v", f.events, f.p.result.Err)
	}
}

func TestInstallRuntimeFailedEnableDoesNotInventCronOrRepeatInstall(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.cron = false
	f.installErr = errors.New("enable failed after enabling unit")
	_ = f.p.Prepare(context.Background())
	if err := f.p.Finalize(context.Background()); !errors.Is(err, f.installErr) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(f.events, []string{"entrypoints", "install"}) || f.cron {
		t.Fatalf("unsafe fallback/retry: %v", f.events)
	}
}

func TestInstallRuntimeCronFailureDoesNotRestartActivatedDaemon(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	attempts := 0
	f.p.ops.removeCron = func(context.Context) (cronRemovalOutcome, error) {
		attempts++
		return cronRemovalOutcome{}, os.ErrPermission
	}
	_ = f.p.Prepare(context.Background())
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"entrypoints", "install", "verify"}) || attempts != 2 || f.p.result.CronRemoval.Verified {
		t.Fatalf("events=%v removal attempts=%d result=%+v", f.events, attempts, f.p.result)
	}
}

func TestInstallRuntimeDeferredRestartSurvivesAlignedRefresh(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	f.resident = f.p.configPath
	f.restart = RestartVerifyResult{BackupWaitTimedOut: true}
	_ = f.p.Prepare(context.Background())
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.p.result.Restart.BackupWaitTimedOut || !f.p.result.Verification.Aligned {
		t.Fatalf("defer erased: %+v", f.p.result)
	}
	restarts := 0
	for _, e := range f.events {
		if len(e) >= 8 && e[:8] == "restart:" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Fatalf("restarts=%d events=%v", restarts, f.events)
	}
}

func TestInstallRuntimeResidentConfigControlsProtectedRestartLock(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	resident := filepath.Join(t.TempDir(), "resident.env")
	if err := os.WriteFile(resident, []byte("BASE_DIR=/resident\nLOCK_PATH=/resident/custom-lock\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.resident = resident
	// The direct resident config predates the running daemon, including its ctime.
	start := time.Now().Unix() + 1
	time.Sleep(time.Until(time.Unix(start, 0)))
	snapshot := f.p.ops.snapshot
	f.p.ops.snapshot = func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
		rv := snapshot(ctx, cfg)
		rv.State.StartTS = start
		return rv
	}
	f.p.ops.restart = func(_ context.Context, base, lock string, known bool, _ time.Duration) RestartVerifyResult {
		if !known || lock != "/resident/custom-lock/.backup.lock" {
			t.Fatalf("restart lock=%q known=%v base=%q", lock, known, base)
		}
		return RestartVerifyResult{BackupWaitTimedOut: true}
	}
	_ = f.p.Prepare(context.Background())
	if !f.p.result.Restart.BackupWaitTimedOut {
		t.Fatal("protected restart was omitted")
	}
}

func TestInstallRuntimeUnknownResidentConfigDefersRestart(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	f.p.ops.restart = func(_ context.Context, _, _ string, known bool, _ time.Duration) RestartVerifyResult {
		if known {
			t.Fatal("unknown resident config treated as safe")
		}
		return RestartVerifyResult{LockPathUnknown: true}
	}
	_ = f.p.Prepare(context.Background())
	if !f.p.result.Restart.LockPathUnknown {
		t.Fatal("restart not deferred")
	}
}

func TestInstallRuntimeCancelledPreparationDoesNotMutate(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.p.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("mutations=%v", f.events)
	}
}

func TestInstallRuntimeStopAfterHandoverIsReadOnly(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	_ = f.p.Prepare(context.Background())
	f.active = false
	if err := f.p.Finalize(context.Background()); err == nil {
		t.Fatal("stopped daemon without cron reported success")
	}
	if f.p.result.Verification.ProcessAlive || f.cron || !reflect.DeepEqual(f.events, []string{"entrypoints", "install", "verify", "remove-cron"}) {
		t.Fatalf("events=%v result=%+v", f.events, f.p.result)
	}
}

func TestInstallRuntimeCronWaitsForFinalization(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	if err := os.WriteFile(f.p.configPath, []byte("SCHEDULER_MODE=cron\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = f.p.Prepare(context.Background())
	if len(f.events) != 0 {
		t.Fatalf("early cron mutations=%v", f.events)
	}
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"configure-cron"}) {
		t.Fatalf("events=%v", f.events)
	}
}

func TestInstallRuntimeOperationalErrorDoesNotBlockMonitoringBootstrap(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installErr = errors.New("service rejected start")
	stubHealthcheckSetupCLIDeps(t)
	reached := false
	healthcheckSetupBuildBootstrap = func(context.Context, string, string) (orchestrator.HealthcheckSetupBootstrap, error) {
		reached = true
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupSkipDisabled}, nil
	}
	if err := runHealthcheckSetupCLI(context.Background(), nil, f.p.baseDir, f.p.configPath, nil, installer.HealthcheckSetupOptions{BeforeBootstrap: f.p.Prepare}); err != nil {
		t.Fatal(err)
	}
	if !reached || !errors.Is(f.p.result.Err, f.installErr) {
		t.Fatalf("bootstrap=%v recorded=%v", reached, f.p.result.Err)
	}
}

func TestInstallRuntimeConfigFailureDoesNotGuessCron(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	if err := os.Remove(f.p.configPath); err != nil {
		t.Fatal(err)
	}
	_ = f.p.Prepare(context.Background())
	if err := f.p.Finalize(context.Background()); err == nil {
		t.Fatal("unreadable configuration accepted")
	}
	if len(f.events) != 0 {
		t.Fatalf("mutations=%v", f.events)
	}
}

func TestInstallRuntimeCancelledAfterStartResumesOnlyVerification(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	install := f.p.ops.install
	f.p.ops.install = func(ctx context.Context) error { err := install(ctx); cancel(); return err }
	if err := f.p.Prepare(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"entrypoints", "install", "verify", "remove-cron"}) {
		t.Fatalf("resume repeated completed phases: %v", f.events)
	}
}

func TestInstallRuntimeReadinessTimeoutDoesNotRepeatActivation(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.p.ops.verify = func(context.Context, *config.Config) RestartVerifyResult {
		f.events = append(f.events, "verify-timeout")
		return RestartVerifyResult{TimedOut: true}
	}
	_ = f.p.Prepare(context.Background())
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"entrypoints", "install", "verify-timeout", "remove-cron"}) {
		t.Fatalf("events=%v", f.events)
	}
}

func TestInstallRuntimeCaptureReplaysOnceWithoutLoggingAgain(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	bootstrap := logging.NewBootstrapLogger()
	f.p.bootstrap = bootstrap
	prepare := f.p.ops.entrypoints
	f.p.ops.entrypoints = func(ctx context.Context) error {
		bootstrap.Info("captured installer operation")
		logging.Info("captured global operation")
		fmt.Println("captured raw output")
		return prepare(ctx)
	}
	outer := captureStdout(t, func() {
		if err := f.p.PrepareCaptured(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(outer, "captured") {
		t.Fatalf("escaped capture: %q", outer)
	}
	count := bootstrap.EntryCount()
	var lines []string
	f.p.Replay(func(s string) { lines = append(lines, s) })
	f.p.Replay(func(s string) { lines = append(lines, s) })
	output := strings.Join(lines, "\n")
	for _, want := range []string{"captured installer operation", "captured global operation", "captured raw output"} {
		if strings.Count(output, want) != 1 {
			t.Fatalf("%q not displayed exactly once: %q", want, output)
		}
	}
	if bootstrap.EntryCount() != count {
		t.Fatalf("replay persisted duplicate logs: before=%d after=%d", count, bootstrap.EntryCount())
	}
}

func TestInstallRuntimeUnknownSystemdStatePreventsActivation(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.p.ops.inspect = func(context.Context) (bool, health.DaemonPresence, error) { return false, health.DaemonPresence{}, nil }
	_ = f.p.Prepare(context.Background())
	if len(f.events) != 0 || f.p.result.Err == nil {
		t.Fatalf("unknown service state allowed activation: events=%v error=%v", f.events, f.p.result.Err)
	}
}

func TestInstallRuntimeOutcomeKeepsPendingApplication(t *testing.T) {
	for _, restart := range []RestartVerifyResult{{BackupWaitTimedOut: true}, {LockPathUnknown: true}, {Err: errors.New("restart rejected")}} {
		result := installRuntimeResult{Mode: "daemon", RestartAttempted: true, Restart: restart, Verification: RestartVerifyResult{ProcessAlive: true, Aligned: true}}
		out := buildInstallOutcomePrompt(result.Verification, true, "ok", "permissions OK", result)
		if strings.Contains(out, "running and aligned") {
			t.Fatalf("pending application shown as aligned success: %q", out)
		}
	}
}

func TestInstallRuntimeApplicationFailureCannotBecomeAlignedSuccess(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	f.installErr = errors.New("systemctl daemon-reload failed")
	_ = f.p.Prepare(context.Background())
	if err := f.p.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := buildInstallOutcomePrompt(f.p.result.Verification, true, "ok", "permissions OK", f.p.result)
	if strings.Contains(out, "running and aligned") || !strings.Contains(out, f.installErr.Error()) {
		t.Fatalf("application failure hidden by alignment: %q", out)
	}
}

func TestInstallRuntimeRewrittenResidentConfigCannotProveActiveBackupLock(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	f.resident = f.p.configPath
	// Exercise the rewritten-file guard with a real positive start timestamp,
	// after the file metadata so the timestamp guard alone cannot pass this test.
	start := time.Now().Unix() + 1
	time.Sleep(time.Until(time.Unix(start, 0)))
	snapshot := f.p.ops.snapshot
	f.p.ops.snapshot = func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
		rv := snapshot(ctx, cfg)
		rv.State.StartTS = start
		return rv
	}
	// The installer saved /new-lock, but a child that already started still holds /old-lock.
	oldLock := filepath.Join(f.p.baseDir, "old-lock", ".backup.lock")
	previousRestart, previousBackup := daemonRestartService, restartVerifyBackupRunning
	t.Cleanup(func() { daemonRestartService = previousRestart; restartVerifyBackupRunning = previousBackup })
	restarts := 0
	daemonRestartService = func(context.Context) error { restarts++; return errors.New("unsafe restart") }
	restartVerifyBackupRunning = func(path string) bool { return path == oldLock }
	f.p.ops.restart = restartAndVerifyDaemon
	_ = f.p.Prepare(context.Background())
	if restarts != 0 || !f.p.result.Restart.LockPathUnknown {
		t.Fatalf("rewritten config hid live backup lock: restarts=%d result=%+v", restarts, f.p.result.Restart)
	}
}

func TestInstallRuntimeResidentLockRequiresIndependentResolution(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		envKey     string
		envValue   string
		wantKnown  bool
		wantSuffix string
	}{
		{name: "default", wantKnown: true, wantSuffix: "lock/.backup.lock"},
		{name: "literal", content: "LOCK_PATH=/resident/lock\n", wantKnown: true, wantSuffix: "/resident/lock/.backup.lock"},
		{name: "base variable", content: "LOCK_PATH=$BASE_DIR/custom\n", wantKnown: true, wantSuffix: "custom/.backup.lock"},
		{name: "braced base variable", content: "LOCK_PATH=${BASE_DIR}/custom\n", wantKnown: true, wantSuffix: "custom/.backup.lock"},
		{name: "backup override", content: "BACKUP_PATH=/resident/backups\nLOCK_PATH=${BACKUP_PATH}/lock\n", envKey: "BACKUP_PATH", envValue: "/installer/backups"},
		{name: "home fallback", content: "LOCK_PATH=${HOME}/lock\n", envKey: "HOME", envValue: "/installer/home"},
		{name: "relative path", content: "LOCK_PATH=relative/lock\n"},
		{name: "whitespace override", content: "LOCK_PATH=/resident/lock\n", envKey: "LOCK_PATH", envValue: " "},
	}
	residentDir := t.TempDir()
	for i, tc := range tests {
		if err := os.WriteFile(filepath.Join(residentDir, fmt.Sprintf("%d.env", i)), []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// All distinct resident files must predate the resident process, including ctime.
	start := time.Now().Unix() + 1
	time.Sleep(time.Until(time.Unix(start, 0)))
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOCK_PATH", "")
			if tc.envKey != "" {
				t.Setenv(tc.envKey, tc.envValue)
			}
			f := newInstallRuntimeFixture(t)
			f.installed, f.active = true, true
			f.resident = filepath.Join(residentDir, fmt.Sprintf("%d.env", i))
			snapshot := f.p.ops.snapshot
			f.p.ops.snapshot = func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
				rv := snapshot(ctx, cfg)
				rv.State.StartTS = start
				return rv
			}
			calls := 0
			f.p.ops.restart = func(_ context.Context, _, lock string, known bool, _ time.Duration) RestartVerifyResult {
				calls++
				if known != tc.wantKnown {
					t.Errorf("resident lock=%q known=%v, want known=%v", lock, known, tc.wantKnown)
				}
				if known {
					want := tc.wantSuffix
					if !filepath.IsAbs(want) {
						want = filepath.Join(f.p.baseDir, want)
					}
					if lock != want {
						t.Errorf("resident lock=%q, want %q", lock, want)
					}
				}
				return RestartVerifyResult{LockPathUnknown: !known}
			}
			if err := f.p.Prepare(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || f.p.result.Restart.LockPathUnknown == tc.wantKnown {
				t.Fatalf("protected restart calls=%d result=%+v", calls, f.p.result.Restart)
			}
		})
	}
}

func TestInstallRuntimeDeferredApplicationUsesApprovedWarning(t *testing.T) {
	for _, restart := range []RestartVerifyResult{{LockPathUnknown: true}, {BackupWaitTimedOut: true}} {
		result := installRuntimeResult{Mode: "daemon", UnitApplied: true, RestartAttempted: true, Restart: restart, Verification: RestartVerifyResult{ProcessAlive: true, Aligned: true}}
		level, field, log := installRuntimeVerdict(result)
		if level != orchestrator.HealthcheckSetupLevelWarn || field != "configuration not applied; manual restart required" || log != "Daemon configuration not applied; manual restart required." {
			t.Errorf("deferred outcome: level=%v field=%q log=%q", level, field, log)
		}
		out := buildInstallOutcomePrompt(result.Verification, true, "ok", "permissions OK", result)
		if !strings.Contains(out, theme.SymbolWarning+" "+"configuration not applied; manual restart required") {
			t.Errorf("missing approved warning in final Daemon field: %q", out)
		}
	}
}

func TestInstallRuntimeResidentLockRejectsInstallerEnvironment(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	resident := filepath.Join(t.TempDir(), "resident.env")
	if err := os.WriteFile(resident, []byte("LOCK_PATH=/resident/lock\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.resident = resident
	start := time.Now().Unix() + 1
	time.Sleep(time.Until(time.Unix(start, 0)))
	snapshot := f.p.ops.snapshot
	f.p.ops.snapshot = func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
		rv := snapshot(ctx, cfg)
		rv.State.StartTS = start
		return rv
	}
	t.Setenv("LOCK_PATH", "/installer/override")
	known := true
	f.p.ops.restart = func(_ context.Context, _, _ string, k bool, _ time.Duration) RestartVerifyResult {
		known = k
		return RestartVerifyResult{LockPathUnknown: !k}
	}
	_ = f.p.Prepare(context.Background())
	if known {
		t.Fatal("installer's LOCK_PATH treated as resident process evidence")
	}
}

func TestInstallRuntimeChangedResidentMetadataDefersRestart(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.installed = true
	f.active = true
	resident := filepath.Join(t.TempDir(), "resident.env")
	if err := os.WriteFile(resident, []byte("LOCK_PATH=/resident/lock\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Preserving mtime alone cannot disguise a file replaced after the daemon started.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(resident, old, old); err != nil {
		t.Fatal(err)
	}
	f.resident = resident
	snapshot := f.p.ops.snapshot
	f.p.ops.snapshot = func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
		rv := snapshot(ctx, cfg)
		rv.State.StartTS = time.Now().Add(-time.Minute).Unix()
		return rv
	}
	known := true
	f.p.ops.restart = func(_ context.Context, _, _ string, k bool, _ time.Duration) RestartVerifyResult {
		known = k
		return RestartVerifyResult{LockPathUnknown: !k}
	}
	_ = f.p.Prepare(context.Background())
	if known {
		t.Fatal("post-start config ctime ignored")
	}
}
