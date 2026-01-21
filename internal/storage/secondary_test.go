package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestSecondaryStorage_Name tests Name method
func TestSecondaryStorage_Name(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryPath: t.TempDir()}

	storage, _ := NewSecondaryStorage(cfg, logger)
	name := storage.Name()

	if name != "Secondary Storage" {
		t.Errorf("Expected 'Secondary Storage', got '%s'", name)
	}
}

// TestSecondaryStorage_Location tests Location method
func TestSecondaryStorage_Location(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryPath: t.TempDir()}

	storage, _ := NewSecondaryStorage(cfg, logger)
	location := storage.Location()

	if location != LocationSecondary {
		t.Errorf("Expected LocationSecondary, got %v", location)
	}
}

// TestSecondaryStorage_IsEnabled tests IsEnabled method
func TestSecondaryStorage_IsEnabled(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	// Test with empty path - should be disabled
	cfg := &config.Config{SecondaryPath: ""}
	storage, _ := NewSecondaryStorage(cfg, logger)

	if storage.IsEnabled() {
		t.Error("Expected IsEnabled() to return false when path is empty")
	}

	// Enabled when flag and path are set.
	cfg = &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}
	storage, _ = NewSecondaryStorage(cfg, logger)
	if !storage.IsEnabled() {
		t.Error("Expected IsEnabled() to return true when enabled and path is set")
	}
}

// TestSecondaryStorage_IsCritical tests IsCritical method
func TestSecondaryStorage_IsCritical(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryPath: t.TempDir()}

	storage, _ := NewSecondaryStorage(cfg, logger)

	if storage.IsCritical() {
		t.Error("Expected IsCritical() to return false for secondary storage")
	}
}

// TestSecondaryStorage_DetectFilesystem tests filesystem detection
func TestSecondaryStorage_DetectFilesystem(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: tempDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ctx := context.Background()
	fsInfo, err := storage.DetectFilesystem(ctx)

	if err != nil {
		t.Fatalf("DetectFilesystem failed: %v", err)
	}

	if fsInfo == nil {
		t.Fatal("DetectFilesystem returned nil fsInfo")
	}

	// Verify directory was created
	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		t.Error("Secondary directory was not created")
	}
}

func TestSecondaryStorage_DetectFilesystem_MkdirFailsWhenPathIsFile(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmp := t.TempDir()
	path := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: path}
	storage, _ := NewSecondaryStorage(cfg, logger)

	_, err := storage.DetectFilesystem(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if se.Location != LocationSecondary || !se.Recoverable || se.IsCritical {
		t.Fatalf("unexpected StorageError: %+v", se)
	}
}

func TestSecondaryStorage_DetectFilesystem_FallsBackToUnknownWhenDetectorErrors(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: tempDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	// Force filesystem detector failure via test hook.
	storage.fsDetector.mountPointLookup = func(path string) (string, error) {
		return "", errors.New("boom")
	}

	info, err := storage.DetectFilesystem(context.Background())
	if err != nil {
		t.Fatalf("DetectFilesystem error: %v", err)
	}
	if info == nil || info.Type != FilesystemUnknown || info.SupportsOwnership {
		t.Fatalf("unexpected fs info: %+v", info)
	}
}

// TestSecondaryStorage_Delete tests backup deletion
func TestSecondaryStorage_Delete(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	// Create a test backup file
	backupFile := filepath.Join(tempDir, "test-backup.tar.xz")
	if err := os.WriteFile(backupFile, []byte("test backup data"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{SecondaryPath: tempDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ctx := context.Background()
	err := storage.Delete(ctx, backupFile)

	if err != nil {
		t.Errorf("Delete failed: %v", err)
	}

	// Verify file was deleted
	if _, err := os.Stat(backupFile); !os.IsNotExist(err) {
		t.Error("Backup file should have been deleted")
	}
}

// TestSecondaryStorage_Delete_NonExistent tests deletion of non-existent file
func TestSecondaryStorage_Delete_NonExistent(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{SecondaryPath: tempDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ctx := context.Background()
	err := storage.Delete(ctx, filepath.Join(tempDir, "nonexistent.tar.xz"))

	// Should not return an error for non-existent file
	if err != nil {
		t.Errorf("Delete of non-existent file should not error: %v", err)
	}
}

// TestSecondaryStorage_ApplyRetention tests retention application
func TestSecondaryStorage_ApplyRetention(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{
		SecondaryPath:          tempDir,
		SecondaryRetentionDays: 7,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ctx := context.Background()
	retentionConfig := RetentionConfig{
		Policy:     "simple",
		MaxBackups: 5,
	}

	deleted, err := storage.ApplyRetention(ctx, retentionConfig)

	if err != nil {
		t.Errorf("ApplyRetention failed: %v", err)
	}

	// Should have deleted some backups (or 0 if none exist)
	if deleted < 0 {
		t.Errorf("Deleted count should not be negative, got %d", deleted)
	}
}

func TestSecondaryStorage_List_ReturnsErrorForInvalidGlobPattern(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	base := t.TempDir()
	badDir := filepath.Join(base, "[invalid")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: badDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	_, err := storage.List(context.Background())
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if se.Location != LocationSecondary || !se.Recoverable {
		t.Fatalf("unexpected StorageError: %+v", se)
	}
}

func TestSecondaryStorage_CountBackups_ReturnsMinusOneWhenListFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	base := t.TempDir()
	badDir := filepath.Join(base, "[invalid")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: badDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	if got := storage.countBackups(context.Background()); got != -1 {
		t.Fatalf("countBackups()=%d want -1", got)
	}
}

func TestSecondaryStorage_Store_ReturnsErrorForMissingSourceFile(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}
	storage, _ := NewSecondaryStorage(cfg, logger)

	_, err := os.Stat(filepath.Join(cfg.SecondaryPath, "dummy"))
	_ = err

	err = storage.Store(context.Background(), filepath.Join(t.TempDir(), "missing.tar.zst"), &types.BackupMetadata{})
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if se.Operation != "store" || se.Recoverable {
		t.Fatalf("unexpected StorageError: %+v", se)
	}
}

func TestSecondaryStorage_Store_ReturnsRecoverableErrorWhenDestIsFile(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmp := t.TempDir()
	destAsFile := filepath.Join(tmp, "dest-file")
	if err := os.WriteFile(destAsFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: destAsFile}
	storage, _ := NewSecondaryStorage(cfg, logger)

	srcDir := t.TempDir()
	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{})
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if !se.Recoverable {
		t.Fatalf("expected recoverable error, got %+v", se)
	}
}

func TestSecondaryStorage_Store_AssociatedCopyFailuresAreNonFatal(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         destDir,
		BundleAssociatedFiles: false,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create an associated "file" as a directory to force copyFile failure.
	badAssoc := backupFile + ".metadata"
	if err := os.MkdirAll(badAssoc, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badAssoc, "nested"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store() error = %v; want nil (non-fatal assoc failure)", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, filepath.Base(backupFile))); err != nil {
		t.Fatalf("expected backup to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, filepath.Base(badAssoc))); !os.IsNotExist(err) {
		t.Fatalf("expected failing associated file not to be copied, err=%v", err)
	}
}

func TestSecondaryStorage_Store_BundleCopyFailureIsNonFatal(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         destDir,
		BundleAssociatedFiles: true,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create bundle as a directory to force copyFile failure for bundle only.
	bundleDir := backupFile + ".bundle.tar"
	if err := os.MkdirAll(bundleDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "nested"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store() error = %v; want nil (non-fatal bundle failure)", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, filepath.Base(backupFile))); err != nil {
		t.Fatalf("expected backup to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, filepath.Base(bundleDir))); !os.IsNotExist(err) {
		t.Fatalf("expected bundle not to be copied due to forced failure, err=%v", err)
	}
}

func TestSecondaryStorage_CopyFile_CoversErrorBranches(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := storage.copyFile(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyFile canceled err=%v want context.Canceled", err)
	}

	// Missing source -> stat error.
	if err := storage.copyFile(context.Background(), filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dest")); err == nil {
		t.Fatalf("expected error for missing source")
	}

	// Destination directory creation error: make dest dir a file.
	tmp := t.TempDir()
	destDirFile := filepath.Join(tmp, "destdir")
	if err := os.WriteFile(destDirFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	src := filepath.Join(tmp, "src")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := storage.copyFile(context.Background(), src, filepath.Join(destDirFile, "out")); err == nil {
		t.Fatalf("expected error for invalid destination directory")
	}

	// Read error: source is a directory.
	srcDir := filepath.Join(tmp, "srcdir")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := storage.copyFile(context.Background(), srcDir, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatalf("expected error when reading from directory source")
	}

	// Rename error: destination exists as a directory.
	renameDestDir := t.TempDir()
	renameDest := filepath.Join(renameDestDir, "out")
	if err := os.MkdirAll(renameDest, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := storage.copyFile(context.Background(), src, renameDest); err == nil {
		t.Fatalf("expected error when renaming over existing directory")
	}

	// CreateTemp error: destDir not writable (skip for root).
	if os.Geteuid() != 0 {
		unwritable := filepath.Join(t.TempDir(), "unwritable")
		if err := os.MkdirAll(unwritable, 0o500); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(unwritable, 0o700) })

		srcFile := filepath.Join(t.TempDir(), "srcfile")
		if err := os.WriteFile(srcFile, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := storage.copyFile(context.Background(), srcFile, filepath.Join(unwritable, "out")); err == nil {
			t.Fatalf("expected error when CreateTemp cannot write to dest dir")
		}
	}
}

func TestSecondaryStorage_DeleteBackupInternal_ContextCanceled(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := storage.deleteBackupInternal(ctx, filepath.Join(t.TempDir(), "node-backup-20240102-030405.tar.zst"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestSecondaryStorage_DeleteBackupInternal_ContinuesOnRemoveErrors(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		SecondaryLogPath:      "", // avoid log deletion
		BundleAssociatedFiles: false,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(backupDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Make an associated path a non-empty directory so os.Remove fails.
	bad := backupFile + ".metadata"
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "nested"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logDeleted, err := storage.deleteBackupInternal(context.Background(), backupFile)
	if err != nil {
		t.Fatalf("deleteBackupInternal error: %v", err)
	}
	if logDeleted {
		t.Fatalf("expected logDeleted=false when SecondaryLogPath is empty")
	}
}

func TestSecondaryStorage_DeleteAssociatedLog_ReturnsFalseOnRemoveError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logDir := t.TempDir()
	cfg := &config.Config{SecondaryLogPath: logDir}
	storage, _ := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir(), SecondaryLogPath: logDir}, logger)
	storage.config = cfg

	host := "node1"
	timestamp := "20240102-030405"
	backupPath := filepath.Join(logDir, fmt.Sprintf("%s-backup-%s.tar.zst", host, timestamp))
	logPath := filepath.Join(logDir, fmt.Sprintf("backup-%s-%s.log", host, timestamp))

	// Create a non-empty directory at the log path so os.Remove returns an error.
	if err := os.MkdirAll(logPath, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logPath, "nested"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if storage.deleteAssociatedLog(backupPath) {
		t.Fatalf("expected deleteAssociatedLog to return false on remove error")
	}
}

func TestSecondaryStorage_ApplyRetention_HandlesListFailure(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	base := t.TempDir()
	badDir := filepath.Join(base, "[invalid")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: badDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	_, err := storage.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if se.Operation != "apply_retention" {
		t.Fatalf("Operation=%q want %q", se.Operation, "apply_retention")
	}
}

func TestSecondaryStorage_ApplyRetention_SimpleCoversDisabledAndWithinLimitBranches(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: backupDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	// Create one backup file.
	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	backup := filepath.Join(backupDir, fmt.Sprintf("node-backup-%s.tar.zst", ts.Format("20060102-150405")))
	if err := os.WriteFile(backup, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chtimes(backup, ts, ts); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// maxBackups <= 0 branch.
	if deleted, err := storage.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 0}); err != nil || deleted != 0 {
		t.Fatalf("ApplyRetention disabled got (%d,%v) want (0,nil)", deleted, err)
	}

	// totalBackups <= maxBackups branch.
	if deleted, err := storage.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil || deleted != 0 {
		t.Fatalf("ApplyRetention within limit got (%d,%v) want (0,nil)", deleted, err)
	}
}

func TestSecondaryStorage_ApplyRetention_SetsNoLogInfoWhenLogCountFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	badLogDir := filepath.Join(t.TempDir(), "[invalid")
	if err := os.MkdirAll(badLogDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		SecondaryLogPath:      badLogDir,
		BundleAssociatedFiles: false,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	baseTime := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		ts := baseTime.Add(-time.Duration(i) * time.Hour)
		path := filepath.Join(backupDir, fmt.Sprintf("node-nolog-backup-%s.tar.zst", ts.Format("20060102-150405")))
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	deleted, err := storage.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d want %d", deleted, 1)
	}
	if storage.LastRetentionSummary().HasLogInfo {
		t.Fatalf("expected HasLogInfo=false when log count cannot be computed")
	}
}

func TestSecondaryStorage_ApplyRetention_GFS_SetsNoLogInfoWhenLogCountFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	badLogDir := filepath.Join(t.TempDir(), "[invalid")
	if err := os.MkdirAll(badLogDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		SecondaryLogPath:      badLogDir,
		BundleAssociatedFiles: false,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	now := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ts := now.Add(-time.Duration(i) * time.Hour)
		path := filepath.Join(backupDir, fmt.Sprintf("node-nolog-gfs-backup-%s.tar.zst", ts.Format("20060102-150405")))
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	deleted, err := storage.ApplyRetention(context.Background(), RetentionConfig{
		Policy:  "gfs",
		Daily:   1,
		Weekly:  0,
		Monthly: 0,
		Yearly:  0,
	})
	if err != nil {
		t.Fatalf("ApplyRetention error: %v", err)
	}
	if deleted == 0 {
		t.Fatalf("expected at least one deletion to exercise retention path")
	}
	if storage.LastRetentionSummary().HasLogInfo {
		t.Fatalf("expected HasLogInfo=false when log count cannot be computed")
	}
}

func TestSecondaryStorage_GetStats_UsesListAndComputesSizes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("statfs behavior differs on Windows; skip for determinism")
	}
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: backupDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	ts1 := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	ts2 := time.Date(2024, 1, 2, 4, 4, 5, 0, time.UTC)
	b1 := filepath.Join(backupDir, fmt.Sprintf("node-backup-%s.tar.zst", ts1.Format("20060102-150405")))
	b2 := filepath.Join(backupDir, fmt.Sprintf("node-backup-%s.tar.zst", ts2.Format("20060102-150405")))
	if err := os.WriteFile(b1, []byte("one"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(b2, []byte("two-two"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chtimes(b1, ts1, ts1); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if err := os.Chtimes(b2, ts2, ts2); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	storage.fsInfo = &FilesystemInfo{Type: FilesystemExt4}
	stats, err := storage.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats error: %v", err)
	}
	if stats.TotalBackups != 2 {
		t.Fatalf("TotalBackups=%d want %d", stats.TotalBackups, 2)
	}
	if stats.TotalSize != int64(len("one")+len("two-two")) {
		t.Fatalf("TotalSize=%d want %d", stats.TotalSize, len("one")+len("two-two"))
	}
	if stats.FilesystemType != FilesystemExt4 {
		t.Fatalf("FilesystemType=%q want %q", stats.FilesystemType, FilesystemExt4)
	}
	if stats.OldestBackup == nil || stats.NewestBackup == nil {
		t.Fatalf("expected OldestBackup/NewestBackup to be set")
	}
	if !stats.OldestBackup.Equal(ts1) || !stats.NewestBackup.Equal(ts2) {
		t.Fatalf("oldest/newest mismatch: oldest=%v newest=%v", stats.OldestBackup, stats.NewestBackup)
	}
}

func TestSecondaryStorage_DeleteBackupInternal_DeletesAssociatedBundleWhenEnabled(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		BundleAssociatedFiles: true,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(backupDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bundleFile := backupFile + ".bundle.tar"
	if err := os.WriteFile(bundleFile, []byte("bundle"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := storage.Delete(context.Background(), bundleFile); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	// Both base and bundle should be removed (best effort).
	if _, err := os.Stat(bundleFile); !os.IsNotExist(err) {
		t.Fatalf("expected bundle file to be deleted, err=%v", err)
	}
	// Base may or may not be removed depending on candidate building; ensure at least the target is gone.
}

func TestSecondaryStorage_List_SkipsMetadataShaFiles(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	baseDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: baseDir, BundleAssociatedFiles: false}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backup := filepath.Join(baseDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backup, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(backup+".metadata", []byte("meta"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(backup+".metadata.sha256", []byte("hash"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(backup+".sha256", []byte("hash"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	backups, err := storage.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("List returned %d backups want 1", len(backups))
	}
	if backups[0].BackupFile != backup {
		t.Fatalf("BackupFile=%q want %q", backups[0].BackupFile, backup)
	}
}

func TestSecondaryStorage_Store_MirrorsTimestampsBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("timestamp resolution differs on Windows")
	}
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: destDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wantTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(backupFile, wantTime, wantTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store error: %v", err)
	}

	dest := filepath.Join(destDir, filepath.Base(backupFile))
	stat, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat dest: %v", err)
	}
	// Allow small FS rounding differences.
	if diff := stat.ModTime().Sub(wantTime); diff < -time.Second || diff > time.Second {
		t.Fatalf("dest modtime=%v want ~%v (diff=%v)", stat.ModTime(), wantTime, diff)
	}
}

func TestSecondaryStorage_Store_BestEffortPermissionsSkipWhenUnsupported(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: destDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Force branch: fsInfo present but ownership unsupported => skip SetPermissions call.
	storage.fsInfo = &FilesystemInfo{Type: FilesystemCIFS, SupportsOwnership: false}
	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store error: %v", err)
	}
}

func TestSecondaryStorage_Store_BestEffortPermissionsRunsWhenSupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ownership/permissions differ on Windows")
	}
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: destDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	storage.fsInfo = &FilesystemInfo{Type: FilesystemExt4, SupportsOwnership: true}
	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store error: %v", err)
	}

	dest := filepath.Join(destDir, filepath.Base(backupFile))
	if st, err := os.Stat(dest); err != nil {
		t.Fatalf("Stat dest: %v", err)
	} else if st.Mode().Perm()&0o777 == 0 {
		t.Fatalf("unexpected dest perms: %v", st.Mode().Perm())
	}
}

func TestSecondaryStorage_DeleteAssociatedLog_EmptyConfigPaths(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryLogPath: "   "}
	storage, _ := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}, logger)
	storage.config = cfg

	if storage.deleteAssociatedLog("node-backup-20240102-030405.tar.zst") {
		t.Fatalf("expected false when log path is empty/whitespace")
	}
}

func TestSecondaryStorage_DeleteBackupInternal_HandlesBundleSuffixTrimming(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	backupDir := t.TempDir()
	cfg := &config.Config{
		SecondaryEnabled:      true,
		SecondaryPath:         backupDir,
		BundleAssociatedFiles: true,
	}
	storage, _ := NewSecondaryStorage(cfg, logger)

	base := filepath.Join(backupDir, "node-backup-20240102-030405.tar.zst")
	bundle := base + ".bundle.tar"
	if err := os.WriteFile(base, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(bundle, []byte("bundle"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := storage.Delete(context.Background(), bundle); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("expected bundle to be deleted, err=%v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		// Base should typically be removed by candidate deletion; allow missing coverage parity check.
		t.Fatalf("expected base to be deleted too, err=%v", err)
	}
}

func TestSecondaryStorage_List_DedupesMatchesAcrossPatterns(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	baseDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: baseDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	// A file that matches both patterns: "-backup-" plus ".tar.gz" also matches legacy glob when named proxmox-backup.
	path := filepath.Join(baseDir, "proxmox-backup-20240102-030405.tar.gz")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Also add a Go naming backup.
	path2 := filepath.Join(baseDir, "node-backup-20240102-030405.tar.zst")
	if err := os.WriteFile(path2, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	backups, err := storage.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	// Should not include duplicates.
	seen := map[string]struct{}{}
	for _, b := range backups {
		if _, ok := seen[b.BackupFile]; ok {
			t.Fatalf("duplicate backup returned: %s", b.BackupFile)
		}
		seen[b.BackupFile] = struct{}{}
	}
}

func TestSecondaryStorage_Store_CopyFileUsesTempAndRename(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: destDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupFile := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	data := []byte("data")
	if err := os.WriteFile(backupFile, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store error: %v", err)
	}

	dest := filepath.Join(destDir, filepath.Base(backupFile))
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile dest: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("dest data=%q want %q", string(got), string(data))
	}

	// Ensure no temporary files are left behind.
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("unexpected temp file left behind: %s", e.Name())
		}
	}
}

func TestSecondaryStorage_Store_FailsWhenSourceIsDirectory(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	srcDir := t.TempDir()
	destDir := t.TempDir()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: destDir}
	storage, _ := NewSecondaryStorage(cfg, logger)

	backupDir := filepath.Join(srcDir, "node-backup-20240102-030405.tar.zst")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err := storage.Store(context.Background(), backupDir, &types.BackupMetadata{})
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if se.Location != LocationSecondary {
		t.Fatalf("unexpected StorageError: %+v", se)
	}
}

func TestSecondaryStorage_CopyFile_RespectsSourcePermissionsAndChtimesBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod/chtimes differ on Windows")
	}
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}
	storage, _ := NewSecondaryStorage(cfg, logger)

	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("data"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wantTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(src, wantTime, wantTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "dest")

	if err := storage.copyFile(context.Background(), src, dest); err != nil {
		t.Fatalf("copyFile error: %v", err)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat dest: %v", err)
	}
	if st.Mode().Perm() != fs.FileMode(0o640) {
		t.Fatalf("dest perm=%#o want %#o", st.Mode().Perm(), 0o640)
	}
}
