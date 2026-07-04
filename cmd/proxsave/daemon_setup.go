// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// dispatchDaemonAdminMode handles the one-shot --daemon-setup / --daemon-remove
// admin commands (switch the scheduler engine and the systemd unit / cron entry).
func dispatchDaemonAdminMode(rt *appRuntime) modeResult {
	switch {
	case rt.args.DaemonSetup:
		return modeResult{exitCode: runDaemonSetup(rt), handled: true}
	case rt.args.DaemonRemove:
		return modeResult{exitCode: runDaemonRemove(rt), handled: true}
	}
	return modeResult{exitCode: types.ExitSuccess.Int()}
}

func runDaemonSetup(rt *appRuntime) int {
	logging.Info("Enabling ProxSave daemon mode...")
	if err := applyDaemonMode(rt.ctx, rt.cfg, rt.args.ConfigPath, daemonSelfExecPath(), rt.bootstrap); err != nil {
		logging.Error("daemon-setup failed: %v", err)
		return types.ExitGenericError.Int()
	}
	logging.Info("Daemon mode enabled: %s is active and the cron entry was removed.", daemonUnitName)
	return types.ExitSuccess.Int()
}

func runDaemonRemove(rt *appRuntime) int {
	logging.Info("Removing ProxSave daemon mode and reverting to cron...")
	if err := applyCronMode(rt.ctx, rt.cfg, rt.args.ConfigPath, daemonSelfExecPath(), rt.bootstrap, true); err != nil {
		logging.Error("daemon-remove failed: %v", err)
		return types.ExitGenericError.Int()
	}
	logging.Info("Daemon removed: reverted to the cron scheduler. Future upgrades will NOT reinstall it (DAEMON_OPT_OUT=true).")
	return types.ExitSuccess.Int()
}

// applyDaemonMode switches an install to the resident daemon: install the systemd
// unit, remove the canonical cron entry (no double execution), and record
// SCHEDULER_MODE=daemon / DAEMON_OPT_OUT=false. The unit install is the critical
// step; if it fails the install stays on cron and can be retried. Cron removal and
// the config write are best-effort (warned, not fatal).
func applyDaemonMode(ctx context.Context, cfg *config.Config, configPath, execToken string, bootstrap *logging.BootstrapLogger) error {
	if err := installDaemonService(ctx, execToken, configPath, bootstrap); err != nil {
		return err
	}
	if err := removeCanonicalCronEntry(ctx, cronCorrectPaths(execToken), bootstrap); err != nil {
		logging.Warning("daemon: failed to remove the cron entry (possible double execution; the per-run lock mitigates): %v", err)
	}
	if err := setBackupEnvKeys(configPath, map[string]string{"SCHEDULER_MODE": "daemon", "DAEMON_OPT_OUT": "false"}); err != nil {
		logging.Warning("daemon: failed to record SCHEDULER_MODE=daemon in %s: %v", configPath, err)
	}
	return nil
}

// applyCronMode reverts an install to cron: remove the systemd unit, re-add the
// canonical cron entry at the configured schedule, and record SCHEDULER_MODE=cron
// (plus DAEMON_OPT_OUT=true when optOut, the --daemon-remove tombstone that stops
// future upgrades from re-migrating).
func applyCronMode(ctx context.Context, cfg *config.Config, configPath, execToken string, bootstrap *logging.BootstrapLogger, optOut bool) error {
	if err := removeDaemonService(ctx, bootstrap); err != nil {
		return err
	}
	// Re-install the single canonical cron line at the configured schedule.
	migrateLegacyCronEntries(ctx, cfg.BaseDir, execToken, bootstrap, cron.TimeToSchedule(cfg.SchedulerTime))

	kv := map[string]string{"SCHEDULER_MODE": "cron"}
	if optOut {
		kv["DAEMON_OPT_OUT"] = "true"
	}
	if err := setBackupEnvKeys(configPath, kv); err != nil {
		logging.Warning("daemon: failed to record cron mode in %s: %v", configPath, err)
	}
	return nil
}

// maybeAutoMigrateDaemon is the --upgrade retrofit: if the install is still on
// cron and the user has NOT opted out, migrate it to the daemon. Best-effort so a
// migration failure never fails the upgrade.
func maybeAutoMigrateDaemon(ctx context.Context, configPath, baseDir, execToken string, bootstrap *logging.BootstrapLogger) {
	cfg, err := config.LoadConfigWithBaseDir(configPath, baseDir)
	if err != nil {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "daemon auto-migrate skipped: config load failed: %v", err)
		return
	}
	if cfg.SchedulerMode == "daemon" {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "daemon already active; no migration")
		return
	}
	if cfg.DaemonOptOut {
		bootstrap.Println("Daemon mode was previously removed (--daemon-remove); leaving the cron scheduler in place.")
		return
	}
	bootstrap.Println("Migrating to the resident daemon scheduler (proxsave-daemon.service)...")
	if err := applyDaemonMode(ctx, cfg, configPath, execToken, bootstrap); err != nil {
		bootstrap.Warning("Daemon migration failed; staying on cron: %v", err)
		return
	}
	bootstrap.Println("Daemon mode enabled: proxsave-daemon.service is active and the cron entry was removed.")
}

// setBackupEnvKeys reads backup.env, applies the given key=value edits (replacing
// or appending each), and writes it back atomically. utils.SetEnvValue preserves
// inline comments and ordering.
func setBackupEnvKeys(configPath string, kv map[string]string) error {
	// Operator-configured path (same trust level as the install/upgrade writers).
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	content := string(data)
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic apply order
	for _, k := range keys {
		content = utils.SetEnvValue(content, k, kv[k])
	}
	return writeConfigFile(configPath, configPath+".daemon.tmp", content)
}

// cronCorrectPaths returns the canonical command tokens that identify a proxsave
// cron line (the /usr/local/bin symlink and the resolved binary), used to drop the
// entry when switching to the daemon.
func cronCorrectPaths(execToken string) []string {
	paths := []string{daemonExecPath}
	if t := strings.TrimSpace(execToken); t != "" && t != daemonExecPath {
		paths = append(paths, t)
	}
	return paths
}

// removeCanonicalCronEntry drops every proxsave-owned cron line and writes the
// crontab back. A no-op (no matching line) does not touch the crontab.
func removeCanonicalCronEntry(ctx context.Context, correctPaths []string, bootstrap *logging.BootstrapLogger) error {
	lines, err := crontabReadLines(ctx)
	if err != nil {
		return err
	}
	kept := dropCanonicalCronLines(lines, correctPaths)
	if len(kept) == len(lines) {
		return nil
	}
	logging.DebugStepBootstrap(bootstrap, "daemon", "removing %d proxsave cron line(s)", len(lines)-len(kept))
	return crontabWriteLines(ctx, kept)
}

// crontabReadLines returns the current crontab as lines ("no crontab for" -> empty).
func crontabReadLines(ctx context.Context) ([]string, error) {
	cmd, err := safeexec.CommandContext(ctx, "crontab", "-l")
	if err != nil {
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "no crontab for") {
			return nil, nil
		}
		return nil, fmt.Errorf("crontab -l failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	if strings.TrimSpace(normalized) == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(normalized, "\n"), "\n"), nil
}

// crontabWriteLines installs the given crontab lines via `crontab -`.
func crontabWriteLines(ctx context.Context, lines []string) error {
	cmd, err := safeexec.CommandContext(ctx, "crontab", "-")
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab update failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
