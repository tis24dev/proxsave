package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestDeleteAssociatedLogBoundedByContext proves deleteAssociatedLog routes its
// removal through safefs: a pre-cancelled context must bound the operation so the
// log file is NOT removed (raw os.Remove would ignore the cancelled ctx and delete
// it, wedging on a dead/stale mount instead of returning promptly).
func TestDeleteAssociatedLogBoundedByContext(t *testing.T) {
	t.Parallel()

	backupDir := t.TempDir()
	logDir := t.TempDir()
	cfg := &config.Config{
		BackupPath:         backupDir,
		LogPath:            logDir,
		FsIoTimeoutSeconds: 30,
	}
	l, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage() error = %v", err)
	}

	// deleteAssociatedLog derives the log name from the backup filename key and
	// resolves it under config.LogPath: backup-<host>-<ts>.log.
	backupFile := filepath.Join(backupDir, "node-backup-20260719-120000.tar.zst")
	logFile := filepath.Join(logDir, "backup-node-20260719-120000.log")
	if err := os.WriteFile(logFile, []byte("log"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: safefs must refuse to run the remove

	_ = l.deleteAssociatedLog(ctx, backupFile)

	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("log file must survive a cancelled-context delete (safefs bounded), stat err: %v", err)
	}
}
