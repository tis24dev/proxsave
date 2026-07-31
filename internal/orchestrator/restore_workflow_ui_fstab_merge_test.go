// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// These four cases were written against SmartMergeFstab, the reader-based merge that
// only the dead full-restore fallback ever called. They are ported here because the
// behaviour they pin - the prompt default, dry-run, and the device remap - belongs to
// the merge the product actually runs, which had no direct test of its own.
//
// The reader version read a blank line and applied the default itself. The UI version
// computes the same default and hands it to ConfirmFstabMerge, so the assertion moves
// from "blank input did X" to "the prompt was offered default X".

type fstabMergeFixture struct {
	fakeFS *FakeFS
	ui     *fakeRestoreWorkflowUI
}

func newFstabMergeFixture(t *testing.T, confirm bool, current, backup string) *fstabMergeFixture {
	t.Helper()
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

	if err := fakeFS.AddFile("/etc/fstab", []byte(current)); err != nil {
		t.Fatalf("AddFile current: %v", err)
	}
	if err := fakeFS.AddFile("/backup/etc/fstab", []byte(backup)); err != nil {
		t.Fatalf("AddFile backup: %v", err)
	}
	return &fstabMergeFixture{fakeFS: fakeFS, ui: &fakeRestoreWorkflowUI{confirmFstabMerge: confirm}}
}

func (f *fstabMergeFixture) merge(t *testing.T, dryRun bool) {
	t.Helper()
	if err := smartMergeFstabWithUI(context.Background(), newTestLogger(), f.ui, "/etc/fstab", "/backup/etc/fstab", dryRun); err != nil {
		t.Fatalf("smartMergeFstabWithUI error: %v", err)
	}
}

func (f *fstabMergeFixture) currentFstab(t *testing.T) string {
	t.Helper()
	got, err := f.fakeFS.ReadFile("/etc/fstab")
	if err != nil {
		t.Fatalf("ReadFile current: %v", err)
	}
	return string(got)
}

// A backup whose root/swap do not match this system is the dangerous case, so the
// prompt must not default to yes, and declining must leave fstab untouched.
func TestSmartMergeFstabWithUI_DefaultsToNoOnMismatch(t *testing.T) {
	f := newFstabMergeFixture(t, false,
		"UUID=curr-root / ext4 defaults 0 1\nUUID=curr-swap none swap sw 0 0\n",
		"UUID=backup-root / ext4 defaults 0 1\nUUID=backup-swap none swap sw 0 0\nserver:/export /mnt/nas nfs defaults 0 0\n")

	f.merge(t, false)

	if f.ui.fstabMergeCalls != 1 {
		t.Fatalf("ConfirmFstabMerge calls=%d, want 1", f.ui.fstabMergeCalls)
	}
	if f.ui.lastFstabMergeDefaultYes {
		t.Fatal("mismatched root/swap must be offered with default = no")
	}
	if strings.Contains(f.currentFstab(t), "ProxSave Restore Merge") {
		t.Fatalf("declining the prompt must leave fstab untouched:\n%s", f.currentFstab(t))
	}
}

// Matching root and swap means the backup describes this machine, so the prompt may
// default to yes and accepting writes the merged entry.
func TestSmartMergeFstabWithUI_DefaultsToYesOnMatch(t *testing.T) {
	f := newFstabMergeFixture(t, true,
		"UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\n",
		"UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\nserver:/export /mnt/nas nfs defaults 0 0\n")

	f.merge(t, false)

	if !f.ui.lastFstabMergeDefaultYes {
		t.Fatal("matching root/swap must be offered with default = yes")
	}
	got := f.currentFstab(t)
	if !strings.Contains(got, "/mnt/nas") {
		t.Fatalf("expected the new mount to be merged in:\n%s", got)
	}
}

// Dry run must reach the prompt and then write nothing at all.
func TestSmartMergeFstabWithUI_DryRunDoesNotWrite(t *testing.T) {
	original := "UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\n"
	f := newFstabMergeFixture(t, true, original,
		"UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\nserver:/export /mnt/nas nfs defaults 0 0\n")
	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	f.merge(t, true)

	if got := f.currentFstab(t); got != original {
		t.Fatalf("dry run must keep fstab unchanged, got:\n%s", got)
	}
	if len(fakeCmd.Calls) != 0 {
		t.Fatalf("dry run must run no commands, got calls=%v", fakeCmd.Calls)
	}
}

// The remap is why the fstab merge needs the backup's device inventory: a backup that
// names /dev/sdb1 must be rewritten to the UUID this system can actually resolve.
// This is the case that broke silently when the fallback stopped extracting the
// inventory next to the fstab.
func TestSmartMergeFstabWithUI_RemapsUnstableDeviceToUUIDWhenInventoryMatches(t *testing.T) {
	f := newFstabMergeFixture(t, true,
		"UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\n",
		"UUID=same-root / ext4 defaults 0 1\nUUID=same-swap none swap sw 0 0\n/dev/sdb1 /mnt/data ext4 defaults 0 2\n")

	// The target device exists here only by its stable reference.
	if err := f.fakeFS.AddDir("/dev/disk/by-uuid"); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if err := f.fakeFS.AddFile("/dev/disk/by-uuid/data-uuid", []byte("")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	// The inventory sits next to the extracted fstab; this is what
	// extractFstabInventoryInto puts there.
	if err := f.fakeFS.AddFile("/backup/var/lib/proxsave-info/commands/system/blkid.txt",
		[]byte("/dev/sdb1: UUID=\"data-uuid\" TYPE=\"ext4\"\n")); err != nil {
		t.Fatalf("AddFile inventory: %v", err)
	}

	f.merge(t, false)

	got := f.currentFstab(t)
	if strings.Contains(got, "/dev/sdb1") {
		t.Fatalf("expected /dev/sdb1 to be remapped, got:\n%s", got)
	}
	if !strings.Contains(got, "UUID=data-uuid") || !strings.Contains(got, "/mnt/data") {
		t.Fatalf("expected the remapped mount entry, got:\n%s", got)
	}
	if !strings.Contains(got, "nofail") {
		t.Fatalf("expected nofail on the restored entry, got:\n%s", got)
	}
}
