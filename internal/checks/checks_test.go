package checks

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCheckDiskSpace(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:         tmpDir,
		LogPath:            tmpDir,
		LockDirPath:        tmpDir,
		MinDiskPrimaryGB:   0.001, // Very small requirement for testing
		MinDiskSecondaryGB: 0,
		MinDiskCloudGB:     0,
		DryRun:             false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckDiskSpace()

	if !result.Passed {
		t.Errorf("CheckDiskSpace failed: %s", result.Message)
	}
}

func TestCheckDiskSpaceInsufficientSpace(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:         tmpDir,
		LogPath:            tmpDir,
		LockDirPath:        tmpDir,
		MinDiskPrimaryGB:   999999.0, // Impossibly large requirement
		MinDiskSecondaryGB: 0,
		MinDiskCloudGB:     0,
		DryRun:             false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckDiskSpace()

	if result.Passed {
		t.Error("CheckDiskSpace should have failed with insufficient space")
	}
}

func TestCheckLockFile(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := &CheckerConfig{
		BackupPath:   tmpDir,
		LogPath:      tmpDir,
		LockDirPath:  tmpDir,
		LockFilePath: lockPath,
		MaxLockAge:   1 * time.Hour,
		DryRun:       false,
	}

	checker := NewChecker(logger, config)

	// First check should pass (no lock file exists)
	result := checker.CheckLockFile()
	if !result.Passed {
		t.Errorf("CheckLockFile failed: %s", result.Message)
	}

	// Lock file should now exist
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("Lock file was not created")
	}

	// Second check should fail (lock file exists and is fresh)
	result2 := checker.CheckLockFile()
	if result2.Passed {
		t.Error("CheckLockFile should have failed with existing lock")
	}

	// Clean up
	checker.ReleaseLock()
}

func TestCheckLockFileStaleLock(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	// Create a stale lock file
	if err := os.WriteFile(lockPath, []byte("old lock"), 0644); err != nil {
		t.Fatalf("Failed to create test lock file: %v", err)
	}

	// Set modification time to 3 hours ago
	oldTime := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatalf("Failed to set lock file time: %v", err)
	}

	config := &CheckerConfig{
		BackupPath:   tmpDir,
		LogPath:      tmpDir,
		LockDirPath:  tmpDir,
		LockFilePath: lockPath,
		MaxLockAge:   1 * time.Hour, // Stale after 1 hour
		DryRun:       false,
	}

	checker := NewChecker(logger, config)

	// Check should pass (stale lock should be removed and new one created)
	result := checker.CheckLockFile()
	if !result.Passed {
		t.Errorf("CheckLockFile failed with stale lock: %s", result.Message)
	}

	// Clean up
	checker.ReleaseLock()
}

func TestCheckPermissions(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:          tmpDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		SkipPermissionCheck: false,
		DryRun:              false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()

	if !result.Passed {
		t.Errorf("CheckPermissions failed: %s", result.Message)
	}
	if result.Code != "PERMISSION_CHECK" {
		t.Errorf("expected Code PERMISSION_CHECK on success, got %q", result.Code)
	}
}

func TestCheckPermissionsPermissionDenied(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	// When running tests as root, permission checks based on chmod may not
	// behave as expected because root can bypass filesystem permissions.
	// In that case, skip this test.
	if os.Geteuid() == 0 {
		t.Skip("skipping permission-denied check when running as root")
	}

	// Create a directory that is not writable
	protectedDir := filepath.Join(tmpDir, "protected")
	if err := os.Mkdir(protectedDir, 0755); err != nil {
		t.Fatalf("failed to create protected dir: %v", err)
	}
	if err := os.Chmod(protectedDir, 0555); err != nil {
		t.Fatalf("failed to chmod protected dir: %v", err)
	}

	config := &CheckerConfig{
		BackupPath:          protectedDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		SkipPermissionCheck: false,
		DryRun:              false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()

	if result.Passed {
		t.Fatalf("CheckPermissions should fail for non-writable directory")
	}
	if result.Code != "PERMISSION_DENIED" {
		t.Errorf("expected Code PERMISSION_DENIED, got %q", result.Code)
	}
}

func TestCheckPermissionsDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:          tmpDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		SkipPermissionCheck: false,
		DryRun:              true,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()

	if !result.Passed {
		t.Fatalf("CheckPermissions should pass in dry-run mode, got: %s", result.Message)
	}
	if result.Code != "PERMISSION_CHECK" {
		t.Errorf("expected Code PERMISSION_CHECK in dry-run mode, got %q", result.Code)
	}
}

func TestCheckPermissionsEIORetryAndFailure(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	// Save and restore original createTestFile to avoid side effects
	origCreate := createTestFile
	defer func() {
		createTestFile = origCreate
	}()

	attempts := 0
	createTestFile = func(name string) (*os.File, error) {
		attempts++
		return nil, syscall.EIO
	}

	config := &CheckerConfig{
		BackupPath:          tmpDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		SkipPermissionCheck: false,
		DryRun:              false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()

	if result.Passed {
		t.Fatalf("CheckPermissions should fail when all attempts return EIO")
	}
	if result.Code != "FS_IO_ERROR" {
		t.Errorf("expected Code FS_IO_ERROR, got %q", result.Code)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts on EIO, got %d", attempts)
	}
}

func TestCheckDirectories(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:  tmpDir,
		LogPath:     tmpDir,
		LockDirPath: tmpDir,
		DryRun:      false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckDirectories()

	if !result.Passed {
		t.Errorf("CheckDirectories failed: %s", result.Message)
	}
}

func TestCheckDirectoriesMissing(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	missingDir := filepath.Join(tmpDir, "nonexistent")

	config := &CheckerConfig{
		BackupPath:  missingDir,
		LogPath:     tmpDir,
		LockDirPath: filepath.Join(tmpDir, "locks"),
		DryRun:      false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckDirectories()

	if !result.Passed {
		t.Fatalf("CheckDirectories failed: %s", result.Message)
	}

	if _, err := os.Stat(missingDir); err != nil {
		t.Errorf("Expected BackupPath directory to be created, stat error: %v", err)
	}
}

func TestCheckDirectoriesDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	missingDir := filepath.Join(tmpDir, "nonexistent")

	config := &CheckerConfig{
		BackupPath:  missingDir,
		LogPath:     tmpDir,
		LockDirPath: filepath.Join(tmpDir, "locks"),
		DryRun:      true,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckDirectories()

	if !result.Passed {
		t.Fatalf("CheckDirectories (dry run) failed: %s", result.Message)
	}

	if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
		t.Errorf("Dry run should not create directory, but %s exists", missingDir)
	}
}

func TestRunAllChecks(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:          tmpDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		MinDiskPrimaryGB:    0.001,
		MinDiskSecondaryGB:  0,
		MinDiskCloudGB:      0,
		LockFilePath:        filepath.Join(tmpDir, ".backup.lock"),
		MaxLockAge:          1 * time.Hour,
		SkipPermissionCheck: false,
		DryRun:              false,
	}

	checker := NewChecker(logger, config)
	ctx := context.Background()

	results, err := checker.RunAllChecks(ctx)
	if err != nil {
		t.Errorf("RunAllChecks failed: %v", err)
	}

	// Should have 4 results: disk space, lock file, permissions, directories
	if len(results) < 4 {
		t.Errorf("Expected at least 4 check results, got %d", len(results))
	}

	// All checks should pass
	for _, result := range results {
		if !result.Passed {
			t.Errorf("Check '%s' failed: %s", result.Name, result.Message)
		}
	}

	// Clean up
	checker.ReleaseLock()
}

func TestRunAllChecksSkipPermissionCheck(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:          tmpDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		MinDiskPrimaryGB:    0.001,
		MinDiskSecondaryGB:  0,
		MinDiskCloudGB:      0,
		LockFilePath:        filepath.Join(tmpDir, ".backup.lock"),
		MaxLockAge:          1 * time.Hour,
		SkipPermissionCheck: true,
		DryRun:              false,
	}

	checker := NewChecker(logger, config)
	ctx := context.Background()

	results, err := checker.RunAllChecks(ctx)
	if err != nil {
		t.Fatalf("RunAllChecks (SkipPermissionCheck=true) failed unexpectedly: %v", err)
	}

	// Permissions check should be skipped: assert that no result is named "Permissions".
	for _, r := range results {
		if r.Name == "Permissions" {
			t.Fatalf("expected Permissions check to be skipped, but it was executed")
		}
	}
}

func TestDryRunMode(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := &CheckerConfig{
		BackupPath:          tmpDir,
		LogPath:             tmpDir,
		LockDirPath:         tmpDir,
		MinDiskPrimaryGB:    0.001,
		MinDiskSecondaryGB:  0,
		MinDiskCloudGB:      0,
		LockFilePath:        lockPath,
		MaxLockAge:          1 * time.Hour,
		SkipPermissionCheck: false,
		DryRun:              true, // Dry run mode
	}

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()

	// Check should pass
	if !result.Passed {
		t.Errorf("CheckLockFile in dry-run mode failed: %s", result.Message)
	}

	// Lock file should NOT be created in dry-run mode
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("Lock file should not be created in dry-run mode")
	}
}

func TestReleaseLock(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := &CheckerConfig{
		BackupPath:   tmpDir,
		LogPath:      tmpDir,
		LockDirPath:  tmpDir,
		LockFilePath: lockPath,
		MaxLockAge:   1 * time.Hour,
		DryRun:       false,
	}

	checker := NewChecker(logger, config)

	// Create lock
	checker.CheckLockFile()

	// Verify lock exists
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("Lock file should exist after CheckLockFile")
	}

	// Release lock
	if err := checker.ReleaseLock(); err != nil {
		t.Errorf("ReleaseLock failed: %v", err)
	}

	// Verify lock is removed
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("Lock file should be removed after ReleaseLock")
	}
}

func TestGetDefaultCheckerConfig(t *testing.T) {
	backupPath := "/test/backup"
	logPath := "/test/log"
	lockDir := "/test/lock"

	config := GetDefaultCheckerConfig(backupPath, logPath, lockDir)

	if config.BackupPath != backupPath {
		t.Errorf("Expected BackupPath %s, got %s", backupPath, config.BackupPath)
	}

	if config.LogPath != logPath {
		t.Errorf("Expected LogPath %s, got %s", logPath, config.LogPath)
	}
	if config.LockDirPath != lockDir {
		t.Errorf("Expected LockDirPath %s, got %s", lockDir, config.LockDirPath)
	}

	if config.MinDiskPrimaryGB != 10.0 {
		t.Errorf("Expected MinDiskPrimaryGB 10.0, got %.2f", config.MinDiskPrimaryGB)
	}
	if config.MinDiskSecondaryGB != 10.0 {
		t.Errorf("Expected MinDiskSecondaryGB 10.0, got %.2f", config.MinDiskSecondaryGB)
	}
	if config.MinDiskCloudGB != 10.0 {
		t.Errorf("Expected MinDiskCloudGB 10.0, got %.2f", config.MinDiskCloudGB)
	}

	if config.MaxLockAge != 2*time.Hour {
		t.Errorf("Expected MaxLockAge 2h, got %v", config.MaxLockAge)
	}

	expectedLockPath := filepath.Join(lockDir, ".backup.lock")
	if config.LockFilePath != expectedLockPath {
		t.Errorf("Expected LockFilePath %s, got %s", expectedLockPath, config.LockFilePath)
	}
}

func TestCheckDiskSpaceForEstimate(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()

	config := &CheckerConfig{
		BackupPath:         tmpDir,
		LogPath:            tmpDir,
		LockDirPath:        tmpDir,
		MinDiskPrimaryGB:   0,
		MinDiskSecondaryGB: 0,
		MinDiskCloudGB:     0,
		SafetyFactor:       1.5,
		MaxLockAge:         time.Hour,
	}

	checker := NewChecker(logger, config)

	result := checker.CheckDiskSpaceForEstimate(0.001)
	if !result.Passed {
		t.Errorf("Expected disk space estimate to pass, got: %s", result.Message)
	}

	config.MinDiskPrimaryGB = 999999
	result = checker.CheckDiskSpaceForEstimate(10_000)
	if result.Passed {
		t.Error("Expected disk space estimate to fail for huge size")
	}
}
