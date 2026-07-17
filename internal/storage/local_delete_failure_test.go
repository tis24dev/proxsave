package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

func newLocalStorageForDeleteTest(t *testing.T, dir string) *LocalStorage {
	t.Helper()
	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: false}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	return local
}

// TestLocalStorageDeleteBackupInternal_ArchiveVsSidecarFailure locks in the
// PS-BH-001 fix: a failure to remove the backup data archive is a real delete
// failure (non-sentinel error) so retention will not count it, while a
// sidecar-only failure (the archive itself is gone) yields
// errBackupSidecarDeleteOnly so retention still counts the backup as deleted.
func TestLocalStorageDeleteBackupInternal_ArchiveVsSidecarFailure(t *testing.T) {
	dir := t.TempDir()
	local := newLocalStorageForDeleteTest(t, dir)

	t.Run("archive removal failure is a real error", func(t *testing.T) {
		base := filepath.Join(dir, "arch1.tar.zst")
		// os.Remove fails on a non-empty directory (ENOTEMPTY).
		if err := os.MkdirAll(filepath.Join(base, "blocker"), 0o755); err != nil {
			t.Fatalf("mkdir blocker: %v", err)
		}
		if _, err := local.deleteBackupInternal(context.Background(), base); err == nil {
			t.Fatal("expected an error when the archive cannot be removed")
		} else if errors.Is(err, errBackupSidecarDeleteOnly) {
			t.Fatalf("archive failure must not be reported as sidecar-only: %v", err)
		}
	})

	t.Run("sidecar-only failure returns the sidecar sentinel", func(t *testing.T) {
		base := filepath.Join(dir, "arch2.tar.zst")
		if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
			t.Fatalf("write archive: %v", err)
		}
		// The .sha256 sidecar is a non-empty directory so its removal fails, but
		// the archive itself removes cleanly.
		if err := os.MkdirAll(filepath.Join(base+".sha256", "blocker"), 0o755); err != nil {
			t.Fatalf("mkdir sidecar blocker: %v", err)
		}
		if _, err := local.deleteBackupInternal(context.Background(), base); !errors.Is(err, errBackupSidecarDeleteOnly) {
			t.Fatalf("sidecar-only failure should return errBackupSidecarDeleteOnly, got %v", err)
		}
		if _, statErr := os.Stat(base); !os.IsNotExist(statErr) {
			t.Fatalf("the archive should have been removed, stat err=%v", statErr)
		}
	})
}

// TestLocalStorageRetention_DoesNotCountBackupWhenArchiveRemovalFails is the
// end-to-end PS-BH-001 check: a backup whose archive cannot be removed must not
// be counted as deleted by retention (which would over-report freed space).
func TestLocalStorageRetention_DoesNotCountBackupWhenArchiveRemovalFails(t *testing.T) {
	dir := t.TempDir()
	local := newLocalStorageForDeleteTest(t, dir)

	now := time.Now()
	newest := filepath.Join(dir, "newest.tar.zst")
	good := filepath.Join(dir, "good.tar.zst")
	bad := filepath.Join(dir, "bad.tar.zst")
	if err := os.WriteFile(newest, []byte("x"), 0o600); err != nil {
		t.Fatalf("write newest: %v", err)
	}
	if err := os.WriteFile(good, []byte("x"), 0o600); err != nil {
		t.Fatalf("write good: %v", err)
	}
	// The bad archive is a non-empty directory, so its removal fails.
	if err := os.MkdirAll(filepath.Join(bad, "blocker"), 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}

	// applySimpleRetention expects the slice sorted newest-first.
	backups := []*types.BackupMetadata{
		{BackupFile: newest, Timestamp: now, Verified: true},
		{BackupFile: good, Timestamp: now.Add(-24 * time.Hour), Verified: true},
		{BackupFile: bad, Timestamp: now.Add(-48 * time.Hour), Verified: true},
	}

	deleted, err := local.applySimpleRetention(context.Background(), backups, 1)
	if err != nil {
		t.Fatalf("applySimpleRetention error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d; want 1 (the un-removable archive must not be counted)", deleted)
	}
	if _, statErr := os.Stat(bad); statErr != nil {
		t.Fatalf("expected the un-removable archive to remain on disk, stat err=%v", statErr)
	}
}
