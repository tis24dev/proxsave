package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPrometheusExporterExport(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New(types.LogLevelError, false)
	exporter := NewPrometheusExporter(dir, logger)

	metrics := &BackupMetrics{
		Hostname:       "test-host",
		ProxmoxType:    "pve",
		ProxmoxVersion: "8.2-1",
		ScriptVersion:  "0.9.0",
		StartTime:      time.Unix(1000, 0),
		EndTime:        time.Unix(1100, 0),
		Duration:       100 * time.Second,
		ExitCode:       0,
		ErrorCount:     1,
		WarningCount:   2,
		LocalBackups:   5,
		SecBackups:     3,
		CloudBackups:   1,
		BytesCollected: 123456789,
		ArchiveSize:    987654321,
		FilesCollected: 42,
		FilesFailed:    2,
	}

	if err := exporter.Export(metrics); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	outputPath := filepath.Join(dir, "proxmox_backup.prom")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("Failed to read metrics file: %v", err)
	}

	content := string(data)
	for _, expected := range []string{
		"proxmox_backup_start_time_seconds 1000",
		"proxmox_backup_end_time_seconds 1100",
		"proxmox_backup_duration_seconds 100.00",
		"proxmox_backup_exit_code 0",
		"proxmox_backup_errors_total 1",
		"proxmox_backup_warnings_total 2",
		"proxmox_backup_bytes_collected 123456789",
		"proxmox_backup_archive_size_bytes 987654321",
		"proxmox_backup_files_collected_total 42",
		"proxmox_backup_files_failed_total 2",
		"proxmox_backup_backups_total{location=\"local\"} 5",
		"proxmox_backup_info{hostname=\"test-host\",proxmox_type=\"pve\",proxmox_version=\"8.2-1\",script_version=\"0.9.0\"} 1",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("metrics output missing %q\n%s", expected, content)
		}
	}
}

func TestPrometheusExporterNilMetrics(t *testing.T) {
	dir := t.TempDir()
	exporter := NewPrometheusExporter(dir, nil)
	if err := exporter.Export(nil); err != nil {
		t.Fatalf("Export(nil) error = %v", err)
	}
}

// TestPrometheusExporterStatusMapping locks in the PS-BH-004 fix: the status gauge
// (0=success, 1=warning, 2=error) is derived from the error/warning counts, so a
// warning-only run (promoted to a non-zero generic exit code upstream) reports
// status=1 (warning) instead of 2 (error).
func TestPrometheusExporterStatusMapping(t *testing.T) {
	cases := []struct {
		name         string
		failed       bool
		exitCode     int
		errorCount   int
		warningCount int
		wantStatus   int
	}{
		{"clean success", false, 0, 0, 0, 0},
		{"warning only (promoted to generic exit code)", false, int(types.ExitGenericError), 0, 3, 1},
		{"errors present", false, int(types.ExitBackupError), 2, 1, 2},
		{"early abort without counts", false, int(types.ExitConfigError), 0, 0, 2},
		// F11-02: a genuine failure with only warnings logged (same counts as the
		// warning-only case above) must be error, not warning. The Failed flag is
		// authoritative regardless of the ambiguous ExitGenericError exit code.
		{"failed run with only warnings is error", true, int(types.ExitGenericError), 0, 3, 2},
		{"failed run clean is error", true, int(types.ExitBackupError), 0, 0, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exporter := NewPrometheusExporter(dir, logging.New(types.LogLevelError, false))
			m := &BackupMetrics{
				StartTime:    time.Unix(1000, 0),
				EndTime:      time.Unix(1100, 0),
				Failed:       tc.failed,
				ExitCode:     tc.exitCode,
				ErrorCount:   tc.errorCount,
				WarningCount: tc.warningCount,
			}
			if err := exporter.Export(m); err != nil {
				t.Fatalf("Export() error = %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "proxmox_backup.prom"))
			if err != nil {
				t.Fatalf("read metrics file: %v", err)
			}
			want := fmt.Sprintf("proxmox_backup_status %d\n", tc.wantStatus)
			if !strings.Contains(string(data), want) {
				t.Fatalf("status mismatch: want %q\n%s", want, string(data))
			}
		})
	}
}
