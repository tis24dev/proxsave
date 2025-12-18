package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestBackupErrorMethods(t *testing.T) {
	underlying := errors.New("boom")
	be := &BackupError{Phase: "archive", Err: underlying, Code: types.ExitArchiveError}

	if !strings.Contains(be.Error(), "archive phase failed") {
		t.Fatalf("Error string mismatch: %s", be.Error())
	}
	if !errors.Is(be, underlying) {
		t.Fatalf("Unwrap should expose underlying error")
	}
}

func TestEarlyErrorStateHasError(t *testing.T) {
	state := &EarlyErrorState{}
	if state.HasError() {
		t.Fatalf("expected HasError false when nil error")
	}
	state.Error = errors.New("init fail")
	if !state.HasError() {
		t.Fatalf("expected HasError true when error set")
	}
}

func TestOrchestratorSetters(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	o := New(logger, false)

	o.SetForceNewAgeRecipient(true)
	if !o.forceNewAgeRecipient || o.ageRecipientCache != nil {
		t.Fatalf("expected forceNewAgeRecipient to set and cache cleared")
	}

	o.SetProxmoxVersion(" 7.4 ")
	if o.proxmoxVersion != "7.4" {
		t.Fatalf("SetProxmoxVersion did not trim: %q", o.proxmoxVersion)
	}

	now := time.Now()
	o.SetStartTime(now)
	if o.startTime != now {
		t.Fatalf("start time not set")
	}
}

func TestSetConfigAndVersion(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	o := New(logger, false)

	cfg := &config.Config{DryRun: true, BackupPath: "/data"}
	o.SetConfig(cfg)
	if o.cfg != cfg {
		t.Fatalf("config not set")
	}
	o.ageRecipientCache = []age.Recipient{nil}
	o.SetConfig(cfg)
	if o.ageRecipientCache != nil {
		t.Fatalf("ageRecipientCache should be cleared on SetConfig")
	}

	o.SetVersion("1.2.3")
	if o.version != "1.2.3" {
		t.Fatalf("version not set")
	}
}

func TestSaveStatsReportDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, true)

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
	orch := New(logger, false)

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

func (m *mockNotifier) Name() string {
	return "MockNotifier"
}

func (m *mockNotifier) Notify(ctx context.Context, stats *BackupStats) error {
	return m.err
}

func TestDispatchPostBackupNoTargets(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)

	if err := orch.dispatchPostBackup(context.Background(), &BackupStats{}); err != nil {
		t.Fatalf("dispatchPostBackup with no targets should not error: %v", err)
	}
}

func TestDispatchPostBackupStorageError(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	orch := New(logger, false)
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
	orch := New(logger, false)
	orch.RegisterNotificationChannel(&mockNotifier{err: errors.New("notify failure")})

	// Notifications are non-critical: errors should NOT abort backup
	err := orch.dispatchPostBackup(context.Background(), &BackupStats{})
	if err != nil {
		t.Fatalf("notification errors should not abort backup, got: %v", err)
	}
}
