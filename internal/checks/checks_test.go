package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestShouldSkipPermissionCheck(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &CheckerConfig{SkipPermissionCheck: true}
	checker := NewChecker(logger, cfg)
	if !checker.ShouldSkipPermissionCheck() {
		t.Fatalf("ShouldSkipPermissionCheck = false; want true")
	}

	cfg.SkipPermissionCheck = false
	if checker.ShouldSkipPermissionCheck() {
		t.Fatalf("ShouldSkipPermissionCheck = true; want false")
	}
}

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

func TestCheckLockFile_WritesExpectedContent(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := &CheckerConfig{
		BackupPath:   tmpDir,
		LogPath:      tmpDir,
		LockDirPath:  tmpDir,
		LockFilePath: lockPath,
		MaxLockAge:   time.Hour,
		DryRun:       false,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if !result.Passed {
		t.Fatalf("CheckLockFile failed: %s", result.Message)
	}
	t.Cleanup(func() { _ = checker.ReleaseLock() })

	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, fmt.Sprintf("pid=%d\n", os.Getpid())) {
		t.Fatalf("lock file content missing pid: %q", text)
	}
	if !strings.Contains(text, "host=") {
		t.Fatalf("lock file content missing host: %q", text)
	}
	if !strings.Contains(text, "time=") {
		t.Fatalf("lock file content missing time: %q", text)
	}

	var ts string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "time=") {
			ts = strings.TrimPrefix(line, "time=")
			break
		}
	}
	if ts == "" {
		t.Fatalf("lock file content missing time value: %q", text)
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("lock file time not RFC3339: %q: %v", ts, err)
	}
}

func TestCheckLockFile_ConcurrentCalls_OnlyOnePasses(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	baseConfig := &CheckerConfig{
		BackupPath:   tmpDir,
		LogPath:      tmpDir,
		LockDirPath:  tmpDir,
		LockFilePath: lockPath,
		MaxLockAge:   time.Hour,
		DryRun:       false,
	}

	const workers = 20
	start := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(workers)

	passed := make(chan bool, workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			checker := NewChecker(logger, baseConfig)
			res := checker.CheckLockFile()
			passed <- res.Passed
		}()
	}

	close(start)
	wg.Wait()
	close(passed)

	passedCount := 0
	for ok := range passed {
		if ok {
			passedCount++
		}
	}

	if passedCount != 1 {
		t.Fatalf("expected exactly 1 successful lock acquisition, got %d", passedCount)
	}

	// Best-effort cleanup (ignore errors).
	_ = os.Remove(lockPath)
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

func TestCheckerConfigValidate(t *testing.T) {
	t.Run("defaults LockDirPath to BackupPath", func(t *testing.T) {
		cfg := &CheckerConfig{
			BackupPath:       "/tmp/backups",
			LogPath:          "/tmp/logs",
			MaxLockAge:       time.Minute,
			SafetyFactor:     1.0,
			MinDiskPrimaryGB: 0,
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if cfg.LockDirPath != cfg.BackupPath {
			t.Fatalf("LockDirPath = %q; want %q", cfg.LockDirPath, cfg.BackupPath)
		}
	})

	tests := []struct {
		name string
		cfg  *CheckerConfig
		want string
	}{
		{"missing backup path", &CheckerConfig{LogPath: "x", MaxLockAge: time.Minute, SafetyFactor: 1.0}, "backup path cannot be empty"},
		{"missing log path", &CheckerConfig{BackupPath: "x", MaxLockAge: time.Minute, SafetyFactor: 1.0}, "log path cannot be empty"},
		{"negative primary min", &CheckerConfig{BackupPath: "x", LogPath: "x", MinDiskPrimaryGB: -1, MaxLockAge: time.Minute, SafetyFactor: 1.0}, "primary minimum disk space cannot be negative"},
		{"negative secondary min", &CheckerConfig{BackupPath: "x", LogPath: "x", MinDiskSecondaryGB: -1, MaxLockAge: time.Minute, SafetyFactor: 1.0}, "secondary minimum disk space cannot be negative"},
		{"negative cloud min", &CheckerConfig{BackupPath: "x", LogPath: "x", MinDiskCloudGB: -1, MaxLockAge: time.Minute, SafetyFactor: 1.0}, "cloud minimum disk space cannot be negative"},
		{"invalid safety factor", &CheckerConfig{BackupPath: "x", LogPath: "x", MaxLockAge: time.Minute, SafetyFactor: 0.5}, "safety factor must be >="},
		{"invalid max lock age", &CheckerConfig{BackupPath: "x", LogPath: "x", MaxLockAge: 0, SafetyFactor: 1.0}, "max lock age must be positive"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q; want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestCheckerDisableCloud(t *testing.T) {
	var nilChecker *Checker
	nilChecker.DisableCloud() // should be a no-op

	checker := &Checker{}
	checker.DisableCloud() // should be a no-op

	cfg := &CheckerConfig{
		CloudEnabled: true,
		CloudPath:    "/tmp/cloud",
	}
	checker = &Checker{config: cfg}
	checker.DisableCloud()

	if cfg.CloudEnabled {
		t.Fatalf("CloudEnabled = true; want false")
	}
	if cfg.CloudPath != "" {
		t.Fatalf("CloudPath = %q; want empty", cfg.CloudPath)
	}
}

func TestCheckDiskSpace_WarnsOnNonCriticalDestinations(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := &CheckerConfig{
		BackupPath:         tmpDir,
		LogPath:            tmpDir,
		LockDirPath:        tmpDir,
		SecondaryEnabled:   true,
		SecondaryPath:      tmpDir,
		CloudEnabled:       false,
		MinDiskPrimaryGB:   0.001,
		MinDiskSecondaryGB: 999999.0, // Force a non-critical warning
		MinDiskCloudGB:     0,
		SafetyFactor:       1.0,
		MaxLockAge:         time.Minute,
	}

	checker := NewChecker(logger, config)
	result := checker.CheckDiskSpace()

	if !result.Passed {
		t.Fatalf("CheckDiskSpace should pass with warnings, got: %s", result.Message)
	}
	if !strings.Contains(strings.ToLower(result.Message), "warning") {
		t.Fatalf("expected warning message, got: %q", result.Message)
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

func TestCheckTempDirectory_Success(t *testing.T) {
	// Ensure /tmp/proxsave exists for the test
	tempRoot := filepath.Join("/tmp", "proxsave")
	os.MkdirAll(tempRoot, 0o755)

	config := GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir())
	logger := logging.New(types.LogLevelDebug, false)
	checker := NewChecker(logger, config)

	result := checker.CheckTempDirectory()
	if !result.Passed {
		t.Errorf("Expected temp directory check to pass, got: %s", result.Message)
	}
	if !strings.Contains(result.Message, "writable with symlink support") {
		t.Errorf("Expected success message with symlink support, got: %s", result.Message)
	}
}

func TestCheckTempDirectory_NotDirectory(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	origTempRoot := tempRootPath
	t.Cleanup(func() { tempRootPath = origTempRoot })

	tmpDir := t.TempDir()
	notDir := filepath.Join(tmpDir, "tempRoot")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tempRootPath = notDir

	config := GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir())
	checker := NewChecker(logger, config)
	result := checker.CheckTempDirectory()
	if result.Passed || result.Code != "NOT_DIRECTORY" {
		t.Fatalf("expected NOT_DIRECTORY, got passed=%v code=%q err=%v", result.Passed, result.Code, result.Error)
	}
}

func TestCheckTempDirectory_NotWritable(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	origTempRoot := tempRootPath
	origWrite := osWriteFile
	t.Cleanup(func() {
		tempRootPath = origTempRoot
		osWriteFile = origWrite
	})

	tempRootPath = t.TempDir()
	osWriteFile = func(name string, data []byte, perm os.FileMode) error { return syscall.EACCES }

	config := GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir())
	checker := NewChecker(logger, config)
	result := checker.CheckTempDirectory()
	if result.Passed || result.Code != "NOT_WRITABLE" {
		t.Fatalf("expected NOT_WRITABLE, got passed=%v code=%q err=%v", result.Passed, result.Code, result.Error)
	}
}

func TestCheckTempDirectory_SymlinkSupport(t *testing.T) {
	// Verify that the temp directory check includes symlink validation
	tempRoot := filepath.Join("/tmp", "proxsave")
	os.MkdirAll(tempRoot, 0o755)

	config := GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir())
	logger := logging.New(types.LogLevelDebug, false)
	checker := NewChecker(logger, config)

	result := checker.CheckTempDirectory()
	if !result.Passed {
		t.Errorf("Expected temp directory check to pass, got: %s", result.Message)
	}

	// Verify test files are cleaned up
	testFile := filepath.Join(tempRoot, ".proxsave-permission-test")
	testSymlink := filepath.Join(tempRoot, ".proxsave-symlink-test")
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Errorf("expected test file to be cleaned up")
	}
	if _, err := os.Lstat(testSymlink); !os.IsNotExist(err) {
		t.Errorf("expected test symlink to be cleaned up")
	}
}

func TestRunAllChecks_IncludesTempDirectory(t *testing.T) {
	// Ensure /tmp/proxsave exists
	os.MkdirAll(filepath.Join("/tmp", "proxsave"), 0o755)

	backupPath := t.TempDir()
	logPath := t.TempDir()
	lockDir := t.TempDir()

	config := GetDefaultCheckerConfig(backupPath, logPath, lockDir)
	config.MinDiskPrimaryGB = 0.001 // Lower disk space requirement for test
	logger := logging.New(types.LogLevelDebug, false)
	checker := NewChecker(logger, config)

	results, err := checker.RunAllChecks(context.Background())
	if err != nil {
		t.Fatalf("RunAllChecks failed: %v", err)
	}

	// Verify Temp Directory check is included in results
	foundTempDir := false
	for _, r := range results {
		if r.Name == "Temp Directory" {
			foundTempDir = true
			if !r.Passed {
				t.Errorf("Temp Directory check failed: %s", r.Message)
			}
			break
		}
	}
	if !foundTempDir {
		t.Error("Temp Directory check not found in RunAllChecks results")
	}
}

func TestRunAllChecks_FailsOnDirectories(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	backupFile := filepath.Join(tmpDir, "backup-as-file")
	if err := os.WriteFile(backupFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	config := GetDefaultCheckerConfig(backupFile, tmpDir, tmpDir)
	config.MinDiskPrimaryGB = 0
	config.SkipPermissionCheck = true
	config.LockFilePath = filepath.Join(tmpDir, ".backup.lock")
	config.MaxLockAge = time.Minute

	checker := NewChecker(logger, config)
	_, err := checker.RunAllChecks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "directory check failed") {
		t.Fatalf("expected directory check failed error, got: %v", err)
	}
}

func TestRunAllChecks_FailsOnTempDirectory(t *testing.T) {
	origTempRoot := tempRootPath
	t.Cleanup(func() { tempRootPath = origTempRoot })

	tmpDir := t.TempDir()
	notDir := filepath.Join(tmpDir, "tempRoot")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tempRootPath = notDir

	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	config := GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir())
	config.MinDiskPrimaryGB = 0
	config.SkipPermissionCheck = true
	config.LockFilePath = filepath.Join(config.LockDirPath, ".backup.lock")
	config.MaxLockAge = time.Minute

	checker := NewChecker(logger, config)
	_, err := checker.RunAllChecks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "temp directory check failed") {
		t.Fatalf("expected temp directory check failed error, got: %v", err)
	}
}

func TestRunAllChecks_FailsOnDiskSpace(t *testing.T) {
	origTempRoot := tempRootPath
	t.Cleanup(func() { tempRootPath = origTempRoot })
	tempRootPath = t.TempDir()

	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.MinDiskPrimaryGB = 999999.0
	config.SkipPermissionCheck = true
	config.LockFilePath = filepath.Join(tmpDir, ".backup.lock")
	config.MaxLockAge = time.Minute

	checker := NewChecker(logger, config)
	_, err := checker.RunAllChecks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "disk space check failed") {
		t.Fatalf("expected disk space check failed error, got: %v", err)
	}
}

func TestRunAllChecks_FailsOnPermissions(t *testing.T) {
	origTempRoot := tempRootPath
	t.Cleanup(func() { tempRootPath = origTempRoot })
	tempRootPath = t.TempDir()

	origCreate := createTestFile
	t.Cleanup(func() { createTestFile = origCreate })
	createTestFile = func(name string) (*os.File, error) {
		return nil, os.ErrPermission
	}

	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.MinDiskPrimaryGB = 0
	config.SkipPermissionCheck = false
	config.LockFilePath = filepath.Join(tmpDir, ".backup.lock")
	config.MaxLockAge = time.Minute

	checker := NewChecker(logger, config)
	_, err := checker.RunAllChecks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permissions check failed") {
		t.Fatalf("expected permissions check failed error, got: %v", err)
	}
}

func TestRunAllChecks_FailsOnLockFile(t *testing.T) {
	origTempRoot := tempRootPath
	t.Cleanup(func() { tempRootPath = origTempRoot })
	tempRootPath = t.TempDir()

	origOpen := osOpenFile
	t.Cleanup(func() { osOpenFile = origOpen })
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EEXIST}
	}

	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.MinDiskPrimaryGB = 0
	config.SkipPermissionCheck = true
	config.LockFilePath = filepath.Join(tmpDir, ".backup.lock")
	config.MaxLockAge = time.Minute

	checker := NewChecker(logger, config)
	_, err := checker.RunAllChecks(context.Background())
	if err == nil || !strings.Contains(err.Error(), "lock file check failed") {
		t.Fatalf("expected lock file check failed error, got: %v", err)
	}
}

func TestCheckLockFile_StatFailsAfterExistenceCheck(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")
	if err := os.WriteFile(lockPath, []byte("lock"), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.LockFilePath = lockPath
	config.MaxLockAge = time.Hour

	origStat := osStat
	t.Cleanup(func() { osStat = origStat })
	calls := 0
	osStat = func(name string) (os.FileInfo, error) {
		if name == lockPath {
			calls++
			if calls == 2 {
				return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.EIO}
			}
		}
		return origStat(name)
	}

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if result.Passed {
		t.Fatalf("expected CheckLockFile to fail, got passed")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "failed to stat lock file") {
		t.Fatalf("expected stat lock file error, got: %v", result.Error)
	}
}

func TestCheckLockFile_RemoveStaleLockFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")
	if err := os.WriteFile(lockPath, []byte("lock"), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
	oldTime := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.LockFilePath = lockPath
	config.MaxLockAge = time.Hour

	origRemove := osRemove
	t.Cleanup(func() { osRemove = origRemove })
	osRemove = func(name string) error {
		if name == lockPath {
			return syscall.EPERM
		}
		return origRemove(name)
	}

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if result.Passed {
		t.Fatalf("expected CheckLockFile to fail, got passed")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "failed to remove stale lock") {
		t.Fatalf("expected remove stale lock error, got: %v", result.Error)
	}
}

func TestCheckLockFile_WriteFails(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skipf("/dev/full not available: %v", err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.LockFilePath = lockPath
	config.MaxLockAge = time.Hour

	origOpen := osOpenFile
	t.Cleanup(func() { osOpenFile = origOpen })
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return os.OpenFile("/dev/full", os.O_WRONLY, 0)
	}

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if result.Passed {
		t.Fatalf("expected CheckLockFile to fail, got passed")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "failed to write lock file") {
		t.Fatalf("expected write lock file error, got: %v", result.Error)
	}
}

func TestCheckLockFile_SyncWarningDoesNotFail(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.LockFilePath = lockPath
	config.MaxLockAge = time.Hour

	origSync := syncFile
	t.Cleanup(func() { syncFile = origSync })
	syncFile = func(f *os.File) error { return errors.New("sync failed") }

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if !result.Passed {
		t.Fatalf("expected CheckLockFile to pass despite sync failure, got: %v", result.Error)
	}
}

func TestCheckLockFile_DefaultLockPath_DryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.LockFilePath = ""
	config.LockDirPath = tmpDir
	config.MaxLockAge = time.Hour
	config.DryRun = true

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if !result.Passed {
		t.Fatalf("expected CheckLockFile to pass in dry-run, got: %v", result.Error)
	}

	// Ensure the default lock path wasn't created in dry-run.
	if _, err := os.Stat(filepath.Join(tmpDir, ".backup.lock")); !os.IsNotExist(err) {
		t.Fatalf("expected no lock file to be created in dry-run")
	}
}

func TestCheckLockFile_CreateFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")

	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	config.LockFilePath = lockPath
	config.MaxLockAge = time.Hour
	config.DryRun = false

	origOpen := osOpenFile
	t.Cleanup(func() { osOpenFile = origOpen })
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, syscall.EPERM
	}

	checker := NewChecker(logger, config)
	result := checker.CheckLockFile()
	if result.Passed {
		t.Fatalf("expected CheckLockFile to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "failed to create lock file") {
		t.Fatalf("expected create lock file error, got: %v", result.Error)
	}
}

func TestCheckPermissions_ReadOnlyFilesystemCode(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)

	origCreate := createTestFile
	t.Cleanup(func() { createTestFile = origCreate })
	createTestFile = func(name string) (*os.File, error) {
		return nil, syscall.EROFS
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()
	if result.Passed {
		t.Fatalf("expected CheckPermissions to fail")
	}
	if result.Code != "FS_READONLY" {
		t.Fatalf("Code=%q; want %q", result.Code, "FS_READONLY")
	}
}

func TestCheckPermissions_DefaultFailureCode(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)

	origCreate := createTestFile
	t.Cleanup(func() { createTestFile = origCreate })
	createTestFile = func(name string) (*os.File, error) {
		return nil, errors.New("boom")
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()
	if result.Passed {
		t.Fatalf("expected CheckPermissions to fail")
	}
	if result.Code != "PERMISSION_CHECK_FAILED" {
		t.Fatalf("Code=%q; want %q", result.Code, "PERMISSION_CHECK_FAILED")
	}
}

func TestCheckPermissions_DoesNotRetryOnPermissionDenied(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)

	origCreate := createTestFile
	t.Cleanup(func() { createTestFile = origCreate })

	attempts := 0
	createTestFile = func(name string) (*os.File, error) {
		attempts++
		return nil, os.ErrPermission
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()
	if result.Passed {
		t.Fatalf("expected CheckPermissions to fail")
	}
	if result.Code != "PERMISSION_DENIED" {
		t.Fatalf("Code=%q; want %q", result.Code, "PERMISSION_DENIED")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt (no retry on permission denied), got %d", attempts)
	}
}

func TestCheckPermissions_RetriesOnEIOThenSucceeds(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	backupDir := t.TempDir()
	logDir := t.TempDir()
	config := GetDefaultCheckerConfig(backupDir, logDir, t.TempDir())

	origCreate := createTestFile
	t.Cleanup(func() { createTestFile = origCreate })

	attempts := 0
	eioAttempts := 0
	createTestFile = func(name string) (*os.File, error) {
		attempts++
		if strings.HasPrefix(name, backupDir+string(os.PathSeparator)) && eioAttempts < 2 {
			eioAttempts++
			return nil, syscall.EIO
		}
		return os.Create(name)
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()
	if !result.Passed {
		t.Fatalf("expected CheckPermissions to pass after retries, got: %v", result.Error)
	}
	if eioAttempts != 2 {
		t.Fatalf("expected 2 EIO attempts, got %d", eioAttempts)
	}
	if attempts != 4 {
		t.Fatalf("expected 4 attempts total (3 for backup dir, 1 for log dir), got %d", attempts)
	}
}

func TestCheckPermissions_RemoveTestFileWarning(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	config := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)

	origRemove := osRemove
	t.Cleanup(func() { osRemove = origRemove })
	osRemove = func(name string) error {
		if strings.Contains(filepath.Base(name), ".permission_test_") {
			return syscall.EPERM
		}
		return origRemove(name)
	}

	checker := NewChecker(logger, config)
	result := checker.CheckPermissions()
	if !result.Passed {
		t.Fatalf("expected CheckPermissions to pass, got: %v", result.Error)
	}
}

func TestCheckDirectories_SkipRootAndDotPaths(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	cfg := &CheckerConfig{
		BackupPath:   "/",
		LogPath:      ".",
		LockDirPath:  "",
		LockFilePath: "",
		DryRun:       false,
	}

	checker := NewChecker(logger, cfg)
	result := checker.CheckDirectories()
	if !result.Passed {
		t.Fatalf("expected CheckDirectories to pass, got: %v", result.Error)
	}
}

func TestCheckDirectories_CreatesLockFileParentDir(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockDir := filepath.Join(tmpDir, "locks", "nested")
	lockPath := filepath.Join(lockDir, ".backup.lock")

	cfg := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	cfg.LockFilePath = lockPath
	cfg.DryRun = false

	checker := NewChecker(logger, cfg)
	result := checker.CheckDirectories()
	if !result.Passed {
		t.Fatalf("expected CheckDirectories to pass, got: %v", result.Error)
	}

	if _, err := os.Stat(lockDir); err != nil {
		t.Fatalf("expected lock parent dir to exist, stat failed: %v", err)
	}
}

func TestCheckDirectories_StatNonExistenceError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	cfg := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	cfg.BackupPath = filepath.Join(tmpDir, "weird")

	origStat := osStat
	t.Cleanup(func() { osStat = origStat })
	osStat = func(name string) (os.FileInfo, error) {
		if name == cfg.BackupPath {
			return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.EIO}
		}
		return origStat(name)
	}

	checker := NewChecker(logger, cfg)
	result := checker.CheckDirectories()
	if result.Passed {
		t.Fatalf("expected CheckDirectories to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "failed to stat directory") {
		t.Fatalf("expected stat directory error, got: %v", result.Error)
	}
}

func TestCheckDirectories_MkdirAllFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	missing := filepath.Join(tmpDir, "missing")
	cfg := GetDefaultCheckerConfig(missing, tmpDir, tmpDir)

	origMkdirAll := osMkdirAll
	t.Cleanup(func() { osMkdirAll = origMkdirAll })
	osMkdirAll = func(path string, perm os.FileMode) error {
		return syscall.EPERM
	}

	checker := NewChecker(logger, cfg)
	result := checker.CheckDirectories()
	if result.Passed {
		t.Fatalf("expected CheckDirectories to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "failed to create directory") {
		t.Fatalf("expected mkdir error, got: %v", result.Error)
	}
}

func TestCheckTempDirectory_StatFailed(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	origTempRoot := tempRootPath
	origStat := osStat
	t.Cleanup(func() {
		tempRootPath = origTempRoot
		osStat = origStat
	})

	tempRootPath = filepath.Join(t.TempDir(), "temp")
	osStat = func(name string) (os.FileInfo, error) {
		if name == tempRootPath {
			return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.EIO}
		}
		return origStat(name)
	}

	checker := NewChecker(logger, GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir()))
	result := checker.CheckTempDirectory()
	if result.Passed || result.Code != "STAT_FAILED" {
		t.Fatalf("expected STAT_FAILED, got passed=%v code=%q err=%v", result.Passed, result.Code, result.Error)
	}
}

func TestCheckTempDirectory_CreateFailed(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	origTempRoot := tempRootPath
	origStat := osStat
	origMkdirAll := osMkdirAll
	t.Cleanup(func() {
		tempRootPath = origTempRoot
		osStat = origStat
		osMkdirAll = origMkdirAll
	})

	tempRootPath = filepath.Join(t.TempDir(), "temp")
	osStat = func(name string) (os.FileInfo, error) {
		if name == tempRootPath {
			return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.ENOENT}
		}
		return origStat(name)
	}
	osMkdirAll = func(path string, perm os.FileMode) error { return syscall.EACCES }

	checker := NewChecker(logger, GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir()))
	result := checker.CheckTempDirectory()
	if result.Passed || result.Code != "CREATE_FAILED" {
		t.Fatalf("expected CREATE_FAILED, got passed=%v code=%q err=%v", result.Passed, result.Code, result.Error)
	}
}

func TestCheckTempDirectory_VerifyFailed(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	origTempRoot := tempRootPath
	origStat := osStat
	t.Cleanup(func() {
		tempRootPath = origTempRoot
		osStat = origStat
	})

	tempRootPath = filepath.Join(t.TempDir(), "temp")
	calls := 0
	osStat = func(name string) (os.FileInfo, error) {
		if name == tempRootPath {
			calls++
			if calls == 1 {
				return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.ENOENT}
			}
			return nil, &os.PathError{Op: "stat", Path: name, Err: syscall.EIO}
		}
		return origStat(name)
	}

	checker := NewChecker(logger, GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir()))
	result := checker.CheckTempDirectory()
	if result.Passed || result.Code != "VERIFY_FAILED" {
		t.Fatalf("expected VERIFY_FAILED, got passed=%v code=%q err=%v", result.Passed, result.Code, result.Error)
	}
}

func TestCheckTempDirectory_NoSymlinkSupport(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	origTempRoot := tempRootPath
	origSymlink := osSymlink
	t.Cleanup(func() {
		tempRootPath = origTempRoot
		osSymlink = origSymlink
	})

	tempRootPath = t.TempDir()
	osSymlink = func(oldname, newname string) error { return syscall.EPERM }

	checker := NewChecker(logger, GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir()))
	result := checker.CheckTempDirectory()
	if result.Passed || result.Code != "NO_SYMLINK_SUPPORT" {
		t.Fatalf("expected NO_SYMLINK_SUPPORT, got passed=%v code=%q err=%v", result.Passed, result.Code, result.Error)
	}
}

func TestReleaseLock_DryRunUsesDefaultPath(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	cfg := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	cfg.LockFilePath = ""
	cfg.DryRun = true

	checker := NewChecker(logger, cfg)
	if err := checker.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock dry-run returned error: %v", err)
	}
}

func TestReleaseLock_RemoveFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")
	if err := os.WriteFile(lockPath, []byte("lock"), 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	cfg := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	cfg.LockFilePath = lockPath

	origRemove := osRemove
	t.Cleanup(func() { osRemove = origRemove })
	osRemove = func(name string) error { return syscall.EPERM }

	checker := NewChecker(logger, cfg)
	err := checker.ReleaseLock()
	if err == nil || !strings.Contains(err.Error(), "failed to release lock") {
		t.Fatalf("expected failed to release lock error, got: %v", err)
	}
}

func TestCheckDiskSpaceForEstimate_PrimaryStatfsFails(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCheckerConfig("/nonexistent/path", "/nonexistent/log", "/nonexistent/lock")
	cfg.MinDiskPrimaryGB = 0.001
	cfg.SafetyFactor = 1.0
	cfg.MaxLockAge = time.Minute

	checker := NewChecker(logger, cfg)
	result := checker.CheckDiskSpaceForEstimate(0.1)
	if result.Passed {
		t.Fatalf("expected failure, got passed")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "disk space check failed") {
		t.Fatalf("expected disk space check failed error, got: %v", result.Error)
	}
}

func TestCheckDiskSpaceForEstimate_WarnsOnNonCriticalErrorsAndInsufficientSpace(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	tmpDir := t.TempDir()
	cfg := GetDefaultCheckerConfig(tmpDir, tmpDir, tmpDir)
	cfg.MinDiskPrimaryGB = 0.001
	cfg.SafetyFactor = 1.0
	cfg.MaxLockAge = time.Minute
	cfg.SecondaryEnabled = true
	cfg.SecondaryPath = "/nonexistent/secondary"
	cfg.MinDiskSecondaryGB = 0.001

	checker := NewChecker(logger, cfg)
	result := checker.CheckDiskSpaceForEstimate(0.1)
	if !result.Passed {
		t.Fatalf("expected pass with warnings, got: %v", result.Error)
	}

	cfg.SecondaryPath = tmpDir
	cfg.MinDiskSecondaryGB = 999999999.0
	result = checker.CheckDiskSpaceForEstimate(0.1)
	if !result.Passed {
		t.Fatalf("expected pass with warnings, got: %v", result.Error)
	}
}

func TestDiskSpaceGB_ErrorsOnMissingPath(t *testing.T) {
	if _, err := diskSpaceGB("/nonexistent/path"); err == nil {
		t.Fatalf("expected diskSpaceGB to error on missing path")
	}
}

func TestCheckSingleDisk_ErrorsOnMissingPath(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCheckerConfig(t.TempDir(), t.TempDir(), t.TempDir())
	checker := NewChecker(logger, cfg)
	if err := checker.checkSingleDisk("Primary", "/nonexistent/path", 0.1); err == nil {
		t.Fatalf("expected checkSingleDisk to error on missing path")
	}
}
