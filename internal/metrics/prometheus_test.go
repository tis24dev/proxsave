package metrics

import (
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
