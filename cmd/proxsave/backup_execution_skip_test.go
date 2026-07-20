package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestRunConfiguredBackup_DisabledReturnsSkip pins F09-03: when BACKUP_ENABLED=false the run
// returns the ExitBackupSkipped sentinel (not ExitSuccess 0), so the daemon can tell a benign
// skip from a real success and the CLI footer colors it as skipped, not green.
func TestRunConfiguredBackup_DisabledReturnsSkip(t *testing.T) {
	opts := backupModeOptions{cfg: &config.Config{BackupEnabled: false}}

	stats, early, code := runConfiguredBackup(opts, nil)
	if stats != nil || early != nil {
		t.Fatalf("disabled backup must return nil stats/earlyError, got %v / %v", stats, early)
	}
	if code != types.ExitBackupSkipped.Int() {
		t.Fatalf("disabled backup exit = %d, want ExitBackupSkipped %d", code, types.ExitBackupSkipped.Int())
	}
}
