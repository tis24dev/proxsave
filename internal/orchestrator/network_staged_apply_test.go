package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type preservingSymlinkFS struct {
	*FakeFS
}

func newPreservingSymlinkFS() *preservingSymlinkFS {
	return &preservingSymlinkFS{FakeFS: NewFakeFS()}
}

func (f *preservingSymlinkFS) Symlink(oldname, newname string) error {
	if err := os.MkdirAll(filepath.Dir(f.onDisk(newname)), 0o755); err != nil {
		return err
	}
	return os.Symlink(oldname, f.onDisk(newname))
}

type tamperSymlinkFS struct {
	FS
	destPath string
	target   string
}

func (f tamperSymlinkFS) Symlink(oldname, newname string) error {
	if filepath.Clean(newname) == filepath.Clean(f.destPath) {
		return f.FS.Symlink(f.target, newname)
	}
	return f.FS.Symlink(oldname, newname)
}

func TestApplyNetworkFilesFromStage_RewritesSafeAbsoluteSymlinkTargetsWithinDestinationRoot(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.MkdirAll("/stage/etc/network", 0o755); err != nil {
		t.Fatalf("create stage dir: %v", err)
	}
	if err := fakeFS.Symlink("/etc/network/interfaces.real", "/stage/etc/network/interfaces"); err != nil {
		t.Fatalf("create staged symlink: %v", err)
	}

	applied, err := applyNetworkFilesFromStage(newTestLogger(), "/stage")
	if err != nil {
		t.Fatalf("applyNetworkFilesFromStage: %v", err)
	}

	info, err := fakeFS.Lstat("/etc/network/interfaces")
	if err != nil {
		t.Fatalf("lstat dest symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink mode, got %s", info.Mode())
	}

	gotTarget, err := fakeFS.Readlink("/etc/network/interfaces")
	if err != nil {
		t.Fatalf("readlink dest symlink: %v", err)
	}
	if gotTarget != "interfaces.real" {
		t.Fatalf("symlink target=%q, want %q", gotTarget, "interfaces.real")
	}

	found := false
	for _, path := range applied {
		if path == "/etc/network/interfaces" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected applied paths to include /etc/network/interfaces, got %#v", applied)
	}
}

func TestApplyNetworkFilesFromStage_RejectsEscapingSymlinkTargets(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.MkdirAll("/stage/etc/network", 0o755); err != nil {
		t.Fatalf("create stage dir: %v", err)
	}
	if err := fakeFS.Symlink("/stage/etc/network/interfaces.real", "/stage/etc/network/interfaces"); err != nil {
		t.Fatalf("create staged symlink: %v", err)
	}

	_, err := applyNetworkFilesFromStage(newTestLogger(), "/stage")
	if err == nil || !strings.Contains(err.Error(), "unsafe symlink target") {
		t.Fatalf("expected unsafe symlink target error, got %v", err)
	}
}

func TestApplyNetworkFilesFromStage_RejectsRelativeSymlinkTargetsThatEscapeDestinationRoot(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.MkdirAll("/stage/etc/network/interfaces.d", 0o755); err != nil {
		t.Fatalf("create stage dir: %v", err)
	}
	if err := fakeFS.Symlink("../../shadow", "/stage/etc/network/interfaces.d/uplink"); err != nil {
		t.Fatalf("create staged symlink: %v", err)
	}

	_, err := applyNetworkFilesFromStage(newTestLogger(), "/stage")
	if err == nil || !strings.Contains(err.Error(), "unsafe symlink target") {
		t.Fatalf("expected unsafe symlink target error, got %v", err)
	}
}

func TestApplyNetworkFilesFromStage_RejectsSymlinkStageDirectory(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.MkdirAll("/stage/etc", 0o755); err != nil {
		t.Fatalf("create stage parent dir: %v", err)
	}
	if err := fakeFS.Symlink("/outside/network", "/stage/etc/network"); err != nil {
		t.Fatalf("create staged dir symlink: %v", err)
	}

	_, err := applyNetworkFilesFromStage(newTestLogger(), "/stage")
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected staged directory symlink error, got %v", err)
	}
}

func TestValidateOverlaySymlinkTargetWithinRoot_RewritesAbsoluteTargetFromResolvedParent(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.MkdirAll("/dest/materialized/network", 0o755); err != nil {
		t.Fatalf("create materialized dir: %v", err)
	}
	if err := fakeFS.Symlink("/dest/materialized", "/dest/etc"); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	rewritten, err := validateOverlaySymlinkTargetWithinRoot(
		"/dest",
		"/dest/etc/network/interfaces",
		"/dest/materialized/network/interfaces.real",
	)
	if err != nil {
		t.Fatalf("validateOverlaySymlinkTargetWithinRoot error: %v", err)
	}
	if rewritten != "interfaces.real" {
		t.Fatalf("rewritten target = %q, want %q", rewritten, "interfaces.real")
	}
}

func TestCopySymlinkOverlayWithinRoot_CleansUpWhenCreatedSymlinkReadbackFails(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	if err := fakeFS.MkdirAll("/stage", 0o755); err != nil {
		t.Fatalf("create stage dir: %v", err)
	}
	if err := fakeFS.Symlink("/dest/target", "/stage/link"); err != nil {
		t.Fatalf("create staged symlink: %v", err)
	}

	restoreFS = readlinkFailFS{
		FS:       fakeFS,
		failPath: "/dest/link",
		err:      errors.New("boom"),
	}

	ok, err := copySymlinkOverlayWithinRoot("/stage/link", "/dest/link", "/dest")
	if ok {
		t.Fatal("copySymlinkOverlayWithinRoot reported success; want failure")
	}
	if err == nil || !strings.Contains(err.Error(), "read created symlink /dest/link") {
		t.Fatalf("expected readback validation error, got %v", err)
	}
	if _, statErr := fakeFS.Lstat("/dest/link"); !os.IsNotExist(statErr) {
		t.Fatalf("expected created symlink cleanup, lstat err = %v", statErr)
	}
}

func TestCopySymlinkOverlayWithinRoot_CleansUpWhenCreatedSymlinkEscapesAfterCreation(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := newPreservingSymlinkFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })

	if err := fakeFS.MkdirAll("/stage", 0o755); err != nil {
		t.Fatalf("create stage dir: %v", err)
	}
	if err := fakeFS.Symlink("/dest/target", "/stage/link"); err != nil {
		t.Fatalf("create staged symlink: %v", err)
	}

	restoreFS = tamperSymlinkFS{
		FS:       fakeFS,
		destPath: "/dest/link",
		target:   "/outside/evil",
	}

	ok, err := copySymlinkOverlayWithinRoot("/stage/link", "/dest/link", "/dest")
	if ok {
		t.Fatal("copySymlinkOverlayWithinRoot reported success; want failure")
	}
	if err == nil || !strings.Contains(err.Error(), "escapes root after creation") {
		t.Fatalf("expected post-create escape error, got %v", err)
	}
	if _, statErr := fakeFS.Lstat("/dest/link"); !os.IsNotExist(statErr) {
		t.Fatalf("expected created symlink cleanup, lstat err = %v", statErr)
	}
}

// TestApplyNetworkFilesFromStage_AppliesDeclaredBackends locks in the BH-004 fix:
// the staged network applier must apply the netplan, systemd-networkd and
// NetworkManager directories that the "network" category backs up, not only
// /etc/network and the hosts/hostname/dnsmasq files.
func TestApplyNetworkFilesFromStage_AppliesDeclaredBackends(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	staged := map[string]string{
		"/stage/etc/netplan/01-netcfg.yaml":                              "network:\n  version: 2\n",
		"/stage/etc/systemd/network/10-eth0.network":                     "[Match]\nName=eth0\n",
		"/stage/etc/NetworkManager/system-connections/eth0.nmconnection": "[connection]\nid=eth0\n",
	}
	for path, content := range staged {
		if err := fakeFS.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write staged %s: %v", path, err)
		}
	}

	applied, err := applyNetworkFilesFromStage(newTestLogger(), "/stage")
	if err != nil {
		t.Fatalf("applyNetworkFilesFromStage: %v", err)
	}

	for src, content := range staged {
		dest := strings.TrimPrefix(src, "/stage")
		got, err := fakeFS.ReadFile(dest)
		if err != nil {
			t.Fatalf("expected %s to be applied: %v", dest, err)
		}
		if string(got) != content {
			t.Fatalf("%s content=%q, want %q", dest, string(got), content)
		}
		found := false
		for _, p := range applied {
			if p == dest {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected applied paths to include %s, got %#v", dest, applied)
		}
	}
}
