// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// dispatchDaemonAdminMode handles the one-shot --daemon-setup / --daemon-remove admin
// commands (switch the scheduler engine and the systemd unit / cron entry) and the
// read-only --daemon-status report.
func dispatchDaemonAdminMode(rt *appRuntime) modeResult {
	switch {
	case rt.args.DaemonSetup:
		return modeResult{exitCode: runDaemonSetup(rt), handled: true}
	case rt.args.DaemonRemove:
		return modeResult{exitCode: runDaemonRemove(rt), handled: true}
	case rt.args.DaemonStatus:
		return modeResult{exitCode: runDaemonStatus(rt), handled: true}
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

// runDaemonStatus prints the resident daemon's real state - the SAME combined verdict the dashboard
// "Daemon status" screen shows (systemd presence refined with the heartbeat + the on-disk binary
// alignment) - non-interactively, then exits. Exit 0 when the daemon is running and aligned,
// non-zero otherwise, so scripts can gate on it.
func runDaemonStatus(rt *appRuntime) int {
	ctx := rt.ctx
	mode := "unknown"
	optOut := "unknown"
	baseDir := ""
	var interval time.Duration
	if rt.cfg != nil {
		mode = rt.cfg.SchedulerMode
		optOut = "no"
		if rt.cfg.DaemonOptOut {
			optOut = "yes"
		}
		interval = rt.cfg.HealthcheckHeartbeatInterval
		baseDir = strings.TrimSpace(rt.cfg.BaseDir)
	}
	if baseDir == "" {
		baseDir, _ = detectedBaseDirOrFallback()
	}
	unit := "not installed"
	if daemonUnitInstalled() {
		unit = "installed"
	}
	active := daemonUnitActiveState(ctx)
	if active == "" {
		active = "unknown"
	}
	ds := health.CheckDaemonState(health.DaemonStateInput{
		BaseDir:           baseDir,
		SchedulerMode:     mode,
		HeartbeatInterval: interval,
		Now:               time.Now(),
		Presence:          daemonPresenceProbe(ctx),
		ProcAlive:         probeProxsaveDaemonAlive,
		ProcStale:         procBinaryStaleProbe,
	})
	level, keyword, _ := daemonStatusStyle(ds)

	logging.Info("Daemon status: %s", keyword)
	logging.Info("Scheduler mode: %s", mode)
	logging.Info("Daemon service (%s): %s", daemonUnitName, unit)
	logging.Info("Service state (systemctl is-active): %s", active)
	logging.Info("Opted out of auto-migration (--daemon-remove): %s", optOut)
	if ds.HaveInfo {
		logging.Info("Running version: %s (%s)", ds.Version, ds.Commit)
	}
	if ds.HaveInfo || ds.AlignChecked {
		align := "unknown"
		if ds.AlignChecked {
			if ds.Aligned {
				align = "aligned"
			} else {
				align = "BEHIND (restart needed)"
			}
		}
		logging.Info("Binary alignment: %s", align)
	}
	if level == orchestrator.HealthcheckSetupLevelOk {
		return types.ExitSuccess.Int()
	}
	return types.ExitGenericError.Int()
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
	// HEALTHCHECK_ENABLED=true matches the fresh-install default so a retrofitted
	// host also gets the dead-man switch out of the box (centralized resolves ping
	// URLs at runtime and degrades gracefully when unpaired).
	if err := setBackupEnvKeys(configPath, map[string]string{
		"SCHEDULER_MODE":      "daemon",
		"DAEMON_OPT_OUT":      "false",
		"HEALTHCHECK_ENABLED": "true",
	}); err != nil {
		logging.Warning("daemon: failed to record SCHEDULER_MODE=daemon in %s: %v", configPath, err)
		return nil
	}
	// installDaemonService already `enable --now`-started the daemon, but it read
	// the config as it was BEFORE the write above. Restart it (only if running) so
	// the resident process picks up HEALTHCHECK_ENABLED=true immediately instead of
	// at the next reboot/upgrade. Config-write-first ordering is avoided so a failed
	// unit install can't leave SCHEDULER_MODE=daemon with no unit (which would make
	// a later --upgrade skip re-migration).
	if err := runSystemctl(ctx, "try-restart", daemonUnitName); err != nil {
		logging.Debug("daemon: try-restart to reload config failed: %v", err)
	}
	// Confirm the (re)started daemon actually came up ALIGNED with the binary now on
	// disk before returning success. Best-effort: an unconfirmed alignment is only a
	// warning (never fails --daemon-setup / the migration).
	if cfg != nil && strings.TrimSpace(cfg.BaseDir) != "" {
		verifyDaemonAlignedBestEffort(ctx, cfg.BaseDir, cfg.HealthcheckHeartbeatInterval)
	}
	return nil
}

// verifyDaemonAlignedBestEffort waits (poll-only, no restart) for the just-(re)started daemon to
// become process-alive with an assessable alignment, then REPORTS its real state - the SAME verdict
// --daemon-status gives (aligned / behind / not running) - never a bare "timeout". It NEVER fails
// the caller (install / --daemon-setup): a behind or unconfirmed daemon is a warning, not an error.
func verifyDaemonAlignedBestEffort(ctx context.Context, baseDir string, interval time.Duration) RestartVerifyResult {
	logging.Info("Verifying daemon alignment...")
	rv := verifyDaemonAligned(ctx, baseDir, interval)
	if level, keyword := installVerifyVerdict(rv); level == orchestrator.HealthcheckSetupLevelOk {
		logging.Info("Daemon verified: %s.", keyword)
	} else {
		logging.Warning("Daemon %s.", keyword)
	}
	return rv
}

// installVerifyVerdict maps a poll-only verify result (verifyDaemonAligned) to the
// aligned / behind / not-running verdict as a (level, keyword) pair - the SAME verdict
// --daemon-status reports. Shared by the log line (verifyDaemonAlignedBestEffort) and the
// graphical install outcome (buildInstallOutcomePrompt) so they never diverge. It must NOT
// go through restartVerifyStatus, whose success arm needs Restarted/FreshInfo that the
// poll-only verify never sets - that mis-mapping made the install always say "not confirmed".
func installVerifyVerdict(rv RestartVerifyResult) (orchestrator.HealthcheckSetupLevel, string) {
	switch {
	case rv.ProcessAlive && rv.Aligned:
		keyword := "running and aligned"
		if v := strings.TrimSpace(rv.State.Version); v != "" {
			keyword += " (v" + v + ")"
		}
		return orchestrator.HealthcheckSetupLevelOk, keyword
	case rv.ProcessAlive && rv.State.AlignChecked:
		return orchestrator.HealthcheckSetupLevelWarn, "running but not aligned (behind)"
	default:
		return orchestrator.HealthcheckSetupLevelWarn, "not running"
	}
}

// installVerifyContext resolves the base dir + heartbeat interval for a post-install
// daemon verify from the just-written config (best-effort; ok=false when unreadable).
func installVerifyContext(configPath string) (baseDir string, interval time.Duration, ok bool) {
	detected, _ := detectedBaseDirOrFallback()
	cfg, err := config.LoadConfigWithBaseDir(configPath, detected)
	if err != nil || cfg == nil {
		return "", 0, false
	}
	baseDir = detected
	if strings.TrimSpace(cfg.BaseDir) != "" {
		baseDir = cfg.BaseDir
	}
	return baseDir, cfg.HealthcheckHeartbeatInterval, true
}

// Seams so a test can drive applyCronMode's ordering without touching the real crontab
// or systemd unit.
var (
	removeDaemonServiceFn      = removeDaemonService
	migrateLegacyCronEntriesFn = migrateLegacyCronEntries
)

var (
	// errDaemonTeardownBackupRunning defers a daemon revert because a backup is still running
	// after the bounded wait; the caller reports it and leaves the daemon in place (never killing it).
	errDaemonTeardownBackupRunning = errors.New("a backup is in progress; the daemon was not removed")
	// errDaemonTeardownConfigUnreadable defers a daemon revert because the config (and thus the
	// real backup lock path) could not be read; fail-closed so a backup on a custom LOCK_PATH is
	// never killed blindly.
	errDaemonTeardownConfigUnreadable = errors.New("the configuration could not be read; the daemon was not removed")
)

// applyCronMode reverts an install to cron: re-add the canonical cron entry at the
// configured schedule, record SCHEDULER_MODE=cron (plus DAEMON_OPT_OUT=true when optOut,
// the --daemon-remove tombstone that stops future upgrades from re-migrating), and only
// THEN remove the systemd unit. The cron fallback is established first so a teardown
// failure never leaves the host unscheduled with a stale mode=daemon (F09-06).
func applyCronMode(ctx context.Context, cfg *config.Config, configPath, execToken string, bootstrap *logging.BootstrapLogger, optOut bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// F09-05: never tear down the daemon on top of a running, daemon-supervised backup
	// (removeDaemonService stops the unit, killing it). Mirror the restart guard (F11-08):
	// resolve the REAL backup lock path and wait bounded for idle. If the config is unreadable
	// or a backup will not free, abort the revert (nothing changed) so the caller can retry.
	if cfg == nil {
		return errDaemonTeardownConfigUnreadable
	}
	lockPath, lockKnown := backupLockFilePath(cfg, cfg.BaseDir)
	if !lockKnown {
		return errDaemonTeardownConfigUnreadable
	}
	if waitForBackupIdle(ctx, lockPath) {
		return errDaemonTeardownBackupRunning
	}

	// Establish the cron fallback FIRST: re-add the canonical cron line and persist
	// SCHEDULER_MODE=cron before removing the daemon unit.
	migrateLegacyCronEntriesFn(ctx, cfg.BaseDir, execToken, bootstrap, cron.TimeToSchedule(cfg.SchedulerTime))

	kv := map[string]string{"SCHEDULER_MODE": "cron"}
	if optOut {
		kv["DAEMON_OPT_OUT"] = "true"
	}
	if err := setBackupEnvKeys(configPath, kv); err != nil {
		logging.Warning("daemon: failed to record cron mode in %s: %v", configPath, err)
	}
	// Teardown last: a failure here leaves the host cron-scheduled with mode=cron, never
	// unscheduled+stale. The per-run lock mitigates the transient double-schedule window.
	return removeDaemonServiceFn(ctx, bootstrap)
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

// reconcileSchedulerAfterInstall makes the scheduler engine a MUTUALLY EXCLUSIVE
// choice after an install/reinstall (which always (re)writes the cron line). It
// takes the mode the wizard picked; when empty (keep-existing / skipped wizard)
// it reads the mode from the just-written config. daemon -> install the unit and
// drop the cron line; cron -> tear down any leftover daemon unit so a re-install
// of a previously-daemon host can never end up double-scheduled (cron + unit).
//
// It returns the daemon restart-verify result and verified=true ONLY in the
// daemon branch that actually ran verifyDaemonAlignedBestEffort; every other
// path (install failed, verify context unreadable, or cron mode) returns a
// zero result with verified=false. The existing statement call sites discard
// both returns; only the TUI finalization captures them to render the outcome.
func reconcileSchedulerAfterInstall(ctx context.Context, wizardMode, configPath string, execInfo ExecInfo, bootstrap *logging.BootstrapLogger) (rv RestartVerifyResult, verified bool) {
	mode := strings.ToLower(strings.TrimSpace(wizardMode))
	if mode != "cron" && mode != "daemon" {
		mode = readConfiguredSchedulerMode(configPath)
	}

	if mode == "daemon" {
		if err := installDaemonService(ctx, daemonExecPath, configPath, bootstrap); err != nil {
			logging.Warning("Failed to enable the daemon service (staying on cron): %v", err)
			return RestartVerifyResult{}, false
		}
		if err := removeCanonicalCronEntry(ctx, cronCorrectPaths(execInfo.ExecPath), bootstrap); err != nil {
			logging.Warning("daemon: failed to remove the cron entry (the per-run lock mitigates double execution): %v", err)
		}
		// `enable --now` does NOT restart an ALREADY-running daemon, so a reinstall/reconfigure
		// (or a rebuilt binary) would leave it on the OLD inode. Restart so the running process is
		// the freshly installed binary before we report alignment.
		if err := restartDaemonService(ctx); err != nil {
			logging.Debug("daemon: restart to load the installed binary failed: %v", err)
		}
		logging.Info("Daemon mode enabled: %s is active and the cron entry was removed.", daemonUnitName)
		// Report the daemon's real state (aligned / behind / not running), best-effort
		// (a verify miss is only logged, never fails the install).
		if baseDir, interval, ok := installVerifyContext(configPath); ok {
			return verifyDaemonAlignedBestEffort(ctx, baseDir, interval), true
		}
		return RestartVerifyResult{}, false
	}

	// cron mode: a previously-installed daemon unit would double-schedule with the
	// cron line just written, so remove it. Gate on the unit FILE existing (not just
	// is-active) so an enabled-but-currently-stopped unit is also torn down, and a
	// host that never had a daemon skips the systemctl calls entirely.
	if daemonUnitInstalled() {
		if err := removeDaemonService(ctx, bootstrap); err != nil {
			logging.Warning("daemon: a previous daemon unit could not be removed (possible double execution): %v", err)
		} else {
			logging.Info("Removed the previous daemon service; this host now uses the cron scheduler.")
		}
	}
	return RestartVerifyResult{}, false
}

// readConfiguredSchedulerMode returns "daemon" or "cron" from an existing
// backup.env (default cron). Used for the keep-existing install path where the
// wizard did not collect a mode.
func readConfiguredSchedulerMode(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "cron"
	}
	if strings.EqualFold(strings.TrimSpace(installer.DeriveInstallWizardPrefill(string(data)).SchedulerMode), "daemon") {
		return "daemon"
	}
	return "cron"
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
