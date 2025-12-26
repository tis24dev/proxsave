package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
)

type stubNotifier struct {
	name       string
	enabled    bool
	result     *notify.NotificationResult
	err        error
	received   *notify.NotificationData
	isCritical bool
}

func (s *stubNotifier) Name() string     { return s.name }
func (s *stubNotifier) IsEnabled() bool  { return s.enabled }
func (s *stubNotifier) IsCritical() bool { return s.isCritical }
func (s *stubNotifier) Send(ctx context.Context, data *notify.NotificationData) (*notify.NotificationResult, error) {
	s.received = data
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func TestNotificationAdapter_Name(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	adapter := NewNotificationAdapter(&stubNotifier{name: "Email"}, logger)
	if got := adapter.Name(); got != "Email" {
		t.Fatalf("Name()=%q; want %q", got, "Email")
	}
}

func TestNotificationAdapter_Success(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	stats := sampleBackupStats()
	notifier := &stubNotifier{
		name:    "Email",
		enabled: true,
		result: &notify.NotificationResult{
			Success:  true,
			Method:   "email-sendmail",
			Duration: 500 * time.Millisecond,
		},
	}

	adapter := NewNotificationAdapter(notifier, logger)
	if err := adapter.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if notifier.received == nil {
		t.Fatalf("expected NotificationData to be sent")
	}
	if got := strings.TrimSpace(stats.EmailStatus); got != "ok" {
		t.Fatalf("expected EmailStatus ok, got %q", got)
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "notification completed successfully") {
		t.Fatalf("expected success log, got %q", logOutput)
	}
}

func TestNotificationAdapter_FallbackWarning(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	stats := sampleBackupStats()
	notifier := &stubNotifier{
		name:    "Email",
		enabled: true,
		result: &notify.NotificationResult{
			Success:      true,
			Method:       "email-pmf-fallback",
			UsedFallback: true,
			Duration:     time.Second,
			Error:        errors.New("primary relay failed"),
		},
	}

	adapter := NewNotificationAdapter(notifier, logger)
	if err := adapter.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if got := strings.TrimSpace(stats.EmailStatus); got != "warning" {
		t.Fatalf("expected EmailStatus warning, got %q", got)
	}
}

func TestNotificationAdapter_Failure(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	stats := sampleBackupStats()
	notifier := &stubNotifier{
		name:    "Email",
		enabled: true,
		result: &notify.NotificationResult{
			Success: false,
			Method:  "email-sendmail",
			Error:   errors.New("smtp down"),
		},
	}

	adapter := NewNotificationAdapter(notifier, logger)
	if err := adapter.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if got := strings.TrimSpace(stats.EmailStatus); got != "error" {
		t.Fatalf("expected EmailStatus error, got %q", got)
	}
}

func TestNotificationAdapter_SendError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	stats := sampleBackupStats()
	notifier := &stubNotifier{
		name:    "Telegram",
		enabled: true,
		err:     errors.New("api timeout"),
	}

	adapter := NewNotificationAdapter(notifier, logger)
	if err := adapter.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if !strings.Contains(stats.TelegramStatus, "failed") {
		t.Fatalf("expected TelegramStatus to contain failed, got %q", stats.TelegramStatus)
	}
}

func TestNotificationAdapter_DisabledNotifier(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	stats := sampleBackupStats()
	stats.EmailStatus = "foo"
	notifier := &stubNotifier{
		name:    "Email",
		enabled: false,
	}

	adapter := NewNotificationAdapter(notifier, logger)
	if err := adapter.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if stats.EmailStatus != "foo" {
		t.Fatalf("expected EmailStatus to remain unchanged, got %q", stats.EmailStatus)
	}
}

func TestDescribeNotificationResultAndSeverity(t *testing.T) {
	err := errors.New("boom")
	tests := []struct {
		name     string
		result   *notify.NotificationResult
		wantDesc string
		wantSev  string
	}{
		{"nil", nil, "unknown", "disabled"},
		{"failure", &notify.NotificationResult{Success: false, Error: err}, "failed: boom", "error"},
		{"fallback", &notify.NotificationResult{Success: true, UsedFallback: true, Method: "email-sendmail"}, "sent via email-sendmail fallback", "warning"},
		{"success", &notify.NotificationResult{Success: true, Method: "email-relay"}, "sent (email-relay)", "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeNotificationResult(tt.result); got != tt.wantDesc {
				t.Fatalf("describeNotificationResult = %q; want %q", got, tt.wantDesc)
			}
			if got := describeNotificationSeverity(tt.result); got != tt.wantSev {
				t.Fatalf("describeNotificationSeverity = %q; want %q", got, tt.wantSev)
			}
		})
	}
}

func TestConvertBackupStatsToNotificationData(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	adapter := NewNotificationAdapter(&stubNotifier{name: "Email", enabled: true}, logger)

	stats := &BackupStats{
		ExitCode:                  1,
		Hostname:                  "host",
		ProxmoxType:               types.ProxmoxUnknown,
		ArchivePath:               filepath.Join("/tmp", "backup.tar.xz"),
		ArchiveSize:               5000,
		CompressedSize:            4000,
		UncompressedSize:          8000,
		LocalBackups:              2,
		LocalFreeSpace:            1024,
		LocalTotalSpace:           2048,
		SecondaryEnabled:          true,
		SecondaryBackups:          1,
		SecondaryFreeSpace:        2048,
		SecondaryTotalSpace:       4096,
		CloudEnabled:              true,
		CloudBackups:              3,
		MaxLocalBackups:           10,
		MaxSecondaryBackups:       20,
		MaxCloudBackups:           30,
		LocalRetentionPolicy:      "simple",
		SecondaryRetentionPolicy:  "gfs",
		CloudRetentionPolicy:      "simple",
		SecondaryGFSDaily:         1,
		SecondaryGFSWeekly:        2,
		SecondaryGFSMonthly:       3,
		SecondaryGFSYearly:        0,
		ErrorCount:                1,
		WarningCount:              2,
		ScriptVersion:             "1.0.0",
		Compression:               types.CompressionZstd,
		CompressionLevel:          6,
		CompressionMode:           "standard",
		CompressionThreads:        0,
		CompressionSavingsPercent: 50.0,
	}

	data := adapter.convertBackupStatsToNotificationData(stats)

	if data.Status != notify.StatusWarning {
		t.Fatalf("Status = %v; want warning", data.Status)
	}
	if data.StatusMessage == "" || !strings.Contains(strings.ToLower(data.StatusMessage), "warnings") {
		t.Fatalf("unexpected StatusMessage: %q", data.StatusMessage)
	}
	if data.BackupFileName != "backup.tar.xz" {
		t.Fatalf("BackupFileName = %q; want backup.tar.xz", data.BackupFileName)
	}
	if data.LocalStatus != "warning" {
		t.Fatalf("LocalStatus = %q; want warning", data.LocalStatus)
	}
	if data.SecondaryStatus != "ok" || data.CloudStatus != "ok" {
		t.Fatalf("Secondary/Cloud status unexpected: %q / %q", data.SecondaryStatus, data.CloudStatus)
	}
	if data.LocalStatusSummary != "2/10" {
		t.Fatalf("LocalStatusSummary = %q; want 2/10", data.LocalStatusSummary)
	}
	if data.SecondaryStatusSummary != "1/-" {
		t.Fatalf("SecondaryStatusSummary = %q; want 1/-", data.SecondaryStatusSummary)
	}
	if data.CloudStatusSummary != "3/30" {
		t.Fatalf("CloudStatusSummary = %q; want 3/30", data.CloudStatusSummary)
	}
	if data.EmailStatus != "disabled" || data.TelegramStatus != "N/A" {
		t.Fatalf("Email/Telegram status unexpected: %q / %q", data.EmailStatus, data.TelegramStatus)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := formatBytesHR(999); got != "999 B" {
		t.Fatalf("formatBytesHR(999) = %q; want 999 B", got)
	}
	if got := formatBytesHR(1024); got != "1.00 KB" {
		t.Fatalf("formatBytesHR(1024) = %q; want 1.00 KB", got)
	}
	if got := calculateUsagePercent(25, 100); got != 75 {
		t.Fatalf("calculateUsagePercent = %f; want 75", got)
	}
	if got := calculateUsedBytes(25, 100); got != 75 {
		t.Fatalf("calculateUsedBytes = %d; want 75", got)
	}
	if got := formatPercentString(12.345); got != "12.3%" {
		t.Fatalf("formatPercentString = %q; want 12.3%%", got)
	}
	if got := formatPercentString(0); got != "0%" {
		t.Fatalf("formatPercentString(0) = %q; want 0%%", got)
	}
	if got := formatBackupStatusSummary("gfs", 2, 0); got != "2/-" {
		t.Fatalf("formatBackupStatusSummary(gfs) = %q; want 2/-", got)
	}
	if got := formatBackupStatusSummary("simple", 0, 0); got != "0/?" {
		t.Fatalf("formatBackupStatusSummary simple no max = %q; want 0/?", got)
	}
	if got := formatBackupStatusSummary("simple", 3, 5); got != "3/5" {
		t.Fatalf("formatBackupStatusSummary simple = %q; want 3/5", got)
	}
}

func TestConvertBackupStatsUsesLogCountsAndCompressionFallback(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	adapter := NewNotificationAdapter(&stubNotifier{name: "Email", enabled: true}, logger)

	// Prepare a temporary log file with known error/warning counts
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "notif.log")
	content := `[2025-11-10 14:30:01] [WARNING] Something minor
[2025-11-10 14:30:02] [ERROR] Something bad
`
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write log file: %v", err)
	}

	stats := &BackupStats{
		ExitCode:             0,
		Hostname:             "host",
		ArchivePath:          "/var/tmp/backup.tar",
		ArchiveSize:          2000,
		CompressedSize:       500,
		UncompressedSize:     1000,
		SecondaryEnabled:     false,
		CloudEnabled:         false,
		MaxLocalBackups:      5,
		LocalRetentionPolicy: "simple",
		LogFilePath:          logFile,
		ErrorCount:           0,
		WarningCount:         0,
		Compression:          types.CompressionZstd,
	}

	data := adapter.convertBackupStatsToNotificationData(stats)

	// Log counts should override stats.ErrorCount/WarningCount
	if data.ErrorCount != 1 || data.WarningCount != 1 {
		t.Fatalf("expected error/warning counts from log = 1/1; got %d/%d", data.ErrorCount, data.WarningCount)
	}
	if len(data.LogCategories) == 0 {
		t.Fatalf("expected LogCategories to be populated from log")
	}

	// Compression ratio should be computed from sizes when explicit savings is <= 0
	if data.CompressionRatio <= 0 {
		t.Fatalf("expected CompressionRatio to be computed, got %f", data.CompressionRatio)
	}

	// With ExitCode 0, local status should default to ok, secondary/cloud to disabled
	if data.LocalStatus != "ok" {
		t.Fatalf("LocalStatus = %q; want ok", data.LocalStatus)
	}
	if data.SecondaryStatus != "disabled" {
		t.Fatalf("SecondaryStatus = %q; want disabled", data.SecondaryStatus)
	}
	if data.CloudStatus != "disabled" {
		t.Fatalf("CloudStatus = %q; want disabled", data.CloudStatus)
	}
}

func sampleBackupStats() *BackupStats {
	return &BackupStats{
		ExitCode:            0,
		Duration:            2 * time.Minute,
		ArchiveSize:         12345,
		Hostname:            "host",
		ArchivePath:         "/var/tmp/backup.tar",
		CompressedSize:      12345,
		LocalBackups:        1,
		LocalFreeSpace:      1024,
		LocalTotalSpace:     2048,
		SecondaryEnabled:    true,
		SecondaryBackups:    1,
		SecondaryFreeSpace:  2048,
		SecondaryTotalSpace: 4096,
		CloudEnabled:        true,
		CloudBackups:        1,
		Timestamp:           time.Now(),
		Compression:         types.CompressionZstd,
	}
}
