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

func TestAccessControlApplyNotCommittedError_UnwrapAndMessage(t *testing.T) {
	var e *AccessControlApplyNotCommittedError
	if e.Error() != ErrAccessControlApplyNotCommitted.Error() {
		t.Fatalf("nil receiver Error()=%q want %q", e.Error(), ErrAccessControlApplyNotCommitted.Error())
	}
	if !errors.Is(error(e), ErrAccessControlApplyNotCommitted) {
		t.Fatalf("expected errors.Is(..., ErrAccessControlApplyNotCommitted) to be true")
	}
	if errors.Unwrap(error(e)) != ErrAccessControlApplyNotCommitted {
		t.Fatalf("expected Unwrap to return ErrAccessControlApplyNotCommitted")
	}

	e2 := &AccessControlApplyNotCommittedError{}
	if e2.Error() != ErrAccessControlApplyNotCommitted.Error() {
		t.Fatalf("Error()=%q want %q", e2.Error(), ErrAccessControlApplyNotCommitted.Error())
	}
}

func TestAccessControlRollbackHandle_Remaining(t *testing.T) {
	var h *accessControlRollbackHandle
	if got := h.remaining(time.Now()); got != 0 {
		t.Fatalf("nil handle remaining=%s want 0", got)
	}

	handle := &accessControlRollbackHandle{
		armedAt: time.Unix(100, 0),
		timeout: 10 * time.Second,
	}
	if got := handle.remaining(time.Unix(105, 0)); got != 5*time.Second {
		t.Fatalf("remaining=%s want %s", got, 5*time.Second)
	}
	if got := handle.remaining(time.Unix(999, 0)); got != 0 {
		t.Fatalf("remaining=%s want 0", got)
	}
}

func TestBuildAccessControlApplyNotCommittedError_PopulatesFields(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if e := buildAccessControlApplyNotCommittedError(nil); e == nil {
		t.Fatalf("expected error struct, got nil")
	} else if e.RollbackArmed || e.RollbackMarker != "" || e.RollbackLog != "" || !e.RollbackDeadline.IsZero() {
		t.Fatalf("unexpected fields for nil handle: %#v", e)
	}

	armedAt := time.Unix(10, 0)
	handle := &accessControlRollbackHandle{
		markerPath: "  /tmp/ac.marker \n",
		logPath:    " /tmp/ac.log\t",
		armedAt:    armedAt,
		timeout:    3 * time.Second,
	}
	if err := fakeFS.AddFile("/tmp/ac.marker", []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}

	e := buildAccessControlApplyNotCommittedError(handle)
	if e.RollbackMarker != "/tmp/ac.marker" {
		t.Fatalf("RollbackMarker=%q", e.RollbackMarker)
	}
	if e.RollbackLog != "/tmp/ac.log" {
		t.Fatalf("RollbackLog=%q", e.RollbackLog)
	}
	if !e.RollbackArmed {
		t.Fatalf("expected RollbackArmed=true")
	}
	if !e.RollbackDeadline.Equal(armedAt.Add(3 * time.Second)) {
		t.Fatalf("RollbackDeadline=%s want %s", e.RollbackDeadline, armedAt.Add(3*time.Second))
	}
}

func TestStageHasPVEAccessControlConfig_DetectsFilesAndErrors(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	ok, err := stageHasPVEAccessControlConfig("  ")
	if err != nil || ok {
		t.Fatalf("expected ok=false err=nil for empty stageRoot, got ok=%v err=%v", ok, err)
	}

	ok, err = stageHasPVEAccessControlConfig("/stage-empty")
	if err != nil || ok {
		t.Fatalf("expected ok=false err=nil for missing files, got ok=%v err=%v", ok, err)
	}

	if err := fakeFS.AddDir("/stage-dir/etc/pve/user.cfg"); err != nil {
		t.Fatalf("add staged dir: %v", err)
	}
	ok, err = stageHasPVEAccessControlConfig("/stage-dir")
	if err != nil || ok {
		t.Fatalf("expected ok=false err=nil when candidates are dirs, got ok=%v err=%v", ok, err)
	}

	if err := fakeFS.AddFile("/stage-hit/etc/pve/user.cfg", []byte("x")); err != nil {
		t.Fatalf("add staged file: %v", err)
	}
	ok, err = stageHasPVEAccessControlConfig("/stage-hit")
	if err != nil || !ok {
		t.Fatalf("expected ok=true err=nil when stage has access control files, got ok=%v err=%v", ok, err)
	}

	fakeFS.StatErrors[filepath.Clean("/stage-err/etc/pve/user.cfg")] = fmt.Errorf("boom")
	ok, err = stageHasPVEAccessControlConfig("/stage-err")
	if err == nil || ok {
		t.Fatalf("expected error, got ok=%v err=%v", ok, err)
	}
}

func TestBuildAccessControlRollbackScript_QuotesPaths(t *testing.T) {
	script := buildAccessControlRollbackScript("/tmp/marker path", "/tmp/backup's.tar.gz", "/tmp/log path")
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

func TestArmAccessControlRollback_SystemdAndBackgroundPaths(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	fakeTime := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeTime

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)

	t.Run("rejects invalid args", func(t *testing.T) {
		if _, err := armAccessControlRollback(context.Background(), logger, "", 1*time.Second, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error for empty backupPath")
		}
		if _, err := armAccessControlRollback(context.Background(), logger, "/backup.tgz", 0, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error for invalid timeout")
		}
	})

	t.Run("uses systemd-run when available", func(t *testing.T) {
		binDir := t.TempDir()
		oldPath := os.Getenv("PATH")
		if err := os.Setenv("PATH", binDir); err != nil {
			t.Fatalf("set PATH: %v", err)
		}
		t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
		writeExecutable(t, binDir, "systemd-run")

		fakeCmd := &FakeCommandRunner{}
		restoreCmd = fakeCmd

		handle, err := armAccessControlRollback(context.Background(), logger, "/backup.tgz", 2*time.Second, "/tmp/proxsave")
		if err != nil {
			t.Fatalf("armAccessControlRollback error: %v", err)
		}
		if handle == nil || handle.unitName == "" {
			t.Fatalf("expected systemd unit name, got %#v", handle)
		}
		if got := fakeCmd.CallsList(); len(got) != 1 || !strings.HasPrefix(got[0], "systemd-run --unit=proxsave-access-control-rollback-20200102_030405") {
			t.Fatalf("unexpected calls: %#v", got)
		}
		if data, err := fakeFS.ReadFile(handle.markerPath); err != nil || string(data) != "pending\n" {
			t.Fatalf("marker read err=%v data=%q", err, string(data))
		}
	})

	t.Run("falls back to background timer on systemd-run failure", func(t *testing.T) {
		binDir := t.TempDir()
		oldPath := os.Getenv("PATH")
		if err := os.Setenv("PATH", binDir); err != nil {
			t.Fatalf("set PATH: %v", err)
		}
		t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
		writeExecutable(t, binDir, "systemd-run")

		fakeCmd := &FakeCommandRunner{
			Errors: map[string]error{},
		}
		restoreCmd = fakeCmd

		timestamp := fakeTime.Current.Format("20060102_150405")
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("access_control_rollback_%s.sh", timestamp))
		systemdKey := "systemd-run --unit=proxsave-access-control-rollback-" + timestamp + " --on-active=2s /bin/sh " + scriptPath
		fakeCmd.Errors[systemdKey] = fmt.Errorf("fail")

		handle, err := armAccessControlRollback(context.Background(), logger, "/backup.tgz", 2*time.Second, "/tmp/proxsave")
		if err != nil {
			t.Fatalf("armAccessControlRollback error: %v", err)
		}
		if handle == nil {
			t.Fatalf("expected handle")
		}
		if handle.unitName != "" {
			t.Fatalf("expected unitName cleared after systemd-run failure, got %q", handle.unitName)
		}

		cmd := fmt.Sprintf("nohup sh -c 'sleep %d; /bin/sh %s' >/dev/null 2>&1 &", 2, scriptPath)
		wantBackground := "sh -c " + cmd
		calls := fakeCmd.CallsList()
		if len(calls) != 2 || calls[1] != wantBackground {
			t.Fatalf("unexpected calls: %#v", calls)
		}
	})

	t.Run("background timer failure returns error", func(t *testing.T) {
		emptyBin := t.TempDir()
		oldPath := os.Getenv("PATH")
		if err := os.Setenv("PATH", emptyBin); err != nil {
			t.Fatalf("set PATH: %v", err)
		}
		t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

		fakeCmd := &FakeCommandRunner{
			Errors: map[string]error{},
		}
		restoreCmd = fakeCmd

		timestamp := fakeTime.Current.Format("20060102_150405")
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("access_control_rollback_%s.sh", timestamp))
		cmd := fmt.Sprintf("nohup sh -c 'sleep %d; /bin/sh %s' >/dev/null 2>&1 &", 1, scriptPath)
		backgroundKey := "sh -c " + cmd
		fakeCmd.Errors[backgroundKey] = fmt.Errorf("boom")

		if _, err := armAccessControlRollback(context.Background(), logger, "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestArmAccessControlRollback_DefaultWorkDirAndMinTimeout(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	fakeTime := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeTime

	emptyBin := t.TempDir()
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", emptyBin); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	handle, err := armAccessControlRollback(context.Background(), newTestLogger(), "/backup.tgz", 500*time.Millisecond, "   ")
	if err != nil {
		t.Fatalf("armAccessControlRollback error: %v", err)
	}
	if handle == nil || handle.workDir != "/tmp/proxsave" {
		t.Fatalf("unexpected handle: %#v", handle)
	}
	if data, err := fakeFS.ReadFile(handle.markerPath); err != nil || string(data) != "pending\n" {
		t.Fatalf("marker err=%v data=%q", err, string(data))
	}
	calls := fakeCmd.CallsList()
	if len(calls) != 1 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	if !strings.Contains(calls[0], "sleep 1; /bin/sh") {
		t.Fatalf("expected timeoutSeconds to clamp to 1, got call=%q", calls[0])
	}
}

func TestArmAccessControlRollback_ReturnsErrorOnMkdirAllFailure(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeFS.MkdirAllErr = fmt.Errorf("disk full")
	restoreFS = fakeFS

	if _, err := armAccessControlRollback(context.Background(), newTestLogger(), "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestArmAccessControlRollback_ReturnsErrorOnMarkerWriteFailure(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeFS.WriteErr = fmt.Errorf("disk full")
	restoreFS = fakeFS

	if _, err := armAccessControlRollback(context.Background(), newTestLogger(), "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "write rollback marker") {
		t.Fatalf("expected write rollback marker error, got %v", err)
	}
}

func TestArmAccessControlRollback_ReturnsErrorOnScriptWriteFailure(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	base := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(base.Root) })

	fakeTime := &FakeTime{Current: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
	restoreTime = fakeTime
	timestamp := fakeTime.Current.Format("20060102_150405")
	scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("access_control_rollback_%s.sh", timestamp))

	restoreFS = writeFileFailFS{FS: base, failPath: scriptPath, err: fmt.Errorf("disk full")}

	if _, err := armAccessControlRollback(context.Background(), newTestLogger(), "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "write rollback script") {
		t.Fatalf("expected write rollback script error, got %v", err)
	}
}

func TestDisarmAccessControlRollback_RemovesMarkerScriptAndStopsTimer(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	binDir := t.TempDir()
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	writeExecutable(t, binDir, "systemctl")

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	handle := &accessControlRollbackHandle{
		markerPath: "/tmp/proxsave/ac.marker",
		scriptPath: "/tmp/proxsave/ac.sh",
		unitName:   "proxsave-access-control-rollback-test",
	}
	if err := fakeFS.AddFile(handle.markerPath, []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}
	if err := fakeFS.AddFile(handle.scriptPath, []byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("add script: %v", err)
	}

	disarmAccessControlRollback(context.Background(), newTestLogger(), handle)

	if _, err := fakeFS.Stat(handle.markerPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected marker removed; stat err=%v", err)
	}
	if _, err := fakeFS.Stat(handle.scriptPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected script removed; stat err=%v", err)
	}

	timerUnit := handle.unitName + ".timer"
	want1 := "systemctl stop " + timerUnit
	want2 := "systemctl reset-failed " + handle.unitName + ".service " + timerUnit
	calls := fakeCmd.CallsList()
	if len(calls) != 2 || calls[0] != want1 || calls[1] != want2 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestMaybeApplyPVEAccessControlFromClusterBackupWithUI_CoversUserFlows(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	origGeteuid := accessControlApplyGeteuid
	origMounted := accessControlIsMounted
	origRealFS := accessControlIsRealRestoreFS
	origArm := accessControlArmRollback
	origDisarm := accessControlDisarmRollback
	origApply := accessControlApplyFromStage
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
		accessControlApplyGeteuid = origGeteuid
		accessControlIsMounted = origMounted
		accessControlIsRealRestoreFS = origRealFS
		accessControlArmRollback = origArm
		accessControlDisarmRollback = origDisarm
		accessControlApplyFromStage = origApply
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	restoreTime = &FakeTime{Current: time.Unix(100, 0)}
	restoreCmd = &FakeCommandRunner{}

	accessControlIsRealRestoreFS = func(fs FS) bool { return true }
	accessControlApplyGeteuid = func() int { return 0 }
	accessControlIsMounted = func(path string) (bool, error) { return true, nil }

	plan := &RestorePlan{
		SystemType:       SystemTypePVE,
		ClusterBackup:    true,
		NormalCategories: []Category{{ID: "pve_access_control"}},
	}
	stageWithAC := "/stage-ac"
	if err := fakeFS.AddFile(stageWithAC+"/etc/pve/user.cfg", []byte("x")); err != nil {
		t.Fatalf("add staged user.cfg: %v", err)
	}
	stageWithoutAC := "/stage-empty"
	logger := newTestLogger()

	t.Run("errors when ui missing", func(t *testing.T) {
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), nil, logger, plan, nil, nil, stageWithAC, false)
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("skips when /etc/pve not mounted", func(t *testing.T) {
		accessControlIsMounted = func(path string) (bool, error) { return false, nil }
		t.Cleanup(func() { accessControlIsMounted = func(path string) (bool, error) { return true, nil } })

		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, nil, stageWithAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("skips when stage has no access control files", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, nil, stageWithoutAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("user skips apply", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: false}},
		}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, nil, stageWithAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("missing rollback backup declines full rollback", func(t *testing.T) {
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			t.Fatalf("unexpected rollback arm")
			return nil, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},  // Apply now
				{ok: false}, // Skip full rollback
			},
		}
		safety := &SafetyBackupResult{BackupPath: "/backups/full.tgz"}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, safety, nil, stageWithAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("proceed without rollback applies and returns without commit prompt", func(t *testing.T) {
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			t.Fatalf("unexpected rollback arm")
			return nil, nil
		}
		called := false
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error {
			called = true
			return nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Proceed without rollback
			},
		}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, nil, stageWithAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if !called {
			t.Fatalf("expected access control apply to be invoked")
		}
	})

	t.Run("commit keeps changes and disarms rollback", func(t *testing.T) {
		markerPath := "/tmp/proxsave/ac.marker"
		scriptPath := "/tmp/proxsave/ac.sh"
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			handle := &accessControlRollbackHandle{
				markerPath: markerPath,
				scriptPath: scriptPath,
				logPath:    "/tmp/proxsave/ac.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			_ = restoreFS.WriteFile(scriptPath, []byte("#!/bin/sh\n"), 0o640)
			return handle, nil
		}
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error { return nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Commit
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/access-control.tgz"}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, rollback, stageWithAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if _, err := fakeFS.Stat(markerPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected rollback marker removed; stat err=%v", err)
		}
		if _, err := fakeFS.Stat(scriptPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected rollback script removed; stat err=%v", err)
		}
	})

	t.Run("rollback requested returns typed error with marker armed", func(t *testing.T) {
		markerPath := "/tmp/proxsave/ac.marker"
		armedAt := nowRestore()
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			handle := &accessControlRollbackHandle{
				markerPath: markerPath,
				scriptPath: "/tmp/proxsave/ac.sh",
				logPath:    "/tmp/proxsave/ac.log",
				armedAt:    armedAt,
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error { return nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},  // Apply now
				{ok: false}, // Rollback
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/access-control.tgz"}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, rollback, stageWithAC, false)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, ErrAccessControlApplyNotCommitted) {
			t.Fatalf("expected ErrAccessControlApplyNotCommitted, got %v", err)
		}
		var typed *AccessControlApplyNotCommittedError
		if !errors.As(err, &typed) || typed == nil {
			t.Fatalf("expected AccessControlApplyNotCommittedError, got %T", err)
		}
		if !typed.RollbackArmed || typed.RollbackMarker != markerPath || typed.RollbackLog != "/tmp/proxsave/ac.log" {
			t.Fatalf("unexpected error fields: %#v", typed)
		}
		if typed.RollbackDeadline.IsZero() || !typed.RollbackDeadline.Equal(armedAt.Add(defaultAccessControlRollbackTimeout)) {
			t.Fatalf("unexpected RollbackDeadline=%s", typed.RollbackDeadline)
		}
		if _, err := fakeFS.Stat(markerPath); err != nil {
			t.Fatalf("expected marker to still exist, stat err=%v", err)
		}
	})

	t.Run("commit prompt abort returns abort error", func(t *testing.T) {
		markerPath := "/tmp/proxsave/ac.marker"
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			handle := &accessControlRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/ac.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error { return nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},                   // Apply now
				{err: input.ErrInputAborted}, // Abort at commit prompt
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/access-control.tgz"}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, rollback, stageWithAC, false)
		if err == nil || !errors.Is(err, input.ErrInputAborted) {
			t.Fatalf("expected abort error, got %v", err)
		}
	})

	t.Run("commit prompt failure returns typed error", func(t *testing.T) {
		markerPath := "/tmp/proxsave/ac.marker"
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			handle := &accessControlRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/ac.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error { return nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},                // Apply now
				{err: fmt.Errorf("boom")}, // Commit prompt fails
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/access-control.tgz"}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, rollback, stageWithAC, false)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, ErrAccessControlApplyNotCommitted) {
			t.Fatalf("expected ErrAccessControlApplyNotCommitted, got %v", err)
		}
		if _, err := fakeFS.Stat(markerPath); err != nil {
			t.Fatalf("expected marker to still exist, stat err=%v", err)
		}
	})

	t.Run("remaining timeout returns typed error without commit prompt", func(t *testing.T) {
		markerPath := "/tmp/proxsave/ac.marker"
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			handle := &accessControlRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/ac.log",
				armedAt:    nowRestore().Add(-timeout - time.Second),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error { return nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: true}}, // Apply now only
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/access-control.tgz"}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, rollback, stageWithAC, false)
		if err == nil || !errors.Is(err, ErrAccessControlApplyNotCommitted) {
			t.Fatalf("expected ErrAccessControlApplyNotCommitted, got %v", err)
		}
	})

	t.Run("dry run skips apply", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, nil, stageWithAC, true)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("non-root skips apply", func(t *testing.T) {
		accessControlApplyGeteuid = func() int { return 1000 }
		t.Cleanup(func() { accessControlApplyGeteuid = func() int { return 0 } })

		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEAccessControlFromClusterBackupWithUI(context.Background(), ui, logger, plan, nil, nil, stageWithAC, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}

func TestMaybeApplyAccessControlWithUI_BranchCoverage(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	origGeteuid := accessControlApplyGeteuid
	origMounted := accessControlIsMounted
	origRealFS := accessControlIsRealRestoreFS
	origArm := accessControlArmRollback
	origApply := accessControlApplyFromStage
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
		accessControlApplyGeteuid = origGeteuid
		accessControlIsMounted = origMounted
		accessControlIsRealRestoreFS = origRealFS
		accessControlArmRollback = origArm
		accessControlApplyFromStage = origApply
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(100, 0)}
	restoreCmd = &FakeCommandRunner{}

	accessControlIsRealRestoreFS = func(fs FS) bool { return true }
	accessControlApplyGeteuid = func() int { return 0 }
	accessControlIsMounted = func(path string) (bool, error) { return true, nil }

	stageRoot := "/stage"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/user.cfg", []byte("x")); err != nil {
		t.Fatalf("add staged user.cfg: %v", err)
	}
	logger := newTestLogger()

	t.Run("nil plan returns nil", func(t *testing.T) {
		if err := maybeApplyAccessControlWithUI(context.Background(), &fakeRestoreWorkflowUI{}, logger, nil, nil, nil, stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("empty stageRoot skips", func(t *testing.T) {
		plan := &RestorePlan{NormalCategories: []Category{{ID: "pve_access_control"}}}
		if err := maybeApplyAccessControlWithUI(context.Background(), &fakeRestoreWorkflowUI{}, logger, plan, nil, nil, "   ", false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("no relevant categories returns nil", func(t *testing.T) {
		plan := &RestorePlan{NormalCategories: []Category{{ID: "pve_firewall"}}}
		if err := maybeApplyAccessControlWithUI(context.Background(), &fakeRestoreWorkflowUI{}, logger, plan, nil, nil, stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("errors when ui missing", func(t *testing.T) {
		plan := &RestorePlan{
			SystemType:       SystemTypePVE,
			ClusterBackup:    true,
			NormalCategories: []Category{{ID: "pve_access_control"}},
		}
		if err := maybeApplyAccessControlWithUI(context.Background(), nil, logger, plan, nil, nil, stageRoot, false); err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("cluster backup path is used", func(t *testing.T) {
		markerPath := "/tmp/proxsave/ac.marker"
		accessControlArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*accessControlRollbackHandle, error) {
			handle := &accessControlRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/ac.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		accessControlApplyFromStage = func(ctx context.Context, logger *logging.Logger, stageRoot string) error { return nil }

		plan := &RestorePlan{
			SystemType:       SystemTypePVE,
			ClusterBackup:    true,
			NormalCategories: []Category{{ID: "pve_access_control"}},
		}
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},  // Apply now
				{ok: false}, // Rollback
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/access-control.tgz"}
		err := maybeApplyAccessControlWithUI(context.Background(), ui, logger, plan, nil, rollback, stageRoot, false)
		if err == nil || !errors.Is(err, ErrAccessControlApplyNotCommitted) {
			t.Fatalf("expected ErrAccessControlApplyNotCommitted, got %v", err)
		}
	})

	t.Run("default path uses stage apply", func(t *testing.T) {
		plan := &RestorePlan{
			SystemType:       SystemTypePBS,
			NormalCategories: []Category{{ID: "pbs_access_control"}},
		}
		if err := maybeApplyAccessControlWithUI(context.Background(), &fakeRestoreWorkflowUI{}, logger, plan, nil, nil, stageRoot, false); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}

