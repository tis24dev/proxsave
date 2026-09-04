package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// The post-notification re-parse decides the PROCESS exit code (exportBackupMetrics
// runs as a defer inside RunGoBackup and mutates the same stats the cmd layer
// returns), yet it lived behind the Prometheus gate: the same notify failure meant
// exit 1 with METRICS_ENABLED=true and exit 0 on a default install. Maintainer
// call (2026-09-02): a notification failure is warning-weight ALWAYS - exit 1 -
// because monitoring must learn notifications are broken exactly when email
// cannot say so. NOTIFICATIONS.md is aligned in the same change.
func TestNotifyFailurePromotesExitEvenWithMetricsOff(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "run.log")
	content := "[2026-09-02 10:00:00] INFO     Backup completed\n" +
		"[2026-09-02 10:00:01] NOTIFY-ERR Telegram: failed: connection refused\n"
	if err := os.WriteFile(logFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	o := &Orchestrator{cfg: &config.Config{MetricsEnabled: false}}
	stats := &BackupStats{LogFilePath: logFile, ExitCode: types.ExitSuccess.Int()}
	o.exportBackupMetrics(&backupRunContext{stats: stats}, nil)

	if stats.ExitCode != types.ExitGenericError.Int() {
		t.Fatalf("ExitCode = %d, want %d: with metrics off the notify failure vanished to success", stats.ExitCode, types.ExitGenericError.Int())
	}
	if stats.NotifyCount != 1 {
		t.Fatalf("NotifyCount = %d, want 1", stats.NotifyCount)
	}
}

// The other half of the contract stays: a genuinely failed run keeps its error
// path (no success re-parse), and dry runs are untouched.
func TestFailedOrDryRunSkipsTheSuccessReparse(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(logFile, []byte("[2026-09-02 10:00:01] NOTIFY-ERR x: failed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("dry run", func(t *testing.T) {
		o := &Orchestrator{cfg: &config.Config{MetricsEnabled: false}, dryRun: true}
		stats := &BackupStats{LogFilePath: logFile, ExitCode: types.ExitSuccess.Int()}
		o.exportBackupMetrics(&backupRunContext{stats: stats}, nil)
		if stats.ExitCode != types.ExitSuccess.Int() || stats.NotifyCount != 0 {
			t.Fatalf("dry run must not re-parse: exit=%d notify=%d", stats.ExitCode, stats.NotifyCount)
		}
	})

	t.Run("failed run", func(t *testing.T) {
		o := &Orchestrator{cfg: &config.Config{MetricsEnabled: false}}
		stats := &BackupStats{LogFilePath: logFile, ExitCode: types.ExitBackupError.Int()}
		o.exportBackupMetrics(&backupRunContext{stats: stats}, errors.New("archive failed"))
		if stats.ExitCode != types.ExitBackupError.Int() || stats.NotifyCount != 0 {
			t.Fatalf("failed run must not re-parse: exit=%d notify=%d", stats.ExitCode, stats.NotifyCount)
		}
	})
}
