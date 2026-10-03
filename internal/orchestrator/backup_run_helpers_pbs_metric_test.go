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
			dir := t.TempDir()
			o := &Orchestrator{
				logger: logging.New(types.LogLevelError, false),
				cfg:    &config.Config{MetricsEnabled: true, MetricsPath: dir, PBSTargetEnabled: tc.enabled},
			}
			o.exportPrometheusBackupMetrics(&BackupStats{
				Hostname:         "test-host",
				LocalBackups:     4,
				SecondaryBackups: 2,
				CloudBackups:     1,
				PBSTarget:        tc.target,
			})
			data, err := os.ReadFile(filepath.Join(dir, "proxmox_backup.prom"))
			if err != nil {
				t.Fatalf("read metrics file: %v", err)
			}
			content := string(data)
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
