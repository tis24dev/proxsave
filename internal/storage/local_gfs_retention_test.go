package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestLocalStorageApplyGFSRetentionDeletesOldBackups(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		BackupPath:            dir,
		BundleAssociatedFiles: false,
	}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	now := time.Now()
	newestPath := filepath.Join(dir, "newest.tar.zst")
	middlePath := filepath.Join(dir, "middle.tar.zst")
	oldestPath := filepath.Join(dir, "oldest.tar.zst")

	for _, p := range []string{newestPath, middlePath, oldestPath} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}

	backups := []*types.BackupMetadata{
		{BackupFile: newestPath, Timestamp: now},
		{BackupFile: middlePath, Timestamp: now.Add(-24 * time.Hour)},
		{BackupFile: oldestPath, Timestamp: now.Add(-48 * time.Hour)},
	}
	retention := RetentionConfig{
		Policy:  "gfs",
		Daily:   1,
		Weekly:  0,
		Monthly: 0,
		Yearly:  0,
	}

	deleted, err := local.applyGFSRetention(context.Background(), backups, retention)
	if err != nil {
		t.Fatalf("applyGFSRetention error: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted=%d; want 2", deleted)
	}

	if _, err := os.Stat(newestPath); err != nil {
		t.Fatalf("expected newest backup to remain, stat error: %v", err)
	}
	for _, p := range []string{middlePath, oldestPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted, stat err=%v", p, err)
		}
	}

	if local.lastRet.BackupsDeleted != 2 || local.lastRet.BackupsRemaining != 1 {
		t.Fatalf("lastRet=%+v; want deleted=2 remaining=1", local.lastRet)
	}
}
