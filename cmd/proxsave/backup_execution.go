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
		return nil, nil, types.ExitSuccess.Int()
	}

	skip, earlyErrorState, exitCode := runPreBackupChecks(opts, orch)
	if earlyErrorState != nil {
		return nil, earlyErrorState, exitCode
	}
	if skip {
		// Benign concurrency skip (another backup is already running): no failure
		// notification, exit 0. The deferred ReleaseBackupLock is a no-op because
		// this process never acquired the lock.
		return nil, nil, exitCode
	}

	logging.Step("Start Go backup orchestration")
	hostname := resolveHostname()
	backupDone := logging.DebugStart(opts.logger, "backup run", "proxmox=%s host=%s", opts.envInfo.Type, hostname)
	stats, err := orch.RunGoBackup(opts.ctx, opts.envInfo, hostname)
	if err != nil {
		backupDone(err)
		return handleBackupRunError(opts.ctx, orch, stats, err)
	}
	backupDone(nil)

	persistBackupStats(orch, stats)
	logBackupStatistics(stats)
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
// no notification, exit 0.
func runPreBackupChecks(opts backupModeOptions, orch *orchestrator.Orchestrator) (bool, *orchestrator.EarlyErrorState, int) {
	preCheckDone := logging.DebugStart(opts.logger, "pre-backup checks", "")
	if err := orch.RunPreBackupChecks(opts.ctx); err != nil {
		preCheckDone(err)
		if errors.Is(err, orchestrator.ErrBackupInProgress) {
			logging.Warning("Skipping backup: %v", err)
			return true, nil, types.ExitSuccess.Int()
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

func logBackupStatistics(stats *orchestrator.BackupStats) {
	// The block is now debug-only; a standard run shows it in the graphical
	// outcome recap (buildBackupOutcomePrompt) instead. Guard before the blank
	// spacers too, so a standard run shows no orphan blank lines.
	if logging.GetDefaultLogger().GetLevel() < types.LogLevelDebug {
		return
	}
	fmt.Println()
	logging.Debug("=== Backup Statistics ===")
	logging.Debug("Files collected: %d", stats.FilesCollected)
	if stats.FilesFailed > 0 {
		// The PVE/PBS collection summary carries the Files-failed warning now.
		logging.Debug("Files failed: %d", stats.FilesFailed)
	}
	logging.Debug("Directories created: %d", stats.DirsCreated)
	logging.Debug("Data collected: %s", formatBytes(stats.BytesCollected))
	logging.Debug("Archive size: %s", formatBytes(stats.ArchiveSize))
	logCompressionRatio(stats)
	logging.Debug("Compression used: %s (level %d, mode %s)", stats.Compression, stats.CompressionLevel, stats.CompressionMode)
	if stats.RequestedCompression != stats.Compression {
		logging.Debug("Requested compression: %s", stats.RequestedCompression)
	}
	logging.Debug("Duration: %s", formatDuration(stats.Duration))
	logBackupArtifactPaths(stats)
	fmt.Println()
}

func logCompressionRatio(stats *orchestrator.BackupStats) {
	logging.Debug("Compression ratio: %s", compressionRatioText(stats))
}

// compressionRatioText renders the compression-ratio value shared by the
// debug-only log block (logCompressionRatio) and the graphical outcome recap
// (appendBackupStatsBlock), so the two never drift.
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

func logBackupArtifactPaths(stats *orchestrator.BackupStats) {
	if stats.BundleCreated {
		logging.Debug("Bundle path: %s", stats.ArchivePath)
		logging.Debug("Bundle contents: archive + checksum + metadata")
		return
	}

	logging.Debug("Archive path: %s", stats.ArchivePath)
	if stats.ManifestPath != "" {
		logging.Debug("Manifest path: %s", stats.ManifestPath)
	}
	if stats.Checksum != "" {
		logging.Debug("Archive checksum (SHA256): %s", stats.Checksum)
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
