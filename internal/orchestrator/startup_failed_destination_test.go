package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// startupFailedConfig is what cmd/proxsave leaves in the configuration when the
// Secondary and the Cloud are on in backup.env and both fail at startup
// (disableSecondaryForRun, disableCloudForRun): switched off for the run, recorded as
// failed. The log paths are kept on purpose, so the log copy is shown to depend on the
// switch alone.
func startupFailedConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		BackupPath:             t.TempDir(),
		RetentionPolicy:        "simple",
		MaxSecondaryBackups:    3,
		SecondaryRetentionDays: 3,
		MaxCloudBackups:        0,
		CloudRetentionDays:     0,
		SecondaryEnabled:       false,
		SecondaryStartupFailed: true,
		SecondaryPath:          "/mnt/nas-backup",
		SecondaryLogPath:       t.TempDir(),
		CloudEnabled:           false,
		CloudStartupFailed:     true,
		CloudRemote:            "gdrive:proxsave/backup",
		CloudLogPath:           "gdrive:proxsave/log",
	}
}

func startupFailedStats(cfg *config.Config) *BackupStats {
	return InitializeBackupStats("node1", &environment.EnvironmentInfo{Type: types.ProxmoxVE}, "1.2.3",
		time.Date(2026, time.October, 4, 2, 0, 0, 0, time.UTC), cfg, types.CompressionXZ, "ultra", 9, 0,
		cfg.BackupPath, "server-1", "aa:bb:cc")
}

// The run statistics carry the outcome where it is born: a destination on in
// backup.env that failed at startup is "error", out of the run, with no count read and
// the retention form of the configuration.
func TestInitializeBackupStatsStartupFailed(t *testing.T) {
	stats := startupFailedStats(startupFailedConfig(t))
	if stats.SecondaryEnabled || stats.CloudEnabled {
		t.Fatalf("a destination failed at startup must stay out of the run: secondary=%v cloud=%v", stats.SecondaryEnabled, stats.CloudEnabled)
	}
	if !stats.SecondaryStartupFailed || !stats.CloudStartupFailed {
		t.Fatalf("startup failure not carried: secondary=%v cloud=%v", stats.SecondaryStartupFailed, stats.CloudStartupFailed)
	}
	if stats.SecondaryStatus != "error" || stats.CloudStatus != "error" {
		t.Fatalf("statuses secondary=%q cloud=%q, want error/error", stats.SecondaryStatus, stats.CloudStatus)
	}
	if stats.SecondaryBackups != -1 || stats.CloudBackups != -1 {
		t.Fatalf("counts secondary=%d cloud=%d, want -1 (never read)", stats.SecondaryBackups, stats.CloudBackups)
	}
	if got := formatBackupStatusSummary(stats.SecondaryRetentionPolicy, stats.SecondaryBackups, stats.MaxSecondaryBackups); got != "?/3" {
		t.Fatalf("secondary summary %q, want ?/3", got)
	}
	if got := formatBackupStatusSummary(stats.CloudRetentionPolicy, stats.CloudBackups, stats.MaxCloudBackups); got != "?/?" {
		t.Fatalf("cloud summary %q, want ?/? (limit 0)", got)
	}

	gfs := startupFailedConfig(t)
	gfs.RetentionPolicy = "gfs"
	gfs.RetentionWeekly = 4
	stats = startupFailedStats(gfs)
	if stats.SecondaryRetentionPolicy != "gfs" || stats.SecondaryGFSDaily != 1 || stats.SecondaryGFSWeekly != 4 {
		t.Fatalf("secondary GFS retention %q daily=%d weekly=%d, want gfs 1/4", stats.SecondaryRetentionPolicy, stats.SecondaryGFSDaily, stats.SecondaryGFSWeekly)
	}
	if got := formatBackupStatusSummary(stats.SecondaryRetentionPolicy, stats.SecondaryBackups, stats.MaxSecondaryBackups); got != "?/-" {
		t.Fatalf("secondary GFS summary %q, want ?/-", got)
	}

	// Switched off in backup.env, or on and initialized: no startup failure.
	off := startupFailedConfig(t)
	off.SecondaryStartupFailed, off.CloudStartupFailed = false, false
	stats = startupFailedStats(off)
	if stats.SecondaryStatus != "disabled" || stats.CloudStatus != "disabled" || stats.SecondaryBackups != 0 || stats.CloudBackups != 0 {
		t.Fatalf("switched off: statuses %q/%q counts %d/%d", stats.SecondaryStatus, stats.CloudStatus, stats.SecondaryBackups, stats.CloudBackups)
	}
	on := startupFailedConfig(t)
	on.SecondaryEnabled, on.CloudEnabled = true, true
	stats = startupFailedStats(on)
	if stats.SecondaryStartupFailed || stats.CloudStartupFailed || stats.SecondaryStatus != "skipped" || stats.CloudStatus != "skipped" {
		t.Fatalf("in the run: startupFailed %v/%v statuses %q/%q", stats.SecondaryStartupFailed, stats.CloudStartupFailed, stats.SecondaryStatus, stats.CloudStatus)
	}
}

// The notifications read the status as for every destination and see the destination
// configured: Enabled true, the status, the "?" summary, no count, no space.
func TestStartupFailedNotificationData(t *testing.T) {
	stats := startupFailedStats(startupFailedConfig(t))
	data := (&NotificationAdapter{logger: logging.New(types.LogLevelError, false)}).convertBackupStatsToNotificationData(stats)
	if !data.SecondaryEnabled || data.SecondaryStatus != "error" || data.SecondaryStatusSummary != "?/3" || data.SecondaryCount != -1 ||
		data.SecondaryFree != "" || data.SecondaryUsed != "" || data.SecondaryPercent != "" || data.SecondarySpaceBytes != 0 || data.SecondaryUsagePercent != 0 {
		t.Fatalf("secondary: enabled=%v status=%q summary=%q count=%d free=%q used=%q percent=%q",
			data.SecondaryEnabled, data.SecondaryStatus, data.SecondaryStatusSummary, data.SecondaryCount, data.SecondaryFree, data.SecondaryUsed, data.SecondaryPercent)
	}
	if !data.CloudEnabled || data.CloudStatus != "error" || data.CloudStatusSummary != "?/?" || data.CloudCount != -1 {
		t.Fatalf("cloud: enabled=%v status=%q summary=%q count=%d", data.CloudEnabled, data.CloudStatus, data.CloudStatusSummary, data.CloudCount)
	}
	if data.SecondaryPath != "/mnt/nas-backup" || data.CloudPath != "gdrive:proxsave/backup" {
		t.Fatalf("paths secondary=%q cloud=%q: a configured destination keeps its path", data.SecondaryPath, data.CloudPath)
	}
}

// A destination failed at startup stays out of the run exactly as before: the step [6]
// SKIP lines, no log copy. Its metrics line stays, at 0, like PBS.
func TestStartupFailedDestinationsStayOutOfTheRun(t *testing.T) {
	cfg := startupFailedConfig(t)
	stats := startupFailedStats(cfg)

	var out bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&out)
	o := &Orchestrator{logger: logger, cfg: cfg}

	o.logDisabledStorageTargets(stats)
	for _, want := range []string{"Secondary Storage: disabled", "Cloud Storage: disabled"} {
		if !strings.Contains(out.String(), "SKIP") || !strings.Contains(out.String(), want) {
			t.Fatalf("step [6] SKIP line %q missing:\n%s", want, out.String())
		}
	}

	out.Reset()
	src := filepath.Join(t.TempDir(), "backup.log")
	if err := os.WriteFile(src, []byte("logdata"), 0o640); err != nil {
		t.Fatalf("write source log: %v", err)
	}
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	if strings.Contains(out.String(), "Secondary: ") || strings.Contains(out.String(), "Cloud: ") {
		t.Fatalf("the log was dispatched to a destination failed at startup:\n%s", out.String())
	}
	if entries, err := os.ReadDir(cfg.SecondaryLogPath); err != nil || len(entries) != 0 {
		t.Fatalf("secondary log dir holds %v (err %v), want nothing", entries, err)
	}

	cfg.MetricsEnabled = true
	cfg.MetricsPath = t.TempDir()
	o.exportPrometheusBackupMetrics(stats)
	content, err := os.ReadFile(filepath.Join(cfg.MetricsPath, "proxmox_backup.prom"))
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	for _, loc := range []string{"secondary", "cloud"} {
		want := `proxmox_backup_backups_total{location="` + loc + `"} 0` + "\n"
		if strings.Count(string(content), `location="`+loc+`"`) != 1 || !strings.Contains(string(content), want) {
			t.Fatalf("want one %q for a destination failed at startup:\n%s", want, content)
		}
	}
}
