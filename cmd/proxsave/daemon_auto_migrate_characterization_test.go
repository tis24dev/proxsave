package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// Characterization for maybeAutoMigrateDaemon's decision gates (the --upgrade
// daemon retrofit). These pin the branches that MUST short-circuit BEFORE
// the systemd migration (applyDaemonMode) is ever reached, so a later refactor
// that moves this call into an extracted finalize phase cannot silently change
// which installs get auto-migrated.
//
// The retrofit decides on the PRESENCE of SCHEDULER_MODE, not its value. A host that already
// carried the key has recorded a scheduler engine and is left alone whatever it says; only a
// host where THIS upgrade's merge had to add the key is migrated. See
// schedulerModeOriginFromUpgrade.
//
// The actual migration branch (cron + not opted out) is intentionally NOT
// exercised here: it shells out to systemctl to install the unit, which needs
// root + systemd and mutates the host, so it belongs to system/integration
// coverage rather than a hermetic unit test. The stdout marker "Migrating to the
// resident daemon" is printed immediately before applyDaemonMode, so its ABSENCE
// proves the gate short-circuited without touching the scheduler.
func TestMaybeAutoMigrateDaemon_Gates(t *testing.T) {
	const migratingMarker = "Migrating to the resident daemon"
	const notInstalledMarker = "the daemon is not installed"

	// The provenance is what the config merge REPORTED, not a value chosen here: MissingKeys
	// names the template keys this upgrade had to add, so naming SCHEDULER_MODE means the host
	// did not carry it, and a nil result means the merge could not report at all.
	injected := &config.UpgradeResult{MissingKeys: []string{"HEALTHCHECK_ENABLED", "SCHEDULER_MODE"}}
	configured := &config.UpgradeResult{MissingKeys: []string{"HEALTHCHECK_ENABLED"}}
	var unavailable *config.UpgradeResult

	run := func(t *testing.T, configPath, baseDir string, upgradeResult *config.UpgradeResult) string {
		t.Helper()
		return captureStdout(t, func() {
			maybeAutoMigrateDaemon(context.Background(), configPath, baseDir, "/usr/local/bin/proxsave", upgradeResult, logging.NewBootstrapLogger())
		})
	}

	// A host that already recorded an engine must not even have its crontab READ: nothing is
	// being installed, so there is nothing to collide with and nothing to check.
	refuseAllProbes := func(t *testing.T) {
		t.Helper()
		origRead, origPaths := crontabReadLinesFn, systemCronPaths
		t.Cleanup(func() { crontabReadLinesFn, systemCronPaths = origRead, origPaths })
		crontabReadLinesFn = func(context.Context) ([]string, error) {
			t.Error("the duplicate-schedule check must not run when nothing is being installed")
			return nil, nil
		}
		systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}
	}

	t.Run("already on daemon: no migration", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfgPath, []byte("SCHEDULER_MODE=daemon\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		out := run(t, cfgPath, dir, injected)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("already-daemon must not migrate; stdout:\n%s", out)
		}
		if strings.Contains(out, notInstalledMarker) {
			t.Errorf("already-daemon must not print the cron notice; stdout:\n%s", out)
		}
	})

	t.Run("key already present: honoured, no migration", func(t *testing.T) {
		refuseAllProbes(t)
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfgPath, []byte("SCHEDULER_MODE=cron\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		out := run(t, cfgPath, dir, configured)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("a recorded engine must prevent migration; stdout:\n%s", out)
		}
		if !strings.Contains(out, "SCHEDULER_MODE=cron is set in") {
			t.Errorf("the notice must name the key and the value that decided; stdout:\n%s", out)
		}
		if !strings.Contains(out, cfgPath) {
			t.Errorf("the notice must name the file it read; stdout:\n%s", out)
		}
	})

	// Nothing is established, so nothing is decided. A merge that could not report is not
	// evidence that the host never chose an engine.
	t.Run("provenance unknown: no migration", func(t *testing.T) {
		refuseAllProbes(t)
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfgPath, []byte("SCHEDULER_MODE=cron\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		out := run(t, cfgPath, dir, unavailable)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("unknown provenance must not migrate; stdout:\n%s", out)
		}
		if !strings.Contains(out, "not established") {
			t.Errorf("the notice must say the provenance was not established; stdout:\n%s", out)
		}
	})

	// The one host the retrofit is for: the key was absent until this upgrade added it.
	t.Run("key injected by this upgrade: migrates", func(t *testing.T) {
		origRead, origPaths, origApply := crontabReadLinesFn, systemCronPaths, applyDaemonModeFn
		t.Cleanup(func() { crontabReadLinesFn, systemCronPaths, applyDaemonModeFn = origRead, origPaths, origApply })
		crontabReadLinesFn = func(context.Context) ([]string, error) { return nil, nil }
		systemCronPaths = []string{filepath.Join(t.TempDir(), "absent")}
		applied := false
		applyDaemonModeFn = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRemovalOutcome, error) {
			applied = true
			return cronRemovalOutcome{Verified: true}, nil
		}

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "backup.env")
		if err := os.WriteFile(cfgPath, []byte("SCHEDULER_MODE=cron\n"), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		out := run(t, cfgPath, dir, injected)
		if !applied {
			t.Errorf("a host that never recorded an engine must be migrated; stdout:\n%s", out)
		}
		if !strings.Contains(out, migratingMarker) {
			t.Errorf("the migration must be announced; stdout:\n%s", out)
		}
		if !strings.Contains(out, "has never recorded a scheduler engine") {
			t.Errorf("the notice must say WHY this host was migrated; stdout:\n%s", out)
		}
	})

	t.Run("unreadable config: silent no-op", func(t *testing.T) {
		dir := t.TempDir()
		missing := filepath.Join(dir, "does-not-exist.env")
		out := run(t, missing, dir, injected)
		if strings.Contains(out, migratingMarker) {
			t.Errorf("unreadable config must not migrate; stdout:\n%s", out)
		}
		if strings.Contains(out, notInstalledMarker) {
			t.Errorf("unreadable config must not print the cron notice; stdout:\n%s", out)
		}
	})
}
