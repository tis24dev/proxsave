package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestNewBashExecutor(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	executor := NewBashExecutor(logger, "/test/path", false)

	if executor == nil {
		t.Fatal("NewBashExecutor should return non-nil executor")
	}

	if executor.scriptPath != "/test/path" {
		t.Errorf("scriptPath = %q; want %q", executor.scriptPath, "/test/path")
	}

	if executor.dryRun {
		t.Error("dryRun should be false")
	}
}

func TestBashExecutorExecuteScript(t *testing.T) {
	tmpDir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)

	// Create a simple test script
	scriptContent := `#!/bin/bash
echo "Hello from bash"
exit 0
`
	scriptPath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	executor := NewBashExecutor(logger, tmpDir, false)

	// Execute script
	output, err := executor.ExecuteScript("test.sh")
	if err != nil {
		t.Errorf("ExecuteScript failed: %v", err)
	}

	if !strings.Contains(output, "Hello from bash") {
		t.Errorf("Output should contain 'Hello from bash', got: %s", output)
	}
}

func TestBashExecutorExecuteScriptNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)

	executor := NewBashExecutor(logger, tmpDir, false)

	// Try to execute non-existent script
	_, err := executor.ExecuteScript("nonexistent.sh")
	if err == nil {
		t.Error("ExecuteScript should return error for non-existent script")
	}
}

func TestBashExecutorExecuteScriptStatError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	fakeFS := NewFakeFS()

	executor := NewBashExecutor(logger, "/scripts", false)
	executor.fs = fakeFS
	scriptPath := filepath.Join(executor.scriptPath, "fail.sh")
	fakeFS.StatErr[scriptPath] = os.ErrPermission
	onDisk := filepath.Join(fakeFS.Root, strings.TrimPrefix(scriptPath, string(filepath.Separator)))
	fakeFS.StatErr[onDisk] = os.ErrPermission

	if _, err := executor.fs.Stat(scriptPath); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("precondition failed: expected stat permission error, got %v", err)
	}

	_, err := executor.ExecuteScript("fail.sh")
	if err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected permission error, got %v", err)
	}
}

func TestBashExecutorExecuteScriptWithArgs(t *testing.T) {
	tmpDir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)

	// Create a script that echoes its arguments
	scriptContent := `#!/bin/bash
echo "Args: $1 $2"
exit 0
`
	scriptPath := filepath.Join(tmpDir, "args.sh")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	executor := NewBashExecutor(logger, tmpDir, false)

	// Execute script with arguments
	output, err := executor.ExecuteScript("args.sh", "hello", "world")
	if err != nil {
		t.Errorf("ExecuteScript failed: %v", err)
	}

	if !strings.Contains(output, "Args: hello world") {
		t.Errorf("Output should contain 'Args: hello world', got: %s", output)
	}
}

func TestBashExecutorExecuteScriptWithEnv(t *testing.T) {
	tmpDir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)

	// Create a script that uses environment variables
	scriptContent := `#!/bin/bash
echo "TEST_VAR=$TEST_VAR"
exit 0
`
	scriptPath := filepath.Join(tmpDir, "env.sh")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	executor := NewBashExecutor(logger, tmpDir, false)

	// Execute script with custom environment
	env := map[string]string{
		"TEST_VAR": "test_value",
	}
	output, err := executor.ExecuteScriptWithEnv("env.sh", env)
	if err != nil {
		t.Errorf("ExecuteScriptWithEnv failed: %v", err)
	}

	if !strings.Contains(output, "TEST_VAR=test_value") {
		t.Errorf("Output should contain 'TEST_VAR=test_value', got: %s", output)
	}
}

func TestBashExecutorDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)

	// Create a test script
	scriptContent := `#!/bin/bash
echo "This should not run"
exit 0
`
	scriptPath := filepath.Join(tmpDir, "dryrun.sh")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	executor := NewBashExecutor(logger, tmpDir, true) // dryRun = true

	// Execute script in dry-run mode
	output, err := executor.ExecuteScript("dryrun.sh")
	if err != nil {
		t.Errorf("ExecuteScript should not error in dry-run: %v", err)
	}

	if output != "[dry-run]" {
		t.Errorf("Output should be '[dry-run]', got: %s", output)
	}
}

func TestBashExecutorValidateScript(t *testing.T) {
	tmpDir := t.TempDir()
	logger := logging.New(types.LogLevelInfo, false)

	// Create a valid executable script
	scriptContent := `#!/bin/bash
exit 0
`
	scriptPath := filepath.Join(tmpDir, "valid.sh")
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to create test script: %v", err)
	}

	executor := NewBashExecutor(logger, tmpDir, false)

	// Validate existing executable script
	if err := executor.ValidateScript("valid.sh"); err != nil {
		t.Errorf("ValidateScript should not error for valid script: %v", err)
	}

	// Test non-existent script
	if err := executor.ValidateScript("nonexistent.sh"); err == nil {
		t.Error("ValidateScript should error for non-existent script")
	}

	// Create non-executable script
	nonExecPath := filepath.Join(tmpDir, "nonexec.sh")
	if err := os.WriteFile(nonExecPath, []byte(scriptContent), 0644); err != nil {
		t.Fatalf("Failed to create non-executable script: %v", err)
	}

	if err := executor.ValidateScript("nonexec.sh"); err == nil {
		t.Error("ValidateScript should error for non-executable script")
	}
}

func TestNewOrchestrator(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/test/path", false)

	if orch == nil {
		t.Fatal("New should return non-nil orchestrator")
	}

	if orch.bashExecutor == nil {
		t.Error("Orchestrator should have bashExecutor")
	}

	if orch.logger == nil {
		t.Error("Orchestrator should have logger")
	}

	if orch.dryRun {
		t.Error("dryRun should be false")
	}
}

func TestOrchestratorRunBackup(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/test/path", false)

	// Test PVE backup
	err := orch.RunBackup(context.Background(), types.ProxmoxVE)
	if err != nil {
		t.Errorf("RunBackup(ProxmoxVE) should not error (placeholder): %v", err)
	}

	// Test PBS backup
	err = orch.RunBackup(context.Background(), types.ProxmoxBS)
	if err != nil {
		t.Errorf("RunBackup(ProxmoxBS) should not error (placeholder): %v", err)
	}
}

func TestOrchestratorRunBackupDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/test/path", true) // dryRun = true

	err := orch.RunBackup(context.Background(), types.ProxmoxVE)
	if err != nil {
		t.Errorf("RunBackup should not error in dry-run: %v", err)
	}
}

func TestOrchestratorGetBashExecutor(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/test/path", false)

	executor := orch.GetBashExecutor()
	if executor == nil {
		t.Error("GetBashExecutor should return non-nil executor")
	}

	if executor != orch.bashExecutor {
		t.Error("GetBashExecutor should return the same instance")
	}
}

func TestSaveStatsReportDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/test/path", true)

	tempDir := t.TempDir()
	orch.SetBackupConfig(tempDir, tempDir, types.CompressionGzip, 6, 0, "standard", nil)

	now := time.Now()
	stats := &BackupStats{
		Hostname:                 "test-host",
		ProxmoxType:              types.ProxmoxVE,
		Timestamp:                now,
		StartTime:                now,
		EndTime:                  time.Now().Add(time.Second),
		Duration:                 time.Second,
		FilesCollected:           10,
		BytesCollected:           1024,
		ArchiveSize:              512,
		ArchivePath:              filepath.Join(tempDir, "archive.tar.gz"),
		RequestedCompression:     types.CompressionGzip,
		RequestedCompressionMode: "standard",
		Compression:              types.CompressionGzip,
		CompressionLevel:         6,
		CompressionMode:          "standard",
		CompressionThreads:       0,
	}

	if err := orch.SaveStatsReport(stats); err != nil {
		t.Fatalf("SaveStatsReport (dry-run) failed: %v", err)
	}

	if stats.ReportPath == "" {
		t.Error("ReportPath should be populated")
	}

	if _, err := os.Stat(stats.ReportPath); !os.IsNotExist(err) {
		t.Error("Dry-run should not create a report file on disk")
	}
}

func TestSaveStatsReportReal(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/test/path", false)

	tempDir := t.TempDir()
	orch.SetBackupConfig(tempDir, tempDir, types.CompressionXZ, 9, 0, "ultra", nil)

	now := time.Now()
	stats := &BackupStats{
		Hostname:                 "pbs-test",
		ProxmoxType:              types.ProxmoxBS,
		Timestamp:                now,
		StartTime:                now,
		EndTime:                  now.Add(2 * time.Second),
		Duration:                 2 * time.Second,
		FilesCollected:           5,
		BytesCollected:           2048,
		ArchiveSize:              1024,
		ArchivePath:              filepath.Join(tempDir, "archive.tar.xz"),
		RequestedCompression:     types.CompressionXZ,
		RequestedCompressionMode: "ultra",
		Compression:              types.CompressionXZ,
		CompressionLevel:         9,
		CompressionMode:          "ultra",
		CompressionThreads:       0,
	}

	if err := orch.SaveStatsReport(stats); err != nil {
		t.Fatalf("SaveStatsReport failed: %v", err)
	}

	if _, err := os.Stat(stats.ReportPath); err != nil {
		t.Fatalf("Expected stats report file to exist: %v", err)
	}
}

func TestNormalizeCompressionLevel(t *testing.T) {
	tests := []struct {
		comp     types.CompressionType
		input    int
		expected int
	}{
		{types.CompressionGzip, 0, 6},
		{types.CompressionGzip, 5, 5},
		{types.CompressionXZ, -1, 6},
		{types.CompressionXZ, 7, 7},
		{types.CompressionZstd, 30, 6},
		{types.CompressionZstd, 15, 15},
		{types.CompressionNone, 4, 0},
	}

	for _, tt := range tests {
		if got := normalizeCompressionLevel(tt.comp, tt.input); got != tt.expected {
			t.Errorf("normalizeCompressionLevel(%s, %d) = %d; want %d", tt.comp, tt.input, got, tt.expected)
		}
	}
}

type mockStorage struct {
	err error
}

func (m *mockStorage) Sync(ctx context.Context, stats *BackupStats) error {
	return m.err
}

type mockNotifier struct {
	err error
}

func (m *mockNotifier) Notify(ctx context.Context, stats *BackupStats) error {
	return m.err
}

func TestDispatchPostBackupNoTargets(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/tmp", false)

	if err := orch.dispatchPostBackup(context.Background(), &BackupStats{}); err != nil {
		t.Fatalf("dispatchPostBackup with no targets should not error: %v", err)
	}
}

func TestDispatchPostBackupStorageError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/tmp", false)
	orch.RegisterStorageTarget(&mockStorage{err: errors.New("storage failure")})

	err := orch.dispatchPostBackup(context.Background(), &BackupStats{})
	if err == nil {
		t.Fatal("expected error when storage target fails")
	}

	var backupErr *BackupError
	if !errors.As(err, &backupErr) {
		t.Fatalf("expected BackupError, got %T", err)
	}
	if backupErr.Phase != "storage" {
		t.Errorf("unexpected phase: %s", backupErr.Phase)
	}
	if backupErr.Code != types.ExitStorageError {
		t.Errorf("unexpected exit code: %d", backupErr.Code)
	}
}

func TestDispatchPostBackupNotificationError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, "/tmp", false)
	orch.RegisterNotificationChannel(&mockNotifier{err: errors.New("notify failure")})

	// Notifications are non-critical: errors should NOT abort backup
	err := orch.dispatchPostBackup(context.Background(), &BackupStats{})
	if err != nil {
		t.Fatalf("notification errors should not abort backup, got: %v", err)
	}
}
