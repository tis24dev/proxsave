package orchestrator

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSmartMergeFstab_DefaultNoOnMismatch_BlankSkips(t *testing.T) {
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
	restoreCmd = &FakeCommandRunner{}
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 20, 12, 34, 56, 0, time.UTC)}

	currentPath := "/etc/fstab"
	backupPath := "/backup/etc/fstab"
	if err := fakeFS.AddFile(currentPath, []byte("UUID=curr-root / ext4 defaults 0 1\nUUID=curr-swap none swap sw 0 0\n")); err != nil {
		t.Fatalf("AddFile current: %v", err)
	}
	if err := fakeFS.AddFile(backupPath, []byte("UUID=backup-root / ext4 defaults 0 1\nUUID=backup-swap none swap sw 0 0\nserver:/export /mnt/nas nfs defaults 0 0\n")); err != nil {
		t.Fatalf("AddFile backup: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("\n")) // blank input -> defaultNo on mismatch
	if err := SmartMergeFstab(context.Background(), newTestLogger(), reader, currentPath, backupPath, false); err != nil {
		t.Fatalf("SmartMergeFstab error: %v", err)
	}

	got, err := fakeFS.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile current: %v", err)
	}
	if strings.Contains(string(got), "ProxSave Restore Merge") {
		t.Fatalf("expected merge to be skipped, but marker was written:\n%s", string(got))
	}
}

func TestSmartMergeFstab_DefaultYesOnMatch_BlankApplies(t *testing.T) {
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
	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 20, 12, 34, 56, 0, time.UTC)}

	currentPath := "/etc/fstab"
	backupPath := "/backup/etc/fstab"
	if err := fakeFS.AddFile(currentPath, []byte("UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\n")); err != nil {
		t.Fatalf("AddFile current: %v", err)
	}
	if err := fakeFS.AddFile(backupPath, []byte("UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\nserver:/export /mnt/nas nfs defaults 0 0\n")); err != nil {
		t.Fatalf("AddFile backup: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("\n")) // blank input -> defaultYes on match
	if err := SmartMergeFstab(context.Background(), newTestLogger(), reader, currentPath, backupPath, false); err != nil {
		t.Fatalf("SmartMergeFstab error: %v", err)
	}

	got, err := fakeFS.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile current: %v", err)
	}
	if !strings.Contains(string(got), "ProxSave Restore Merge") || !strings.Contains(string(got), "server:/export /mnt/nas") {
		t.Fatalf("expected merged fstab to include marker and mount, got:\n%s", string(got))
	}

	backupFstab := "/etc/fstab.bak-20260120-123456"
	if _, err := fakeFS.Stat(backupFstab); err != nil {
		t.Fatalf("expected fstab backup %s to exist: %v", backupFstab, err)
	}

	foundReload := false
	for _, call := range fakeCmd.Calls {
		if call == "systemctl daemon-reload" {
			foundReload = true
			break
		}
	}
	if !foundReload {
		t.Fatalf("expected systemctl daemon-reload call, got calls=%v", fakeCmd.Calls)
	}
}

func TestSmartMergeFstab_DryRunDoesNotWrite(t *testing.T) {
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
	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 20, 12, 34, 56, 0, time.UTC)}

	currentPath := "/etc/fstab"
	backupPath := "/backup/etc/fstab"
	original := "UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\n"
	if err := fakeFS.AddFile(currentPath, []byte(original)); err != nil {
		t.Fatalf("AddFile current: %v", err)
	}
	if err := fakeFS.AddFile(backupPath, []byte("UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\nserver:/export /mnt/nas nfs defaults 0 0\n")); err != nil {
		t.Fatalf("AddFile backup: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("y\n"))
	if err := SmartMergeFstab(context.Background(), newTestLogger(), reader, currentPath, backupPath, true); err != nil {
		t.Fatalf("SmartMergeFstab error: %v", err)
	}

	got, err := fakeFS.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile current: %v", err)
	}
	if string(got) != original {
		t.Fatalf("expected dry-run to keep fstab unchanged, got:\n%s", string(got))
	}
	if len(fakeCmd.Calls) != 0 {
		t.Fatalf("expected no command calls in dry-run, got calls=%v", fakeCmd.Calls)
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

	if err := extractArchiveNative(context.Background(), archivePath, destRoot, newTestLogger(), nil, RestoreModeFull, nil, "", skipFn); err != nil {
		t.Fatalf("extractArchiveNative error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destRoot, "etc", "test.txt")); err != nil {
		t.Fatalf("expected etc/test.txt to be extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "etc", "fstab")); !os.IsNotExist(err) {
		t.Fatalf("expected etc/fstab to be skipped, got err=%v", err)
	}
}
