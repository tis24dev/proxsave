// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

func runConfiguredBackup(opts backupModeOptions, orch *orchestrator.Orchestrator) (*orchestrator.BackupStats, *orchestrator.EarlyErrorState, int) {
	if !opts.cfg.BackupEnabled {
		logging.Warning("Backup is disabled in configuration")
		return nil, nil, types.ExitBackupSkipped.Int()
	}

	skip, earlyErrorState, exitCode := runPreBackupChecks(opts, orch)
	if earlyErrorState != nil {
		return nil, earlyErrorState, exitCode
	}
	if skip {
		// Benign concurrency skip (another backup is already running): no failure
		// notification, ExitBackupSkipped (16) so the daemon does not ping a false-green
		// finish and the CLI footer shows a skip, not success. The deferred ReleaseBackupLock
		// is a no-op because this process never acquired the lock.
		return nil, nil, exitCode
	}

	logging.Step("Start Go backup orchestration")
	// Already repaired and reported at the top of runBackupModeSteps, ahead of the
	// storage constructors that a dropped plumb actually breaks.
	hostname := opts.hostname
	backupDone := logging.DebugStart(opts.logger, "backup run", "proxmox=%s host=%s", opts.envInfo.Type, hostname)
	stats, err := orch.RunGoBackup(opts.ctx, opts.envInfo, hostname)
	if err != nil {
		backupDone(err)
		return handleBackupRunError(opts.ctx, orch, stats, err)
	}
	backupDone(nil)

	persistBackupStats(orch, stats)
	logBackupStatistics(stats, opts.outcomeRendersRecap)
	logging.Info("✓ Backup completed")
	logServerIdentityValues(opts.serverIDValue, opts.serverMACValue)
	logMonitoringPortalLink(stats)

	if opts.heapProfilePath != "" {
		logging.Info("Heap profiling saved: %s", opts.heapProfilePath)
	}

	logBackupExitStatus(stats.ExitCode)
	return stats, nil, stats.ExitCode
}

// runPreBackupChecks returns (skip, earlyError, exitCode). skip=true means a
// benign concurrency skip (another backup is already running): no early error,
// no notification, ExitBackupSkipped (16) so the daemon suppresses a false-green
// finish and the CLI footer shows a skip rather than success (F09-03).
func runPreBackupChecks(opts backupModeOptions, orch *orchestrator.Orchestrator) (bool, *orchestrator.EarlyErrorState, int) {
	preCheckDone := logging.DebugStart(opts.logger, "pre-backup checks", "")
	if err := orch.RunPreBackupChecks(opts.ctx); err != nil {
		preCheckDone(err)
		if errors.Is(err, orchestrator.ErrBackupInProgress) {
			logging.Warning("Skipping backup: %v", err)
			return true, nil, types.ExitBackupSkipped.Int()
		}
		logging.Error("Pre-backup validation failed: %v", err)
		return false, &orchestrator.EarlyErrorState{
			Phase:     "pre_backup_checks",
			Error:     err,
			ExitCode:  types.ExitBackupError,
			Timestamp: time.Now(),
		}, types.ExitBackupError.Int()
	}
	preCheckDone(nil)
	fmt.Println()
	return false, nil, types.ExitSuccess.Int()
}

func handleBackupRunError(ctx context.Context, orch *orchestrator.Orchestrator, stats *orchestrator.BackupStats, err error) (*orchestrator.BackupStats, *orchestrator.EarlyErrorState, int) {
	if ctx.Err() == context.Canceled {
		logging.Warning("Backup was canceled")
		orch.FinalizeAfterRun(ctx, stats)
		return stats, nil, exitCodeInterrupted
	}

	var backupErr *orchestrator.BackupError
	if errors.As(err, &backupErr) {
		logging.Error("Backup %s failed: %v", backupErr.Phase, backupErr.Err)
		orch.FinalizeAfterRun(ctx, stats)
		return stats, nil, backupErr.Code.Int()
	}

	logging.Error("Backup orchestration failed: %v", err)
	orch.FinalizeAfterRun(ctx, stats)
	return stats, nil, types.ExitBackupError.Int()
}

func persistBackupStats(orch *orchestrator.Orchestrator, stats *orchestrator.BackupStats) {
	if err := orch.SaveStatsReport(stats); err != nil {
		logging.Warning("Failed to persist backup statistics: %v", err)
	} else if stats.ReportPath != "" {
		logging.Info("✓ Statistics report saved to %s", stats.ReportPath)
	}
}

// logBackupStatistics writes the recap to the console: the COMPACT rows at Info, the
// full block at Debug. skip suppresses BOTH, for a run whose screen renders the recap
// itself (backupModeOptions.outcomeRendersRecap).
//
// The block was debug-only before c0cc02a, and that gate WAS the de-duplication: the
// graphical stats block and the Debug gate arrived together in 3ed0716, two halves of
// one design where exactly one side renders the recap. Reading the gate as a verbosity
// choice and lifting it for Info put the compact rows into the dashboard viewport
// underneath the outcome block that already showed them. Suppressing here on the
// caller's say-so restores that invariant, and closes the case the original gate never
// covered: at Debug the full block reached the viewport too, because capturing the
// console swaps the writer and not the level (internal/logging/capture.go, SwapOutput).
//
// Where the rows go on an unattended run, corrected: NOT into the run log file. This
// runs from runConfiguredBackup, after the orchestrator has closed that file
// (FinalizeAndCloseLog), so the recap reaches stdout only. It still matters there: the
// daemon runs the backup as a child whose stdout goes to journald AND into the bounded
// tail it POSTs to Healthchecks as the failure diagnostic (daemon.go, buildBackupCmd).
// Deleting the Info arm would take that payload away.
func logBackupStatistics(stats *orchestrator.BackupStats, skip bool) {
	if stats == nil || skip {
		return
	}
	debug := logging.GetDefaultLogger().GetLevel() >= types.LogLevelDebug

	// The header and the blank spacers belong to the full block: at Info the rows join
	// the run's other lines instead of opening a section of their own.
	if !debug {
		for _, line := range backupStatsRecap(stats, true) {
			logging.Info("%s", line.Text)
		}
		return
	}

	fmt.Println()
	logging.Debug("=== Backup Statistics ===")
	for _, line := range backupStatsRecap(stats, false) {
		logging.Debug("%s", line.Text)
	}
	fmt.Println()
}

// compressionRatioText renders the compression-ratio value for the shared recap
// builder (backupStatsRecap), and therefore for both front-ends.
func compressionRatioText(stats *orchestrator.BackupStats) string {
	switch {
	case stats.CompressionSavingsPercent > 0:
		return fmt.Sprintf("%.1f%%", stats.CompressionSavingsPercent)
	case stats.CompressionRatioPercent > 0:
		return fmt.Sprintf("%.1f%%", stats.CompressionRatioPercent)
	case stats.BytesCollected > 0:
		ratio := float64(stats.ArchiveSize) / float64(stats.BytesCollected) * 100
		return fmt.Sprintf("%.1f%%", ratio)
	default:
		return "N/A"
	}
}

// consoleStatusGlyph returns a TEXT-presentation glyph (all width 1, terminal-stable)
// for the console "Exit status" line, matching the plain checkmarks used everywhere
// else in the run output. It deliberately avoids notify.GetStatusEmoji, whose
// emoji-presentation glyphs (e.g. "⚠️" = U+26A0 U+FE0F) render at a width the terminal
// and lipgloss disagree on, shifting the framed graphical panel's border by one column.
func consoleStatusGlyph(status notify.NotificationStatus) string {
	switch status {
	case notify.StatusSuccess:
		return "✓"
	case notify.StatusWarning:
		return "⚠"
	case notify.StatusFailure:
		return "✗"
	default:
		return "•"
	}
}

func logBackupExitStatus(exitCode int) {
	status := notify.StatusFromExitCode(exitCode)
	statusLabel := strings.ToUpper(status.String())
	glyph := consoleStatusGlyph(status)
	logging.Info("Exit status: %s %s (code=%d)", glyph, statusLabel, exitCode)
}

// runHostnameOrReport returns the name this run writes its archives under, and says
// so out loud when the run's own name never arrived.
//
// The run resolves its name exactly once (initializeRunLogFile assigns
// rt.hostname = resolveHostname(), main_runtime.go) and dispatchBackupMode copies it
// into opts.hostname, which TestBackupModeOptionsCarryTheRunHostname pins. So an
// empty value here cannot mean a machine that failed to name itself: resolveHostname
// has no return path that yields "", it falls back to the "unknown" sentinel. Empty
// can only mean the plumb was dropped.
//
// The recompute stays, and it is called from the top of runBackupModeSteps so the
// repaired name reaches the storage constructors rather than only the writer.
// Recomputing for the WRITER alone is exactly what turned a dropped plumb into a
// working-but-wrong run: the archives kept the correct FQDN while the three
// constructors got "" and lost the alias, so retention scoped this machine's own
// work out and pruned nothing (discussion #292), and archives that look right are
// how the defect reached a release.
//
// Be precise about what the report is worth, because two easier claims are false.
// A dropped plumb does NOT leave retention without a name: NewLocalStorage sets
// hostname from resolveRetentionHostname() and puts the plumbed value only into
// hostAliases (internal/storage/local.go), so the empty-hostname warning in
// applyRetentionHostScope is never the one that fires here. And on a host whose
// "hostname -f" already equals its kernel name there are no aliases to lose, so a
// dropped plumb costs that machine nothing at all. This line is therefore not a
// second opinion on a failure retention would report anyway. It is the only signal
// on a build where the plumb was cut.
//
// WARNING rather than ERROR on purpose: it keeps the run off green through
// ParseLogCounts and applyIssueExitCode (internal/orchestrator/extensions.go)
// without inventing a failure class. Nothing is logged on any ordinary machine.
// resolveHostname has no return path yielding "", it falls back to the "unknown"
// sentinel, and dispatchBackupMode is the only non-test producer of
// backupModeOptions, so the branch is unreachable unless the wiring is cut in
// source. TestBackupModeOptionsCarryTheRunHostname is what blocks that at CI and
// remains the stronger guard of the two.
func runHostnameOrReport(logger *logging.Logger, hostname string) string {
	if hostname != "" {
		return hostname
	}
	if logger != nil {
		logger.Warning("The run's hostname did not reach the backup: the archives are still written under the resolved name, but retention on every storage backend has lost it and will scope this machine's own work out (discussion #292)")
	}
	return resolveHostname()
}
