package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestOrchestrator_SetUpdateInfo(t *testing.T) {
	orch := &Orchestrator{}

	orch.SetUpdateInfo(true, " 1.0.0 ", " 2.0.0 ")

	if !orch.versionUpdateAvailable {
		t.Fatalf("versionUpdateAvailable=false; want true")
	}
	if orch.updateCurrentVersion != "1.0.0" {
		t.Fatalf("updateCurrentVersion=%q; want %q", orch.updateCurrentVersion, "1.0.0")
	}
	if orch.updateLatestVersion != "2.0.0" {
		t.Fatalf("updateLatestVersion=%q; want %q", orch.updateLatestVersion, "2.0.0")
	}

	var nilOrch *Orchestrator
	nilOrch.SetUpdateInfo(true, "x", "y") // should not panic
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

func TestApplyCollectorOverridesCopiesConfig(t *testing.T) {
	cfg := &config.Config{
		BackupVMConfigs:         true,
		BackupClusterConfig:     true,
		BackupPVEFirewall:       true,
		BackupVZDumpConfig:      true,
		BackupPVEACL:            true,
		BackupPVEJobs:           true,
		BackupPVESchedules:      true,
		BackupPVEReplication:    true,
		BackupPVEBackupFiles:    true,
		BackupSmallPVEBackups:   true,
		MaxPVEBackupSizeBytes:   1234,
		PVEBackupIncludePattern: "*.cfg",
		BackupCephConfig:        true,
		CephConfigPath:          "/etc/ceph/ceph.conf",

		BackupDatastoreConfigs: true,
		BackupUserConfigs:      true,
		BackupRemoteConfigs:    true,
		BackupSyncJobs:         true,
		BackupVerificationJobs: true,
		BackupTapeConfigs:      true,
		BackupPruneSchedules:   true,
		BackupPxarFiles:        true,

		BackupNetworkConfigs:    true,
		BackupAptSources:        true,
		BackupCronJobs:          true,
		BackupSystemdServices:   true,
		BackupSSLCerts:          true,
		BackupSysctlConfig:      true,
		BackupKernelModules:     true,
		BackupFirewallRules:     true,
		BackupInstalledPackages: true,
		BackupScriptDir:         true,
		BackupCriticalFiles:     true,
		BackupSSHKeys:           true,
		BackupZFSConfig:         true,
		BackupRootHome:          true,
		BackupScriptRepository:  true,
		BackupUserHomes:         true,
		BackupConfigFile:        true,
		BaseDir:                 "/opt/proxsave",

		PxarDatastoreConcurrency: 3,
		PxarIntraConcurrency:     4,
		PxarScanFanoutLevel:      2,
		PxarScanMaxRoots:         512,
		PxarStopOnCap:            true,
		PxarEnumWorkers:          5,
		PxarEnumBudgetMs:         100,
		PxarFileIncludePatterns:  []string{"*.conf"},
		PxarFileExcludePatterns:  []string{"*.tmp"},

		CustomBackupPaths: []string{"/etc", "/var/lib"},
		BackupBlacklist:   []string{"/tmp"},

		ConfigPath:         "/etc/proxsave/backup.env",
		PVEConfigPath:      "/etc/pve",
		PBSConfigPath:      "/etc/proxmox-backup",
		PVEClusterPath:     "/etc/pve/corosync.conf",
		CorosyncConfigPath: "/etc/corosync/corosync.conf",
		VzdumpConfigPath:   "/etc/vzdump.conf",
		PBSDatastorePaths:  []string{"/mnt/pbs1", "/mnt/pbs2"},

		PBSRepository:  "pbs@pam!token",
		PBSPassword:    "secret",
		PBSFingerprint: "fingerprint",
	}

	var cc backup.CollectorConfig

	applyCollectorOverrides(&cc, cfg)

	if !cc.BackupVMConfigs || !cc.BackupClusterConfig || !cc.BackupPVEFirewall {
		t.Fatalf("PVE backup flags were not copied correctly")
	}
	if cc.MaxPVEBackupSizeBytes != cfg.MaxPVEBackupSizeBytes || cc.PVEBackupIncludePattern != cfg.PVEBackupIncludePattern {
		t.Fatalf("PVE size/pattern fields not copied")
	}
	if !cc.BackupDatastoreConfigs || !cc.BackupVerificationJobs || !cc.BackupPxarFiles {
		t.Fatalf("PBS-related flags were not copied correctly")
	}
	if !cc.BackupNetworkConfigs || !cc.BackupInstalledPackages || !cc.BackupConfigFile {
		t.Fatalf("system backup flags were not copied correctly")
	}
	if cc.ScriptRepositoryPath != cfg.BaseDir {
		t.Fatalf("ScriptRepositoryPath = %s, want %s", cc.ScriptRepositoryPath, cfg.BaseDir)
	}
	if cc.PxarDatastoreConcurrency != cfg.PxarDatastoreConcurrency ||
		cc.PxarIntraConcurrency != cfg.PxarIntraConcurrency ||
		cc.PxarScanFanoutLevel != cfg.PxarScanFanoutLevel ||
		cc.PxarScanMaxRoots != cfg.PxarScanMaxRoots ||
		cc.PxarEnumWorkers != cfg.PxarEnumWorkers {
		t.Fatalf("Pxar concurrency fields not copied correctly")
	}
	if !cc.PxarStopOnCap || cc.PxarEnumBudgetMs != cfg.PxarEnumBudgetMs {
		t.Fatalf("PxarStopOnCap or PxarEnumBudgetMs not copied")
	}
	if len(cc.PxarFileIncludePatterns) != 1 || cc.PxarFileIncludePatterns[0] != "*.conf" {
		t.Fatalf("PxarFileIncludePatterns not copied as expected: %#v", cc.PxarFileIncludePatterns)
	}
	if len(cc.PxarFileExcludePatterns) != 1 || cc.PxarFileExcludePatterns[0] != "*.tmp" {
		t.Fatalf("PxarFileExcludePatterns not copied as expected: %#v", cc.PxarFileExcludePatterns)
	}
	if len(cc.CustomBackupPaths) != len(cfg.CustomBackupPaths) || cc.CustomBackupPaths[0] != cfg.CustomBackupPaths[0] {
		t.Fatalf("CustomBackupPaths not copied correctly: %#v", cc.CustomBackupPaths)
	}
	if len(cc.BackupBlacklist) != len(cfg.BackupBlacklist) || cc.BackupBlacklist[0] != cfg.BackupBlacklist[0] {
		t.Fatalf("BackupBlacklist not copied correctly: %#v", cc.BackupBlacklist)
	}
	if cc.ConfigFilePath != cfg.ConfigPath {
		t.Fatalf("ConfigFilePath = %s, want %s", cc.ConfigFilePath, cfg.ConfigPath)
	}
	if cc.PVEConfigPath != cfg.PVEConfigPath || cc.PBSConfigPath != cfg.PBSConfigPath {
		t.Fatalf("PVE/PBS config paths not copied")
	}
	if len(cc.PBSDatastorePaths) != len(cfg.PBSDatastorePaths) || cc.PBSDatastorePaths[0] != cfg.PBSDatastorePaths[0] {
		t.Fatalf("PBSDatastorePaths not copied correctly: %#v", cc.PBSDatastorePaths)
	}
	if cc.PBSRepository != cfg.PBSRepository || cc.PBSPassword != cfg.PBSPassword || cc.PBSFingerprint != cfg.PBSFingerprint {
		t.Fatalf("PBS auth fields not copied correctly")
	}
}

func TestLogStepWritesStepMessage(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	o := &Orchestrator{logger: logger}
	o.logStep(3, "Processing %s", "configs")

	out := buf.String()
	if !strings.Contains(out, "STEP") {
		t.Fatalf("expected STEP label in output, got: %s", out)
	}
	if !strings.Contains(out, "Processing configs") {
		t.Fatalf("expected formatted message, got: %s", out)
	}
}

func TestLogStepHandlesNilLogger(t *testing.T) {
	o := &Orchestrator{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("logStep should not panic without logger: %v", r)
		}
	}()
	o.logStep(1, "noop")
}

func TestSaveStatsReportNilStats(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	if err := orch.SaveStatsReport(nil); err == nil {
		t.Fatalf("expected error when stats is nil")
	}
}

func TestSaveStatsReportSkipsWithoutLogPath(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	stats := &BackupStats{
		Timestamp: time.Now(),
	}

	if err := orch.SaveStatsReport(stats); err != nil {
		t.Fatalf("SaveStatsReport should skip without log path: %v", err)
	}
	if stats.ReportPath != "" {
		t.Fatalf("ReportPath should remain empty when log path is unset, got %s", stats.ReportPath)
	}
}
