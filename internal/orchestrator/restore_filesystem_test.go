package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeFstabMerge_ProposesNetworkAndVerifiedUUIDMounts(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	// Mark the data UUID as present on the current system.
	if err := fakeFS.AddDir("/dev/disk/by-uuid"); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if err := fakeFS.AddFile("/dev/disk/by-uuid/data-uuid", []byte("")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}

	current := []FstabEntry{
		{Device: "UUID=curr-root", MountPoint: "/", Type: "ext4", Options: "defaults", Dump: "0", Pass: "1"},
		{Device: "UUID=curr-swap", MountPoint: "none", Type: "swap", Options: "sw", Dump: "0", Pass: "0"},
	}
	backup := []FstabEntry{
		{Device: "UUID=backup-root", MountPoint: "/", Type: "ext4", Options: "defaults", Dump: "0", Pass: "1"},
		{Device: "UUID=backup-swap", MountPoint: "none", Type: "swap", Options: "sw", Dump: "0", Pass: "0"},
		{Device: "server:/export", MountPoint: "/mnt/nas", Type: "nfs", Options: "defaults", Dump: "0", Pass: "0", RawLine: "server:/export /mnt/nas nfs defaults 0 0"},
		{Device: "UUID=data-uuid", MountPoint: "/mnt/data", Type: "ext4", Options: "defaults", Dump: "0", Pass: "2", RawLine: "UUID=data-uuid /mnt/data ext4 defaults 0 2"},
		{Device: "/dev/sdb1", MountPoint: "/mnt/unsafe", Type: "ext4", Options: "defaults", Dump: "0", Pass: "2"},
	}

	res := analyzeFstabMerge(newTestLogger(), current, backup)

	if !res.RootComparable || res.RootMatch {
		t.Fatalf("root comparable=%v match=%v; want comparable=true match=false", res.RootComparable, res.RootMatch)
	}
	if !res.SwapComparable || res.SwapMatch {
		t.Fatalf("swap comparable=%v match=%v; want comparable=true match=false", res.SwapComparable, res.SwapMatch)
	}

	if len(res.ProposedMounts) != 2 {
		t.Fatalf("ProposedMounts len=%d; want 2 (got=%+v)", len(res.ProposedMounts), res.ProposedMounts)
	}
	if res.ProposedMounts[0].MountPoint != "/mnt/nas" || res.ProposedMounts[1].MountPoint != "/mnt/data" {
		t.Fatalf("unexpected proposed mountpoints: %+v", []string{res.ProposedMounts[0].MountPoint, res.ProposedMounts[1].MountPoint})
	}

	if len(res.SkippedMounts) != 1 || res.SkippedMounts[0].MountPoint != "/mnt/unsafe" {
		t.Fatalf("SkippedMounts=%+v; want 1 entry for /mnt/unsafe", res.SkippedMounts)
	}
}

func TestExtractArchiveNative_SkipFnSkipsFstab(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	destRoot := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"etc/fstab":    "fstab",
		"etc/test.txt": "hello",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	skipFn := func(name string) bool {
		name = strings.TrimPrefix(strings.TrimSpace(name), "./")
		return name == "etc/fstab"
	}

	if err := extractArchiveNative(context.Background(), restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    destRoot,
		logger:      newTestLogger(),
		mode:        RestoreModeFull,
		skipFn:      skipFn,
	}); err != nil {
		t.Fatalf("extractArchiveNative error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destRoot, "etc", "test.txt")); err != nil {
		t.Fatalf("expected etc/test.txt to be extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "etc", "fstab")); !os.IsNotExist(err) {
		t.Fatalf("expected etc/fstab to be skipped, got err=%v", err)
	}
}
