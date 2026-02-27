package orchestrator

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type writeFailFS struct {
	FS
	failPath string
	err      error
}

func (f writeFailFS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return f.err
	}
	return f.FS.WriteFile(path, data, perm)
}

func newDiscardLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

func TestNetworkApplyNotCommittedError_ErrorAndUnwrap(t *testing.T) {
	var err *NetworkApplyNotCommittedError
	if got := err.Error(); got != ErrNetworkApplyNotCommitted.Error() {
		t.Fatalf("Error()=%q want %q", got, ErrNetworkApplyNotCommitted.Error())
	}
	if got := err.Unwrap(); !errors.Is(got, ErrNetworkApplyNotCommitted) {
		t.Fatalf("Unwrap()=%v want %v", got, ErrNetworkApplyNotCommitted)
	}

	err = &NetworkApplyNotCommittedError{RollbackLog: "/tmp/log"}
	if got := err.Error(); got != ErrNetworkApplyNotCommitted.Error() {
		t.Fatalf("Error()=%q want %q", got, ErrNetworkApplyNotCommitted.Error())
	}
	if got := err.Unwrap(); got != ErrNetworkApplyNotCommitted {
		t.Fatalf("Unwrap()=%v want %v", got, ErrNetworkApplyNotCommitted)
	}
}

func TestNetworkRollbackHandleRemaining(t *testing.T) {
	var h *networkRollbackHandle
	if got := h.remaining(time.Now()); got != 0 {
		t.Fatalf("nil remaining=%s want 0", got)
	}

	h = &networkRollbackHandle{
		armedAt: time.Date(2026, 2, 1, 1, 2, 3, 0, time.UTC),
		timeout: 10 * time.Second,
	}
	if got := h.remaining(h.armedAt); got != 10*time.Second {
		t.Fatalf("remaining=%s want %s", got, 10*time.Second)
	}
	if got := h.remaining(h.armedAt.Add(4 * time.Second)); got != 6*time.Second {
		t.Fatalf("remaining=%s want %s", got, 6*time.Second)
	}
	if got := h.remaining(h.armedAt.Add(20 * time.Second)); got != 0 {
		t.Fatalf("remaining=%s want 0", got)
	}
}

func TestShouldAttemptNetworkApply(t *testing.T) {
	if shouldAttemptNetworkApply(nil) {
		t.Fatalf("expected false for nil plan")
	}
	if shouldAttemptNetworkApply(&RestorePlan{NormalCategories: []Category{{ID: "storage_pve"}}}) {
		t.Fatalf("expected false when network category not present")
	}
	if !shouldAttemptNetworkApply(&RestorePlan{NormalCategories: []Category{{ID: "network"}}}) {
		t.Fatalf("expected true when network category present")
	}
}

func TestExtractIPFromSnapshot_EmptyArgsReturnUnknown(t *testing.T) {
	if got := extractIPFromSnapshot("", "vmbr0"); got != "unknown" {
		t.Fatalf("got %q want unknown", got)
	}
	if got := extractIPFromSnapshot("/snap.txt", ""); got != "unknown" {
		t.Fatalf("got %q want unknown", got)
	}
}

func TestExtractIPFromSnapshot_IgnoresLinesOutsideAddrSection(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := strings.Join([]string{
		"$ ip -br addr",
		"lo UNKNOWN 127.0.0.1/8",
		"$ ip route show",
		"vmbr0 UP 192.0.2.10/24",
		"",
	}, "\n")
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if got := extractIPFromSnapshot("/snap.txt", "vmbr0"); got != "unknown" {
		t.Fatalf("got %q want unknown", got)
	}
}

func TestExtractIPFromSnapshot_SkipsInvalidTokensButReturnsFirstValid(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := strings.Join([]string{
		"$ ip -br addr",
		"vmbr0 UP not-an-ip 2a01:db8::2/64",
		"",
	}, "\n")
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if got := extractIPFromSnapshot("/snap.txt", "vmbr0"); got != "2a01:db8::2/64" {
		t.Fatalf("got %q want %q", got, "2a01:db8::2/64")
	}
}

func TestExtractIPFromSnapshot_SkipsErrorLinesAndParsesNext(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := strings.Join([]string{
		"$ ip -br addr",
		"ERROR: ip failed",
		"vmbr0 UP 192.0.2.55/24",
		"",
	}, "\n")
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if got := extractIPFromSnapshot("/snap.txt", "vmbr0"); got != "192.0.2.55/24" {
		t.Fatalf("got %q want %q", got, "192.0.2.55/24")
	}
}

func TestExtractIPFromSnapshot_ReturnsUnknownWhenNoValidAddressTokens(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := strings.Join([]string{
		"$ ip -br addr",
		"vmbr0 UP not-an-ip also-bad",
		"",
	}, "\n")
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if got := extractIPFromSnapshot("/snap.txt", "vmbr0"); got != "unknown" {
		t.Fatalf("got %q want unknown", got)
	}
}

func TestExtractIPFromSnapshot_IgnoresCommandsBeforeAddrSection(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := strings.Join([]string{
		"$ ip route show",
		"default via 192.0.2.1 dev vmbr0",
		"$ ip -br addr",
		"vmbr0 UP 192.0.2.99/24",
		"",
	}, "\n")
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if got := extractIPFromSnapshot("/snap.txt", "vmbr0"); got != "192.0.2.99/24" {
		t.Fatalf("got %q want %q", got, "192.0.2.99/24")
	}
}

func TestBuildNetworkApplyNotCommittedError_HandleNilIfaceEmpty(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	restoreFS = NewFakeFS()
	restoreCmd = &FakeCommandRunner{}

	logger := newDiscardLogger()

	got := buildNetworkApplyNotCommittedError(context.Background(), logger, "", nil)
	if got.RollbackArmed {
		t.Fatalf("RollbackArmed=true want false")
	}
	if got.RollbackLog != "" || got.RollbackMarker != "" {
		t.Fatalf("expected empty rollback paths, got log=%q marker=%q", got.RollbackLog, got.RollbackMarker)
	}
	if got.RestoredIP != "unknown" || got.OriginalIP != "unknown" {
		t.Fatalf("restored=%q original=%q want unknown/unknown", got.RestoredIP, got.OriginalIP)
	}
	if !got.RollbackDeadline.IsZero() {
		t.Fatalf("RollbackDeadline=%s want zero", got.RollbackDeadline)
	}
}

func TestBuildNetworkApplyNotCommittedError_ArmedWithMarkerAndSnapshots(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	fakeCmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ip -o addr show dev vmbr0 scope global": []byte(strings.Join([]string{
				"2: vmbr0    inet 192.0.2.10/24 brd 192.0.2.255 scope global vmbr0",
				"2: vmbr0    inet6 2001:db8::1/64 scope global",
			}, "\n")),
			"ip route show default": []byte("default via 192.0.2.1 dev vmbr0\n"),
		},
	}
	restoreCmd = fakeCmd

	logger := newDiscardLogger()

	handle := &networkRollbackHandle{
		workDir:    "/work",
		markerPath: "/work/marker",
		logPath:    "/work/log",
		armedAt:    time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC),
		timeout:    3 * time.Minute,
	}

	if err := fakeFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	beforeSnapshot := strings.Join([]string{
		"$ ip -br addr",
		"vmbr0 UP 10.0.0.2/24",
		"",
	}, "\n")
	if err := fakeFS.WriteFile("/work/before.txt", []byte(beforeSnapshot), 0o600); err != nil {
		t.Fatalf("write before snapshot: %v", err)
	}

	got := buildNetworkApplyNotCommittedError(context.Background(), logger, "vmbr0", handle)
	if !got.RollbackArmed {
		t.Fatalf("RollbackArmed=false want true")
	}
	if got.RollbackLog != "/work/log" || got.RollbackMarker != "/work/marker" {
		t.Fatalf("paths log=%q marker=%q want /work/log /work/marker", got.RollbackLog, got.RollbackMarker)
	}
	if got.RestoredIP != "192.0.2.10/24, 2001:db8::1/64" {
		t.Fatalf("RestoredIP=%q", got.RestoredIP)
	}
	if got.OriginalIP != "10.0.0.2/24" {
		t.Fatalf("OriginalIP=%q want %q", got.OriginalIP, "10.0.0.2/24")
	}
	wantDeadline := handle.armedAt.Add(handle.timeout)
	if !got.RollbackDeadline.Equal(wantDeadline) {
		t.Fatalf("RollbackDeadline=%s want %s", got.RollbackDeadline, wantDeadline)
	}
}

func TestBuildNetworkApplyNotCommittedError_MarkerMissingAndIPQueryFails(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	restoreCmd = &FakeCommandRunner{
		Errors: map[string]error{
			"ip -o addr show dev vmbr0 scope global": errors.New("boom"),
		},
	}

	logger := newDiscardLogger()
	handle := &networkRollbackHandle{
		workDir:    "/work",
		markerPath: "/work/missing",
		armedAt:    time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC),
		timeout:    1 * time.Minute,
	}

	got := buildNetworkApplyNotCommittedError(context.Background(), logger, "vmbr0", handle)
	if got.RollbackArmed {
		t.Fatalf("RollbackArmed=true want false when marker missing")
	}
	if got.RestoredIP != "unknown" {
		t.Fatalf("RestoredIP=%q want unknown", got.RestoredIP)
	}
	if got.OriginalIP != "unknown" {
		t.Fatalf("OriginalIP=%q want unknown", got.OriginalIP)
	}
}

func TestRollbackAlreadyRunning(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	logger := newDiscardLogger()
	pathDir := t.TempDir()

	t.Run("skip when handle nil", func(t *testing.T) {
		t.Setenv("PATH", pathDir)
		restoreCmd = &FakeCommandRunner{}
		if rollbackAlreadyRunning(context.Background(), logger, nil) {
			t.Fatalf("expected false for nil handle")
		}
	})

	t.Run("skip when unit name empty", func(t *testing.T) {
		t.Setenv("PATH", pathDir)
		restoreCmd = &FakeCommandRunner{}
		if rollbackAlreadyRunning(context.Background(), logger, &networkRollbackHandle{unitName: ""}) {
			t.Fatalf("expected false when unitName empty")
		}
	})

	t.Run("skip when systemctl missing", func(t *testing.T) {
		t.Setenv("PATH", pathDir)
		restoreCmd = &FakeCommandRunner{}
		if rollbackAlreadyRunning(context.Background(), logger, &networkRollbackHandle{unitName: "x"}) {
			t.Fatalf("expected false when systemctl not available")
		}
	})

	t.Run("running when active or activating", func(t *testing.T) {
		writeExecutable(t, pathDir, "systemctl")
		t.Setenv("PATH", pathDir)

		for _, tc := range []struct {
			state string
			want  bool
		}{
			{state: "active\n", want: true},
			{state: "activating\n", want: true},
			{state: "inactive\n", want: false},
		} {
			restoreCmd = &FakeCommandRunner{
				Outputs: map[string][]byte{
					"systemctl is-active unit.service": []byte(tc.state),
				},
			}
			if got := rollbackAlreadyRunning(context.Background(), logger, &networkRollbackHandle{unitName: "unit"}); got != tc.want {
				t.Fatalf("state=%q got=%v want=%v", tc.state, got, tc.want)
			}
		}
	})

	t.Run("not running when systemctl errors", func(t *testing.T) {
		writeExecutable(t, pathDir, "systemctl")
		t.Setenv("PATH", pathDir)
		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{
				"systemctl is-active unit.service": errors.New("boom"),
			},
		}
		if rollbackAlreadyRunning(context.Background(), logger, &networkRollbackHandle{unitName: "unit"}) {
			t.Fatalf("expected false when systemctl is-active errors")
		}
	})
}

func TestArmNetworkRollback_ValidationErrors(t *testing.T) {
	if _, err := armNetworkRollback(context.Background(), newDiscardLogger(), "", 10*time.Second, ""); err == nil {
		t.Fatalf("expected error for empty backup path")
	}
	if _, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 0, ""); err == nil {
		t.Fatalf("expected error for invalid timeout")
	}
}

func TestArmNetworkRollback_CreateRollbackDirFailureReturnsError(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeFS.MkdirAllErr = errors.New("boom")
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	if _, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 30*time.Second, "/secure"); err == nil || !strings.Contains(err.Error(), "create rollback directory") {
		t.Fatalf("err=%v want create rollback directory error", err)
	}
}

func TestArmNetworkRollback_MarkerWriteFailureReturnsError(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeFS.WriteErr = errors.New("boom")
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	if _, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 30*time.Second, "/secure"); err == nil || !strings.Contains(err.Error(), "write rollback marker") {
		t.Fatalf("err=%v want write rollback marker error", err)
	}
}

func TestArmNetworkRollback_ScriptWriteFailureReturnsError(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	scriptPath := "/secure/network_rollback_20260201_123456.sh"
	restoreFS = writeFailFS{FS: fakeFS, failPath: scriptPath, err: errors.New("boom")}

	if _, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 30*time.Second, "/secure"); err == nil || !strings.Contains(err.Error(), "write rollback script") {
		t.Fatalf("err=%v want write rollback script error", err)
	}
}

func TestArmNetworkRollback_UsesSystemdRunWhenAvailable(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	pathDir := t.TempDir()
	writeExecutable(t, pathDir, "systemd-run")
	t.Setenv("PATH", pathDir)

	fakeCmd := &FakeCommandRunner{
		Outputs: map[string][]byte{},
	}
	restoreCmd = fakeCmd

	logger := newDiscardLogger()
	handle, err := armNetworkRollback(context.Background(), logger, "/backup.tar", 30*time.Second, "")
	if err != nil {
		t.Fatalf("armNetworkRollback error: %v", err)
	}
	if handle == nil || handle.unitName == "" {
		t.Fatalf("expected handle with unitName set")
	}
	if !strings.Contains(handle.unitName, "20260201_123456") {
		t.Fatalf("unitName=%q want timestamp", handle.unitName)
	}

	data, err := fakeFS.ReadFile(handle.scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if !strings.Contains(string(data), "Restart networking after rollback") {
		t.Fatalf("expected restartNetworking block in script")
	}

	foundSystemdRun := false
	for _, call := range fakeCmd.CallsList() {
		if strings.HasPrefix(call, "systemd-run --unit=") {
			foundSystemdRun = true
		}
		if strings.HasPrefix(call, "sh -c ") {
			t.Fatalf("unexpected fallback sh -c call: %s", call)
		}
	}
	if !foundSystemdRun {
		t.Fatalf("expected systemd-run to be called; calls=%v", fakeCmd.CallsList())
	}
}

func TestArmNetworkRollback_CustomWorkDirAndSystemdRunOutput(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	pathDir := t.TempDir()
	writeExecutable(t, pathDir, "systemd-run")
	t.Setenv("PATH", pathDir)

	expectedSystemdRun := "systemd-run --unit=proxsave-network-rollback-20260201_123456 --on-active=2s /bin/sh /secure/network_rollback_20260201_123456.sh"
	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			expectedSystemdRun: []byte("unit started\n"),
		},
	}

	handle, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 2*time.Second, "/secure")
	if err != nil {
		t.Fatalf("armNetworkRollback error: %v", err)
	}
	if handle == nil || handle.workDir != "/secure" {
		t.Fatalf("handle=%#v want workDir=/secure", handle)
	}
}

func TestArmNetworkRollback_SystemdRunFailureFallsBackToNohup(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	pathDir := t.TempDir()
	writeExecutable(t, pathDir, "systemd-run")
	t.Setenv("PATH", pathDir)

	fakeCmd := &FakeCommandRunner{
		Errors: map[string]error{},
	}
	restoreCmd = fakeCmd

	logger := newDiscardLogger()
	expectedSystemdRun := "systemd-run --unit=proxsave-network-rollback-20260201_123456 --on-active=30s /bin/sh /tmp/proxsave/network_rollback_20260201_123456.sh"
	fakeCmd.Errors[expectedSystemdRun] = errors.New("boom")

	handle, err := armNetworkRollback(context.Background(), logger, "/backup.tar", 30*time.Second, "")
	if err != nil {
		t.Fatalf("armNetworkRollback error: %v", err)
	}
	if handle == nil {
		t.Fatalf("expected handle")
	}
	if handle.unitName != "" {
		t.Fatalf("unitName=%q want empty after systemd-run failure", handle.unitName)
	}

	foundSystemdRun := false
	foundFallback := false
	for _, call := range fakeCmd.CallsList() {
		if strings.HasPrefix(call, "systemd-run ") {
			foundSystemdRun = true
		}
		if strings.HasPrefix(call, "sh -c nohup sh -c 'sleep ") {
			foundFallback = true
		}
	}
	if !foundSystemdRun || !foundFallback {
		t.Fatalf("expected both systemd-run and fallback; calls=%v", fakeCmd.CallsList())
	}
}

func TestArmNetworkRollback_WithoutSystemdRunUsesNohup(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	logger := newDiscardLogger()
	handle, err := armNetworkRollback(context.Background(), logger, "/backup.tar", 1*time.Second, "")
	if err != nil {
		t.Fatalf("armNetworkRollback error: %v", err)
	}
	if handle == nil {
		t.Fatalf("expected handle")
	}
	if handle.unitName != "" {
		t.Fatalf("unitName=%q want empty in nohup mode", handle.unitName)
	}

	foundFallback := false
	for _, call := range fakeCmd.CallsList() {
		if strings.HasPrefix(call, "sh -c nohup sh -c 'sleep ") {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatalf("expected fallback sh -c call; calls=%v", fakeCmd.CallsList())
	}
}

func TestArmNetworkRollback_SubSecondTimeoutArmsAtLeastOneSecond(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	handle, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 500*time.Millisecond, "")
	if err != nil {
		t.Fatalf("armNetworkRollback error: %v", err)
	}
	if handle == nil {
		t.Fatalf("expected handle")
	}

	foundSleep1 := false
	for _, call := range fakeCmd.CallsList() {
		if strings.Contains(call, "sleep 1;") {
			foundSleep1 = true
		}
	}
	if !foundSleep1 {
		t.Fatalf("expected sleep 1 in nohup command; calls=%v", fakeCmd.CallsList())
	}
}

func TestArmNetworkRollback_FallbackCommandFailureReturnsError(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	restoreCmd = &FakeCommandRunner{
		Errors: map[string]error{
			"sh -c nohup sh -c 'sleep 1; /bin/sh /tmp/proxsave/network_rollback_20260201_123456.sh' >/dev/null 2>&1 &": errors.New("boom"),
		},
	}

	_, err := armNetworkRollback(context.Background(), newDiscardLogger(), "/backup.tar", 1*time.Second, "")
	if err == nil || !strings.Contains(err.Error(), "failed to arm rollback timer") {
		t.Fatalf("err=%v want failed to arm rollback timer", err)
	}
}

func TestDisarmNetworkRollback_RemovesMarkerAndStopsTimer(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	pathDir := t.TempDir()
	writeExecutable(t, pathDir, "systemctl")
	t.Setenv("PATH", pathDir)

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	handle := &networkRollbackHandle{
		markerPath: "/tmp/marker",
		unitName:   "proxsave-network-rollback-test",
	}
	if err := fakeFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	disarmNetworkRollback(context.Background(), newDiscardLogger(), handle)
	if _, err := fakeFS.Stat(handle.markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected marker to be removed, stat err=%v", err)
	}

	calls := strings.Join(fakeCmd.CallsList(), "\n")
	if !strings.Contains(calls, "systemctl stop "+handle.unitName+".timer") {
		t.Fatalf("expected systemctl stop call; calls=%v", fakeCmd.CallsList())
	}
	if !strings.Contains(calls, "systemctl reset-failed "+handle.unitName+".service "+handle.unitName+".timer") {
		t.Fatalf("expected systemctl reset-failed call; calls=%v", fakeCmd.CallsList())
	}
}

func TestDisarmNetworkRollback_NilHandleNoop(t *testing.T) {
	disarmNetworkRollback(context.Background(), newDiscardLogger(), nil)
}

func TestDisarmNetworkRollback_MarkerRemoveFailureAndStopError(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	pathDir := t.TempDir()
	writeExecutable(t, pathDir, "systemctl")
	t.Setenv("PATH", pathDir)

	fakeCmd := &FakeCommandRunner{
		Errors: map[string]error{
			"systemctl stop unit.timer": errors.New("boom"),
		},
	}
	restoreCmd = fakeCmd

	handle := &networkRollbackHandle{
		markerPath: "/tmp/marker",
		unitName:   "unit",
	}
	if err := fakeFS.WriteFile(handle.markerPath, []byte("pending\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	restoreFS = removeFailFS{FS: fakeFS, failPath: handle.markerPath, err: errors.New("remove boom")}
	disarmNetworkRollback(context.Background(), newDiscardLogger(), handle)

	calls := strings.Join(fakeCmd.CallsList(), "\n")
	if !strings.Contains(calls, "systemctl stop unit.timer") {
		t.Fatalf("expected systemctl stop call; calls=%v", fakeCmd.CallsList())
	}
	if !strings.Contains(calls, "systemctl reset-failed unit.service unit.timer") {
		t.Fatalf("expected systemctl reset-failed call; calls=%v", fakeCmd.CallsList())
	}
}

func TestMaybeRepairNICNamesCLI_SkippedWhenArchiveMissing(t *testing.T) {
	origTime := restoreTime
	t.Cleanup(func() { restoreTime = origTime })
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	reader := bufio.NewReader(strings.NewReader(""))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "")
	if result == nil {
		t.Fatalf("expected result")
	}
	if !strings.Contains(result.SkippedReason, "backup archive not available") {
		t.Fatalf("SkippedReason=%q", result.SkippedReason)
	}
	if !result.AppliedAt.Equal(nowRestore()) {
		t.Fatalf("AppliedAt=%s want %s", result.AppliedAt, nowRestore())
	}
}

func TestMaybeRepairNICNamesCLI_ReturnsNilOnPlanError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.WriteFile("/backup.zip", []byte("not a tar"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader(""))
	if got := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.zip"); got != nil {
		t.Fatalf("expected nil on plan error, got %#v", got)
	}
}

func TestMaybeRepairNICNamesCLI_AppliesMappingWithoutConflicts(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader(""))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil || !result.Applied() {
		t.Fatalf("expected applied result, got %#v", result)
	}

	updated, err := fakeFS.ReadFile("/etc/network/interfaces")
	if err != nil {
		t.Fatalf("read updated interfaces: %v", err)
	}
	if string(updated) != "auto enp3s0\niface enp3s0 inet manual\n" {
		t.Fatalf("updated=%q", string(updated))
	}
}

func TestMaybeRepairNICNamesCLI_SkipsWhenNamingOverridesDetectedAndUserConfirms(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.MkdirAll("/etc/udev/rules.d", 0o755); err != nil {
		t.Fatalf("mkdir udev: %v", err)
	}
	rule := `SUBSYSTEM=="net", ATTR{address}=="aa:bb:cc:dd:ee:ff", NAME="eth0"`
	if err := fakeFS.WriteFile("/etc/udev/rules.d/70-persistent-net.rules", []byte(rule+"\n"), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("y\n"))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil {
		t.Fatalf("expected result")
	}
	if !strings.Contains(result.SkippedReason, "persistent NIC naming rules") {
		t.Fatalf("SkippedReason=%q", result.SkippedReason)
	}
}

func TestMaybeRepairNICNamesCLI_OverridesDetectedUserChoosesProceed(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.MkdirAll("/etc/udev/rules.d", 0o755); err != nil {
		t.Fatalf("mkdir udev: %v", err)
	}
	rule := `SUBSYSTEM=="net", ATTR{address}=="aa:bb:cc:dd:ee:ff", NAME="eth0"`
	if err := fakeFS.WriteFile("/etc/udev/rules.d/70-persistent-net.rules", []byte(rule+"\n"), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("n\n"))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil || !result.Applied() {
		t.Fatalf("expected applied result, got %#v", result)
	}
}

func TestMaybeRepairNICNamesCLI_OverridesDetectionErrorStillApplies(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	restoreFS = readDirFailFS{FS: fakeFS, failPath: "/etc/udev/rules.d", err: errors.New("boom")}

	reader := bufio.NewReader(strings.NewReader(""))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil || !result.Applied() {
		t.Fatalf("expected applied result, got %#v", result)
	}
}

func TestMaybeRepairNICNamesCLI_ConflictsPromptAndSkip(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sysDir, "eno1"), 0o755); err != nil {
		t.Fatalf("mkdir eno1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write enp3s0 address: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "eno1", "address"), []byte("11:22:33:44:55:66\n"), 0o644); err != nil {
		t.Fatalf("write eno1 address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	reader := bufio.NewReader(strings.NewReader("n\n"))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil {
		t.Fatalf("expected result")
	}
	if result.Applied() {
		t.Fatalf("expected no changes when conflicts skipped, got %#v", result)
	}
	if !strings.Contains(result.SkippedReason, "conflicting NIC mappings") {
		t.Fatalf("SkippedReason=%q", result.SkippedReason)
	}
}

func TestMaybeRepairNICNamesCLI_ConflictsPromptAndApply(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sysDir, "eno1"), 0o755); err != nil {
		t.Fatalf("mkdir eno1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write enp3s0 address: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "eno1", "address"), []byte("11:22:33:44:55:66\n"), 0o644); err != nil {
		t.Fatalf("write eno1 address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("y\n"))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil || !result.Applied() {
		t.Fatalf("expected applied result, got %#v", result)
	}
}

func TestMaybeRepairNICNamesCLI_OverridesPromptErrorContinues(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.MkdirAll("/etc/udev/rules.d", 0o755); err != nil {
		t.Fatalf("mkdir udev: %v", err)
	}
	rule := `SUBSYSTEM=="net", ATTR{address}=="aa:bb:cc:dd:ee:ff", NAME="eth0"`
	if err := fakeFS.WriteFile("/etc/udev/rules.d/70-persistent-net.rules", []byte(rule+"\n"), 0o644); err != nil {
		t.Fatalf("write rule: %v", err)
	}

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader(""))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil || !result.Applied() {
		t.Fatalf("expected applied result despite prompt error, got %#v", result)
	}
}

func TestMaybeRepairNICNamesCLI_ConflictPromptErrorLeavesConflictsExcluded(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sysDir, "eno1"), 0o755); err != nil {
		t.Fatalf("mkdir eno1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write enp3s0 address: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "eno1", "address"), []byte("11:22:33:44:55:66\n"), 0o644); err != nil {
		t.Fatalf("write eno1 address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	reader := bufio.NewReader(strings.NewReader(""))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil {
		t.Fatalf("expected result")
	}
	if result.Applied() {
		t.Fatalf("expected conflicts excluded due to prompt error, got %#v", result)
	}
	if !strings.Contains(result.SkippedReason, "conflicting NIC mappings") {
		t.Fatalf("SkippedReason=%q", result.SkippedReason)
	}
}

func TestMaybeRepairNICNamesCLI_MoreThan32ConflictsTriggersTruncationBranch(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir

	var ifaceJSON []string
	for i := 0; i < 33; i++ {
		oldName := fmt.Sprintf("eno%d", i)
		newName := fmt.Sprintf("enp%d", i)
		mac := fmt.Sprintf("aa:bb:cc:dd:ee:%02x", i)
		ifaceJSON = append(ifaceJSON, fmt.Sprintf(`{"name":"%s","mac":"%s"}`, oldName, mac))

		if err := os.MkdirAll(filepath.Join(sysDir, newName), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", newName, err)
		}
		if err := os.WriteFile(filepath.Join(sysDir, newName, "address"), []byte(mac+"\n"), 0o644); err != nil {
			t.Fatalf("write %s address: %v", newName, err)
		}

		if err := os.MkdirAll(filepath.Join(sysDir, oldName), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", oldName, err)
		}
		conflictMAC := fmt.Sprintf("11:22:33:44:55:%02x", i)
		if err := os.WriteFile(filepath.Join(sysDir, oldName, "address"), []byte(conflictMAC+"\n"), 0o644); err != nil {
			t.Fatalf("write %s address: %v", oldName, err)
		}
	}

	invJSON := []byte(`{"interfaces":[` + strings.Join(ifaceJSON, ",") + `]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	reader := bufio.NewReader(strings.NewReader("n\n"))
	result := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar")
	if result == nil {
		t.Fatalf("expected result")
	}
	if result.Applied() {
		t.Fatalf("expected no changes when conflicts skipped, got %#v", result)
	}
}

func TestMaybeRepairNICNamesCLI_ReturnsNilOnApplyError(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSysNet := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSysNet
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 2, 3, 4, 5, 0, time.UTC)}

	pathDir := t.TempDir()
	t.Setenv("PATH", pathDir)

	sysDir := t.TempDir()
	sysClassNetPath = sysDir
	if err := os.MkdirAll(filepath.Join(sysDir, "enp3s0"), 0o755); err != nil {
		t.Fatalf("mkdir enp3s0: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "enp3s0", "address"), []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatalf("write address: %v", err)
	}

	invJSON := []byte(`{"interfaces":[{"name":"eno1","mac":"aa:bb:cc:dd:ee:ff"}]}`)
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{
			Name:     "./var/lib/proxsave-info/commands/system/network_inventory.json",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Data:     invJSON,
		},
	})

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	restoreFS = mkdirAllFailFS{FS: fakeFS, failPath: "/tmp/proxsave", err: errors.New("boom")}

	reader := bufio.NewReader(strings.NewReader(""))
	if got := maybeRepairNICNamesCLI(context.Background(), reader, newDiscardLogger(), "/backup.tar"); got != nil {
		t.Fatalf("expected nil on apply error, got %#v", got)
	}
}

func TestApplyNetworkConfig_SelectsAvailableCommand(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	logger := newDiscardLogger()

	t.Run("ifreload", func(t *testing.T) {
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "ifreload")
		writeExecutable(t, pathDir, "systemctl")
		writeExecutable(t, pathDir, "ifup")
		t.Setenv("PATH", pathDir)

		fakeCmd := &FakeCommandRunner{}
		restoreCmd = fakeCmd

		if err := applyNetworkConfig(context.Background(), logger); err != nil {
			t.Fatalf("applyNetworkConfig error: %v", err)
		}
		if calls := fakeCmd.CallsList(); len(calls) != 1 || calls[0] != "ifreload -a" {
			t.Fatalf("calls=%v want [ifreload -a]", calls)
		}
	})

	t.Run("systemctl", func(t *testing.T) {
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "systemctl")
		t.Setenv("PATH", pathDir)

		fakeCmd := &FakeCommandRunner{}
		restoreCmd = fakeCmd

		if err := applyNetworkConfig(context.Background(), logger); err != nil {
			t.Fatalf("applyNetworkConfig error: %v", err)
		}
		if calls := fakeCmd.CallsList(); len(calls) != 1 || calls[0] != "systemctl restart networking" {
			t.Fatalf("calls=%v want [systemctl restart networking]", calls)
		}
	})

	t.Run("ifup", func(t *testing.T) {
		pathDir := t.TempDir()
		writeExecutable(t, pathDir, "ifup")
		t.Setenv("PATH", pathDir)

		fakeCmd := &FakeCommandRunner{}
		restoreCmd = fakeCmd

		if err := applyNetworkConfig(context.Background(), logger); err != nil {
			t.Fatalf("applyNetworkConfig error: %v", err)
		}
		if calls := fakeCmd.CallsList(); len(calls) != 1 || calls[0] != "ifup -a" {
			t.Fatalf("calls=%v want [ifup -a]", calls)
		}
	})

	t.Run("none", func(t *testing.T) {
		pathDir := t.TempDir()
		t.Setenv("PATH", pathDir)

		restoreCmd = &FakeCommandRunner{}
		if err := applyNetworkConfig(context.Background(), logger); err == nil || !strings.Contains(err.Error(), "no supported network reload command") {
			t.Fatalf("err=%v want supported network reload command error", err)
		}
	})
}

func TestParseSSHClientIP(t *testing.T) {
	t.Run("SSH_CONNECTION", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "203.0.113.9 1234 10.0.0.1 22")
		t.Setenv("SSH_CLIENT", "")
		if got := parseSSHClientIP(); got != "203.0.113.9" {
			t.Fatalf("got %q want %q", got, "203.0.113.9")
		}
	})

	t.Run("SSH_CLIENT", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "")
		t.Setenv("SSH_CLIENT", "203.0.113.8 2222 22")
		if got := parseSSHClientIP(); got != "203.0.113.8" {
			t.Fatalf("got %q want %q", got, "203.0.113.8")
		}
	})

	t.Run("none", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "")
		t.Setenv("SSH_CLIENT", "")
		if got := parseSSHClientIP(); got != "" {
			t.Fatalf("got %q want empty", got)
		}
	})
}

func TestDetectManagementInterface(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	logger := newDiscardLogger()

	t.Run("ssh route", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "203.0.113.9 1234 10.0.0.1 22")
		t.Setenv("SSH_CLIENT", "")
		restoreCmd = &FakeCommandRunner{
			Outputs: map[string][]byte{
				"ip route get 203.0.113.9": []byte("203.0.113.9 dev vmbr0 src 192.0.2.2\n"),
			},
		}
		iface, src := detectManagementInterface(context.Background(), logger)
		if iface != "vmbr0" || src != "ssh" {
			t.Fatalf("iface=%q src=%q want vmbr0 ssh", iface, src)
		}
	})

	t.Run("default route fallback", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "203.0.113.9 1234 10.0.0.1 22")
		t.Setenv("SSH_CLIENT", "")
		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{
				"ip route get 203.0.113.9": errors.New("boom"),
			},
			Outputs: map[string][]byte{
				"ip route show default": []byte("default via 192.0.2.1 dev nic1\n"),
			},
		}
		iface, src := detectManagementInterface(context.Background(), logger)
		if iface != "nic1" || src != "default-route" {
			t.Fatalf("iface=%q src=%q want nic1 default-route", iface, src)
		}
	})

	t.Run("none", func(t *testing.T) {
		t.Setenv("SSH_CONNECTION", "")
		t.Setenv("SSH_CLIENT", "")
		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{
				"ip route show default": errors.New("boom"),
			},
		}
		iface, src := detectManagementInterface(context.Background(), logger)
		if iface != "" || src != "" {
			t.Fatalf("iface=%q src=%q want empty", iface, src)
		}
	})
}

func TestRouteInterfaceForIPAndDefaultRouteInterface_ErrorCases(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	restoreCmd = &FakeCommandRunner{
		Errors: map[string]error{
			"ip route get 203.0.113.9":  errors.New("boom"),
			"ip route show default":     errors.New("boom"),
			"ip route show default -x":  errors.New("boom"),
			"ip route show default --y": errors.New("boom"),
		},
	}
	if got := routeInterfaceForIP(context.Background(), "203.0.113.9"); got != "" {
		t.Fatalf("routeInterfaceForIP=%q want empty on error", got)
	}
	if got := defaultRouteInterface(context.Background()); got != "" {
		t.Fatalf("defaultRouteInterface=%q want empty on error", got)
	}
}

func TestDefaultRouteInterface_EmptyOutputReturnsEmpty(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ip route show default": []byte(""),
		},
	}
	if got := defaultRouteInterface(context.Background()); got != "" {
		t.Fatalf("defaultRouteInterface=%q want empty", got)
	}
}

func TestDefaultNetworkPortChecks(t *testing.T) {
	if got := defaultNetworkPortChecks(SystemTypePVE); len(got) != 1 || got[0].Port != 8006 {
		t.Fatalf("PVE checks=%v", got)
	}
	if got := defaultNetworkPortChecks(SystemTypePBS); len(got) != 1 || got[0].Port != 8007 {
		t.Fatalf("PBS checks=%v", got)
	}
	if got := defaultNetworkPortChecks(SystemTypeUnknown); got != nil {
		t.Fatalf("unknown checks=%v want nil", got)
	}
}

func TestPromptNetworkCommitWithCountdown_InputAborted(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	logger := newDiscardLogger()

	committed, err := promptNetworkCommitWithCountdown(context.Background(), reader, logger, 2*time.Second)
	if committed {
		t.Fatalf("expected committed=false")
	}
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestRollbackNetworkFilesNow_ErrorCasesAndScriptFailure(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
	})

	logger := newDiscardLogger()

	if _, err := rollbackNetworkFilesNow(context.Background(), logger, "", ""); err == nil {
		t.Fatalf("expected error for empty backup path")
	}

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	t.Run("mkdir error", func(t *testing.T) {
		restoreFS = &FakeFS{Root: fakeFS.Root, MkdirAllErr: errors.New("boom"), StatErr: make(map[string]error), StatErrors: make(map[string]error), OpenFileErr: make(map[string]error)}
		restoreCmd = &FakeCommandRunner{}
		if _, err := rollbackNetworkFilesNow(context.Background(), logger, "/backup.tar", "/work"); err == nil {
			t.Fatalf("expected error for mkdir failure")
		}
	})

	t.Run("marker write error", func(t *testing.T) {
		failFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(failFS.Root) })
		failFS.WriteErr = errors.New("boom")
		restoreFS = failFS
		restoreCmd = &FakeCommandRunner{}

		if _, err := rollbackNetworkFilesNow(context.Background(), logger, "/backup.tar", "/work"); err == nil || !strings.Contains(err.Error(), "write rollback marker") {
			t.Fatalf("err=%v want write rollback marker error", err)
		}
	})

	t.Run("script write error removes marker", func(t *testing.T) {
		restoreFS = fakeFS
		restoreCmd = &FakeCommandRunner{}

		scriptPath := "/work/network_rollback_now_20260201_123456.sh"
		restoreFS = writeFailFS{FS: fakeFS, failPath: scriptPath, err: errors.New("boom")}

		_, err := rollbackNetworkFilesNow(context.Background(), logger, "/backup.tar", "/work")
		if err == nil || !strings.Contains(err.Error(), "write rollback script") {
			t.Fatalf("err=%v want write rollback script error", err)
		}

		markerPath := "/work/network_rollback_now_pending_20260201_123456"
		if _, statErr := fakeFS.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("expected marker to be removed on script write failure, stat err=%v", statErr)
		}
	})

	t.Run("marker remove error is non-fatal", func(t *testing.T) {
		restoreCmd = &FakeCommandRunner{
			Outputs: map[string][]byte{
				"sh /work/network_rollback_now_20260201_123456.sh": []byte("ok\n"),
			},
		}

		markerPath := "/work/network_rollback_now_pending_20260201_123456"
		restoreFS = removeFailFS{FS: fakeFS, failPath: markerPath, err: errors.New("remove boom")}

		logPath, err := rollbackNetworkFilesNow(context.Background(), logger, "/backup.tar", "/work")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if logPath != "/work/network_rollback_now_20260201_123456.log" {
			t.Fatalf("logPath=%q", logPath)
		}
	})

	t.Run("script run error returns log path", func(t *testing.T) {
		restoreFS = fakeFS

		restoreCmd = &FakeCommandRunner{
			Errors: map[string]error{
				"sh /work/network_rollback_now_20260201_123456.sh": errors.New("boom"),
			},
		}

		logPath, err := rollbackNetworkFilesNow(context.Background(), logger, "/backup.tar", "/work")
		if err == nil || !strings.Contains(err.Error(), "rollback script failed") {
			t.Fatalf("err=%v want rollback script failed", err)
		}
		if logPath != "/work/network_rollback_now_20260201_123456.log" {
			t.Fatalf("logPath=%q", logPath)
		}
	})
}

func TestRollbackNetworkFilesNow_DefaultWorkDirUsesTmpProxsave(t *testing.T) {
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
	restoreTime = &FakeTime{Current: time.Date(2026, 2, 1, 12, 34, 56, 0, time.UTC)}

	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			"sh /tmp/proxsave/network_rollback_now_20260201_123456.sh": []byte("ok\n"),
		},
	}

	logPath, err := rollbackNetworkFilesNow(context.Background(), newDiscardLogger(), "/backup.tar", "")
	if err != nil {
		t.Fatalf("rollbackNetworkFilesNow error: %v", err)
	}
	if logPath != "/tmp/proxsave/network_rollback_now_20260201_123456.log" {
		t.Fatalf("logPath=%q", logPath)
	}
}

func TestBuildRollbackScript_IncludesRestartBlockWhenEnabled(t *testing.T) {
	script := buildRollbackScript("/marker", "/backup with spaces.tar", "/tmp/log file.log", true)
	if !strings.Contains(script, "Restart networking after rollback") {
		t.Fatalf("expected restartNetworking block")
	}
	if !strings.Contains(script, "LOG='/tmp/log file.log'") {
		t.Fatalf("expected quoted LOG, got:\n%s", script)
	}
	if !strings.Contains(script, "BACKUP='/backup with spaces.tar'") {
		t.Fatalf("expected quoted BACKUP, got:\n%s", script)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote(""); got != "''" {
		t.Fatalf("shellQuote empty=%q", got)
	}
	if got := shellQuote("simple"); got != "simple" {
		t.Fatalf("shellQuote simple=%q", got)
	}
	want := `'a b '\''c'\'''`
	if got := shellQuote("a b 'c'"); got != want {
		t.Fatalf("shellQuote=%q want %q", got, want)
	}
}

func TestRunCommandLogged_SuccessAndFailure(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	logger := newDiscardLogger()

	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			"echo hi": []byte("hi\n"),
		},
	}
	if err := runCommandLogged(context.Background(), logger, "echo", "hi"); err != nil {
		t.Fatalf("runCommandLogged error: %v", err)
	}

	restoreCmd = &FakeCommandRunner{
		Errors: map[string]error{
			"false x": errors.New("boom"),
		},
	}
	err := runCommandLogged(context.Background(), logger, "false", "x")
	if err == nil || !strings.Contains(err.Error(), "false") || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err=%v want wrapped failure", err)
	}
}
