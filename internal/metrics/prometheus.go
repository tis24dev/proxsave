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

	Failed       bool // the backup run itself failed (runErr != nil); authoritative for status
	ExitCode     int
	ErrorCount   int
	WarningCount int
	// NotifyCount tallies notification/communication failures: warning-weight for the
	// status (never escalate to error), so a notify-only run is status 1, not 2.
	NotifyCount    int
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
func (pe *PrometheusExporter) Export(m *BackupMetrics) (err error) {
	if pe == nil || m == nil {
		return nil
	}

	if pe.textfileDir == "" {
		return fmt.Errorf("metrics textfile directory is empty")
	}

	// 0755 on purpose (not 0750): this is the Prometheus textfile-collector
	// directory, which node_exporter — typically a non-root user — must be able to
	// traverse and read to scrape the metrics file.
	// #nosec G301 -- world-traversable is required for the non-root metrics scraper.
	if err := os.MkdirAll(pe.textfileDir, 0o755); err != nil {
		return fmt.Errorf("create metrics directory %s: %w", pe.textfileDir, err)
	}

	tmpPath := filepath.Join(pe.textfileDir, "proxmox_backup.prom.tmp")
	finalPath := filepath.Join(pe.textfileDir, "proxmox_backup.prom")

	// 0644 on purpose: the metrics file must be world-readable so node_exporter
	// (typically a non-root user) can scrape it, same rationale as the 0755 dir above.
	// #nosec G302 -- world-readable is required for the non-root metrics scraper.
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create metrics file %s: %w", tmpPath, err)
	}
	defer func() {
		if f == nil {
			return
		}
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close metrics file %s: %w", tmpPath, closeErr)
		}
	}()

	var writeErr error
	wrap := func(err error) error {
		if err == nil {
			return nil
		}
		if writeErr == nil {
			writeErr = fmt.Errorf("write metrics file %s: %w", tmpPath, err)
		}
		return writeErr
	}
	writef := func(format string, a ...any) error {
		if writeErr != nil {
			return writeErr
		}
		_, err := fmt.Fprintf(f, format, a...)
		return wrap(err)
	}

	// Helper to write a single metric with HELP/TYPE
	writeMetric := func(name, mtype, help, value string) error {
		if writeErr != nil {
			return writeErr
		}
		if err := writef("# HELP %s %s\n", name, help); err != nil {
			return err
		}
		if err := writef("# TYPE %s %s\n", name, mtype); err != nil {
			return err
		}
		if err := writef("%s\n", value); err != nil {
			return err
		}
		return nil
	}

	// Timestamps
	startTs := float64(m.StartTime.Unix())
	endTs := float64(m.EndTime.Unix())
	if m.EndTime.IsZero() && !m.StartTime.IsZero() {
		endTs = float64(m.StartTime.Unix() + int64(m.Duration.Seconds()))
	}

	// Status gauge: 0=success, 1=warning, 2=error. A genuinely FAILED backup run
	// (m.Failed, set from runErr != nil) is authoritative -> error, even when the
	// terminal [ERROR] line was not yet counted at export time and only warnings were
	// logged (F11-02: keying off counts+exit-code alone masked such a failure as a
	// warning, because the generic failure exit code collides with the warning-only
	// promotion code). A warning-only run is NOT a failure (m.Failed == false): it is
	// promoted to a non-zero (generic) exit code upstream but must stay status=1, not 2
	// (PS-BH-004). Notification/communication errors never set m.Failed, so they never
	// escalate a run to error. A non-zero exit code with no failure and no counted
	// errors/warnings (e.g. an early abort) still maps to error.
	status := 0
	switch {
	case m.Failed || m.ErrorCount > 0:
		status = 2
	case m.WarningCount > 0 || m.NotifyCount > 0:
		// A notification/communication failure is warning-weight: it must keep the run
		// at status 1 and, crucially, be checked BEFORE the non-zero-exit-code fallback
		// below (a notify-only run carries the generic exit code) so it never reads as error.
		status = 1
	case m.ExitCode != 0:
		status = 2
	}

	// Core metrics
	if err := writeMetric(
		"proxmox_backup_start_time_seconds",
		"gauge",
		"Unix timestamp of backup start",
		fmt.Sprintf("proxmox_backup_start_time_seconds %.0f", startTs),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_end_time_seconds",
		"gauge",
		"Unix timestamp of backup end",
		fmt.Sprintf("proxmox_backup_end_time_seconds %.0f", endTs),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_duration_seconds",
		"gauge",
		"Duration of last backup in seconds",
		fmt.Sprintf("proxmox_backup_duration_seconds %.2f", m.Duration.Seconds()),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_exit_code",
		"gauge",
		"Exit code of last backup",
		fmt.Sprintf("proxmox_backup_exit_code %d", m.ExitCode),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_status",
		"gauge",
		"Status of last backup (0=success,1=warning,2=error)",
		fmt.Sprintf("proxmox_backup_status %d", status),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_errors_total",
		"gauge",
		"Total number of errors in last backup",
		fmt.Sprintf("proxmox_backup_errors_total %d", m.ErrorCount),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_warnings_total",
		"gauge",
		"Total number of warnings in last backup",
		fmt.Sprintf("proxmox_backup_warnings_total %d", m.WarningCount),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_bytes_collected",
		"gauge",
		"Total number of bytes collected during last backup",
		fmt.Sprintf("proxmox_backup_bytes_collected %d", m.BytesCollected),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_archive_size_bytes",
		"gauge",
		"Size of last backup archive in bytes",
		fmt.Sprintf("proxmox_backup_archive_size_bytes %d", m.ArchiveSize),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_files_collected_total",
		"gauge",
		"Total files successfully collected during last backup",
		fmt.Sprintf("proxmox_backup_files_collected_total %d", m.FilesCollected),
	); err != nil {
		return err
	}

	if err := writeMetric(
		"proxmox_backup_files_failed_total",
		"gauge",
		"Total files that failed to collect during last backup",
		fmt.Sprintf("proxmox_backup_files_failed_total %d", m.FilesFailed),
	); err != nil {
		return err
	}

	// Per-location backup counts
	if err := writef("# HELP proxmox_backup_backups_total Number of backups per location\n"); err != nil {
		return err
	}
	if err := writef("# TYPE proxmox_backup_backups_total gauge\n"); err != nil {
		return err
	}
	if err := writef("proxmox_backup_backups_total{location=\"local\"} %d\n", m.LocalBackups); err != nil {
		return err
	}
	if err := writef("proxmox_backup_backups_total{location=\"secondary\"} %d\n", m.SecBackups); err != nil {
		return err
	}
	if err := writef("proxmox_backup_backups_total{location=\"cloud\"} %d\n", m.CloudBackups); err != nil {
		return err
	}

	// Static info metric with labels
	if err := writef("# HELP proxmox_backup_info Static information about this backup instance\n"); err != nil {
		return err
	}
	if err := writef("# TYPE proxmox_backup_info gauge\n"); err != nil {
		return err
	}
	if err := writef(
		"proxmox_backup_info{hostname=%q,proxmox_type=%q,proxmox_version=%q,script_version=%q} 1\n",
		m.Hostname,
		m.ProxmoxType,
		m.ProxmoxVersion,
		m.ScriptVersion,
	); err != nil {
		return err
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync metrics file %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close metrics file %s: %w", tmpPath, err)
	}
	f = nil

	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename metrics file to %s: %w", finalPath, err)
	}

	if pe.logger != nil {
		pe.logger.Debug("Prometheus metrics exported to %s", finalPath)
	}

	return nil
}
