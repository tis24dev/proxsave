package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// Characterization for maybeAutoMigrateDaemon's decision gates (the --upgrade
// daemon retrofit). These pin the three branches that MUST short-circuit BEFORE
// the systemd migration (applyDaemonMode) is ever reached, so a later refactor
// that moves this call into an extracted finalize phase cannot silently change
// which installs get auto-migrated.
//
// The actual migration branch (cron + not opted out) is intentionally NOT
// exercised here: it shells out to systemctl to install the unit, which needs
// root + systemd and mutates the host, so it belongs to system/integration
// coverage rather than a hermetic unit test. The stdout marker "Migrating to the
// resident daemon" is printed immediately before applyDaemonMode, so its ABSENCE
// proves the gate short-circuited without touching the scheduler.
func TestMaybeAutoMigrateDaemon_Gates(t *testing.T) {
	const migratingMarker = "Migrating to the resident daemon"
	const optOutMarker = "leaving the cron scheduler in place"

	run := func(t *testing.T, configPath, baseDir string) string {
		t.Helper()
		return captureStdout(t, func() {
			maybeAutoMigrateDaemon(context.Background(), configPath, baseDir, "/usr/local/bin/proxsave", logging.NewBootstrapLogger())
		})
	}

	t.Run("already on daemon: no migration", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfgPath, []byte("SCHEDULER_MODE=daemon\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		out := run(t, cfgPath, dir)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("already-daemon must not migrate; stdout:\n%s", out)
		}
		if strings.Contains(out, optOutMarker) {
			t.Errorf("already-daemon must not print the opt-out notice; stdout:\n%s", out)
		}
	})

	t.Run("opted out: honored, no migration", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfgPath, []byte("SCHEDULER_MODE=cron\nDAEMON_OPT_OUT=true\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		out := run(t, cfgPath, dir)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("opt-out must prevent migration; stdout:\n%s", out)
		}
		if !strings.Contains(out, optOutMarker) {
			t.Errorf("opt-out must print the notice %q; stdout:\n%s", optOutMarker, out)
		}
	})

	t.Run("unreadable config: silent no-op", func(t *testing.T) {
		dir := t.TempDir()
		missing := filepath.Join(dir, "does-not-exist.env")
		out := run(t, missing, dir)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("unreadable config must not migrate; stdout:\n%s", out)
		}
		if strings.Contains(out, optOutMarker) {
			t.Errorf("unreadable config must not print the opt-out notice; stdout:\n%s", out)
		}
	})
}
