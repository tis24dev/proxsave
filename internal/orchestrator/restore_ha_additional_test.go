package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

type haTestEnv struct {
	fs        *FakeFS
	cmd       *FakeCommandRunner
	fakeTime  *FakeTime
	plan      *RestorePlan
	stageRoot string
	logger    *logging.Logger
}

func setupHATestEnv(t *testing.T) *haTestEnv {
	t.Helper()

	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	origGeteuid := haApplyGeteuid
	origMounted := haIsMounted
	origRealFS := haIsRealRestoreFS
	origArm := haArmRollback
	origDisarm := haDisarmRollback
	origApply := haApplyFromStage
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
		haApplyGeteuid = origGeteuid
		haIsMounted = origMounted
		haIsRealRestoreFS = origRealFS
		haArmRollback = origArm
		haDisarmRollback = origDisarm
		haApplyFromStage = origApply
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	fakeTime := &FakeTime{Current: time.Unix(100, 0)}
	restoreTime = fakeTime

	haApplyGeteuid = func() int { return 0 }
	haIsMounted = func(path string) (bool, error) { return true, nil }
	haIsRealRestoreFS = func(fs FS) bool { return true }
	haArmRollback = armHARollback
	haDisarmRollback = disarmHARollback
	haApplyFromStage = applyPVEHAFromStage

	return &haTestEnv{
		fs:       fakeFS,
		cmd:      fakeCmd,
		fakeTime: fakeTime,
		plan: &RestorePlan{
			SystemType:       SystemTypePVE,
			NormalCategories: []Category{{ID: "pve_ha"}},
		},
		stageRoot: "/stage",
		logger:    newTestLogger(),
	}
}

func TestHAApplyNotCommittedErrorHelpers(t *testing.T) {
	env := setupHATestEnv(t)

	var nilErr *HAApplyNotCommittedError
	if nilErr.Error() != ErrHAApplyNotCommitted.Error() {
		t.Fatalf("nil error string = %q, want %q", nilErr.Error(), ErrHAApplyNotCommitted.Error())
	}
	if got := (&HAApplyNotCommittedError{}).Error(); got != ErrHAApplyNotCommitted.Error() {
		t.Fatalf("error string = %q, want %q", got, ErrHAApplyNotCommitted.Error())
	}

	errValue := (&HAApplyNotCommittedError{}).Unwrap()
	if errValue != ErrHAApplyNotCommitted {
		t.Fatalf("unwrap = %v, want %v", errValue, ErrHAApplyNotCommitted)
	}
	if !errors.Is(&HAApplyNotCommittedError{}, ErrHAApplyNotCommitted) {
		t.Fatalf("expected HAApplyNotCommittedError to match ErrHAApplyNotCommitted")
	}

	now := env.fakeTime.Current
	if got := (*haRollbackHandle)(nil).remaining(now); got != 0 {
		t.Fatalf("nil remaining = %s, want 0", got)
	}

	handle := &haRollbackHandle{armedAt: now.Add(-time.Second), timeout: 3 * time.Second}
	if got := handle.remaining(now); got != 2*time.Second {
		t.Fatalf("remaining = %s, want %s", got, 2*time.Second)
	}

	handle.armedAt = now.Add(-5 * time.Second)
	if got := handle.remaining(now); got != 0 {
		t.Fatalf("expired remaining = %s, want 0", got)
	}
}

func TestBuildHAApplyNotCommittedError_ReflectsMarkerState(t *testing.T) {
	env := setupHATestEnv(t)

	empty := buildHAApplyNotCommittedError(nil)
	if empty.RollbackArmed || empty.RollbackMarker != "" || empty.RollbackLog != "" || !empty.RollbackDeadline.IsZero() {
		t.Fatalf("unexpected empty error fields: %#v", empty)
	}

	handle := &haRollbackHandle{
		markerPath: " /tmp/proxsave/ha.marker ",
		logPath:    " /tmp/proxsave/ha.log ",
		armedAt:    env.fakeTime.Current,
		timeout:    3 * time.Second,
	}

	built := buildHAApplyNotCommittedError(handle)
	if built.RollbackArmed {
		t.Fatalf("expected rollback to be unarmed when marker is absent")
	}
	if built.RollbackMarker != "/tmp/proxsave/ha.marker" || built.RollbackLog != "/tmp/proxsave/ha.log" {
		t.Fatalf("unexpected trimmed fields: %#v", built)
	}
	if !built.RollbackDeadline.Equal(env.fakeTime.Current.Add(3 * time.Second)) {
		t.Fatalf("RollbackDeadline=%s want %s", built.RollbackDeadline, env.fakeTime.Current.Add(3*time.Second))
	}

	if err := env.fs.AddFile("/tmp/proxsave/ha.marker", []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}
	built = buildHAApplyNotCommittedError(handle)
	if !built.RollbackArmed {
		t.Fatalf("expected rollback to be armed when marker exists")
	}
}

func TestStageHasPVEHAConfig_DetectsFilesAndErrors(t *testing.T) {
	env := setupHATestEnv(t)

	ok, err := stageHasPVEHAConfig(env.stageRoot)
	if err != nil {
		t.Fatalf("stageHasPVEHAConfig error: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false when stage is empty")
	}

	if err := env.fs.AddFile(env.stageRoot+"/etc/pve/ha/groups.cfg", []byte("grp\n")); err != nil {
		t.Fatalf("add groups.cfg: %v", err)
	}
	ok, err = stageHasPVEHAConfig(env.stageRoot)
	if err != nil {
		t.Fatalf("stageHasPVEHAConfig error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true when staged HA config exists")
	}

	restoreFS = statFailFS{
		FS:       env.fs,
		failPath: env.stageRoot + "/etc/pve/ha/resources.cfg",
		err:      fmt.Errorf("boom"),
	}
	if _, err := stageHasPVEHAConfig(env.stageRoot); err == nil || !strings.Contains(err.Error(), "stat") {
		t.Fatalf("expected wrapped stat error, got %v", err)
	}
}

func TestBuildHARollbackScript_QuotesPaths(t *testing.T) {
	script := buildHARollbackScript("/tmp/marker path", "/tmp/backup's.tar.gz", "/tmp/log path")
	if !strings.Contains(script, "MARKER='/tmp/marker path'") {
		t.Fatalf("expected MARKER to be quoted, got script:\n%s", script)
	}
	if !strings.Contains(script, "LOG='/tmp/log path'") {
		t.Fatalf("expected LOG to be quoted, got script:\n%s", script)
	}
	if !strings.Contains(script, "BACKUP='/tmp/backup'\\''s.tar.gz'") {
		t.Fatalf("expected BACKUP to escape single quotes, got script:\n%s", script)
	}
	if !strings.HasSuffix(script, "\n") {
		t.Fatalf("expected script to end with newline")
	}
}

func TestArmHARollback_CoversSchedulingPaths(t *testing.T) {
	t.Run("rejects invalid input", func(t *testing.T) {
		env := setupHATestEnv(t)
		if _, err := armHARollback(context.Background(), env.logger, " ", time.Second, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error for empty backup path")
		}
		if _, err := armHARollback(context.Background(), env.logger, "/backup.tgz", 0, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error for invalid timeout")
		}
	})

	t.Run("fails when rollback directory cannot be created", func(t *testing.T) {
		env := setupHATestEnv(t)
		restoreFS = mkdirAllFailFS{
			FS:       env.fs,
			failPath: "/tmp/proxsave",
			err:      fmt.Errorf("boom"),
		}
		if _, err := armHARollback(context.Background(), env.logger, "/backup.tgz", time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "create rollback directory") {
			t.Fatalf("expected mkdir failure, got %v", err)
		}
	})

	t.Run("fails when marker or script cannot be written", func(t *testing.T) {
		env := setupHATestEnv(t)
		t.Setenv("PATH", t.TempDir())

		timestamp := env.fakeTime.Current.Format("20060102_150405")
		markerPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("ha_rollback_pending_%s", timestamp))
		restoreFS = writeFileFailFS{
			FS:       env.fs,
			failPath: markerPath,
			err:      fmt.Errorf("disk full"),
		}
		if _, err := armHARollback(context.Background(), env.logger, "/backup.tgz", time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "write rollback marker") {
			t.Fatalf("expected marker write failure, got %v", err)
		}

		env = setupHATestEnv(t)
		t.Setenv("PATH", t.TempDir())
		timestamp = env.fakeTime.Current.Format("20060102_150405")
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("ha_rollback_%s.sh", timestamp))
		restoreFS = writeFileFailFS{
			FS:       env.fs,
			failPath: scriptPath,
			err:      fmt.Errorf("disk full"),
		}
		if _, err := armHARollback(context.Background(), env.logger, "/backup.tgz", time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "write rollback script") {
			t.Fatalf("expected script write failure, got %v", err)
		}
	})

	t.Run("background timer writes marker and script when systemd-run unavailable", func(t *testing.T) {
		env := setupHATestEnv(t)
		t.Setenv("PATH", t.TempDir())

		handle, err := armHARollback(context.Background(), env.logger, "/backup.tgz", 2*time.Second, "")
		if err != nil {
			t.Fatalf("armHARollback error: %v", err)
		}
		if handle == nil {
			t.Fatalf("expected handle")
		}
		if handle.workDir != "/tmp/proxsave" {
			t.Fatalf("workDir=%q, want %q", handle.workDir, "/tmp/proxsave")
		}
		if !handle.armedAt.Equal(env.fakeTime.Current) {
			t.Fatalf("armedAt=%s, want %s", handle.armedAt, env.fakeTime.Current)
		}
		if _, err := env.fs.Stat(handle.markerPath); err != nil {
			t.Fatalf("expected marker file, stat err=%v", err)
		}
		script, err := env.fs.ReadFile(handle.scriptPath)
		if err != nil {
			t.Fatalf("read rollback script: %v", err)
		}
		if !strings.Contains(string(script), "BACKUP=/backup.tgz") {
			t.Fatalf("expected backup path in script, got:\n%s", string(script))
		}

		wantBackground := "sh -c nohup sh -c 'sleep 2; /bin/sh " + handle.scriptPath + "' >/dev/null 2>&1 &"
		calls := env.cmd.CallsList()
		if len(calls) != 1 || calls[0] != wantBackground {
			t.Fatalf("unexpected calls: %#v", calls)
		}
	})

	t.Run("sub-second timeout rounds up to one second", func(t *testing.T) {
		env := setupHATestEnv(t)
		t.Setenv("PATH", t.TempDir())

		handle, err := armHARollback(context.Background(), env.logger, "/backup.tgz", 100*time.Millisecond, "/tmp/proxsave")
		if err != nil {
			t.Fatalf("armHARollback error: %v", err)
		}
		wantBackground := "sh -c nohup sh -c 'sleep 1; /bin/sh " + handle.scriptPath + "' >/dev/null 2>&1 &"
		calls := env.cmd.CallsList()
		if len(calls) != 1 || calls[0] != wantBackground {
			t.Fatalf("unexpected calls: %#v", calls)
		}
	})

	t.Run("systemd-run failure falls back to background timer", func(t *testing.T) {
		env := setupHATestEnv(t)
		binDir := t.TempDir()
		t.Setenv("PATH", binDir)
		writeExecutable(t, binDir, "systemd-run")

		timestamp := env.fakeTime.Current.Format("20060102_150405")
		unitName := "proxsave-ha-rollback-" + timestamp
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("ha_rollback_%s.sh", timestamp))
		systemdKey := "systemd-run --unit=" + unitName + " --on-active=2s /bin/sh " + scriptPath
		env.cmd.Errors = map[string]error{
			systemdKey: fmt.Errorf("boom"),
		}

		handle, err := armHARollback(context.Background(), env.logger, "/backup.tgz", 2*time.Second, "/tmp/proxsave")
		if err != nil {
			t.Fatalf("armHARollback error: %v", err)
		}
		if handle.unitName != "" {
			t.Fatalf("expected unitName to be cleared after systemd-run failure, got %q", handle.unitName)
		}

		wantBackground := "sh -c nohup sh -c 'sleep 2; /bin/sh " + scriptPath + "' >/dev/null 2>&1 &"
		calls := env.cmd.CallsList()
		if len(calls) != 2 || calls[0] != systemdKey || calls[1] != wantBackground {
			t.Fatalf("unexpected calls: %#v", calls)
		}
	})

	t.Run("background timer failure returns error", func(t *testing.T) {
		env := setupHATestEnv(t)
		t.Setenv("PATH", t.TempDir())

		timestamp := env.fakeTime.Current.Format("20060102_150405")
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("ha_rollback_%s.sh", timestamp))
		backgroundKey := "sh -c nohup sh -c 'sleep 1; /bin/sh " + scriptPath + "' >/dev/null 2>&1 &"
		env.cmd.Errors = map[string]error{
			backgroundKey: fmt.Errorf("boom"),
		}

		if _, err := armHARollback(context.Background(), env.logger, "/backup.tgz", time.Second, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestDisarmHARollback_RemovesMarkerAndStopsTimer(t *testing.T) {
	env := setupHATestEnv(t)

	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	writeExecutable(t, binDir, "systemctl")

	handle := &haRollbackHandle{
		markerPath: "/tmp/proxsave/ha.marker",
		unitName:   "proxsave-ha-rollback-test",
		scriptPath: "/tmp/proxsave/ha.sh",
		logPath:    "/tmp/proxsave/ha.log",
	}
	if err := env.fs.AddFile(handle.markerPath, []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}
	if err := env.fs.AddFile(handle.scriptPath, []byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("add script: %v", err)
	}

	disarmHARollback(context.Background(), env.logger, handle)

	if _, err := env.fs.Stat(handle.markerPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected marker removed; stat err=%v", err)
	}
	if _, err := env.fs.Stat(handle.scriptPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected script removed; stat err=%v", err)
	}

	timerUnit := handle.unitName + ".timer"
	want1 := "systemctl stop " + timerUnit
	want2 := "systemctl reset-failed " + handle.unitName + ".service " + timerUnit
	calls := env.cmd.CallsList()
	if len(calls) != 2 || calls[0] != want1 || calls[1] != want2 {
		t.Fatalf("unexpected calls: %#v", calls)
	}

	disarmHARollback(context.Background(), env.logger, nil)
}

func TestMaybeApplyPVEHAWithUI_BranchCoverage(t *testing.T) {
	newEnv := func(t *testing.T) *haTestEnv {
		t.Helper()
		env := setupHATestEnv(t)
		stageWithHA := env.stageRoot + "/etc/pve/ha/resources.cfg"
		if err := env.fs.AddFile(stageWithHA, []byte("res\n")); err != nil {
			t.Fatalf("add staged HA config: %v", err)
		}
		return env
	}

	t.Run("nil plan returns nil", func(t *testing.T) {
		env := newEnv(t)
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, nil, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("errors when ui missing", func(t *testing.T) {
		env := newEnv(t)
		if err := maybeApplyPVEHAWithUI(context.Background(), nil, env.logger, env.plan, nil, nil, env.stageRoot, false); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("skips on non-system restore fs", func(t *testing.T) {
		env := newEnv(t)
		haIsRealRestoreFS = func(fs FS) bool { return false }
		t.Cleanup(func() { haIsRealRestoreFS = func(fs FS) bool { return true } })

		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("dry run, non-root, empty stage and cluster restore all skip", func(t *testing.T) {
		env := newEnv(t)
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, env.stageRoot, true); err != nil {
			t.Fatalf("expected nil on dry run, got %v", err)
		}

		haApplyGeteuid = func() int { return 1000 }
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil for non-root, got %v", err)
		}
		haApplyGeteuid = func() int { return 0 }

		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, "   ", false); err != nil {
			t.Fatalf("expected nil for empty stageRoot, got %v", err)
		}

		plan := *env.plan
		plan.NeedsClusterRestore = true
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, &plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil for cluster restore, got %v", err)
		}
	})

	t.Run("skips when stage has no HA config or mount unavailable", func(t *testing.T) {
		env := newEnv(t)
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, "/empty", false); err != nil {
			t.Fatalf("expected nil when stage has no HA config, got %v", err)
		}

		haIsMounted = func(path string) (bool, error) { return false, nil }
		t.Cleanup(func() { haIsMounted = func(path string) (bool, error) { return true, nil } })
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil when /etc/pve is not mounted, got %v", err)
		}
	})

	t.Run("stage detection and initial prompt errors are propagated", func(t *testing.T) {
		env := newEnv(t)
		restoreFS = statFailFS{
			FS:       env.fs,
			failPath: env.stageRoot + "/etc/pve/ha/resources.cfg",
			err:      fmt.Errorf("boom"),
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, env.stageRoot, false); err == nil {
			t.Fatalf("expected staged stat error")
		}

		restoreFS = env.fs
		haIsMounted = func(path string) (bool, error) { return false, fmt.Errorf("boom") }
		if err := maybeApplyPVEHAWithUI(context.Background(), &fakeRestoreWorkflowUI{}, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil when mount check warns then skips, got %v", err)
		}
		haIsMounted = func(path string) (bool, error) { return true, nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{err: input.ErrInputAborted}},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, nil, env.stageRoot, false); err == nil || !errors.Is(err, input.ErrInputAborted) {
			t.Fatalf("expected apply prompt error, got %v", err)
		}
	})

	t.Run("user skips apply", func(t *testing.T) {
		env := newEnv(t)
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: false}},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("proceed without rollback applies and returns", func(t *testing.T) {
		env := newEnv(t)
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			t.Fatalf("unexpected rollback arm")
			return nil, nil
		}
		appliedCalled := false
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			appliedCalled = true
			return []string{"/etc/pve/ha/resources.cfg"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{ok: true},
			},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if !appliedCalled {
			t.Fatalf("expected HA apply to be called")
		}
	})

	t.Run("full rollback and no rollback prompts can be declined or fail", func(t *testing.T) {
		env := newEnv(t)
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{ok: false},
			},
		}
		safetyBackup := &SafetyBackupResult{BackupPath: "/backups/full.tgz"}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, safetyBackup, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		ui = &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{err: fmt.Errorf("boom")},
			},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, safetyBackup, nil, env.stageRoot, false); err == nil {
			t.Fatalf("expected full rollback prompt error")
		}

		ui = &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{ok: false},
			},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}

		ui = &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{err: fmt.Errorf("boom")},
			},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, nil, env.stageRoot, false); err == nil {
			t.Fatalf("expected no-rollback prompt error")
		}
	})

	t.Run("full rollback backup is used when HA rollback backup missing", func(t *testing.T) {
		env := newEnv(t)
		markerPath := "/tmp/proxsave/ha-full.marker"
		disarmed := false
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			if backupPath != "/backups/full.tgz" {
				t.Fatalf("backupPath=%q, want %q", backupPath, "/backups/full.tgz")
			}
			handle := &haRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/ha-full.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		haDisarmRollback = func(ctx context.Context, logger *logging.Logger, handle *haRollbackHandle) {
			disarmed = true
			disarmHARollback(ctx, logger, handle)
		}
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/ha/resources.cfg"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{ok: true},
				{ok: true},
			},
		}
		safetyBackup := &SafetyBackupResult{BackupPath: "/backups/full.tgz"}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, safetyBackup, nil, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if !disarmed {
			t.Fatalf("expected rollback to be disarmed on commit")
		}
	})

	t.Run("no changes applied disarms rollback", func(t *testing.T) {
		env := newEnv(t)
		markerPath := "/tmp/proxsave/ha-empty.marker"
		disarmed := false
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			handle := &haRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/ha-empty.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		haDisarmRollback = func(ctx context.Context, logger *logging.Logger, handle *haRollbackHandle) {
			disarmed = true
			disarmHARollback(ctx, logger, handle)
		}
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return nil, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: true}},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/ha.tgz"}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, rollback, env.stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if !disarmed {
			t.Fatalf("expected rollback to be disarmed when nothing was applied")
		}
	})

	t.Run("apply errors are propagated", func(t *testing.T) {
		env := newEnv(t)
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return nil, fmt.Errorf("boom")
		}
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{ok: true},
			},
		}
		if err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, nil, env.stageRoot, false); err == nil {
			t.Fatalf("expected apply error")
		}
	})
}

func TestMaybeApplyPVEHAWithUI_CommitOutcomes(t *testing.T) {
	newEnv := func(t *testing.T) *haTestEnv {
		t.Helper()
		env := setupHATestEnv(t)
		if err := env.fs.AddFile(env.stageRoot+"/etc/pve/ha/resources.cfg", []byte("res\n")); err != nil {
			t.Fatalf("add staged HA config: %v", err)
		}
		return env
	}

	baseRollback := &SafetyBackupResult{BackupPath: "/backups/ha.tgz"}

	makeHandle := func(markerPath string, armedAt time.Time) *haRollbackHandle {
		handle := &haRollbackHandle{
			markerPath: markerPath,
			scriptPath: markerPath + ".sh",
			logPath:    markerPath + ".log",
			armedAt:    armedAt,
			timeout:    defaultHARollbackTimeout,
		}
		_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
		return handle
	}

	t.Run("rollback choice returns typed error", func(t *testing.T) {
		env := newEnv(t)
		markerPath := "/tmp/proxsave/ha-rollback.marker"
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			return makeHandle(markerPath, nowRestore()), nil
		}
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/ha/resources.cfg"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{ok: false},
			},
		}
		err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, baseRollback, env.stageRoot, false)
		if err == nil || !errors.Is(err, ErrHAApplyNotCommitted) {
			t.Fatalf("expected ErrHAApplyNotCommitted, got %v", err)
		}
		var typed *HAApplyNotCommittedError
		if !errors.As(err, &typed) || typed == nil {
			t.Fatalf("expected typed HAApplyNotCommittedError, got %T", err)
		}
		if !typed.RollbackArmed || typed.RollbackMarker != markerPath {
			t.Fatalf("unexpected typed error fields: %#v", typed)
		}
	})

	t.Run("commit prompt abort returns abort error", func(t *testing.T) {
		env := newEnv(t)
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			return makeHandle("/tmp/proxsave/ha-abort.marker", nowRestore()), nil
		}
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/ha/resources.cfg"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{err: input.ErrInputAborted},
			},
		}
		err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, baseRollback, env.stageRoot, false)
		if err == nil || !errors.Is(err, input.ErrInputAborted) {
			t.Fatalf("expected input abort, got %v", err)
		}
	})

	t.Run("commit prompt failure returns typed error", func(t *testing.T) {
		env := newEnv(t)
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			return makeHandle("/tmp/proxsave/ha-fail.marker", nowRestore()), nil
		}
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/ha/resources.cfg"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},
				{err: fmt.Errorf("boom")},
			},
		}
		err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, baseRollback, env.stageRoot, false)
		if err == nil || !errors.Is(err, ErrHAApplyNotCommitted) {
			t.Fatalf("expected ErrHAApplyNotCommitted, got %v", err)
		}
	})

	t.Run("expired rollback handle returns typed error without commit prompt", func(t *testing.T) {
		env := newEnv(t)
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			return makeHandle("/tmp/proxsave/ha-expired.marker", nowRestore().Add(-defaultHARollbackTimeout-time.Second)), nil
		}
		haApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/ha/resources.cfg"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: true}},
		}
		err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, baseRollback, env.stageRoot, false)
		if err == nil || !errors.Is(err, ErrHAApplyNotCommitted) {
			t.Fatalf("expected ErrHAApplyNotCommitted, got %v", err)
		}
		if ui.calls != 1 {
			t.Fatalf("expected only initial apply prompt, got %d calls", ui.calls)
		}
	})

	t.Run("arm rollback failure is wrapped", func(t *testing.T) {
		env := newEnv(t)
		haArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*haRollbackHandle, error) {
			return nil, fmt.Errorf("boom")
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: true}},
		}
		err := maybeApplyPVEHAWithUI(context.Background(), ui, env.logger, env.plan, nil, baseRollback, env.stageRoot, false)
		if err == nil || !strings.Contains(err.Error(), "arm HA rollback") {
			t.Fatalf("expected wrapped arm error, got %v", err)
		}
	})
}

func TestApplyPVEHAFromStage_BranchCoverage(t *testing.T) {
	t.Run("blank stage root returns nil", func(t *testing.T) {
		if applied, err := applyPVEHAFromStage(newTestLogger(), "   "); err != nil || len(applied) != 0 {
			t.Fatalf("applied=%#v err=%v; want nil,nil", applied, err)
		}
	})

	t.Run("ensure dir, staged stat, copy and remove failures are propagated", func(t *testing.T) {
		env := setupHATestEnv(t)
		stageRoot := env.stageRoot
		if err := env.fs.AddFile(stageRoot+"/etc/pve/ha/resources.cfg", []byte("res\n")); err != nil {
			t.Fatalf("add resources.cfg: %v", err)
		}

		restoreFS = mkdirAllFailFS{
			FS:       env.fs,
			failPath: "/etc/pve/ha",
			err:      fmt.Errorf("boom"),
		}
		if _, err := applyPVEHAFromStage(env.logger, stageRoot); err == nil || !strings.Contains(err.Error(), "ensure /etc/pve/ha") {
			t.Fatalf("expected ensure error, got %v", err)
		}

		restoreFS = statFailFS{
			FS:       env.fs,
			failPath: stageRoot + "/etc/pve/ha/resources.cfg",
			err:      fmt.Errorf("boom"),
		}
		if _, err := applyPVEHAFromStage(env.logger, stageRoot); err == nil || !strings.Contains(err.Error(), "stat") {
			t.Fatalf("expected stage stat error, got %v", err)
		}

		restoreFS = readFileFailFS{
			FS:       env.fs,
			failPath: stageRoot + "/etc/pve/ha/resources.cfg",
			err:      fmt.Errorf("boom"),
		}
		if _, err := applyPVEHAFromStage(env.logger, stageRoot); err == nil {
			t.Fatalf("expected copy error")
		}

		if err := env.fs.AddFile("/etc/pve/ha/groups.cfg", []byte("grp\n")); err != nil {
			t.Fatalf("add existing groups.cfg: %v", err)
		}
		restoreFS = removeFailFS{
			FS:       env.fs,
			failPath: "/etc/pve/ha/groups.cfg",
			err:      fmt.Errorf("boom"),
		}
		if _, err := applyPVEHAFromStage(env.logger, stageRoot); err == nil || !strings.Contains(err.Error(), "remove /etc/pve/ha/groups.cfg") {
			t.Fatalf("expected remove error, got %v", err)
		}
	})
}
