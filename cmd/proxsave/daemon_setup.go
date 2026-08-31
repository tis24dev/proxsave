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

// runDaemonSetup and runDaemonRemove pass a NIL bootstrap on purpose, and it is the one
// thing to preserve if either signature is ever touched.
//
// Both run inside runRuntime, i.e. AFTER bootstrapRuntime called bootstrap.Flush. A flushed
// BootstrapLogger is a dead end: it still prints to the console, but it has set flushed=true
// and nothing will ever replay what it records afterwards, so every line the apply* helpers
// emitted through it stayed out of the run log, out of the warning count and out of the
// final WARNINGS/ERRORS recap - while the two logging.Info lines framing them, three lines
// apart on screen, went to all three. That is how the #298 advisory came to be the only
// message of the operation with no trace on disk.
//
// logBootstrapInfo / logBootstrapWarning fall through to logging.Info / logging.Warning on a
// nil bootstrap, which is exactly the sink everything else after the pre-run checks uses.
// The --upgrade paths keep passing a real bootstrap: they run in dispatchPreRuntimeModes,
// before the run logger exists at all, where the bootstrap IS the only sink.
func runDaemonSetup(rt *appRuntime) int {
	logging.Info("Enabling ProxSave daemon mode...")
	cronOutcome, err := applyDaemonMode(rt.ctx, rt.cfg, rt.args.ConfigPath, daemonSelfExecPath(), nil)
	if err != nil {
		logging.Error("daemon-setup failed: %v", err)
		return types.ExitGenericError.Int()
	}
	logging.Info("Daemon mode enabled: %s is active. %s", daemonUnitName, cronRemovalClause(cronOutcome))
	return types.ExitSuccess.Int()
}

func runDaemonRemove(rt *appRuntime) int {
	logging.Info("Removing ProxSave daemon mode and reverting to cron...")
	revert, err := applyCronMode(rt.ctx, rt.cfg, rt.args.ConfigPath, daemonSelfExecPath(), nil)
	if err != nil {
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
	if !revert.CronScheduled {
		// Short on purpose: the reason is already on screen, as the WARNING
		// migrateLegacyCronEntries emits at the point it gave up.
		logging.Error("Cron write failed.")
		return types.ExitGenericError.Int()
	}
	logging.Info("Daemon removed: reverted to the cron scheduler. %s", cronModeRecordClause(revert))
	return types.ExitSuccess.Int()
}

// canonicalCronLinePresent reads the root crontab and reports whether a proxsave cron line is
// actually in it, and SEPARATELY whether it could look at all.
//
// The two answers cannot be one. An unreadable crontab is not evidence of a schedule - a host with
// no cron binary is precisely the host this check exists for - but it is not evidence of the
// absence of one either, and a caller handed a bare false said exactly that: the revert screen
// denied any schedule on a host where nobody had measured. Reporting the verification alongside
// the answer is the same shape cronRemovalOutcome.Verified already uses for the same reason.
func canonicalCronLinePresent(ctx context.Context) (present, verified bool) {
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		return false, false
	}
	for _, line := range lines {
		if commandTokenMatchesTarget(strings.Trim(cronCommandToken(line), "\"'")) {
			return true, true
		}
	}
	return false, true
}

// cronModeRecordClause renders whether the revert's config write landed, as one standalone
// sentence both front-ends use.
//
// The recorded case is what tells the operator no later upgrade will reinstall the daemon, so
// the failed case may not borrow its wording: the unit is gone either way, but a host whose
// backup.env still says daemon is a host the runtime will keep reading as daemon-scheduled.
func cronModeRecordClause(revert cronRevertReport) string {
	if revert.ModeRecorded {
		return "SCHEDULER_MODE=cron is recorded in the configuration, so upgrades leave the scheduler as it is."
	}
	return "The configuration could NOT be updated, so it still records the daemon engine while no daemon is installed."
}

// runDaemonStatus prints the resident daemon's real state - the SAME combined verdict the dashboard
// "Daemon status" screen shows (systemd presence refined with the heartbeat + the on-disk binary
// alignment) - non-interactively, then exits. Exit 0 when the daemon is running and aligned,
// non-zero otherwise, so scripts can gate on it.
func runDaemonStatus(rt *appRuntime) int {
	ctx := rt.ctx
	mode := "unknown"
	baseDir := ""
	var interval time.Duration
	if rt.cfg != nil {
		mode = rt.cfg.SchedulerMode
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

// prepareCronHandoverForDaemon is the cron side of the switch to the daemon: adopt the run time
// the host is actually using, delete the proxsave cron lines, and count what still schedules
// ProxSave afterwards.
//
// It is a separate function because everything ELSE in applyDaemonMode shells out - systemctl,
// the relay-secret provisioning, the alignment probe - so applyDaemonMode cannot be driven by a
// test. These three steps only touch the crontab and backup.env, both of which have seams, and
// each of them carries a guarantee that was pinned by nothing while it lived inline: the
// ADOPTION could be deleted, the ORDER could be inverted, and the COUNT could be dropped, and
// the suite stayed green through all three.
//
// The order is the guarantee that matters most. The proxsave cron line is the only record of
// when a cron host actually runs its backup - SCHEDULER_TIME is a leftover nothing keeps in step
// - so the adoption has to read it BEFORE the removal deletes it. Inverted, a host whose cron
// line said 21:00 comes out of here running at whatever the key still held.
func prepareCronHandoverForDaemon(ctx context.Context, configPath, execToken string, bootstrap *logging.BootstrapLogger) cronRemovalOutcome {
	// One read BEFORE anything is written. An unreadable crontab is then known while the config
	// is still untouched, instead of being met for the first time by a step that has already
	// changed something.
	//
	// It is not the only read of the handover: removeCanonicalCronEntry reads again to compute
	// what to keep, and the detector reads again to count what survives. What this one buys is
	// the ORDER - the guard runs before the adoption writes - and the fact that the hour adopted
	// comes from a snapshot taken before any of it.
	//
	// It is not a refusal either. A host with no cron installed at all has nothing to hand over
	// and must still get its daemon, so this returns an unverified outcome and the caller
	// carries on.
	lines, err := crontabReadLinesFn(ctx)
	if err != nil {
		logging.Warning("daemon: the crontab could not be read, so no cron entry was inspected or removed: %v", err)
		return cronRemovalOutcome{}
	}
	adoptSchedulerTimeForDaemon(configPath, lines, bootstrap)
	outcome, err := removeCanonicalCronEntry(ctx, cronCorrectPaths(execToken), bootstrap)
	if err != nil {
		logging.Warning("daemon: failed to remove the cron entry (possible double execution; the per-run lock mitigates): %v", err)
	}
	// A proxsave cron line that is still there after the removal is what matters, and whether an
	// hour was ADOPTED is not the same question. An adoption only happens when the hour changes,
	// so on the host ProxSave installed itself - SCHEDULER_TIME already equal to the line's hour
	// - nothing is adopted, and gating on that said nothing on exactly the host where the
	// surviving line and the daemon are guaranteed to share the minute.
	//
	// The hour stays either way: SCHEDULER_TIME is ProxSave's own variable and a failed crontab
	// write is no reason to rewrite it. Only removing the line fixes anything here.
	if hhmm, ok := schedulerTimeFromCronLines(lines); ok && (err != nil || !outcome.Verified || outcome.Removed == 0) {
		reportUnremovedCronEntry(hhmm, bootstrap)
	}
	// #298: the removal above can only see cron lines whose COMMAND is named proxsave or
	// proxmox-backup. A wrapper entry survives it silently, and the daemon being installed now
	// shares the night with it. Say so - and only say so; the wrapper is hand-written, it can
	// carry a mount guard, an flock and its own exit handling, and deleting it on a name
	// heuristic destroys a safety net we did not write.
	outcome.UnmanagedSchedules = warnIndirectProxsaveCronOnDaemonInstall(ctx, bootstrap)
	return outcome
}

// applyDaemonMode switches an install to the resident daemon: install the systemd
// unit, remove the canonical cron entry (no double execution), and record
// SCHEDULER_MODE=daemon. The unit install is the critical
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
	cronOutcome := prepareCronHandoverForDaemon(ctx, configPath, execToken, bootstrap)
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
	// systemCronProxsaveRefsFn is the seam over the SYSTEM-cron habitat: /etc/crontab, the
	// active entries of /etc/cron.d, and the executable entries of the four run-parts
	// directories. It exists for two reasons, and the second is the one that made it
	// non-optional.
	//
	// First, applyCronMode has to reach that habitat to tell the truth on a revert: the
	// detector could already see a wrapper there, but the path that writes the cron line
	// could not, so --daemon-remove appended a second nightly backup and said nothing.
	//
	// Second: systemCronPaths points at the REAL /etc. Without this seam every test that
	// calls applyCronMode would start reading the /etc of the machine running `go test` -
	// seven of them today - and would still pass on a developer box, so the day it began
	// reporting a stranger's cron.d, or hanging on their command path, the suite would be
	// the last thing to notice. Closing that deliberately is cheaper than discovering it.
	//
	// It is a DATA seam, not a logging one, so a test can feed a synthetic finding and
	// assert the exact sentence the operator gets. Unlike wrapperCronLinesFn its result
	// decides NOTHING: applyCronMode writes the same cron line whether it is empty or not.
	systemCronProxsaveRefsFn = systemCronProxsaveRefs
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

// cronRevertReport is what a revert learned that a CALLER may still need to say. Today it
// carries one thing: the /etc schedule advisory, already emitted here through the bootstrap
// logger for the CLI.
//
// It exists because that channel does not reach everyone. runDashboardDaemonAdmin mutes the
// global logger and sets the bootstrap console quiet for the whole operation and never
// flushes it, so on the TUI the advisory was recorded and dropped and the operator saw a
// green "REVERTED TO CRON" over a host that may now be scheduled twice. That is the same
// shape of defect as issue #298 itself: the front-end asserting an outcome the code had not
// established. Returning the lines rather than logging louder keeps the decision about how
// to present them with whoever owns the screen.
type cronRevertReport struct {
	// SystemCronAdvisory is the operator-facing notice about a ProxSave schedule found
	// under /etc, or nil when none was found. Lines are pre-rendered and must be printed
	// with "%s": a crontab line may contain a literal "%".
	SystemCronAdvisory []string
	// UnmanagedAdvisory is the operator-facing notice about the ROOT CRONTAB entries that still
	// schedule ProxSave next to the line this revert just wrote, or nil when none were found.
	// Same shape and same rule as SystemCronAdvisory: pre-rendered, printed with "%s".
	//
	// The revert creates that duplicate deliberately - withholding its line would leave a
	// misidentified host with nothing scheduled - so it has to be reported, and on the dashboard
	// the result screen is the only channel: the logger is muted for the whole operation and
	// never flushed.
	//
	// It carries the LINES and not a count, because the two habitats are the same kind of finding
	// and the screen says "what still schedules this host is below". Carrying a count for one and
	// lines for the other made that sentence false for the count: there was nothing below.
	UnmanagedAdvisory []string
	// CronVerified is true only when the crontab was actually READ. False means the answer in
	// CronScheduled is not a measurement, and a caller must not turn it into one in either
	// direction.
	CronVerified bool
	// CronScheduled is true only when a canonical proxsave cron line is READ BACK from the
	// crontab after the write. migrateLegacyCronEntries is void and has four early returns that
	// write nothing - unknown exec path, no binary to point at, `crontab -l` failing, `crontab
	// -` failing - and none of them is visible to this function, which then removes the daemon
	// unit anyway. A host with a broken or absent cron ended with no unit and no cron line at
	// exit 0, and the run that would have noticed is the backup that was never scheduled.
	//
	// Read back rather than returned, for the reason cronRemovalOutcome.Verified exists: what
	// matters is whether the line is there, not whether the writer believed it wrote one.
	CronScheduled bool
	// ModeRecorded is true only when SCHEDULER_MODE=cron actually reached the config file.
	// The write is best-effort - the daemon is already being torn down and a failure here must
	// not fail the revert - so the callers' closing message has to state which of the two
	// happened rather than assert the good one. That assertion is what tells the operator no
	// later upgrade will reinstall the daemon, and on a read-only or full filesystem it was
	// false: the same shape as issue #298, an outcome reported that the code had not
	// established.
	ModeRecorded bool
}

// applyCronMode reverts an install to cron: make sure a cron schedule exists, record
// SCHEDULER_MODE=cron and HEALTHCHECK_ENABLED=false, and only THEN remove the systemd unit.
//
// It used to write a third key, DAEMON_OPT_OUT=true, whose only job was to stop the next
// --upgrade from re-migrating a host that had just chosen cron. That key is gone: the retrofit
// now honours SCHEDULER_MODE because it is PRESENT, so the mode written here is itself the
// record, and nothing has to contradict it. The cron fallback is established first so a teardown
// failure never leaves the host unscheduled with a stale mode=daemon (F09-06).
//
// "Make sure a cron schedule exists" is not always "append one": on a host already
// scheduled through an operator wrapper, appending would produce a SECOND nightly backup.
// See the cron-fallback block below.
//
// The HEALTHCHECK_ENABLED rollback is the exact mirror of the key applyDaemonMode forces
// on; see the write below for why it is not optional.
func applyCronMode(ctx context.Context, cfg *config.Config, configPath, execToken string, bootstrap *logging.BootstrapLogger) (cronRevertReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// F09-05: never tear down the daemon on top of a running, daemon-supervised backup
	// (removeDaemonService stops the unit, killing it). Mirror the restart guard (F11-08):
	// resolve the REAL backup lock path and wait bounded for idle. If the config is unreadable
	// or a backup will not free, abort the revert (nothing changed) so the caller can retry.
	if cfg == nil {
		return cronRevertReport{}, errDaemonTeardownConfigUnreadable
	}
	lockPath, lockKnown := backupLockFilePath(cfg, cfg.BaseDir)
	if !lockKnown {
		return cronRevertReport{}, errDaemonTeardownConfigUnreadable
	}
	if waitForBackupIdle(ctx, lockPath) {
		return cronRevertReport{}, errDaemonTeardownBackupRunning
	}

	// Establish the cron fallback FIRST: make sure a cron schedule exists and persist
	// SCHEDULER_MODE=cron before the daemon unit is removed (F09-06).
	//
	// Normally that means letting migrateLegacyCronEntries rewrite the crontab so it holds
	// exactly ONE canonical "<SCHEDULER_TIME> /usr/local/bin/proxsave --backup" line: it drops
	// every proxsave-owned entry it can see and appends a fresh one. On a host where ProxSave
	// is the only thing scheduling ProxSave, that append IS the revert and must keep happening.
	//
	// It JOINS rather than replaces on exactly one shape of host: one whose backup is already
	// scheduled through an operator WRAPPER (issue #298). buildReinstallCronLines cannot see
	// that line, because its command basename is not "proxsave" or "proxmox-backup", so such a
	// host ends the revert with two scheduled backups, both at SCHEDULER_TIME (02:00 by
	// default), which is the very window the wrapper already occupies: the second run dies on
	// the per-run lock with exit 16 and the host reports a failed backup every night. That was
	// the third duplicate source in #298, the one the reporter hit AFTER reverting.
	//
	// The canonical line is written UNCONDITIONALLY, and anything the detector found is
	// reported next to it rather than acted on.
	//
	// This branch used to withhold the line whenever the detector saw an operator command that
	// might run ProxSave, so as not to create a second nightly backup. Its stated worst case
	// was "the host keeps the proxsave line it already had", and that was wrong for every
	// caller: applyDaemonMode deletes every proxsave-named cron line on the way INTO daemon
	// mode, so a host arriving here has none. One false positive then left it with no daemon,
	// no cron line, exit 0, and nothing on the host able to notice - the backup that would have
	// noticed is the one that was never scheduled.
	//
	// The detector's rules answer "is this named after proxsave", not "does this run a proxsave
	// backup". A script that merely mentions /opt/proxsave, anything stored under a directory
	// called proxsave and any unreadable file called proxsave-something all match. Deciding a
	// host's only schedule on that is the wrong side of the project's own ranking, stated at
	// cron_indirect_refs.go and again for the /etc habitat below: a double schedule is a
	// recoverable annoyance the operator can see, an unscheduled host is silent data loss.
	migrateLegacyCronEntriesFn(ctx, cfg.BaseDir, execToken, bootstrap, cron.TimeToSchedule(cfg.SchedulerTime))
	cronScheduled, cronVerified := canonicalCronLinePresent(ctx)
	var unmanagedAdvisory []string
	if wrappers := existingWrapperCronFallback(ctx); len(wrappers) > 0 {
		unmanagedAdvisory = append(unmanagedAdvisory, fmt.Sprintf("Reverting to cron: %d unmanaged crontab line(s) also appear to schedule ProxSave:", len(wrappers)))
		for _, line := range wrappers {
			unmanagedAdvisory = append(unmanagedAdvisory, "  - "+line)
		}
		for _, line := range unmanagedAdvisory {
			logBootstrapInfo(bootstrap, "%s", line)
		}
		// The count lives in the WARNING itself: DEBUG_LEVEL=warning hides the lines above it,
		// and what has to survive is that this host may now run the backup more than once.
		//
		// It states the crontab as it was READ BACK, not the write it hoped for. cronScheduled
		// above already established whether the line is there, and claiming a write that did not
		// land is the defect this whole area keeps producing.
		if cronScheduled {
			logBootstrapWarning(bootstrap, "The cron entry is in the crontab and %d unmanaged cron line(s) were left untouched, so this host may run the backup more than once.", len(wrappers))
		} else {
			logBootstrapWarning(bootstrap, "%d unmanaged cron line(s) were left untouched and no proxsave cron entry could be written, so tonight the host runs whatever those lines do.", len(wrappers))
		}
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
	modeRecorded := true
	if err := setBackupEnvKeys(configPath, map[string]string{
		"SCHEDULER_MODE":      "cron",
		"HEALTHCHECK_ENABLED": "false",
	}); err != nil {
		modeRecorded = false
		logging.Warning("daemon: failed to record cron mode in %s: %v", configPath, err)
	}
	// Teardown last: a failure here leaves the host cron-scheduled with mode=cron, never
	// unscheduled+stale. The per-run lock mitigates the transient double-schedule window.
	err := removeDaemonServiceFn(ctx, bootstrap)

	// The /etc advisory closes the half of #298 the wrapper branch above cannot reach: the
	// detector could already see a ProxSave schedule in /etc/crontab or /etc/cron.d, but
	// nothing on the WRITE path ever looked, so a host scheduled from there was told the
	// revert had restored its cron schedule and quietly got a second nightly backup.
	// Saying so is the whole fix; the line above was written regardless, on purpose.
	//
	// It runs AFTER the teardown, and the position is chosen rather than inherited.
	// systemCronProxsaveRefsFn does file I/O, including open(2) on operator-controlled cron
	// commands that can sit on a stalled NAS mount - the very mounts these wrappers exist
	// to guard - and there is no context to cancel that with. Here, a stall costs only the
	// message: the daemon is gone, the cron line is in place, SCHEDULER_MODE=cron is on
	// disk, and the host is consistent. Between the cron write and the env write it would
	// have stranded the host with a fresh cron line, mode=daemon and the daemon still
	// running, which is precisely the double schedule this whole path exists to prevent.
	// A teardown that FAILED does not make the advisory untrue either, so it is printed
	// either way and the teardown's error is still what this function returns.
	refs := systemCronProxsaveRefsFn()
	findings := systemCronScheduleFindings(refs)
	for _, line := range findings {
		logBootstrapInfo(bootstrap, "%s", line)
	}
	if len(findings) > 0 {
		logBootstrapWarning(bootstrap, "%s", systemCronOwnershipNote(len(refs)))
	}
	return cronRevertReport{
		SystemCronAdvisory: systemCronScheduleAdvisory(refs),
		UnmanagedAdvisory:  unmanagedAdvisory,
		CronScheduled:      cronScheduled,
		CronVerified:       cronVerified,
		ModeRecorded:       modeRecorded,
	}, err
}

// maybeAutoMigrateDaemon is the --upgrade retrofit: install the resident daemon on a host that
// has never recorded a scheduler engine. Best-effort so a migration failure never fails the
// upgrade.
//
// The decision is the PRESENCE of SCHEDULER_MODE in backup.env, read off the config merge's own
// report, and not its value.
//
// The mapping is done HERE rather than at the call site on purpose. upgradeFinalizePhase is not
// drivable by a test - it execs a binary and writes outside a temp dir - so anything decided
// there is unpinned: a call site that passed a hardcoded origin would migrate every host and no
// test would go red. What it passes now is the merge result itself, a value it already holds, so
// the only mistake left there is a type error. A host that already had the key has chosen an engine and is left exactly as it
// is: cron means cron, and no later upgrade revisits it. A host where this upgrade's merge had
// to add the key has chosen nothing, and it is the only host retrofitted.
//
// The value cannot carry that distinction. An absent key resolves to the template default
// "cron" (internal/config/config.go:841), so once the merge has run "chose cron" and "never
// chose" are the same bytes on disk. That is why this used to need a second key,
// DAEMON_OPT_OUT, written by --daemon-remove purely so the next upgrade would not undo it.
// Presence answers the same question without a key whose only job is to contradict another
// one.
func maybeAutoMigrateDaemon(ctx context.Context, configPath, baseDir, execToken string, upgradeResult *config.UpgradeResult, bootstrap *logging.BootstrapLogger) {
	origin := schedulerModeOriginFromUpgrade(upgradeResult)
	cfg, err := config.LoadConfigWithBaseDir(configPath, baseDir)
	if err != nil {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "daemon auto-migrate skipped: config load failed: %v", err)
		return
	}
	if cfg.SchedulerMode == "daemon" {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "daemon already active; no migration")
		return
	}
	// Neither branch below is a WARNING. Honouring a recorded engine is the normal outcome,
	// and an unavailable merge result has already produced its own warning at the point the
	// merge failed (cmd/proxsave/upgrade.go:282).
	switch origin {
	case schedulerModeOriginConfigured:
		logBootstrapInfo(bootstrap, "SCHEDULER_MODE=%s is set in %s: the scheduler engine is left as configured and the daemon is not installed.", cfg.SchedulerMode, configPath)
		return
	case schedulerModeOriginUnknown:
		logBootstrapInfo(bootstrap, "The configuration merge reported no result, so whether %s already set SCHEDULER_MODE is not established: the scheduler engine is left unchanged and the daemon is not installed.", configPath)
		return
	}
	logBootstrapInfo(bootstrap, "SCHEDULER_MODE was absent from %s until this upgrade added it: this host has never recorded a scheduler engine.", configPath)
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
		// One problem, one WARNING, same shape as warnIndirectProxsaveCronOnDaemonInstall. Five
		// warning lines for a single refusal read as five problems in the run's
		// "WARNINGS/ERRORS DURING RUN (warnings=N)" recap, and that count is what an operator
		// scans. The findings and the way forward sit below it at INFO; the verdict and its
		// consequence are the one line that has to survive DEBUG_LEVEL=warning, so REFUSED and
		// "No changes" both live there rather than in the header they used to share.
		logBootstrapInfo(bootstrap, "%d unmanaged cron line(s) schedule ProxSave:", len(refs))
		for _, line := range describeIndirectCronRefs(refs) {
			logBootstrapInfo(bootstrap, "  - %s", line)
		}
		logBootstrapWarning(bootstrap, "Daemon would duplicate backups, REFUSED; the losing run exits %d (backup skipped). No changes; cron backups continue.", types.ExitBackupSkipped.Int())
		logBootstrapInfo(bootstrap, "Remove/disable unwanted entries (%s), then run 'proxsave --daemon-setup', which installs the daemon and reports what it found rather than refusing.", cronRefEditHint(refs))
		return
	}
	logBootstrapInfo(bootstrap, "Migrating to the resident daemon scheduler (%s)...", daemonUnitName)
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
	logBootstrapInfo(bootstrap, "Daemon mode enabled: %s is active. %s", daemonUnitName, cronRemovalClause(cronOutcome))
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
		_ = warnIndirectProxsaveCronOnDaemonInstall(ctx, bootstrap)
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
	// UnmanagedSchedules counts the entries that still schedule ProxSave after the removal
	// above: an operator command the canonical matcher cannot see, or a line under /etc that
	// ProxSave may not edit. Zero on the ordinary host.
	//
	// It is carried out rather than only logged because the dashboard mutes the global logger
	// and the bootstrap console for the whole operation and never flushes them, so on that
	// front-end the result screen is the only channel left. Without this field it showed a
	// green INSTALLED over a host that now runs the backup twice - the same shape of defect as
	// issue #298 itself, the front-end asserting an outcome the code had not established.
	UnmanagedSchedules int
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

// cronRemovalScreenClause is cronRemovalClause as the VALUE of a "Cron entry:" label, for the
// dashboard screens, which state one fact per line rather than a sentence.
//
// It is a second rendering rather than a second wording: the four outcomes and the distinctions
// between them are cronRemovalClause's, and any new one has to be added in both places or the two
// front-ends drift - which is the whole reason that renderer exists.
func cronRemovalScreenClause(outcome cronRemovalOutcome) string {
	switch {
	case !outcome.Verified:
		return "could not be checked, one may still be scheduled alongside the daemon."
	case outcome.Removed == 0:
		return "none was present to remove."
	case outcome.Removed == 1:
		return "removed."
	default:
		return fmt.Sprintf("%d proxsave entries removed.", outcome.Removed)
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
