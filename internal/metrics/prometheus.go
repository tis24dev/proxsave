package metrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// BackupMetrics represents the subset of backup statistics exported as Prometheus metrics.
type BackupMetrics struct {
	Hostname       string
	ProxmoxType    string
	ProxmoxVersion string
	ScriptVersion  string

	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration

	ExitCode       int
	ErrorCount     int
	WarningCount   int
	LocalBackups   int
	SecBackups     int
	CloudBackups   int
	BytesCollected int64
	ArchiveSize    int64
	FilesCollected int
	FilesFailed    int
}

// PrometheusExporter writes backup metrics in Prometheus textfile format for node_exporter.
type PrometheusExporter struct {
	textfileDir string
	logger      *logging.Logger
}

// NewPrometheusExporter creates a new PrometheusExporter using the provided directory.
func NewPrometheusExporter(textfileDir string, logger *logging.Logger) *PrometheusExporter {
	return &PrometheusExporter{
		textfileDir: strings.TrimRight(textfileDir, "/"),
		logger:      logger,
	}
}

// Export writes the given metrics snapshot to proxmox_backup.prom in textfileDir.
func (pe *PrometheusExporter) Export(m *BackupMetrics) error {
	if pe == nil || m == nil {
		return nil
	}

	if pe.textfileDir == "" {
		return fmt.Errorf("metrics textfile directory is empty")
	}

	if err := os.MkdirAll(pe.textfileDir, 0o755); err != nil {
		return fmt.Errorf("create metrics directory %s: %w", pe.textfileDir, err)
	}

	tmpPath := filepath.Join(pe.textfileDir, "proxmox_backup.prom.tmp")
	finalPath := filepath.Join(pe.textfileDir, "proxmox_backup.prom")

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create metrics file %s: %w", tmpPath, err)
	}
	defer f.Close()

	// Helper to write a single metric with HELP/TYPE
	writeMetric := func(name, mtype, help, value string) {
		fmt.Fprintf(f, "# HELP %s %s\n", name, help)
		fmt.Fprintf(f, "# TYPE %s %s\n", name, mtype)
		fmt.Fprintln(f, value)
	}

	// Timestamps
	startTs := float64(m.StartTime.Unix())
	endTs := float64(m.EndTime.Unix())
	if m.EndTime.IsZero() && !m.StartTime.IsZero() {
		endTs = float64(m.StartTime.Unix() + int64(m.Duration.Seconds()))
	}

	// Status gauge: 0=success, 1=warning, 2=error
	status := 0
	if m.ExitCode != 0 {
		status = 2
	} else if m.WarningCount > 0 {
		status = 1
	}

	// Core metrics
	writeMetric(
		"proxmox_backup_start_time_seconds",
		"gauge",
		"Unix timestamp of backup start",
		fmt.Sprintf("proxmox_backup_start_time_seconds %.0f", startTs),
	)

	writeMetric(
		"proxmox_backup_end_time_seconds",
		"gauge",
		"Unix timestamp of backup end",
		fmt.Sprintf("proxmox_backup_end_time_seconds %.0f", endTs),
	)

	writeMetric(
		"proxmox_backup_duration_seconds",
		"gauge",
		"Duration of last backup in seconds",
		fmt.Sprintf("proxmox_backup_duration_seconds %.2f", m.Duration.Seconds()),
	)

	writeMetric(
		"proxmox_backup_exit_code",
		"gauge",
		"Exit code of last backup",
		fmt.Sprintf("proxmox_backup_exit_code %d", m.ExitCode),
	)

	writeMetric(
		"proxmox_backup_status",
		"gauge",
		"Status of last backup (0=success,1=warning,2=error)",
		fmt.Sprintf("proxmox_backup_status %d", status),
	)

	writeMetric(
		"proxmox_backup_errors_total",
		"gauge",
		"Total number of errors in last backup",
		fmt.Sprintf("proxmox_backup_errors_total %d", m.ErrorCount),
	)

	writeMetric(
		"proxmox_backup_warnings_total",
		"gauge",
		"Total number of warnings in last backup",
		fmt.Sprintf("proxmox_backup_warnings_total %d", m.WarningCount),
	)

	writeMetric(
		"proxmox_backup_bytes_collected",
		"gauge",
		"Total number of bytes collected during last backup",
		fmt.Sprintf("proxmox_backup_bytes_collected %d", m.BytesCollected),
	)

	writeMetric(
		"proxmox_backup_archive_size_bytes",
		"gauge",
		"Size of last backup archive in bytes",
		fmt.Sprintf("proxmox_backup_archive_size_bytes %d", m.ArchiveSize),
	)

	writeMetric(
		"proxmox_backup_files_collected_total",
		"gauge",
		"Total files successfully collected during last backup",
		fmt.Sprintf("proxmox_backup_files_collected_total %d", m.FilesCollected),
	)

	writeMetric(
		"proxmox_backup_files_failed_total",
		"gauge",
		"Total files that failed to collect during last backup",
		fmt.Sprintf("proxmox_backup_files_failed_total %d", m.FilesFailed),
	)

	// Per-location backup counts
	fmt.Fprintf(f, "# HELP proxmox_backup_backups_total Number of backups per location\n")
	fmt.Fprintf(f, "# TYPE proxmox_backup_backups_total gauge\n")
	fmt.Fprintf(f, "proxmox_backup_backups_total{location=\"local\"} %d\n", m.LocalBackups)
	fmt.Fprintf(f, "proxmox_backup_backups_total{location=\"secondary\"} %d\n", m.SecBackups)
	fmt.Fprintf(f, "proxmox_backup_backups_total{location=\"cloud\"} %d\n", m.CloudBackups)

	// Static info metric with labels
	fmt.Fprintf(f, "# HELP proxmox_backup_info Static information about this backup instance\n")
	fmt.Fprintf(f, "# TYPE proxmox_backup_info gauge\n")
	fmt.Fprintf(
		f,
		"proxmox_backup_info{hostname=%q,proxmox_type=%q,proxmox_version=%q,script_version=%q} 1\n",
		m.Hostname,
		m.ProxmoxType,
		m.ProxmoxVersion,
		m.ScriptVersion,
	)

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync metrics file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename metrics file to %s: %w", finalPath, err)
	}

	if pe.logger != nil {
		pe.logger.Debug("Prometheus metrics exported to %s", finalPath)
	}

	return nil
}
