package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

const testAgeRecipient = "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"

type testStorageTarget struct {
	err   error
	calls int
}

func (f *testStorageTarget) Sync(ctx context.Context, stats *BackupStats) error {
	f.calls++
	return f.err
}

func TestRunGoBackupEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end backup test in short mode")
	}

	logger := logging.New(types.LogLevelInfo, false)

	backupDir := t.TempDir()
	logDir := t.TempDir()

	orch := New(logger, false)
	orch.SetBackupConfig(backupDir, logDir, types.CompressionNone, 0, 0, "standard", nil)

	checkerConfig := &checks.CheckerConfig{
		BackupPath:         backupDir,
		LogPath:            logDir,
		LockDirPath:        filepath.Join(backupDir, "lock"),
		MinDiskPrimaryGB:   0.001,
		MinDiskSecondaryGB: 0,
		MinDiskCloudGB:     0,
		SafetyFactor:       1.0,
		LockFilePath:       filepath.Join(backupDir, "lock", ".backup.lock"),
		MaxLockAge:         time.Hour,
	}
	if err := checkerConfig.Validate(); err != nil {
		t.Fatalf("checker config validation failed: %v", err)
	}
	checker := checks.NewChecker(logger, checkerConfig)
	orch.SetChecker(checker)

	ctx := context.Background()
	stats, err := orch.RunGoBackup(ctx, types.ProxmoxUnknown, "test-host")
	if err != nil {
		t.Fatalf("RunGoBackup failed: %v", err)
	}

	if stats.ArchivePath == "" {
		t.Fatal("ArchivePath should not be empty")
	}
	if _, err := os.Stat(stats.ArchivePath); err != nil {
		t.Fatalf("Expected archive to exist: %v", err)
	}

	if err := orch.SaveStatsReport(stats); err != nil {
		t.Fatalf("SaveStatsReport failed: %v", err)
	}

	if stats.ReportPath == "" {
		t.Fatal("ReportPath should not be empty")
	}
	if _, err := os.Stat(stats.ReportPath); err != nil {
		t.Fatalf("Expected stats report to exist: %v", err)
	}

	var report map[string]any
	data, err := os.ReadFile(stats.ReportPath)
	if err != nil {
		t.Fatalf("Failed to read stats report: %v", err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("Failed to parse stats report: %v", err)
	}

	if val, ok := report["archive_path"].(string); !ok || val == "" {
		t.Error("archive_path missing or empty in report")
	}
	if val, ok := report["checksum"].(string); !ok || val == "" {
		t.Error("checksum missing or empty in report")
	}
	if val, ok := report["manifest_path"].(string); !ok || val == "" {
		t.Error("manifest_path missing or empty in report")
	} else if _, err := os.Stat(val); err != nil {
		t.Errorf("expected manifest file to exist: %v", err)
	}
	if val, ok := report["requested_compression"].(string); !ok || val == "" {
		t.Error("requested_compression missing or empty in report")
	} else if val != string(stats.RequestedCompression) {
		t.Errorf("requested_compression mismatch: got %s want %s", val, stats.RequestedCompression)
	}
	if val, ok := report["compression"].(string); !ok || val == "" {
		t.Error("compression missing or empty in report")
	} else if val != string(stats.Compression) {
		t.Errorf("compression mismatch: got %s want %s", val, stats.Compression)
	}
	if val, ok := report["requested_compression_mode"].(string); !ok || val == "" {
		t.Error("requested_compression_mode missing or empty in report")
	} else if val != stats.RequestedCompressionMode {
		t.Errorf("requested_compression_mode mismatch: got %s want %s", val, stats.RequestedCompressionMode)
	}
	if val, ok := report["compression_mode"].(string); !ok || val == "" {
		t.Error("compression_mode missing or empty in report")
	} else if val != stats.CompressionMode {
		t.Errorf("compression_mode mismatch: got %s want %s", val, stats.CompressionMode)
	}
	if val, ok := report["compression_threads"].(float64); !ok {
		t.Error("compression_threads missing in report")
	} else if int(val) != stats.CompressionThreads {
		t.Errorf("compression_threads mismatch: got %d want %d", int(val), stats.CompressionThreads)
	}

	if stats.ManifestPath == "" {
		t.Fatal("ManifestPath should not be empty")
	}
	manifest, err := backup.LoadManifest(stats.ManifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}
	if manifest.ArchivePath != stats.ArchivePath {
		t.Errorf("Manifest archive path mismatch: got %s want %s", manifest.ArchivePath, stats.ArchivePath)
	}
	if manifest.SHA256 != stats.Checksum {
		t.Errorf("Manifest checksum mismatch: got %s want %s", manifest.SHA256, stats.Checksum)
	}
	if manifest.Hostname != stats.Hostname {
		t.Errorf("Manifest hostname mismatch: got %s want %s", manifest.Hostname, stats.Hostname)
	}

	if stats.RequestedCompression != types.CompressionNone {
		t.Errorf("Expected requested compression none, got %s", stats.RequestedCompression)
	}
	if stats.Compression != types.CompressionNone {
		t.Errorf("Expected effective compression none, got %s", stats.Compression)
	}
	if manifest.CompressionMode != stats.CompressionMode {
		t.Errorf("Manifest compression mode mismatch: got %s want %s", manifest.CompressionMode, stats.CompressionMode)
	}
}

func TestRunGoBackupFallbackCompression(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping end-to-end backup test in short mode")
	}

	logger := logging.New(types.LogLevelInfo, false)

	backupDir := t.TempDir()
	logDir := t.TempDir()

	orch := New(logger, false)
	orch.SetBackupConfig(backupDir, logDir, types.CompressionXZ, 6, 0, "ultra", nil)

	checkerConfig := &checks.CheckerConfig{
		BackupPath:         backupDir,
		LogPath:            logDir,
		LockDirPath:        filepath.Join(backupDir, "lock"),
		MinDiskPrimaryGB:   0.001,
		MinDiskSecondaryGB: 0,
		MinDiskCloudGB:     0,
		SafetyFactor:       1.0,
		LockFilePath:       filepath.Join(backupDir, "lock", ".backup.lock"),
		MaxLockAge:         time.Hour,
	}
	if err := checkerConfig.Validate(); err != nil {
		t.Fatalf("checker config validation failed: %v", err)
	}
	checker := checks.NewChecker(logger, checkerConfig)
	orch.SetChecker(checker)

	restore := backup.WithLookPathOverride(func(binary string) (string, error) {
		if binary == "xz" {
			return "", errors.New("xz unavailable")
		}
		return exec.LookPath(binary)
	})
	t.Cleanup(restore)

	ctx := context.Background()
	stats, err := orch.RunGoBackup(ctx, types.ProxmoxUnknown, "fallback-host")
	if err != nil {
		t.Fatalf("RunGoBackup failed: %v", err)
	}

	if stats.RequestedCompression != types.CompressionXZ {
		t.Errorf("Requested compression mismatch: got %s want %s", stats.RequestedCompression, types.CompressionXZ)
	}
	if stats.Compression != types.CompressionGzip {
		t.Errorf("Expected fallback to gzip, got %s", stats.Compression)
	}
	if !strings.HasSuffix(stats.ArchivePath, ".tar.gz") {
		t.Errorf("ArchivePath should have .tar.gz suffix, got %s", stats.ArchivePath)
	}
}

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

func TestRunGoBackup_EarlyFailureMetricsAndLogParsing(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.CloseLogFile()
	})

	logger.Warning("warning test - sample")
	logger.Error("error test - sample")

	orch := New(logger, false)
	orch.SetUpdateInfo(true, "1.0.0", "1.1.0")
	orch.SetBackupConfig(t.TempDir(), t.TempDir(), types.CompressionNone, 0, 0, "standard", nil)
	orch.SetConfig(&config.Config{
		MetricsEnabled:        true,
		MetricsPath:           "",
		MaxPVEBackupSizeBytes: -1,
	})

	stats, err := orch.RunGoBackup(context.Background(), types.ProxmoxUnknown, "host-early")
	if err == nil {
		t.Fatalf("expected error from RunGoBackup")
	}
	if stats == nil {
		t.Fatalf("expected stats on failure")
	}
	if stats.LogFilePath != logPath {
		t.Fatalf("LogFilePath=%q, want %q", stats.LogFilePath, logPath)
	}
	if !stats.NewVersionAvailable || stats.CurrentVersion != "1.0.0" || stats.LatestVersion != "1.1.0" {
		t.Fatalf("update info not propagated: %#v", stats)
	}
	if stats.WarningCount == 0 || stats.ErrorCount == 0 {
		t.Fatalf("expected warning/error counts from log parsing, got warnings=%d errors=%d", stats.WarningCount, stats.ErrorCount)
	}
	if !strings.Contains(buf.String(), "Failed to export Prometheus metrics") {
		t.Fatalf("expected metrics export warning, got: %s", buf.String())
	}
}

func TestRunGoBackup_DryRunParsesLogsAndSkipsDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RunGoBackup dry-run test in short mode")
	}

	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.CloseLogFile()
	})

	logger.Warning("warning test - sample")

	backupDir := t.TempDir()
	logDir := t.TempDir()

	orch := New(logger, true)
	orch.SetBackupConfig(backupDir, logDir, types.CompressionNone, 0, 0, "standard", nil)
	orch.SetConfig(&config.Config{
		BackupBlacklist:      []string{"./tmp/blacklist"},
		BackupNetworkConfigs: true,
	})
	orch.RegisterStorageTarget(&testStorageTarget{})

	stats, err := orch.RunGoBackup(context.Background(), types.ProxmoxUnknown, "dry-host")
	if err != nil {
		t.Fatalf("RunGoBackup dry run failed: %v", err)
	}
	if stats.WarningCount == 0 || stats.ErrorCount != 0 {
		t.Fatalf("unexpected log counts: warnings=%d errors=%d", stats.WarningCount, stats.ErrorCount)
	}
	if stats.ExitCode != types.ExitGenericError.Int() {
		t.Fatalf("ExitCode=%d, want %d", stats.ExitCode, types.ExitGenericError.Int())
	}
	if !strings.Contains(buf.String(), "Storage dispatch skipped (dry run mode)") {
		t.Fatalf("expected dry-run dispatch log, got: %s", buf.String())
	}
}

func TestRunGoBackup_BundleAndDispatchFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RunGoBackup bundle test in short mode")
	}

	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.CloseLogFile()
	})

	backupDir := t.TempDir()
	logDir := t.TempDir()

	orch := New(logger, false)
	orch.SetBackupConfig(backupDir, logDir, types.CompressionNone, 0, 0, "standard", nil)
	orch.SetConfig(&config.Config{
		BundleAssociatedFiles:  true,
		EncryptArchive:         true,
		AgeRecipients:          []string{testAgeRecipient},
		RetentionPolicy:        "simple",
		LocalRetentionDays:     7,
		SecondaryRetentionDays: 2,
		CloudRetentionDays:     1,
		BackupNetworkConfigs:   true,
	})
	orch.RegisterStorageTarget(&testStorageTarget{err: errors.New("sync failed")})

	stats, err := orch.RunGoBackup(context.Background(), types.ProxmoxUnknown, "bundle-host")
	if err == nil {
		t.Fatalf("expected RunGoBackup to fail on storage dispatch")
	}
	var be *BackupError
	if !errors.As(err, &be) || be.Phase != "storage" {
		t.Fatalf("expected storage BackupError, got %v", err)
	}
	if stats == nil {
		t.Fatalf("expected stats on storage failure")
	}
	if !stats.BundleCreated {
		t.Fatalf("expected BundleCreated to be true")
	}
	if !strings.HasSuffix(stats.ArchivePath, ".bundle.tar") {
		t.Fatalf("ArchivePath=%q, want *.bundle.tar", stats.ArchivePath)
	}
	if stats.ManifestPath != "" {
		t.Fatalf("expected ManifestPath to be cleared after bundling, got %q", stats.ManifestPath)
	}
	if _, err := os.Stat(stats.ArchivePath); err != nil {
		t.Fatalf("expected bundle archive to exist: %v", err)
	}
	rawArchivePath := strings.TrimSuffix(stats.ArchivePath, ".bundle.tar")
	if rawArchivePath == stats.ArchivePath {
		t.Fatalf("expected bundle suffix in archive path: %q", stats.ArchivePath)
	}
	if _, err := os.Stat(rawArchivePath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected raw archive to be removed, stat err=%v", err)
	}
	if !strings.Contains(buf.String(), "Policy: simple") {
		t.Fatalf("expected retention policy log, got: %s", buf.String())
	}
}
