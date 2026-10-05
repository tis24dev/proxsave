package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestExportPrometheusBackupMetricsPBSLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		target  *block.Result
		want    string // the pbs line, "" = none
	}{
		{"enabled with count", true, &block.Result{Name: block.PBSName, Status: block.StatusOK, Backups: 7}, `proxmox_backup_backups_total{location="pbs"} 7`},
		{"enabled zero snapshots", true, &block.Result{Name: block.PBSName, Status: block.StatusOK, Backups: 0}, `proxmox_backup_backups_total{location="pbs"} 0`},
		{"enabled unknown", true, &block.Result{Name: block.PBSName, Status: block.StatusError, Backups: -1}, `proxmox_backup_backups_total{location="pbs"} 0`},
		{"enabled no outcome (early error)", true, nil, `proxmox_backup_backups_total{location="pbs"} 0`},
		{"disabled", false, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := exportPrometheusForTest(t, &config.Config{SecondaryEnabled: true, CloudEnabled: true, PBSTargetEnabled: tc.enabled}, tc.target)
			for _, line := range []string{
				`proxmox_backup_backups_total{location="local"} 4`,
				`proxmox_backup_backups_total{location="secondary"} 2`,
				`proxmox_backup_backups_total{location="cloud"} 1`,
			} {
				if !strings.Contains(content, line+"\n") {
					t.Fatalf("missing %q\n%s", line, content)
				}
			}
			got := strings.Count(content, `location="pbs"`)
			if tc.want == "" {
				if got != 0 {
					t.Fatalf("pbs line present with PBS disabled\n%s", content)
				}
				return
			}
			if got != 1 || !strings.Contains(content, tc.want+"\n") {
				t.Fatalf("want one %q, got %d pbs lines\n%s", tc.want, got, content)
			}
		})
	}
}

// The secondary and cloud lines follow SECONDARY_ENABLED and CLOUD_ENABLED; local is always there.
func TestExportPrometheusBackupMetricsSecondaryCloudLines(t *testing.T) {
	const (
		local = `proxmox_backup_backups_total{location="local"} 4`
		sec   = `proxmox_backup_backups_total{location="secondary"} 2`
		cloud = `proxmox_backup_backups_total{location="cloud"} 1`
	)
	for _, tc := range []struct {
		name       string
		sec, cloud bool
	}{
		{"both on", true, true},
		{"secondary on, cloud off", true, false},
		{"secondary off, cloud on", false, true},
		{"both off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := exportPrometheusForTest(t, &config.Config{SecondaryEnabled: tc.sec, CloudEnabled: tc.cloud}, nil)
			for line, want := range map[string]bool{local: true, sec: tc.sec, cloud: tc.cloud} {
				if got := strings.Contains(content, line+"\n"); got != want {
					t.Fatalf("%q present = %v, want %v\n%s", line, got, want, content)
				}
			}
			if strings.Contains(content, `location="pbs"`) {
				t.Fatalf("pbs line present with PBS disabled\n%s", content)
			}
		})
	}
}

// A secondary or cloud that is on in backup.env and failed at startup keeps its line at 0,
// like PBS: its count was never read (-1) and the run switched it off.
func TestExportPrometheusBackupMetricsStartupFailedLines(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cfg        config.Config
		sec, cloud string // the line, "" = none
	}{
		{"secondary failed at startup", config.Config{SecondaryStartupFailed: true, CloudEnabled: true},
			`proxmox_backup_backups_total{location="secondary"} 0`, `proxmox_backup_backups_total{location="cloud"} 1`},
		{"cloud failed at startup", config.Config{SecondaryEnabled: true, CloudStartupFailed: true},
			`proxmox_backup_backups_total{location="secondary"} 2`, `proxmox_backup_backups_total{location="cloud"} 0`},
		{"both failed at startup", config.Config{SecondaryStartupFailed: true, CloudStartupFailed: true},
			`proxmox_backup_backups_total{location="secondary"} 0`, `proxmox_backup_backups_total{location="cloud"} 0`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			stats := &BackupStats{Hostname: "test-host", LocalBackups: 4, SecondaryBackups: 2, CloudBackups: 1}
			if cfg.SecondaryStartupFailed {
				stats.SecondaryBackups = -1
			}
			if cfg.CloudStartupFailed {
				stats.CloudBackups = -1
			}
			content := exportPrometheusStatsForTest(t, &cfg, stats)
			for loc, want := range map[string]string{"secondary": tc.sec, "cloud": tc.cloud} {
				if got := strings.Count(content, `location="`+loc+`"`); got != 1 || !strings.Contains(content, want+"\n") {
					t.Fatalf("want one %q, got %d %s lines\n%s", want, got, loc, content)
				}
			}
		})
	}
}

func exportPrometheusForTest(t *testing.T, cfg *config.Config, target *block.Result) string {
	t.Helper()
	return exportPrometheusStatsForTest(t, cfg, &BackupStats{
		Hostname:         "test-host",
		LocalBackups:     4,
		SecondaryBackups: 2,
		CloudBackups:     1,
		PBSTarget:        target,
	})
}

func exportPrometheusStatsForTest(t *testing.T, cfg *config.Config, stats *BackupStats) string {
	t.Helper()
	dir := t.TempDir()
	cfg.MetricsEnabled = true
	cfg.MetricsPath = dir
	o := &Orchestrator{logger: logging.New(types.LogLevelError, false), cfg: cfg}
	o.exportPrometheusBackupMetrics(stats)
	data, err := os.ReadFile(filepath.Join(dir, "proxmox_backup.prom"))
	if err != nil {
		t.Fatalf("read metrics file: %v", err)
	}
	return string(data)
}
