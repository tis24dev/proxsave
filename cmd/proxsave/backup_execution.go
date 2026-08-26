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
	// The run resolves its name once, so the archives it writes and the retention
	// pass that later prunes them provably carry the same string.
	hostname := opts.hostname
	if hostname == "" {
		hostname = resolveHostname()
	}
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
