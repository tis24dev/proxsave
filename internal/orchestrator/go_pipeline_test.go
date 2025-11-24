package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/backup"
	"github.com/tis24dev/proxmox-backup/internal/checks"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

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
