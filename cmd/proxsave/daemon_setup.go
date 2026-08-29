// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/tis24dev/proxsave/internal/safefs"
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
	cronOutcome, err := applyDaemonMode(rt.ctx, rt.cfg, rt.args.ConfigPath, daemonSelfExecPath(), rt.bootstrap)
	if err != nil {
		logging.Error("daemon-setup failed: %v", err)
		return types.ExitGenericError.Int()
	}
	logging.Info("Daemon mode enabled: %s is active. %s", daemonUnitName, cronRemovalClause(cronOutcome))
	return types.ExitSuccess.Int()
}

func runDaemonRemove(rt *appRuntime) int {
	logging.Info("Removing ProxSave daemon mode and reverting to cron...")
	if err := applyCronMode(rt.ctx, rt.cfg, rt.args.ConfigPath, daemonSelfExecPath(), rt.bootstrap, true); err != nil {
		if errors.Is(err, errDaemonTeardownBackupRunning) {
			logging.Warning("daemon-remove deferred: a backup is in progress; the daemon was NOT removed. Retry when the backup finishes.")
			return types.ExitGenericError.Int()
		}
		if errors.Is(err, errDaemonTeardownConfigUnreadable) {
			logging.Error("daemon-remove aborted: the configuration could not be read; the daemon was NOT removed.")
			return types.ExitGenericError.Int()
		}
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
//
// It RETURNS what the cron removal actually did rather than swallowing it, because
// every caller ends by telling the operator that "the cron entry was removed" and that
// claim used to be printed whether or not a line had matched. On the hosts of issue #298
// nothing matched (the backup ran through a wrapper script whose command basename is not
// "proxsave"), the sentence was printed anyway, and the operator had no way to learn that
// the daemon had just been installed NEXT TO a still-live schedule. The outcome is
// returned instead of logged here so the dashboard - which mutes the global logger and the
// bootstrap console for the whole op and renders its own result screen - can say the same
// true thing the CLI says.
//
// This is an OPERATOR-INITIATED path (--daemon-setup, and the dashboard's install action),
// so a surviving wrapper entry is a loud warning and the migration proceeds: refusing here
// would leave a host that legitimately wants both with no way to enable the daemon at all.
// The UNATTENDED --upgrade retrofit refuses instead, before it ever reaches this function
// (maybeAutoMigrateDaemon).
func applyDaemonMode(ctx context.Context, cfg *config.Config, configPath, execToken string, bootstrap *logging.BootstrapLogger) (cronRemovalOutcome, error) {
	if err := installDaemonService(ctx, execToken, configPath, bootstrap); err != nil {
		return cronRemovalOutcome{}, err
	}
	cronOutcome, err := removeCanonicalCronEntry(ctx, cronCorrectPaths(execToken), bootstrap)
	if err != nil {
		logging.Warning("daemon: failed to remove the cron entry (possible double execution; the per-run lock mitigates): %v", err)
	}
	// #298: the removal above can only see cron lines whose COMMAND is named proxsave or
	// proxmox-backup. A wrapper entry survives it silently, and the daemon we just installed
	// now shares the night with it. Say so - and only say so; the wrapper is hand-written,
	// it can carry a mount guard, an flock and its own exit handling, and deleting it on a
	// name heuristic destroys a safety net we did not write.
	warnIndirectProxsaveCronOnDaemonInstall(ctx, bootstrap)
	// HEALTHCHECK_ENABLED=true matches the fresh-install default so a retrofitted
	// host also gets the dead-man switch out of the box (centralized resolves ping
	// URLs at runtime and degrades gracefully when unpaired).
	//
	// This OVERWRITES an operator's explicit HEALTHCHECK_ENABLED=false, and preserving that
	// value is deliberately OUT OF SCOPE rather than half-done. Telling "the operator typed
	// false" apart from "the template default landed here" needs KEY ABSENCE - the same
	// discriminator seedSchedulerTimeFromCrontab depends on (schedule_helpers.go: "'Never
	// set' is KEY ABSENCE ... which is why every caller runs this BEFORE the writer that
	// would materialize the template default"). On the auto-migration path that
	// discriminator is already gone: upgradeFinalizePhase merges the shipped template, which
	// carries HEALTHCHECK_ENABLED=false, BEFORE it calls maybeAutoMigrateDaemon, so the key
	// exists with value "false" on every upgraded host whether or not anyone chose it. The
	// only provenance signal that survives the merge, config.UpgradeResult.MissingKeys,
	// names the keys THAT merge injected: useful exactly once, on the first upgrade that
	// adds the block, and absent entirely on the --daemon-setup and dashboard paths. A rule
	// that honoured an operator's false only for hosts arriving from one particular version
	// would be worse than the honest behaviour here - choosing the daemon engine turns
	// monitoring on.
	//
	// What makes that acceptable is that the force is ONE-SHOT at the mode TRANSITION, not
	// sticky: maybeAutoMigrateDaemon returns early when SCHEDULER_MODE is already daemon, so
	// an operator who sets HEALTHCHECK_ENABLED=false AFTER migrating keeps it across every
	// later --upgrade, and applyCronMode now rolls the key back on the way out.
	if err := setBackupEnvKeys(configPath, map[string]string{
		"SCHEDULER_MODE":      "daemon",
		"DAEMON_OPT_OUT":      "false",
		"HEALTHCHECK_ENABLED": "true",
	}); err != nil {
		logging.Warning("daemon: failed to record SCHEDULER_MODE=daemon in %s: %v", configPath, err)
		return cronOutcome, nil
	}
	// One-shot relay-secret self-heal (hook a): a retrofitted centralized host can obtain the
	// healthcheck relay secret WITHOUT Telegram pairing now that the server issues it for a
	// chat-less known ServerID. Runs after HEALTHCHECK_ENABLED=true was written above but
	// BEFORE the try-restart below: persisting+confirming the secret first means the restarted
	// daemon finds it already on disk, so its startup self-heal (hook b) skips provisioning.
	// This avoids a concurrent double-issuance race (hook a and a HC-enabled restarted daemon
	// both minting a fresh secret, whose last-write-wins persist could leave the on-disk secret
	// and the server hash mismatched and the host stuck failing centralized auth). The
	// pre-restart daemon read config BEFORE the write above so it has HC disabled and never
	// runs hook b meanwhile. Best-effort (the daemon's startup self-heal retries if this misses,
	// e.g. the server change has not landed yet or the host is not known to the server yet).
	if cfg != nil {
		provisionRelaySecretOnDaemonSetup(ctx, configPath, cfg.BaseDir)
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
	return cronOutcome, nil
}

// verifyDaemonAlignedBestEffort waits (poll-only, no restart) for the just-(re)started daemon to
// become process-alive with an assessable alignment, then REPORTS its real state - aligned,
// behind, alignment-unverifiable, or not running - never a bare "timeout". It NEVER fails the
// caller (install / --daemon-setup): a behind or unconfirmed daemon is a warning, not an error.
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

// installVerifyVerdict maps a poll-only verify result (verifyDaemonAligned) to a
// (level, keyword) pair, shared by the log line (verifyDaemonAlignedBestEffort) and the
// graphical install outcome (buildInstallOutcomePrompt) so the two never diverge.
//
// It must NOT go through classifyRestartVerify. That classifier's success arm requires
// Restarted, which the poll-only verify never sets (verifyDaemonAligned builds its result
// from a zero value and only ever assigns State/ProcessAlive/Aligned/FreshInfo/TimedOut),
// so every poll-only result would land on a "restarted but..." arm - the mis-mapping that
// once made the install always say "not confirmed". Four of that classifier's six arms are
// structurally unreachable here (no Err, no LockPathUnknown, no BackupWaitTimedOut, no
// Restarted), and its shape has no BEHIND verdict at all, which is the one verdict an
// install most needs to report.
//
// This is NOT the same verdict --daemon-status gives: daemonStatusStyle turns green only on
// a FRESH HEARTBEAT and ignores alignment, whereas a just-restarted daemon is aligned long
// before it writes its first heartbeat. The two answer different questions and word their
// "behind" differently on purpose.
//
// The keyword is interpolated mid-sentence by the log line ("Daemon %s."), which is why it
// is lower case unlike the dashboard's ALL-CAPS keywords.
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
	case rv.ProcessAlive:
		// Alive, but the /proc alignment probe never returned a verdict for it, so alignment
		// is UNKNOWN (health.DaemonState gates every "behind" verdict on AlignChecked).
		// Reporting "not running" here was wrong twice over: the process IS running, and the
		// thing that could not be established was its ALIGNMENT, not its existence.
		return orchestrator.HealthcheckSetupLevelWarn, "running, but alignment could not be verified"
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

// Seams so a test can drive the daemon setup/teardown paths - applyCronMode's
// ordering, what the cron removal reports, and whether the --upgrade retrofit
// migrated at all - without touching the real crontab or the systemd unit.
var (
	removeDaemonServiceFn      = removeDaemonService
	migrateLegacyCronEntriesFn = migrateLegacyCronEntries
	// crontabReadLinesFn lets a test feed a synthetic crontab to the SCHEDULER_TIME
	// seeding (seedSchedulerTimeFromCrontab), to the cron removal
	// (removeCanonicalCronEntry) and to the wrapper detection
	// (detectIndirectProxsaveCron / existingWrapperCronFallback) without touching the
	// host's real one.
	crontabReadLinesFn = crontabReadLines
	// crontabWriteLinesFn is the write-side twin of crontabReadLinesFn, and it is not
	// optional now that removeCanonicalCronEntry reports a COUNT: a test has to feed a
	// synthetic crontab that actually contains a proxsave line to observe a non-zero
	// count, and without this seam that test would shell out to the real `crontab -` and
	// install the synthetic table on the machine running the suite. Only
	// removeCanonicalCronEntry goes through it; repointLegacyCronEntries still calls
	// crontabWriteLines directly.
	crontabWriteLinesFn = crontabWriteLines
	// wrapperCronLinesFn is the seam over the operator-wrapper detector: given the crontab
	// lines it returns the ones that already schedule ProxSave through a command this
	// codebase does not own (issue #298's /usr/local/sbin/proxsave-nas-guard). A NON-EMPTY
	// result means "positively identified"; an empty result means "none found, OR could not
	// be told", and every caller must treat those two identically - which is why the
	// detector needs no error return and why applyCronMode's fallback for empty is to keep
	// behaving exactly as it does today.
	wrapperCronLinesFn = wrapperCronLines
	// applyDaemonModeFn exists so a test can observe that the --upgrade retrofit
	// REFUSED (#298). Without it the only evidence of "did not migrate" is the systemd
	// unit install itself, which a unit test must not run, and a refusal that quietly
	// stopped refusing would look exactly like a pass. Only maybeAutoMigrateDaemon goes
	// through it; runDaemonSetup still calls applyDaemonMode directly, so overriding this
	// seam cannot accidentally neuter --daemon-setup in a test.
	applyDaemonModeFn = applyDaemonMode
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

// existingWrapperCronFallback returns the crontab lines that ALREADY schedule ProxSave
// through a command this codebase does not own: the operator wrapper of issue #298,
// "30 02 * * * /usr/local/sbin/proxsave-nas-guard", a script that verifies the final NAS
// mount is really CIFS/SMB before invoking ProxSave.
//
// Such a line is invisible to every other cron helper in this package, and that is by
// design, not by accident: commandTokenMatchesTarget matches the command token's BASENAME
// against "proxsave" and "proxmox-backup" and never scans the rest of the line, because a
// substring scan would delete an operator's "cp /usr/local/bin/proxsave /backup/" job. The
// price of that guarantee is that a wrapper is neither removed, nor read for
// SCHEDULER_TIME, nor counted as a proxsave schedule, so a caller that wants to know
// whether the host is already scheduled has to ask separately. That is this function.
//
// A read failure returns nil ON PURPOSE. The only caller's fallback for "nothing
// identified" is to write its own cron line, and being scheduled twice is a recoverable
// annoyance where being unscheduled is silent data loss - the same ordering F09-06 already
// encodes for the teardown.
func existingWrapperCronFallback(ctx context.Context) []string {
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		return nil
	}
	return wrapperCronLinesFn(lines)
}

// applyCronMode reverts an install to cron: make sure a cron schedule exists, record
// SCHEDULER_MODE=cron and HEALTHCHECK_ENABLED=false (plus DAEMON_OPT_OUT=true when optOut,
// the --daemon-remove tombstone that stops future upgrades from re-migrating), and only
// THEN remove the systemd unit. The cron fallback is established first so a teardown
// failure never leaves the host unscheduled with a stale mode=daemon (F09-06).
//
// "Make sure a cron schedule exists" is not always "append one": on a host already
// scheduled through an operator wrapper, appending would produce a SECOND nightly backup.
// See the cron-fallback block below.
//
// The HEALTHCHECK_ENABLED rollback is the exact mirror of the key applyDaemonMode forces
// on; see the write below for why it is not optional.
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

	// Establish the cron fallback FIRST: make sure a cron schedule exists and persist
	// SCHEDULER_MODE=cron before the daemon unit is removed (F09-06).
	//
	// Normally that means letting migrateLegacyCronEntries rewrite the crontab so it holds
	// exactly ONE canonical "<SCHEDULER_TIME> /usr/local/bin/proxsave --backup" line: it drops
	// every proxsave-owned entry it can see and appends a fresh one. On a host where ProxSave
	// is the only thing scheduling ProxSave, that append IS the revert and must keep happening.
	//
	// It is wrong on exactly one shape of host: one whose backup is already scheduled through
	// an operator WRAPPER (issue #298). buildReinstallCronLines cannot see that line, because
	// its command basename is not "proxsave" or "proxmox-backup", so the append does not
	// REPLACE the wrapper, it JOINS it. The host ends the revert with two scheduled backups,
	// both at SCHEDULER_TIME (02:00 by default), which is the very window the wrapper already
	// occupies: the second run then dies on the per-run lock with exit 16 and the host reports
	// a failed backup every night. This was the third duplicate source in #298, the one the
	// reporter hit AFTER reverting.
	//
	// So when a wrapper is positively identified we do not append. We run the REMOVAL instead,
	// which is not the same as doing nothing: it drops any canonical proxsave line the crontab
	// still carries, so a host with both a wrapper and a leftover proxsave entry also ends with
	// exactly ONE schedule - the operator's own, which is what it had before the migration and
	// what the operator wrote the wrapper for.
	//
	// Uncertainty falls back to the append on purpose: an unreadable crontab, or a detector
	// that identifies nothing, is treated as "no wrapper", because a double schedule is a
	// recoverable annoyance while an unscheduled host is silent data loss.
	if wrappers := existingWrapperCronFallback(ctx); len(wrappers) > 0 {
		logBootstrapInfo(bootstrap, "Reverting to cron: this host already schedules ProxSave through an entry ProxSave does not own, so no second cron line was added (that would have run the backup twice):")
		for _, line := range wrappers {
			logBootstrapInfo(bootstrap, "  %s", line)
		}
		logBootstrapInfo(bootstrap, "ProxSave does not manage that entry: it keeps its own run time and SCHEDULER_TIME does not apply to it.")
		outcome, err := removeCanonicalCronEntry(ctx, cronCorrectPaths(execToken), bootstrap)
		switch {
		case err != nil:
			logging.Warning("daemon: failed to drop the canonical proxsave cron line standing next to that entry (possible double execution; the per-run lock mitigates): %v", err)
		case outcome.Removed > 0:
			logBootstrapInfo(bootstrap, "Removed %d redundant proxsave cron line(s) that would have duplicated it.", outcome.Removed)
		}
	} else {
		migrateLegacyCronEntriesFn(ctx, cfg.BaseDir, execToken, bootstrap, cron.TimeToSchedule(cfg.SchedulerTime))
	}

	// HEALTHCHECK_ENABLED=false is the exact MIRROR of the write applyDaemonMode makes on
	// the way in, and it is not cosmetic. The resident daemon is the SOLE pinger, so a
	// cron-scheduled host transmits nothing whatever this key says (docs/HEALTHCHECKS.md:
	// "A host still on the cron scheduler reports nothing, no matter how the keys below are
	// set"; the template calls the whole block "daemon only"). Leaving the key true after a
	// revert is therefore not a harmless leftover but a claim the rest of the codebase then
	// acts on, because HEALTHCHECK_ENABLED=true is treated as a proxy for "this host runs
	// the daemon" in at least three places: installer.DeriveInstallWizardPrefill maps
	// enabled=false to HealthcheckMode "off", BuildHealthcheckSetupBootstrap skips on it
	// under the verdict whose declaration comment is literally "cron mode /
	// HEALTHCHECK_ENABLED=false", and the run-start init used to WARN "daemon not installed"
	// on EVERY cron run, promoting an otherwise clean backup to exit 1 (issue #298).
	//
	// It goes in the SAME setBackupEnvKeys call as the mode, deliberately: two writes could
	// leave a host recorded as cron while still claiming monitoring if the second one failed.
	//
	// The in-memory cfg.HealthcheckEnabled is NOT mirrored here. Both callers drop this cfg
	// immediately - runDaemonRemove returns into appRunState.finalize, which only stores the
	// exit code, and runDashboardDaemonAdmin re-reads backup.env on every later screen - and
	// applyDaemonMode does not mirror its own write either. Mutating the caller's config on
	// only one of the two paths would be the asymmetry, not the fix.
	kv := map[string]string{
		"SCHEDULER_MODE":      "cron",
		"HEALTHCHECK_ENABLED": "false",
	}
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

// backfillHealthcheckOptOut repairs, once, the on-disk state issue #298 left behind on
// every host that migrated to the daemon and then reverted with --daemon-remove.
//
// applyDaemonMode force-writes HEALTHCHECK_ENABLED=true, and applyCronMode used to record
// only SCHEDULER_MODE=cron, so the revert left the key asserting that daemon-only
// monitoring was on for a host that has no daemon and cannot transmit anything. The
// run-start init then read that as "monitoring is configured", found no unit, warned, and
// applyIssueExitCode promoted an otherwise clean backup to exit 1 on every single run.
// applyCronMode now rolls the key back, but that only helps a host that reverts from here
// on: the four hosts in the report already carry the stale value and would keep carrying it
// forever, because nothing they run ever writes that key again.
//
// The gate is deliberately the exact three-key shape that ONLY the broken transition can
// produce, never a two-key guess: cron engine, plus the --daemon-remove tombstone proving
// this host really did go daemon and come back, plus the key still true. A cron host that
// never touched the daemon has DAEMON_OPT_OUT=false and is not eligible, so an operator who
// deliberately enabled the key on a plain cron install is never touched. The residual case
// this does clobber is an operator who ran --daemon-remove and then re-enabled the key on
// purpose; the runtime gate in initializeHealthcheckSection already reports cron mode as a
// SKIP rather than a WARNING, so that key was doing nothing for them either way.
//
// Best-effort and silent-on-failure like every other write on this path: a --upgrade must
// never fail because a repair could not be applied, and the runtime gate keeps the host
// exiting 0 regardless of whether this lands.
func backfillHealthcheckOptOut(cfg *config.Config, configPath string, bootstrap *logging.BootstrapLogger) {
	if cfg == nil || cfg.SchedulerMode != "cron" || !cfg.DaemonOptOut || !cfg.HealthcheckEnabled {
		return
	}
	if err := setBackupEnvKeys(configPath, map[string]string{"HEALTHCHECK_ENABLED": "false"}); err != nil {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "healthcheck backfill skipped: %v", err)
		return
	}
	cfg.HealthcheckEnabled = false
	logBootstrapInfo(bootstrap, "Monitoring is reported by the resident daemon only, and this host reverted to cron: HEALTHCHECK_ENABLED was left at true by that revert and is now set to false in %s.", configPath)
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
		backfillHealthcheckOptOut(cfg, configPath, bootstrap)
		return
	}
	// #298: the crontab may still run ProxSave through a command the canonical matcher
	// cannot see (an operator wrapper, a shell -c, a runner like flock). Installing the
	// daemon on top of one gives two backups a night with no message at all, which is
	// the single outcome nobody can accept here. Removing that entry instead is NOT an
	// option this path may take: a wrapper is hand-written, it can carry a mount guard,
	// an flock, its own logging and its own exit handling, and an unattended upgrade is
	// the worst possible moment to guess. So refuse, name the exact line, and change
	// NOTHING - the host stays fully scheduled on cron and the operator decides.
	//
	// An unreadable crontab is deliberately not a refusal: a host with no cron installed
	// at all has nothing to collide with and must still be retrofitted.
	if refs, err := detectIndirectProxsaveCron(ctx); err != nil {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "daemon auto-migrate: the crontab could not be read, so the wrapper check was skipped: %v", err)
	} else if len(refs) > 0 {
		logBootstrapWarning(bootstrap, "Daemon auto-migration REFUSED: this host still schedules ProxSave through a command this upgrade does not own, and installing the daemon on top of it would run every backup twice (the loser of the race exits %d, backup skipped).", types.ExitBackupSkipped.Int())
		for _, line := range describeIndirectCronRefs(refs) {
			logBootstrapWarning(bootstrap, "  %s", line)
		}
		logBootstrapWarning(bootstrap, "Nothing was changed: this host stays on the cron scheduler and keeps backing up exactly as before.")
		logBootstrapWarning(bootstrap, "To migrate, remove or disable that entry (%s) and run 'proxsave --daemon-setup', which proceeds with a warning if the entry is intentional. To stop this check from running on every upgrade, set DAEMON_OPT_OUT=true in %s.", cronRefEditHint(refs), configPath)
		return
	}
	bootstrap.Println("Migrating to the resident daemon scheduler (" + daemonUnitName + ")...")
	cronOutcome, err := applyDaemonModeFn(ctx, cfg, configPath, execToken, bootstrap)
	if err != nil {
		bootstrap.Warning("Daemon migration failed; staying on cron: %v", err)
		return
	}
	// This host was on cron a moment ago (the SchedulerMode gate above proves it), so a
	// clause saying no proxsave cron entry was present is the operator's cue that something
	// OTHER than a proxsave-named command was scheduling the backup and is still scheduled.
	// The upgrade used to print a removal here unconditionally, which is what made the
	// #298 migration completely silent. Reaching this line at all means the refusal above
	// found nothing, so the clause is the only wrapper-shaped signal left on this path.
	bootstrap.Println("Daemon mode enabled: " + daemonUnitName + " is active. " + cronRemovalClause(cronOutcome))
}

// setBackupEnvKeys reads backup.env, applies the given key=value edits (replacing
// or appending each), and writes it back atomically. utils.SetEnvValue preserves
// inline comments and ordering.
func setBackupEnvKeys(configPath string, kv map[string]string) error {
	// Operator-configured path (same trust level as the install/upgrade writers).
	data, err := safefs.ReadFileUnderRoot(configPath)
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
	return installer.WriteConfigFileAtomic(configPath, configPath+".daemon.tmp", content)
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
		cronOutcome, err := removeCanonicalCronEntry(ctx, cronCorrectPaths(execInfo.ExecPath), bootstrap)
		if err != nil {
			logging.Warning("daemon: failed to remove the cron entry (the per-run lock mitigates double execution): %v", err)
		}
		// This reconcile installs the daemon just like applyDaemonMode does, and it calls the
		// removal DIRECTLY rather than through applyDaemonMode, so it needs the #298 wrapper
		// warning of its own: an install/reinstall that picked daemon mode on a wrapper host
		// would otherwise end double-scheduled and silent.
		warnIndirectProxsaveCronOnDaemonInstall(ctx, bootstrap)
		// `enable --now` does NOT restart an ALREADY-running daemon, so a reinstall/reconfigure
		// (or a rebuilt binary) would leave it on the OLD inode. Restart so the running process is
		// the freshly installed binary before we report alignment.
		if err := restartDaemonService(ctx); err != nil {
			logging.Debug("daemon: restart to load the installed binary failed: %v", err)
		}
		logging.Info("Daemon mode enabled: %s is active. %s", daemonUnitName, cronRemovalClause(cronOutcome))
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
	data, err := safefs.ReadFileUnderRoot(configPath)
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

// cronRemovalOutcome is what removeCanonicalCronEntry actually did to the crontab,
// carried back to the caller so the operator-facing "daemon mode enabled" report can
// STATE it instead of asserting it.
//
// It exists because the old signature returned only an error, and a nil error covered two
// opposite facts: "the proxsave cron line was deleted" and "nothing matched, the crontab
// was never touched". Every call site printed the first one. On the hosts of issue #298 the
// truth was the second: the backup was scheduled through an operator wrapper
// (/usr/local/sbin/proxsave-nas-guard) whose command basename is neither "proxsave" nor
// "proxmox-backup", so commandTokenMatchesTarget correctly refused to match it, nothing was
// removed, and the migration reported a removal anyway. The daemon was then installed next
// to a schedule the operator had just been told was gone, and both fired at 02:00.
type cronRemovalOutcome struct {
	// Removed is the number of proxsave-owned cron lines actually deleted.
	Removed int
	// Verified is true only when the crontab was READ and, when something matched,
	// successfully WRITTEN BACK. False means we cannot honestly say anything about this
	// host's crontab, which is NOT the same as "there was nothing to remove" and must
	// never be reported as if it were.
	Verified bool
}

// cronRemovalClause renders an outcome as one standalone sentence, so the three CLI
// reports (--daemon-setup, the --upgrade auto-migration, the install reconcile) and the
// dashboard result screen all state the same verified fact in the same words. Keeping the
// wording in ONE place is the same reason daemonUnitName is a constant: the operator must
// not be told two different things depending on which front-end they opened.
//
// The single-line case deliberately keeps the historic wording byte-for-byte. It is the
// overwhelmingly common outcome and it was never the untruthful one, so transcripts and
// operator greps that look for it keep working; only the cases that used to lie change.
//
// Zero removed is NOT phrased as a problem. It is the normal outcome of a fresh install
// that never had a cron line, and this sentence is printed on that path too, so it only has
// to be true. What turns it into a signal is its CONTEXT - a host that WAS on cron and
// suddenly has no proxsave cron entry - which is the caller's job to interpret.
func cronRemovalClause(outcome cronRemovalOutcome) string {
	switch {
	case !outcome.Verified:
		return "The crontab could not be checked, so a proxsave cron entry may still be scheduled alongside it."
	case outcome.Removed == 0:
		return "No proxsave cron entry was present to remove."
	case outcome.Removed == 1:
		return "The cron entry was removed."
	default:
		return fmt.Sprintf("%d proxsave cron entries were removed.", outcome.Removed)
	}
}

// removeCanonicalCronEntry drops every proxsave-owned cron line and writes the crontab
// back, REPORTING how many lines it removed. A no-op (no matching line) does not touch the
// crontab.
//
// What counts as "proxsave-owned" is dropCanonicalCronLines' rule: the cron line's COMMAND
// token, matched by basename against "proxsave" and "proxmox-backup". The rest of the line
// is never scanned, on purpose (see containsBinaryReference): a substring scan would delete
// an operator's "cp /usr/local/bin/proxsave /backup/" job. The cost of that guarantee is
// that a wrapper script is invisible here, which is exactly why the count is RETURNED
// rather than assumed by the caller.
//
// It reports the count and nothing else. It does NOT warn about a surviving wrapper, even
// though the lines are in hand here: applyCronMode calls this on the --daemon-remove path,
// where no daemon is being installed and such a warning would be wrong. The two daemon
// INSTALL paths call warnIndirectProxsaveCronOnDaemonInstall themselves.
//
// It reads and writes through crontabReadLinesFn / crontabWriteLinesFn rather than the raw
// functions so a test can drive both halves with a synthetic crontab and assert the reported
// count without a `crontab` binary on the machine running the suite.
func removeCanonicalCronEntry(ctx context.Context, correctPaths []string, bootstrap *logging.BootstrapLogger) (cronRemovalOutcome, error) {
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		return cronRemovalOutcome{}, err
	}
	kept := dropCanonicalCronLines(lines, correctPaths)
	removed := len(lines) - len(kept)
	if removed == 0 {
		return cronRemovalOutcome{Verified: true}, nil
	}
	logging.DebugStepBootstrap(bootstrap, "daemon", "removing %d proxsave cron line(s)", removed)
	if err := crontabWriteLinesFn(ctx, kept); err != nil {
		// The lines matched but the table was never installed. Report NOTHING removed and
		// NOTHING verified, so no caller can trade the old lie ("removed" when nothing
		// matched) for a new one ("nothing was present" when the write failed).
		return cronRemovalOutcome{}, err
	}
	return cronRemovalOutcome{Removed: removed, Verified: true}, nil
}

// crontabReadLines returns the current crontab as lines ("no crontab for" -> empty).
func crontabReadLines(ctx context.Context) ([]string, error) {
	cmd, err := safeexec.CommandContext(ctx, "crontab", "-l")
	if err != nil {
		return nil, err
	}
	safeexec.ApplyWaitDelay(cmd)
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
	// DELIBERATELY NOT safeexec.ApplyWaitDelay, unlike crontabReadLines above it. The
	// asymmetry is the point, and it is not an omission: a reader has nothing to lose when
	// its drain is cut, a writer has the operator's crontab.
	//
	// WaitDelay bounds the DRAIN, and cutting the drain calls closeDescriptors, which
	// SIGPIPEs anything still holding the inherited pipes. If a descendant is merely wedged
	// that costs nothing, but if it is still WORKING it is killed mid-install. Measured on
	// this exact frame: with the bound the table was NEVER WRITTEN where the unbounded run
	// installed it correctly in eight seconds, and with a payload past the 64 KiB pipe
	// buffer the bound truncated it to exactly 65536 bytes. A working descendant is the
	// more likely of the two, so the bound would trade a rare hang for a likelier silent
	// loss of the operator's table.
	//
	// The error would not save us either. A descendant holding the stdin READ end leaves
	// crontab installing a TRUNCATED table and still exiting 0, and that case is
	// indistinguishable here from a healthy one: same ErrWaitDelay, same empty capture (a
	// real crontab prints nothing on success), same elapsed time. Bounding this frame
	// safely needs a READ BACK through crontabReadLines, not a timer.
	//
	// What remains unbounded is a wedged descendant holding the pipes for its whole life.
	// Stock cron ships crontab as a single setuid binary that forks nothing, so neither
	// that stall nor the damage above can arise on a stock host; both need a crontab that
	// leaves children behind.
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab update failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
