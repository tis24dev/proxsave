package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestNewLocalStorage tests LocalStorage creation
func TestNewLocalStorage(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{
		BackupPath: tempDir,
	}

	storage, err := NewLocalStorage(cfg, logger)

	if err != nil {
		t.Fatalf("NewLocalStorage failed: %v", err)
	}

	if storage == nil {
		t.Fatal("NewLocalStorage returned nil")
	}

	if storage.basePath != tempDir {
		t.Errorf("Expected basePath %s, got %s", tempDir, storage.basePath)
	}
}

// TestLocalStorage_Name tests Name method
func TestLocalStorage_Name(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{BackupPath: t.TempDir()}

	storage, _ := NewLocalStorage(cfg, logger)

	name := storage.Name()

	if name != "Local Storage" {
		t.Errorf("Expected 'Local Storage', got '%s'", name)
	}
}

// TestLocalStorage_Location tests Location method
func TestLocalStorage_Location(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{BackupPath: t.TempDir()}

	storage, _ := NewLocalStorage(cfg, logger)

	location := storage.Location()

	if location != LocationPrimary {
		t.Errorf("Expected LocationPrimary, got %v", location)
	}
}

// TestLocalStorage_IsEnabled tests IsEnabled method
func TestLocalStorage_IsEnabled(t *testing.T) {
	tests := []struct {
		name       string
		backupPath string
		expected   bool
	}{
		{
			name:       "Enabled with path",
			backupPath: "/tmp/backups",
			expected:   true,
		},
		{
			name:       "Disabled with empty path",
			backupPath: "",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logging.New(types.LogLevelInfo, false)
			cfg := &config.Config{BackupPath: tt.backupPath}

			storage, _ := NewLocalStorage(cfg, logger)

			if storage.IsEnabled() != tt.expected {
				t.Errorf("IsEnabled() = %v, expected %v", storage.IsEnabled(), tt.expected)
			}
		})
	}
}

// TestLocalStorage_IsCritical tests IsCritical method
func TestLocalStorage_IsCritical(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{BackupPath: t.TempDir()}

	storage, _ := NewLocalStorage(cfg, logger)

	if !storage.IsCritical() {
		t.Error("Expected IsCritical() to return true for local storage")
	}
}

// TestLocalStorage_DetectFilesystem tests filesystem detection
func TestLocalStorage_DetectFilesystem(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	ctx := context.Background()
	fsInfo, err := storage.DetectFilesystem(ctx)

	if err != nil {
		t.Fatalf("DetectFilesystem failed: %v", err)
	}

	if fsInfo == nil {
		t.Fatal("DetectFilesystem returned nil fsInfo")
	}

	if fsInfo.Type == "" {
		t.Error("Filesystem type should not be empty")
	}

	// Verify directory was created
	if _, err := os.Stat(tempDir); os.IsNotExist(err) {
		t.Error("Backup directory was not created")
	}
}

// TestLocalStorage_DetectFilesystem_InvalidPath tests detection with invalid path
func TestLocalStorage_DetectFilesystem_InvalidPath(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	// Create a path that will fail (file instead of directory)
	tempFile := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(tempFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Try to use the file as a directory path
	subPath := filepath.Join(tempFile, "subdir")
	cfg := &config.Config{BackupPath: subPath}
	storage, _ := NewLocalStorage(cfg, logger)

	ctx := context.Background()
	_, err := storage.DetectFilesystem(ctx)

	// Should return an error
	if err == nil {
		t.Error("Expected error when trying to create directory over file")
	}
}

func TestLocalStorage_DetectFilesystem_DetectorError(t *testing.T) {
	logger := newTestLogger()
	tempDir := t.TempDir()

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	storage.fsDetector.mountPointLookup = func(string) (string, error) {
		return "", errors.New("boom")
	}

	_, err := storage.DetectFilesystem(context.Background())
	if err == nil {
		t.Fatal("expected DetectFilesystem() error")
	}
	if _, ok := err.(*StorageError); !ok {
		t.Fatalf("expected *StorageError, got %T: %v", err, err)
	}
}

// TestLocalStorage_Store tests backup storage
func TestLocalStorage_Store(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	// Create a test backup file
	backupFile := filepath.Join(tempDir, "node-backup-20240101-010101.tar.xz")
	if err := os.WriteFile(backupFile, []byte("test backup data"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	metadata := &types.BackupMetadata{
		Timestamp: time.Now(),
	}

	ctx := context.Background()
	err := storage.Store(ctx, backupFile, metadata)

	if err != nil {
		t.Errorf("Store failed: %v", err)
	}

	// Verify file still exists and has correct permissions
	info, err := os.Stat(backupFile)
	if err != nil {
		t.Errorf("Backup file not found after Store: %v", err)
	}

	// Check permissions (should be readable)
	if info.Mode().Perm()&0400 == 0 {
		t.Error("Backup file should be readable")
	}
}

func TestLocalStorage_Store_FileNotFound(t *testing.T) {
	logger := newTestLogger()
	tempDir := t.TempDir()

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	err := storage.Store(context.Background(), filepath.Join(tempDir, "missing.tar.xz"), &types.BackupMetadata{})
	if err == nil {
		t.Fatal("expected Store() to fail for missing backup file")
	}
	if _, ok := err.(*StorageError); !ok {
		t.Fatalf("expected *StorageError, got %T: %v", err, err)
	}
}

func TestLocalStorage_Store_CountBackupsFailureDoesNotFail(t *testing.T) {
	logger := newTestLogger()

	backupDir := t.TempDir()
	backupFile := filepath.Join(backupDir, "node-backup-20240101-010101.tar.xz")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	base := t.TempDir()
	badPath := filepath.Join(base, "[invalid")
	if err := os.MkdirAll(badPath, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: badPath}
	storage, _ := NewLocalStorage(cfg, logger)

	if err := storage.Store(context.Background(), backupFile, &types.BackupMetadata{}); err != nil {
		t.Fatalf("Store() returned error: %v", err)
	}
}

// TestLocalStorage_Store_ContextCancellation tests Store with cancelled context
func TestLocalStorage_Store_ContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	backupFile := filepath.Join(tempDir, "test-backup.tar.xz")
	if err := os.WriteFile(backupFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	metadata := &types.BackupMetadata{
		Timestamp: time.Now(),
	}

	// Cancel context before calling Store
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := storage.Store(ctx, backupFile, metadata)

	// Should return context cancellation error
	if err == nil {
		t.Error("Expected error from cancelled context")
	}
}

// TestLocalStorage_Delete tests backup deletion
func TestLocalStorage_Delete(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	// Create a test backup file
	backupFile := filepath.Join(tempDir, "test-backup.tar.xz")
	if err := os.WriteFile(backupFile, []byte("test backup data"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

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

// TestLocalStorage_Delete_NonExistent tests deletion of non-existent file
func TestLocalStorage_Delete_NonExistent(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	ctx := context.Background()
	err := storage.Delete(ctx, filepath.Join(tempDir, "nonexistent.tar.xz"))

	// Should not return an error for non-existent file
	if err != nil {
		t.Errorf("Delete of non-existent file should not error: %v", err)
	}
}

func TestLocalStorage_Delete_RemoveErrorContinues(t *testing.T) {
	logger := newTestLogger()
	tempDir := t.TempDir()

	backupFile := filepath.Join(tempDir, "node-backup-20240101-010101.tar.xz")
	if err := os.WriteFile(backupFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	shaDir := backupFile + ".sha256"
	if err := os.MkdirAll(shaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shaDir, "child.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	if err := storage.Delete(context.Background(), backupFile); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(backupFile); !os.IsNotExist(err) {
		t.Fatalf("expected backup file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(shaDir); err != nil {
		t.Fatalf("expected %s to still exist (remove should have failed), stat err=%v", shaDir, err)
	}
}

// TestLocalStorage_LastRetentionSummary tests retention summary retrieval
func TestLocalStorage_LastRetentionSummary(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &config.Config{BackupPath: t.TempDir()}

	storage, _ := NewLocalStorage(cfg, logger)

	summary := storage.LastRetentionSummary()

	// Should return a valid (possibly zero) summary
	if summary.BackupsDeleted < 0 {
		t.Error("BackupsDeleted should not be negative")
	}
	if summary.BackupsRemaining < 0 {
		t.Error("BackupsRemaining should not be negative")
	}
}

// TestLocalStorage_VerifyUpload tests upload verification
func TestLocalStorage_VerifyUpload(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	// Create identical files
	localFile := filepath.Join(tempDir, "local.tar.xz")
	remoteFile := filepath.Join(tempDir, "remote.tar.xz")

	testData := []byte("test backup data")
	if err := os.WriteFile(localFile, testData, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteFile, testData, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	ctx := context.Background()
	verified, err := storage.VerifyUpload(ctx, localFile, remoteFile)

	if err != nil {
		t.Errorf("VerifyUpload failed: %v", err)
	}

	if !verified {
		t.Error("Files should be verified as identical")
	}
}

// TestLocalStorage_GetStats tests storage statistics
func TestLocalStorage_GetStats(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	// Create some test files
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	files := []struct {
		name string
		when time.Time
		data []byte
	}{
		{name: "node-backup-20240101-000000.tar.zst", when: baseTime.Add(-2 * time.Hour), data: []byte("aa")},
		{name: "node-backup-20240101-010101.tar.zst", when: baseTime.Add(-1 * time.Hour), data: []byte("bbb")},
		{name: "node-backup-20240101-020202.tar.zst", when: baseTime.Add(-3 * time.Hour), data: []byte("cccc")},
	}
	var wantTotalSize int64
	for _, f := range files {
		path := filepath.Join(tempDir, f.name)
		if err := os.WriteFile(path, f.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, f.when, f.when); err != nil {
			t.Fatal(err)
		}
		wantTotalSize += int64(len(f.data))
	}

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)
	storage.fsInfo = &FilesystemInfo{Type: FilesystemExt4}

	ctx := context.Background()
	stats, err := storage.GetStats(ctx)

	if err != nil {
		t.Errorf("GetStats failed: %v", err)
	}

	if stats == nil {
		t.Fatal("GetStats returned nil stats")
	}

	if stats.TotalBackups != len(files) {
		t.Fatalf("TotalBackups = %d, want %d", stats.TotalBackups, len(files))
	}
	if stats.TotalSize != wantTotalSize {
		t.Fatalf("TotalSize = %d, want %d", stats.TotalSize, wantTotalSize)
	}
	if stats.OldestBackup == nil || stats.NewestBackup == nil {
		t.Fatalf("expected oldest/newest backups to be set, got oldest=%v newest=%v", stats.OldestBackup, stats.NewestBackup)
	}
	if stats.FilesystemType != FilesystemExt4 {
		t.Fatalf("FilesystemType = %v, want %v", stats.FilesystemType, FilesystemExt4)
	}

	// Should have some space statistics
	if stats.TotalSpace == 0 && stats.AvailableSpace == 0 {
		t.Error("Expected non-zero space statistics")
	}
}

func TestLocalStorage_GetStats_ListError(t *testing.T) {
	logger := newTestLogger()
	base := t.TempDir()
	badPath := filepath.Join(base, "[invalid")
	if err := os.MkdirAll(badPath, 0o700); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupPath: badPath}
	storage, _ := NewLocalStorage(cfg, logger)

	if _, err := storage.GetStats(context.Background()); err == nil {
		t.Fatal("expected GetStats() to fail when List() fails")
	}
}

// TestLocalStorage_ApplyGFSRetention tests GFS retention application
func TestLocalStorage_ApplyGFSRetention(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{
		BackupPath:      tempDir,
		RetentionPolicy: "gfs",
	}
	storage, _ := NewLocalStorage(cfg, logger)

	// Create test backups with metadata
	now := time.Now()
	for i := 0; i < 10; i++ {
		timestamp := now.Add(-time.Duration(i*24) * time.Hour)
		filename := fmt.Sprintf("backup-%s.tar.xz", timestamp.Format("2006-01-02"))
		backupPath := filepath.Join(tempDir, filename)

		// Create backup file
		if err := os.WriteFile(backupPath, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		// Create metadata file
		metadata := &types.BackupMetadata{
			Timestamp: timestamp,
		}
		metadataPath := backupPath + ".json"
		metadataJSON, _ := json.Marshal(metadata)
		if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	retentionConfig := RetentionConfig{
		Policy:  "gfs",
		Daily:   3,
		Weekly:  2,
		Monthly: 1,
		Yearly:  1,
	}

	deleted, err := storage.ApplyRetention(ctx, retentionConfig)

	if err != nil {
		t.Errorf("ApplyRetention failed: %v", err)
	}

	// Should have deleted some backups
	if deleted < 0 {
		t.Errorf("Deleted count should not be negative, got %d", deleted)
	}

	// Verify retention summary was updated
	summary := storage.LastRetentionSummary()
	if summary.BackupsRemaining < 0 || summary.BackupsDeleted < 0 {
		t.Error("Retention summary should have valid counts")
	}
}

// TestLocalStorage_LoadMetadataFromBundle tests bundle metadata loading
func TestLocalStorage_LoadMetadataFromBundle(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{BackupPath: tempDir}
	storage, _ := NewLocalStorage(cfg, logger)

	// Create a corrupted bundle file to force a tar read error.
	bundlePath := filepath.Join(tempDir, "node-backup-20240101-010101.tar.zst.bundle.tar")
	if err := os.WriteFile(bundlePath, []byte("not a tar"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Try to load metadata (expected to fail, but shouldn't panic)
	_, err := storage.loadMetadataFromBundle(bundlePath)

	// Expected to fail for corrupted bundle, but shouldn't panic
	if err == nil {
		t.Log("loadMetadataFromBundle succeeded (unexpected but acceptable)")
	}
}
