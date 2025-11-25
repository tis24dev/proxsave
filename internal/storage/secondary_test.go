package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
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

	cfg := &config.Config{SecondaryPath: tempDir}
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
