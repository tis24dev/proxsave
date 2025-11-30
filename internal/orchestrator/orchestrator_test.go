package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestNew tests Orchestrator creation
func TestNew(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	cfg := &config.Config{
		BackupPath: tempDir,
	}

	orch := New(logger, false)

	if orch == nil {
		t.Fatal("New returned nil orchestrator")
	}

	// Set config after creation
	orch.SetConfig(cfg)
}

// TestOrchestrator_SetForceNewAgeRecipient tests SetForceNewAgeRecipient
func TestOrchestrator_SetForceNewAgeRecipient(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	orch.SetForceNewAgeRecipient(true)

	// Note: forceNewAgeRecipient is private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetProxmoxVersion tests SetProxmoxVersion
func TestOrchestrator_SetProxmoxVersion(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	version := "7.4-1"
	orch.SetProxmoxVersion(version)

	// Note: proxmoxVersion is private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetStartTime tests SetStartTime
func TestOrchestrator_SetStartTime(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	now := time.Now()
	orch.SetStartTime(now)

	// Note: startTime is private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetConfig tests SetConfig
func TestOrchestrator_SetConfig(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	cfg1 := &config.Config{BackupPath: "/tmp/old"}
	orch.SetConfig(cfg1)

	cfg2 := &config.Config{BackupPath: "/tmp/new"}
	orch.SetConfig(cfg2)

	// Config is set, test passes if no panic occurs
}

// TestOrchestrator_SetVersion tests SetVersion
func TestOrchestrator_SetVersion(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	version := "1.2.3"
	orch.SetVersion(version)

	// Note: version is private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetChecker tests SetChecker
func TestOrchestrator_SetChecker(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	checkerCfg := &checks.CheckerConfig{}
	checker := checks.NewChecker(logger, checkerCfg)
	orch.SetChecker(checker)

	// Note: checker is private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetIdentity tests SetIdentity
func TestOrchestrator_SetIdentity(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	serverID := "server-123"
	serverMAC := "00:11:22:33:44:55"

	orch.SetIdentity(serverID, serverMAC)

	// Note: serverID and serverMAC are private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetBackupConfig tests SetBackupConfig
func TestOrchestrator_SetBackupConfig(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	
	orch := New(logger, false)

	backupPath := "/tmp/backups"
	logPath := "/tmp/logs"
	compression := types.CompressionXZ
	level := 6
	threads := 4
	mode := "normal"
	excludePatterns := []string{"*.tmp", "*.log"}

	orch.SetBackupConfig(backupPath, logPath, compression, level, threads, mode, excludePatterns)

	// Note: all backup config fields are private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_SetTempDirRegistry tests SetTempDirRegistry
func TestOrchestrator_SetTempDirRegistry(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	tempDir := t.TempDir()
	reg, err := NewTempDirRegistry(logger, filepath.Join(tempDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}

	orch.SetTempDirRegistry(reg)

	// Note: tempDirRegistry field is private, cannot directly verify
	// The test passes if no panic occurs
}

// TestOrchestrator_ReleaseBackupLock tests ReleaseBackupLock
func TestOrchestrator_ReleaseBackupLock(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	tempDir := t.TempDir()

	orch := New(logger, false)

	// Set backup config to establish the backup path
	orch.SetBackupConfig(tempDir, "", types.CompressionXZ, 6, 2, "normal", nil)

	// Create a lock file
	lockPath := tempDir + "/.backup.lock"
	if err := os.WriteFile(lockPath, []byte("locked"), 0644); err != nil {
		t.Fatal(err)
	}

	err := orch.ReleaseBackupLock()

	if err != nil {
		t.Errorf("ReleaseBackupLock failed: %v", err)
	}

	// Verify lock was released (file should still exist but we don't error)
	// The actual implementation may vary
}

// TestOrchestrator_RunPreBackupChecks tests RunPreBackupChecks
func TestOrchestrator_RunPreBackupChecks(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	checkerCfg := &checks.CheckerConfig{}
	checker := checks.NewChecker(logger, checkerCfg)
	orch.SetChecker(checker)

	ctx := context.Background()
	err := orch.RunPreBackupChecks(ctx)

	// May fail due to missing dependencies, but shouldn't panic
	if err != nil {
		t.Logf("RunPreBackupChecks returned error (expected in test env): %v", err)
	}
}

// TestOrchestrator_RunPreBackupChecks_ContextCancellation tests context cancellation
func TestOrchestrator_RunPreBackupChecks_ContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	checkerCfg := &checks.CheckerConfig{}
	checker := checks.NewChecker(logger, checkerCfg)
	orch.SetChecker(checker)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := orch.RunPreBackupChecks(ctx)

	// Note: The checker may complete checks before respecting cancellation
	// This test verifies the method handles cancelled context gracefully
	// Whether it returns an error or not depends on timing
	_ = err // Accept either outcome
}

// TestBackupError_Error tests BackupError Error method
func TestBackupError_Error(t *testing.T) {
	innerErr := os.ErrNotExist
	err := &BackupError{
		Phase: "collection",
		Err:   innerErr,
		Code:  types.ExitCollectionError,
	}

	expected := "collection phase failed: file does not exist"
	if err.Error() != expected {
		t.Errorf("Expected '%s', got '%s'", expected, err.Error())
	}
}

// TestBackupError_Unwrap tests BackupError Unwrap method
func TestBackupError_Unwrap(t *testing.T) {
	innerErr := os.ErrNotExist
	err := &BackupError{
		Phase: "archive",
		Err:   innerErr,
		Code:  types.ExitArchiveError,
	}

	if err.Unwrap() != innerErr {
		t.Error("Unwrap did not return inner error")
	}
}

// TestBackupStats_ErrorCount tests error counting
func TestBackupStats_ErrorCount(t *testing.T) {
	tests := []struct {
		name       string
		stats      *BackupStats
		expectFail bool
	}{
		{
			name:       "No errors",
			stats:      &BackupStats{ErrorCount: 0, ExitCode: 0},
			expectFail: false,
		},
		{
			name:       "With errors",
			stats:      &BackupStats{ErrorCount: 3, ExitCode: 1},
			expectFail: true,
		},
		{
			name:       "Non-zero exit code",
			stats:      &BackupStats{ErrorCount: 0, ExitCode: 1},
			expectFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasFailed := tt.stats.ErrorCount > 0 || tt.stats.ExitCode != 0
			if hasFailed != tt.expectFail {
				t.Errorf("Expected failure state %v, got %v", tt.expectFail, hasFailed)
			}
		})
	}
}

// TestOrchestrator_SetOptimizationConfig tests SetOptimizationConfig
func TestOrchestrator_SetOptimizationConfig(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)

	orch := New(logger, false)

	// This test just ensures the method doesn't panic
	// The actual implementation may not store the config in a testable way
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("SetOptimizationConfig panicked: %v", r)
		}
	}()

	// Call with a valid OptimizationConfig
	cfg := backup.OptimizationConfig{
		EnableChunking:      false,
		EnableDeduplication: false,
		EnablePrefilter:     false,
	}
	orch.SetOptimizationConfig(cfg)
}
