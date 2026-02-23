package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

type readDirFailFS struct {
	FS
	failPath string
	err      error
}

func (f readDirFailFS) ReadDir(path string) ([]os.DirEntry, error) {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return nil, f.err
	}
	return f.FS.ReadDir(path)
}

type readDirOverrideFS struct {
	FS
	overridePath string
	entries      []os.DirEntry
	err          error
}

func (f readDirOverrideFS) ReadDir(path string) ([]os.DirEntry, error) {
	if filepath.Clean(path) == filepath.Clean(f.overridePath) {
		if f.err != nil {
			return nil, f.err
		}
		return f.entries, nil
	}
	return f.FS.ReadDir(path)
}

type statFailFS struct {
	FS
	failPath string
	err      error
}

func (f statFailFS) Stat(path string) (os.FileInfo, error) {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return nil, f.err
	}
	return f.FS.Stat(path)
}

type readFileFailFS struct {
	FS
	failPath string
	err      error
}

func (f readFileFailFS) ReadFile(path string) ([]byte, error) {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return nil, f.err
	}
	return f.FS.ReadFile(path)
}

type writeFileFailFS struct {
	FS
	failPath string
	err      error
}

func (f writeFileFailFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return f.err
	}
	return f.FS.WriteFile(path, data, perm)
}

type removeFailFS struct {
	FS
	failPath string
	err      error
}

func (f removeFailFS) Remove(path string) error {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return f.err
	}
	return f.FS.Remove(path)
}

type readlinkFailFS struct {
	FS
	failPath string
	err      error
}

func (f readlinkFailFS) Readlink(path string) (string, error) {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return "", f.err
	}
	return f.FS.Readlink(path)
}

type badInfoDirEntry struct {
	name string
}

func (e badInfoDirEntry) Name() string               { return e.name }
func (e badInfoDirEntry) IsDir() bool                { return false }
func (e badInfoDirEntry) Type() fs.FileMode          { return 0 }
func (e badInfoDirEntry) Info() (fs.FileInfo, error) { return nil, fmt.Errorf("boom") }

type symlinkFailFS struct {
	FS
	failNewname string
	err         error
}

func (f symlinkFailFS) Symlink(oldname, newname string) error {
	if filepath.Clean(newname) == filepath.Clean(f.failNewname) {
		return f.err
	}
	return f.FS.Symlink(oldname, newname)
}

type statFailOnNthFS struct {
	FS
	path   string
	failOn int
	calls  int
	err    error
}

func (f *statFailOnNthFS) Stat(path string) (os.FileInfo, error) {
	if filepath.Clean(path) == filepath.Clean(f.path) {
		f.calls++
		if f.calls >= f.failOn {
			return nil, f.err
		}
	}
	return f.FS.Stat(path)
}

type multiReadDirFS struct {
	FS
	entries map[string][]os.DirEntry
	errors  map[string]error
}

func (f multiReadDirFS) ReadDir(path string) ([]os.DirEntry, error) {
	clean := filepath.Clean(path)
	if f.errors != nil {
		if err, ok := f.errors[clean]; ok {
			return nil, err
		}
	}
	if f.entries != nil {
		if entries, ok := f.entries[clean]; ok {
			return entries, nil
		}
	}
	return f.FS.ReadDir(path)
}

type staticFileInfo struct {
	name string
	mode fs.FileMode
}

func (i staticFileInfo) Name() string       { return i.name }
func (i staticFileInfo) Size() int64        { return 0 }
func (i staticFileInfo) Mode() fs.FileMode  { return i.mode }
func (i staticFileInfo) ModTime() time.Time { return time.Time{} }
func (i staticFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i staticFileInfo) Sys() any           { return nil }

type staticDirEntry struct {
	name string
	mode fs.FileMode
}

func (e staticDirEntry) Name() string      { return e.name }
func (e staticDirEntry) IsDir() bool       { return e.mode.IsDir() }
func (e staticDirEntry) Type() fs.FileMode { return e.mode }
func (e staticDirEntry) Info() (fs.FileInfo, error) {
	return staticFileInfo{name: e.name, mode: e.mode}, nil
}

type scriptedConfirmAction struct {
	ok  bool
	err error
}

type scriptedRestoreWorkflowUI struct {
	*fakeRestoreWorkflowUI
	script []scriptedConfirmAction
	calls  int
}

func (s *scriptedRestoreWorkflowUI) ConfirmAction(ctx context.Context, title, message, yesLabel, noLabel string, timeout time.Duration, defaultYes bool) (bool, error) {
	if s.calls >= len(s.script) {
		return false, fmt.Errorf("unexpected ConfirmAction call %d (title=%q)", s.calls+1, strings.TrimSpace(title))
	}
	action := s.script[s.calls]
	s.calls++
	return action.ok, action.err
}

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", name, err)
	}
	return path
}

func TestFirewallApplyNotCommittedError_UnwrapAndMessage(t *testing.T) {
	var e *FirewallApplyNotCommittedError
	if e.Error() != ErrFirewallApplyNotCommitted.Error() {
		t.Fatalf("nil receiver Error()=%q want %q", e.Error(), ErrFirewallApplyNotCommitted.Error())
	}
	if !errors.Is(error(e), ErrFirewallApplyNotCommitted) {
		t.Fatalf("expected errors.Is(..., ErrFirewallApplyNotCommitted) to be true")
	}
	if errors.Unwrap(error(e)) != ErrFirewallApplyNotCommitted {
		t.Fatalf("expected Unwrap to return ErrFirewallApplyNotCommitted")
	}

	e2 := &FirewallApplyNotCommittedError{}
	if e2.Error() != ErrFirewallApplyNotCommitted.Error() {
		t.Fatalf("Error()=%q want %q", e2.Error(), ErrFirewallApplyNotCommitted.Error())
	}
}

func TestFirewallRollbackHandle_Remaining(t *testing.T) {
	var h *firewallRollbackHandle
	if got := h.remaining(time.Now()); got != 0 {
		t.Fatalf("nil handle remaining=%s want 0", got)
	}

	handle := &firewallRollbackHandle{
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

func TestBuildFirewallApplyNotCommittedError_PopulatesFields(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if e := buildFirewallApplyNotCommittedError(nil); e == nil {
		t.Fatalf("expected error struct, got nil")
	} else if e.RollbackArmed || e.RollbackMarker != "" || e.RollbackLog != "" || !e.RollbackDeadline.IsZero() {
		t.Fatalf("unexpected fields for nil handle: %#v", e)
	}

	armedAt := time.Unix(10, 0)
	handle := &firewallRollbackHandle{
		markerPath: "  /tmp/fw.marker \n",
		logPath:    " /tmp/fw.log\t",
		armedAt:    armedAt,
		timeout:    3 * time.Second,
	}
	if err := fakeFS.AddFile("/tmp/fw.marker", []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}

	e := buildFirewallApplyNotCommittedError(handle)
	if e.RollbackMarker != "/tmp/fw.marker" {
		t.Fatalf("RollbackMarker=%q", e.RollbackMarker)
	}
	if e.RollbackLog != "/tmp/fw.log" {
		t.Fatalf("RollbackLog=%q", e.RollbackLog)
	}
	if !e.RollbackArmed {
		t.Fatalf("expected RollbackArmed=true")
	}
	if !e.RollbackDeadline.Equal(armedAt.Add(3 * time.Second)) {
		t.Fatalf("RollbackDeadline=%s want %s", e.RollbackDeadline, armedAt.Add(3*time.Second))
	}
}

func TestBuildFirewallRollbackScript_QuotesPaths(t *testing.T) {
	script := buildFirewallRollbackScript("/tmp/marker path", "/tmp/backup's.tar.gz", "/tmp/log path")
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

func TestCopyFileExact_ReturnsFalseWhenSourceMissingOrDir(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	ok, err := copyFileExact("/missing", "/dest")
	if err != nil || ok {
		t.Fatalf("copyFileExact missing ok=%v err=%v want ok=false err=nil", ok, err)
	}

	if err := fakeFS.AddDir("/srcdir"); err != nil {
		t.Fatalf("add dir: %v", err)
	}
	ok, err = copyFileExact("/srcdir", "/dest")
	if err != nil || ok {
		t.Fatalf("copyFileExact dir ok=%v err=%v want ok=false err=nil", ok, err)
	}
}

func TestCopyFileExact_PropagatesAtomicWriteFailure(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	fakeTime := &FakeTime{Current: time.Unix(0, 12345)}
	restoreTime = fakeTime

	if err := fakeFS.AddFile("/src/file", []byte("data\n")); err != nil {
		t.Fatalf("add src: %v", err)
	}

	dest := "/dest/file"
	tmpPath := dest + ".proxsave.tmp." + strconv.FormatInt(fakeTime.Current.UnixNano(), 10)
	fakeFS.OpenFileErr[filepath.Clean(tmpPath)] = fmt.Errorf("open tmp denied")

	_, err := copyFileExact("/src/file", dest)
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestSyncDirExact_CopiesSymlinks(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.AddFile("/stage/target", []byte("x")); err != nil {
		t.Fatalf("add target: %v", err)
	}
	if err := fakeFS.Symlink("/stage/target", "/stage/link"); err != nil {
		t.Fatalf("add symlink: %v", err)
	}

	applied, err := syncDirExact("/stage", "/dest")
	if err != nil {
		t.Fatalf("syncDirExact error: %v", err)
	}

	destTarget, err := fakeFS.Readlink("/dest/link")
	if err != nil {
		t.Fatalf("read dest symlink: %v", err)
	}
	if strings.TrimSpace(destTarget) == "" {
		t.Fatalf("expected non-empty symlink target")
	}
	found := false
	for _, p := range applied {
		if p == "/dest/link" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected /dest/link to be reported as applied, got %#v", applied)
	}
}

func TestSelectStageHostFirewall_ErrorsOnReadDirFailure(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	base := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(base.Root) })
	restoreFS = readDirFailFS{FS: base, failPath: "/stage/etc/pve/nodes", err: fmt.Errorf("boom")}

	_, _, _, err := selectStageHostFirewall(newTestLogger(), "/stage")
	if err == nil || !strings.Contains(err.Error(), "readdir") {
		t.Fatalf("expected readdir error, got %v", err)
	}
}

func TestSelectStageHostFirewall_PicksCurrentNodeWhenPresent(t *testing.T) {
	origFS := restoreFS
	origHostname := firewallHostname
	t.Cleanup(func() {
		restoreFS = origFS
		firewallHostname = origHostname
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	firewallHostname = func() (string, error) { return "node1.example", nil }

	stageRoot := "/stage"
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/node1/host.fw", []byte("a")); err != nil {
		t.Fatalf("add node1 host.fw: %v", err)
	}
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/other/host.fw", []byte("b")); err != nil {
		t.Fatalf("add other host.fw: %v", err)
	}
	if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/notadir", []byte("c")); err != nil {
		t.Fatalf("add notadir: %v", err)
	}

	path, sourceNode, ok, err := selectStageHostFirewall(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("selectStageHostFirewall error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if sourceNode != "node1" {
		t.Fatalf("sourceNode=%q want %q", sourceNode, "node1")
	}
	if !strings.HasSuffix(path, "/stage/etc/pve/nodes/node1/host.fw") {
		t.Fatalf("unexpected path: %q", path)
	}
}

func TestSelectStageHostFirewall_SkipsWhenMultipleCandidatesNoneMatches(t *testing.T) {
	origFS := restoreFS
	origHostname := firewallHostname
	t.Cleanup(func() {
		restoreFS = origFS
		firewallHostname = origHostname
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	firewallHostname = func() (string, error) { return "current", nil }

	stageRoot := "/stage"
	for _, node := range []string{"nodeA", "nodeB"} {
		if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/"+node+"/host.fw", []byte("x")); err != nil {
			t.Fatalf("add host.fw for %s: %v", node, err)
		}
	}

	path, sourceNode, ok, err := selectStageHostFirewall(newTestLogger(), stageRoot)
	if err != nil {
		t.Fatalf("selectStageHostFirewall error: %v", err)
	}
	if ok || path != "" || sourceNode != "" {
		t.Fatalf("expected skip, got ok=%v path=%q source=%q", ok, path, sourceNode)
	}
}

func TestRestartPVEFirewallService_CommandFallbacks(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	binDir := t.TempDir()
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	writeExecutable(t, binDir, "systemctl")
	writeExecutable(t, binDir, "pve-firewall")

	t.Run("try-restart ok", func(t *testing.T) {
		fake := &FakeCommandRunner{}
		restoreCmd = fake

		if err := restartPVEFirewallService(context.Background()); err != nil {
			t.Fatalf("restartPVEFirewallService error: %v", err)
		}
		if got := fake.CallsList(); len(got) != 1 || got[0] != "systemctl try-restart pve-firewall" {
			t.Fatalf("unexpected calls: %#v", got)
		}
	})

	t.Run("restart ok", func(t *testing.T) {
		fake := &FakeCommandRunner{
			Errors: map[string]error{
				"systemctl try-restart pve-firewall": fmt.Errorf("fail"),
			},
		}
		restoreCmd = fake

		if err := restartPVEFirewallService(context.Background()); err != nil {
			t.Fatalf("restartPVEFirewallService error: %v", err)
		}
		if got := fake.CallsList(); len(got) != 2 || got[0] != "systemctl try-restart pve-firewall" || got[1] != "systemctl restart pve-firewall" {
			t.Fatalf("unexpected calls: %#v", got)
		}
	})

	t.Run("fallback to pve-firewall", func(t *testing.T) {
		fake := &FakeCommandRunner{
			Errors: map[string]error{
				"systemctl try-restart pve-firewall": fmt.Errorf("fail"),
				"systemctl restart pve-firewall":     fmt.Errorf("fail"),
			},
		}
		restoreCmd = fake

		if err := restartPVEFirewallService(context.Background()); err != nil {
			t.Fatalf("restartPVEFirewallService error: %v", err)
		}
		calls := fake.CallsList()
		if len(calls) != 3 {
			t.Fatalf("unexpected calls: %#v", calls)
		}
		if calls[2] != "pve-firewall restart" {
			t.Fatalf("expected fallback call, got %#v", calls)
		}
	})

	t.Run("no commands available", func(t *testing.T) {
		fake := &FakeCommandRunner{}
		restoreCmd = fake

		emptyBin := t.TempDir()
		if err := os.Setenv("PATH", emptyBin); err != nil {
			t.Fatalf("set PATH: %v", err)
		}

		if err := restartPVEFirewallService(context.Background()); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestArmFirewallRollback_SystemdAndBackgroundPaths(t *testing.T) {
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
		if _, err := armFirewallRollback(context.Background(), logger, "", 1*time.Second, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error for empty backupPath")
		}
		if _, err := armFirewallRollback(context.Background(), logger, "/backup.tgz", 0, "/tmp/proxsave"); err == nil {
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

		handle, err := armFirewallRollback(context.Background(), logger, "/backup.tgz", 2*time.Second, "/tmp/proxsave")
		if err != nil {
			t.Fatalf("armFirewallRollback error: %v", err)
		}
		if handle == nil || handle.unitName == "" {
			t.Fatalf("expected systemd unit name, got %#v", handle)
		}
		if got := fakeCmd.CallsList(); len(got) != 1 || !strings.HasPrefix(got[0], "systemd-run --unit=proxsave-firewall-rollback-20200102_030405") {
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
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("firewall_rollback_%s.sh", timestamp))
		systemdKey := "systemd-run --unit=proxsave-firewall-rollback-" + timestamp + " --on-active=2s /bin/sh " + scriptPath
		fakeCmd.Errors[systemdKey] = fmt.Errorf("fail")

		handle, err := armFirewallRollback(context.Background(), logger, "/backup.tgz", 2*time.Second, "/tmp/proxsave")
		if err != nil {
			t.Fatalf("armFirewallRollback error: %v", err)
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
		scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("firewall_rollback_%s.sh", timestamp))
		cmd := fmt.Sprintf("nohup sh -c 'sleep %d; /bin/sh %s' >/dev/null 2>&1 &", 1, scriptPath)
		backgroundKey := "sh -c " + cmd
		fakeCmd.Errors[backgroundKey] = fmt.Errorf("boom")

		if _, err := armFirewallRollback(context.Background(), logger, "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil {
			t.Fatalf("expected error")
		}
	})
}

func TestDisarmFirewallRollback_RemovesMarkerAndStopsTimer(t *testing.T) {
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

	handle := &firewallRollbackHandle{
		markerPath: "/tmp/proxsave/fw.marker",
		unitName:   "proxsave-firewall-rollback-test",
	}
	if err := fakeFS.AddFile(handle.markerPath, []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}

	disarmFirewallRollback(context.Background(), newTestLogger(), handle)

	if _, err := fakeFS.Stat(handle.markerPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected marker removed; stat err=%v", err)
	}

	timerUnit := handle.unitName + ".timer"
	want1 := "systemctl stop " + timerUnit
	want2 := "systemctl reset-failed " + handle.unitName + ".service " + timerUnit
	calls := fakeCmd.CallsList()
	if len(calls) != 2 || calls[0] != want1 || calls[1] != want2 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestMaybeApplyPVEFirewallWithUI_CoversUserFlows(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	origGeteuid := firewallApplyGeteuid
	origMounted := firewallIsMounted
	origRealFS := firewallIsRealRestoreFS
	origArm := firewallArmRollback
	origDisarm := firewallDisarmRollback
	origApply := firewallApplyFromStage
	origRestart := firewallRestartService
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
		firewallApplyGeteuid = origGeteuid
		firewallIsMounted = origMounted
		firewallIsRealRestoreFS = origRealFS
		firewallArmRollback = origArm
		firewallDisarmRollback = origDisarm
		firewallApplyFromStage = origApply
		firewallRestartService = origRestart
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	restoreTime = &FakeTime{Current: time.Unix(100, 0)}
	restoreCmd = &FakeCommandRunner{}

	firewallIsRealRestoreFS = func(fs FS) bool { return true }
	firewallApplyGeteuid = func() int { return 0 }
	firewallIsMounted = func(path string) (bool, error) { return true, nil }
	firewallRestartService = func(ctx context.Context) error { return nil }

	plan := &RestorePlan{
		SystemType:       SystemTypePVE,
		NormalCategories: []Category{{ID: "pve_firewall"}},
	}
	stageRoot := "/stage"
	logger := newTestLogger()

	t.Run("errors when ui missing", func(t *testing.T) {
		err := maybeApplyPVEFirewallWithUI(context.Background(), nil, logger, plan, nil, nil, stageRoot, false)
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("skips when /etc/pve not mounted", func(t *testing.T) {
		firewallIsMounted = func(path string) (bool, error) { return false, nil }
		t.Cleanup(func() { firewallIsMounted = func(path string) (bool, error) { return true, nil } })

		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("skips when stage has no firewall data", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("user skips apply", func(t *testing.T) {
		if err := fakeFS.AddDir(stageRoot + "/etc/pve/nodes"); err != nil {
			t.Fatalf("add stage nodes: %v", err)
		}
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: false}},
		}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("missing rollback backup declines full rollback", func(t *testing.T) {
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
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
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, safety, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("full rollback accepted but no changes applied disarms rollback", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) { return nil, nil }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Proceed with full rollback
			},
		}
		safety := &SafetyBackupResult{BackupPath: "/backups/full.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, safety, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if _, err := fakeFS.Stat(markerPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected rollback marker removed; stat err=%v", err)
		}
	})

	t.Run("proceed without rollback applies and returns without commit prompt", func(t *testing.T) {
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			t.Fatalf("unexpected rollback arm")
			return nil, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Proceed without rollback
			},
		}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("commit keeps changes and disarms rollback", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Commit
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if _, err := fakeFS.Stat(markerPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected rollback marker removed; stat err=%v", err)
		}
	})

	t.Run("rollback requested returns typed error with marker armed", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		armedAt := nowRestore()
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    armedAt,
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},  // Apply now
				{ok: false}, // Rollback
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, stageRoot, false)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, ErrFirewallApplyNotCommitted) {
			t.Fatalf("expected ErrFirewallApplyNotCommitted, got %v", err)
		}
		var typed *FirewallApplyNotCommittedError
		if !errors.As(err, &typed) || typed == nil {
			t.Fatalf("expected FirewallApplyNotCommittedError, got %T", err)
		}
		if !typed.RollbackArmed || typed.RollbackMarker != markerPath || typed.RollbackLog != "/tmp/proxsave/fw.log" {
			t.Fatalf("unexpected error fields: %#v", typed)
		}
		if typed.RollbackDeadline.IsZero() || !typed.RollbackDeadline.Equal(armedAt.Add(defaultFirewallRollbackTimeout)) {
			t.Fatalf("unexpected RollbackDeadline=%s", typed.RollbackDeadline)
		}
		if _, err := fakeFS.Stat(markerPath); err != nil {
			t.Fatalf("expected marker to still exist, stat err=%v", err)
		}
	})

	t.Run("commit prompt abort returns abort error", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},                   // Apply now
				{err: input.ErrInputAborted}, // Abort at commit prompt
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, stageRoot, false)
		if err == nil || !errors.Is(err, input.ErrInputAborted) {
			t.Fatalf("expected abort error, got %v", err)
		}
	})

	t.Run("commit prompt failure returns typed error", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},                // Apply now
				{err: fmt.Errorf("boom")}, // Commit prompt fails
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, stageRoot, false)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !errors.Is(err, ErrFirewallApplyNotCommitted) {
			t.Fatalf("expected ErrFirewallApplyNotCommitted, got %v", err)
		}
		if _, err := fakeFS.Stat(markerPath); err != nil {
			t.Fatalf("expected marker to still exist, stat err=%v", err)
		}
	})

	t.Run("remaining timeout returns typed error without commit prompt", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore().Add(-timeout - time.Second),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: true}}, // Apply now only
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, stageRoot, false)
		if err == nil || !errors.Is(err, ErrFirewallApplyNotCommitted) {
			t.Fatalf("expected ErrFirewallApplyNotCommitted, got %v", err)
		}
	})

	t.Run("dry run skips apply", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, stageRoot, true)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("non-root skips apply", func(t *testing.T) {
		firewallApplyGeteuid = func() int { return 1000 }
		t.Cleanup(func() { firewallApplyGeteuid = func() int { return 0 } })

		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, stageRoot, false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}

func TestMaybeApplyPVEFirewallWithUI_AdditionalBranches(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origCmd := restoreCmd
	origGeteuid := firewallApplyGeteuid
	origMounted := firewallIsMounted
	origRealFS := firewallIsRealRestoreFS
	origArm := firewallArmRollback
	origDisarm := firewallDisarmRollback
	origApply := firewallApplyFromStage
	origRestart := firewallRestartService
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		restoreCmd = origCmd
		firewallApplyGeteuid = origGeteuid
		firewallIsMounted = origMounted
		firewallIsRealRestoreFS = origRealFS
		firewallArmRollback = origArm
		firewallDisarmRollback = origDisarm
		firewallApplyFromStage = origApply
		firewallRestartService = origRestart
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Unix(100, 0)}
	restoreCmd = &FakeCommandRunner{}

	firewallIsRealRestoreFS = func(fs FS) bool { return true }
	firewallApplyGeteuid = func() int { return 0 }
	firewallIsMounted = func(path string) (bool, error) { return true, nil }
	firewallRestartService = func(ctx context.Context) error { return nil }

	logger := newTestLogger()
	plan := &RestorePlan{SystemType: SystemTypePVE, NormalCategories: []Category{{ID: "pve_firewall"}}}

	t.Run("plan nil returns nil", func(t *testing.T) {
		err := maybeApplyPVEFirewallWithUI(context.Background(), nil, logger, nil, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("wrong system type returns nil", func(t *testing.T) {
		p := &RestorePlan{SystemType: SystemTypePBS, NormalCategories: []Category{{ID: "pve_firewall"}}}
		err := maybeApplyPVEFirewallWithUI(context.Background(), nil, logger, p, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("missing category returns nil", func(t *testing.T) {
		p := &RestorePlan{SystemType: SystemTypePVE, NormalCategories: []Category{{ID: "network"}}}
		err := maybeApplyPVEFirewallWithUI(context.Background(), nil, logger, p, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("non-real filesystem skips apply", func(t *testing.T) {
		firewallIsRealRestoreFS = func(fs FS) bool { return false }
		t.Cleanup(func() { firewallIsRealRestoreFS = func(fs FS) bool { return true } })

		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("blank stage root skips apply", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "   ", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("cluster restore skips apply", func(t *testing.T) {
		p := &RestorePlan{SystemType: SystemTypePVE, NeedsClusterRestore: true, NormalCategories: []Category{{ID: "pve_firewall"}}}
		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, p, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("mount check warning path", func(t *testing.T) {
		firewallIsMounted = func(path string) (bool, error) { return true, fmt.Errorf("boom") }
		t.Cleanup(func() { firewallIsMounted = func(path string) (bool, error) { return true, nil } })

		ui := &scriptedRestoreWorkflowUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}, script: nil}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("apply prompt error returns error", func(t *testing.T) {
		if err := fakeFS.AddDir("/stage/etc/pve/nodes"); err != nil {
			t.Fatalf("add stage nodes: %v", err)
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{err: fmt.Errorf("input fail")}},
		}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("no rollback declined returns nil", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},  // Apply now
				{ok: false}, // Skip apply (no rollback)
			},
		}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("no rollback prompt error returns error", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},                // Apply now
				{err: fmt.Errorf("boom")}, // No rollback prompt fails
			},
		}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("full rollback prompt error returns error", func(t *testing.T) {
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},                // Apply now
				{err: fmt.Errorf("boom")}, // Full rollback prompt fails
			},
		}
		safety := &SafetyBackupResult{BackupPath: "/backups/full.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, safety, nil, "/stage", false)
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("arm rollback error returns wrapped error", func(t *testing.T) {
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			return nil, fmt.Errorf("arm failed")
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			t.Fatalf("unexpected apply")
			return nil, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script:                []scriptedConfirmAction{{ok: true}},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, "/stage", false)
		if err == nil || !strings.Contains(err.Error(), "arm firewall rollback") {
			t.Fatalf("expected wrapped arm error, got %v", err)
		}
	})

	t.Run("apply from stage error returns error", func(t *testing.T) {
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return nil, fmt.Errorf("apply failed")
		}
		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Proceed without rollback
			},
		}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
		if err == nil || !strings.Contains(err.Error(), "apply failed") {
			t.Fatalf("expected apply error, got %v", err)
		}
	})

	t.Run("restart failure logs warning but continues", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}
		firewallRestartService = func(ctx context.Context) error { return fmt.Errorf("restart failed") }

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true}, // Apply now
				{ok: true}, // Commit
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, "/stage", false)
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
		if _, err := fakeFS.Stat(markerPath); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected rollback marker removed; stat err=%v", err)
		}
	})

	t.Run("commit context canceled returns canceled error", func(t *testing.T) {
		markerPath := "/tmp/proxsave/fw.marker"
		firewallArmRollback = func(ctx context.Context, logger *logging.Logger, backupPath string, timeout time.Duration, workDir string) (*firewallRollbackHandle, error) {
			handle := &firewallRollbackHandle{
				markerPath: markerPath,
				logPath:    "/tmp/proxsave/fw.log",
				armedAt:    nowRestore(),
				timeout:    timeout,
			}
			_ = restoreFS.WriteFile(markerPath, []byte("pending\n"), 0o640)
			return handle, nil
		}
		firewallApplyFromStage = func(logger *logging.Logger, stageRoot string) ([]string, error) {
			return []string{"/etc/pve/firewall/cluster.fw"}, nil
		}

		ui := &scriptedRestoreWorkflowUI{
			fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
			script: []scriptedConfirmAction{
				{ok: true},              // Apply now
				{err: context.Canceled}, // Commit prompt canceled
			},
		}
		rollback := &SafetyBackupResult{BackupPath: "/backups/firewall.tgz"}
		err := maybeApplyPVEFirewallWithUI(context.Background(), ui, logger, plan, nil, rollback, "/stage", false)
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled error, got %v", err)
		}
	})
}

func TestApplyPVEFirewallFromStage_CoversAdditionalPaths(t *testing.T) {
	origFS := restoreFS
	origHostname := firewallHostname
	t.Cleanup(func() {
		restoreFS = origFS
		firewallHostname = origHostname
	})

	t.Run("blank stage root", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		applied, err := applyPVEFirewallFromStage(newTestLogger(), "   ")
		if err != nil || len(applied) != 0 {
			t.Fatalf("expected nil, got applied=%#v err=%v", applied, err)
		}
	})

	t.Run("staged firewall as file", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		stageRoot := "/stage"
		if err := fakeFS.AddFile(stageRoot+"/etc/pve/firewall", []byte("fw\n")); err != nil {
			t.Fatalf("add staged firewall file: %v", err)
		}

		applied, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
		if err != nil {
			t.Fatalf("applyPVEFirewallFromStage error: %v", err)
		}
		found := false
		for _, p := range applied {
			if p == "/etc/pve/firewall" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected /etc/pve/firewall in applied paths, got %#v", applied)
		}
		if got, err := fakeFS.ReadFile("/etc/pve/firewall"); err != nil || string(got) != "fw\n" {
			t.Fatalf("dest firewall err=%v data=%q", err, string(got))
		}
	})

	t.Run("firewall stat error returns error", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		stageRoot := "/stage"
		stageFirewall := filepath.Join(stageRoot, "etc", "pve", "firewall")
		fakeFS.StatErrors[filepath.Clean(stageFirewall)] = fmt.Errorf("boom")

		_, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
		if err == nil || !strings.Contains(err.Error(), "stat staged firewall config") {
			t.Fatalf("expected stat staged firewall config error, got %v", err)
		}
	})

	t.Run("host fw selection error bubbles", func(t *testing.T) {
		base := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(base.Root) })
		restoreFS = readDirFailFS{FS: base, failPath: "/stage/etc/pve/nodes", err: fmt.Errorf("boom")}

		_, err := applyPVEFirewallFromStage(newTestLogger(), "/stage")
		if err == nil || !strings.Contains(err.Error(), "readdir") {
			t.Fatalf("expected readdir error, got %v", err)
		}
	})

	t.Run("defaults to localhost when hostname empty", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS
		firewallHostname = func() (string, error) { return "   ", nil }

		stageRoot := "/stage"
		if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/node1/host.fw", []byte("host\n")); err != nil {
			t.Fatalf("add staged host.fw: %v", err)
		}

		applied, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
		if err != nil {
			t.Fatalf("applyPVEFirewallFromStage error: %v", err)
		}
		found := false
		for _, p := range applied {
			if p == "/etc/pve/nodes/localhost/host.fw" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected mapped host.fw applied, got %#v", applied)
		}
		if got, err := fakeFS.ReadFile("/etc/pve/nodes/localhost/host.fw"); err != nil || string(got) != "host\n" {
			t.Fatalf("dest host.fw err=%v data=%q", err, string(got))
		}
	})
}

func TestSelectStageHostFirewall_EmptyCases(t *testing.T) {
	origFS := restoreFS
	origHostname := firewallHostname
	t.Cleanup(func() {
		restoreFS = origFS
		firewallHostname = origHostname
	})

	firewallHostname = func() (string, error) { return "node1", nil }

	t.Run("nodes directory missing", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		path, node, ok, err := selectStageHostFirewall(newTestLogger(), "/stage")
		if err != nil || ok || path != "" || node != "" {
			t.Fatalf("expected no selection, got ok=%v path=%q node=%q err=%v", ok, path, node, err)
		}
	})

	t.Run("no host.fw candidates", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddDir("/stage/etc/pve/nodes/node1"); err != nil {
			t.Fatalf("add nodes dir: %v", err)
		}

		path, node, ok, err := selectStageHostFirewall(newTestLogger(), "/stage")
		if err != nil || ok || path != "" || node != "" {
			t.Fatalf("expected no selection, got ok=%v path=%q node=%q err=%v", ok, path, node, err)
		}
	})
}

func TestArmFirewallRollback_DefaultWorkDirAndMinTimeout(t *testing.T) {
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

	handle, err := armFirewallRollback(context.Background(), newTestLogger(), "/backup.tgz", 500*time.Millisecond, "   ")
	if err != nil {
		t.Fatalf("armFirewallRollback error: %v", err)
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

func TestArmFirewallRollback_ReturnsErrorOnMkdirAllFailure(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeFS.MkdirAllErr = fmt.Errorf("disk full")
	restoreFS = fakeFS

	if _, err := armFirewallRollback(context.Background(), newTestLogger(), "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil {
		t.Fatalf("expected error")
	}
}

func TestArmFirewallRollback_ReturnsErrorOnMarkerWriteFailure(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	fakeFS.WriteErr = fmt.Errorf("disk full")
	restoreFS = fakeFS

	if _, err := armFirewallRollback(context.Background(), newTestLogger(), "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "write rollback marker") {
		t.Fatalf("expected write rollback marker error, got %v", err)
	}
}

func TestArmFirewallRollback_ReturnsErrorOnScriptWriteFailure(t *testing.T) {
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
	scriptPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("firewall_rollback_%s.sh", timestamp))

	restoreFS = writeFileFailFS{FS: base, failPath: scriptPath, err: fmt.Errorf("disk full")}

	if _, err := armFirewallRollback(context.Background(), newTestLogger(), "/backup.tgz", 1*time.Second, "/tmp/proxsave"); err == nil || !strings.Contains(err.Error(), "write rollback script") {
		t.Fatalf("expected write rollback script error, got %v", err)
	}
}

func TestDisarmFirewallRollback_MissingMarkerAndNoSystemctl(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	emptyBin := t.TempDir()
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", emptyBin); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })

	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	handle := &firewallRollbackHandle{
		markerPath: "/tmp/proxsave/missing.marker",
		unitName:   "unit",
	}
	disarmFirewallRollback(context.Background(), newTestLogger(), handle)

	if calls := fakeCmd.CallsList(); len(calls) != 0 {
		t.Fatalf("expected no systemctl calls, got %#v", calls)
	}
}

func TestDisarmFirewallRollback_ContinuesOnMarkerRemoveError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	base := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(base.Root) })
	if err := base.AddFile("/tmp/proxsave/fw.marker", []byte("pending\n")); err != nil {
		t.Fatalf("add marker: %v", err)
	}
	restoreFS = removeFailFS{FS: base, failPath: "/tmp/proxsave/fw.marker", err: fmt.Errorf("perm")}

	handle := &firewallRollbackHandle{
		markerPath: "/tmp/proxsave/fw.marker",
	}
	disarmFirewallRollback(context.Background(), newTestLogger(), handle)
}

func TestCopyFileExact_PropagatesStatAndReadFailures(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	base := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(base.Root) })
	if err := base.AddFile("/src/file", []byte("x")); err != nil {
		t.Fatalf("add src: %v", err)
	}

	restoreFS = statFailFS{FS: base, failPath: "/src/file", err: fmt.Errorf("boom")}
	if _, err := copyFileExact("/src/file", "/dest/file"); err == nil {
		t.Fatalf("expected stat error")
	}

	restoreFS = readFileFailFS{FS: base, failPath: "/src/file", err: os.ErrNotExist}
	ok, err := copyFileExact("/src/file", "/dest/file")
	if err != nil || ok {
		t.Fatalf("expected ok=false err=nil for readfile not exist, got ok=%v err=%v", ok, err)
	}

	restoreFS = readFileFailFS{FS: base, failPath: "/src/file", err: fmt.Errorf("read boom")}
	if _, err := copyFileExact("/src/file", "/dest/file"); err == nil {
		t.Fatalf("expected read error")
	}
}

func TestSyncDirExact_CoversErrorPathsAndPrune(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	t.Run("source missing or not a dir returns nil", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if applied, err := syncDirExact("/missing", "/dest"); err != nil || len(applied) != 0 {
			t.Fatalf("expected nil, got applied=%#v err=%v", applied, err)
		}
		if err := fakeFS.AddFile("/stagefile", []byte("x")); err != nil {
			t.Fatalf("add stagefile: %v", err)
		}
		if applied, err := syncDirExact("/stagefile", "/dest"); err != nil || len(applied) != 0 {
			t.Fatalf("expected nil, got applied=%#v err=%v", applied, err)
		}
	})

	t.Run("dest exists as file returns error", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}
		if err := fakeFS.AddFile("/dest", []byte("x")); err != nil {
			t.Fatalf("add dest file: %v", err)
		}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "ensure") {
			t.Fatalf("expected ensure error, got %v", err)
		}
	})

	t.Run("readDir error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}
		restoreFS = readDirFailFS{FS: fakeFS, failPath: "/stage", err: fmt.Errorf("boom")}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "readdir") {
			t.Fatalf("expected readdir error, got %v", err)
		}
	})

	t.Run("entry.Info error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}

		restoreFS = readDirOverrideFS{
			FS:           fakeFS,
			overridePath: "/stage",
			entries:      []os.DirEntry{badInfoDirEntry{name: "bad"}},
		}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "stat /stage/bad") {
			t.Fatalf("expected entry.Info error, got %v", err)
		}
	})

	t.Run("readlink error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddFile("/stage/target", []byte("x")); err != nil {
			t.Fatalf("add target: %v", err)
		}
		if err := fakeFS.Symlink("/stage/target", "/stage/link"); err != nil {
			t.Fatalf("add symlink: %v", err)
		}

		restoreFS = readlinkFailFS{FS: fakeFS, failPath: "/stage/link", err: fmt.Errorf("boom")}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "readlink") {
			t.Fatalf("expected readlink error, got %v", err)
		}
	})

	t.Run("prune remove failure returns error", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

		if err := fakeFS.AddFile("/stage/keep", []byte("x")); err != nil {
			t.Fatalf("add keep: %v", err)
		}
		if err := fakeFS.AddFile("/dest/remove", []byte("x")); err != nil {
			t.Fatalf("add extraneous: %v", err)
		}

		restoreFS = removeFailFS{FS: fakeFS, failPath: "/dest/remove", err: fmt.Errorf("perm")}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "remove") {
			t.Fatalf("expected remove error, got %v", err)
		}
	})

	t.Run("prunes empty dirs not present in stage", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddFile("/stage/keep", []byte("x")); err != nil {
			t.Fatalf("add keep: %v", err)
		}
		if err := fakeFS.AddDir("/dest/oldDir"); err != nil {
			t.Fatalf("add oldDir: %v", err)
		}

		if _, err := syncDirExact("/stage", "/dest"); err != nil {
			t.Fatalf("syncDirExact error: %v", err)
		}
		if _, err := fakeFS.Stat("/dest/oldDir"); err == nil || !os.IsNotExist(err) {
			t.Fatalf("expected oldDir removed; stat err=%v", err)
		}
	})
}

func TestDisarmFirewallRollback_NilHandleAndEmptyPaths(t *testing.T) {
	disarmFirewallRollback(context.Background(), newTestLogger(), nil)

	handle := &firewallRollbackHandle{
		markerPath: "   ",
		unitName:   "   ",
	}
	disarmFirewallRollback(context.Background(), newTestLogger(), handle)
}

func TestSelectStageHostFirewall_IgnoresNilAndBlankEntries(t *testing.T) {
	origFS := restoreFS
	origHostname := firewallHostname
	t.Cleanup(func() {
		restoreFS = origFS
		firewallHostname = origHostname
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	firewallHostname = func() (string, error) { return "node1", nil }

	stageNodes := "/stage/etc/pve/nodes"
	if err := fakeFS.AddDir(stageNodes); err != nil {
		t.Fatalf("add stage nodes: %v", err)
	}
	if err := fakeFS.AddDir(stageNodes + "/ "); err != nil {
		t.Fatalf("add blank node dir: %v", err)
	}
	if err := fakeFS.AddFile(stageNodes+"/node1/host.fw", []byte("host\n")); err != nil {
		t.Fatalf("add host.fw: %v", err)
	}

	entries, err := fakeFS.ReadDir(stageNodes)
	if err != nil {
		t.Fatalf("readDir stage nodes: %v", err)
	}
	entries = append([]os.DirEntry{nil}, entries...)

	restoreFS = readDirOverrideFS{
		FS:           fakeFS,
		overridePath: stageNodes,
		entries:      entries,
	}

	path, node, ok, err := selectStageHostFirewall(newTestLogger(), "/stage")
	if err != nil {
		t.Fatalf("selectStageHostFirewall error: %v", err)
	}
	if !ok || node != "node1" || !strings.HasSuffix(path, "/stage/etc/pve/nodes/node1/host.fw") {
		t.Fatalf("unexpected selection ok=%v node=%q path=%q", ok, node, path)
	}
}

func TestApplyPVEFirewallFromStage_PropagatesSyncAndCopyErrors(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origHostname := firewallHostname
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		firewallHostname = origHostname
	})

	t.Run("syncDirExact failure bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		stageRoot := "/stage"
		if err := fakeFS.AddFile(stageRoot+"/etc/pve/firewall/cluster.fw", []byte("x")); err != nil {
			t.Fatalf("add staged firewall: %v", err)
		}
		if err := fakeFS.AddFile("/etc/pve/firewall", []byte("not a dir")); err != nil {
			t.Fatalf("add dest firewall file: %v", err)
		}

		_, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
		if err == nil || !strings.Contains(err.Error(), "ensure /etc/pve/firewall") {
			t.Fatalf("expected ensure error, got %v", err)
		}
	})

	t.Run("firewall file copy failure bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		fakeTime := &FakeTime{Current: time.Unix(0, 12345)}
		restoreTime = fakeTime

		stageRoot := "/stage"
		if err := fakeFS.AddFile(stageRoot+"/etc/pve/firewall", []byte("fw\n")); err != nil {
			t.Fatalf("add staged firewall file: %v", err)
		}

		tmpPath := "/etc/pve/firewall.proxsave.tmp." + strconv.FormatInt(fakeTime.Current.UnixNano(), 10)
		fakeFS.OpenFileErr[filepath.Clean(tmpPath)] = fmt.Errorf("open tmp denied")

		_, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
		if err == nil || !strings.Contains(err.Error(), "write /etc/pve/firewall") {
			t.Fatalf("expected write error, got %v", err)
		}
	})

	t.Run("host.fw copy failure bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		fakeTime := &FakeTime{Current: time.Unix(0, 12345)}
		restoreTime = fakeTime

		firewallHostname = func() (string, error) { return "current", nil }

		stageRoot := "/stage"
		if err := fakeFS.AddFile(stageRoot+"/etc/pve/nodes/other/host.fw", []byte("host\n")); err != nil {
			t.Fatalf("add staged host.fw: %v", err)
		}

		destHostFW := "/etc/pve/nodes/current/host.fw"
		tmpPath := destHostFW + ".proxsave.tmp." + strconv.FormatInt(fakeTime.Current.UnixNano(), 10)
		fakeFS.OpenFileErr[filepath.Clean(tmpPath)] = fmt.Errorf("open tmp denied")

		_, err := applyPVEFirewallFromStage(newTestLogger(), stageRoot)
		if err == nil || !strings.Contains(err.Error(), "write "+destHostFW) {
			t.Fatalf("expected host.fw write error, got %v", err)
		}
	})
}

func TestSyncDirExact_AdditionalEdgeCases(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	t.Run("stat error bubbles", func(t *testing.T) {
		base := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(base.Root) })
		restoreFS = statFailFS{FS: base, failPath: "/stage", err: fmt.Errorf("boom")}

		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "stat /stage") {
			t.Fatalf("expected stat error, got %v", err)
		}
	})

	t.Run("walkStage ignores disappeared dir readDir not-exist", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}

		restoreFS = readDirOverrideFS{
			FS:           fakeFS,
			overridePath: "/stage",
			entries: []os.DirEntry{
				staticDirEntry{name: "sub", mode: fs.ModeDir},
			},
		}

		if _, err := syncDirExact("/stage", "/dest"); err != nil {
			t.Fatalf("syncDirExact error: %v", err)
		}
		if info, err := fakeFS.Stat("/dest/sub"); err != nil || !info.IsDir() {
			t.Fatalf("expected /dest/sub directory, err=%v info=%v", err, info)
		}
	})

	t.Run("walkStage skips nil/blank/dot entries", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}

		restoreFS = readDirOverrideFS{
			FS:           fakeFS,
			overridePath: "/stage",
			entries: []os.DirEntry{
				nil,
				staticDirEntry{name: "   ", mode: 0},
				staticDirEntry{name: ".", mode: 0},
			},
		}

		if applied, err := syncDirExact("/stage", "/dest"); err != nil || len(applied) != 0 {
			t.Fatalf("expected nil, got applied=%#v err=%v", applied, err)
		}
	})

	t.Run("symlink parent ensure error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}
		if err := fakeFS.Symlink("/stage/target", "/stage/link"); err != nil {
			t.Fatalf("add stage symlink: %v", err)
		}
		if err := fakeFS.AddDir("/dest"); err != nil {
			t.Fatalf("add dest dir: %v", err)
		}

		restoreFS = &statFailOnNthFS{FS: fakeFS, path: "/dest", failOn: 2, err: fmt.Errorf("boom")}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "ensure /dest") {
			t.Fatalf("expected ensure error, got %v", err)
		}
	})

	t.Run("symlink creation error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}
		if err := fakeFS.Symlink("/stage/target", "/stage/link"); err != nil {
			t.Fatalf("add stage symlink: %v", err)
		}

		restoreFS = symlinkFailFS{FS: fakeFS, failNewname: "/dest/link", err: fmt.Errorf("boom")}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "symlink /dest/link") {
			t.Fatalf("expected symlink error, got %v", err)
		}
	})

	t.Run("directory ensure error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.AddDir("/stage/sub"); err != nil {
			t.Fatalf("add stage subdir: %v", err)
		}
		if err := fakeFS.AddFile("/dest/sub", []byte("not a dir")); err != nil {
			t.Fatalf("add dest file: %v", err)
		}
		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "ensure /dest/sub") {
			t.Fatalf("expected ensure /dest/sub error, got %v", err)
		}
	})

	t.Run("directory recursion error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage/sub"); err != nil {
			t.Fatalf("add stage subdir: %v", err)
		}
		restoreFS = readDirFailFS{FS: fakeFS, failPath: "/stage/sub", err: fmt.Errorf("boom")}

		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "readdir /stage/sub") {
			t.Fatalf("expected recursion readdir error, got %v", err)
		}
	})

	t.Run("copy file error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddFile("/stage/file", []byte("x")); err != nil {
			t.Fatalf("add stage file: %v", err)
		}
		restoreFS = readFileFailFS{FS: fakeFS, failPath: "/stage/file", err: fmt.Errorf("read boom")}

		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "read /stage/file") {
			t.Fatalf("expected read error, got %v", err)
		}
	})

	t.Run("pruneDest readDir error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}
		restoreFS = readDirFailFS{FS: fakeFS, failPath: "/dest", err: fmt.Errorf("boom")}

		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "readdir /dest") {
			t.Fatalf("expected pruneDest readdir error, got %v", err)
		}
	})

	t.Run("pruneDest ignores not-exist", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}
		restoreFS = readDirFailFS{FS: fakeFS, failPath: "/dest", err: os.ErrNotExist}

		if _, err := syncDirExact("/stage", "/dest"); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("pruneDest skips nil/blank/dot entries", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}

		restoreFS = multiReadDirFS{
			FS: fakeFS,
			entries: map[string][]os.DirEntry{
				"/dest": {
					nil,
					staticDirEntry{name: "   ", mode: 0},
					staticDirEntry{name: ".", mode: 0},
				},
			},
		}

		if _, err := syncDirExact("/stage", "/dest"); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("pruneDest entry.Info error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}

		restoreFS = multiReadDirFS{
			FS: fakeFS,
			entries: map[string][]os.DirEntry{
				"/dest": {badInfoDirEntry{name: "bad"}},
			},
		}

		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "stat /dest/bad") {
			t.Fatalf("expected pruneDest info error, got %v", err)
		}
	})

	t.Run("pruneDest recursion error bubbles", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.AddDir("/stage"); err != nil {
			t.Fatalf("add stage dir: %v", err)
		}

		restoreFS = multiReadDirFS{
			FS: fakeFS,
			entries: map[string][]os.DirEntry{
				"/dest":     {staticDirEntry{name: "sub", mode: fs.ModeDir}},
				"/dest/sub": nil,
			},
			errors: map[string]error{
				"/dest/sub": fmt.Errorf("boom"),
			},
		}

		if _, err := syncDirExact("/stage", "/dest"); err == nil || !strings.Contains(err.Error(), "readdir /dest/sub") {
			t.Fatalf("expected pruneDest recursion error, got %v", err)
		}
	})
}
