package orchestrator

import (
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

func TestApplyNetworkFilesFromStage_PreservesSymlinkEntries(t *testing.T) {
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
	if gotTarget != "/stage/etc/network/interfaces.real" {
		t.Fatalf("symlink target=%q, want %q", gotTarget, "/stage/etc/network/interfaces.real")
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
