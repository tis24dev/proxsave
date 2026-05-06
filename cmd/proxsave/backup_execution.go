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

	if earlyErrorState, exitCode := runPreBackupChecks(opts, orch); earlyErrorState != nil {
		return nil, earlyErrorState, exitCode
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
	logging.Info("✓ Go backup orchestration completed")
	logServerIdentityValues(opts.serverIDValue, opts.serverMACValue)

	if opts.heapProfilePath != "" {
		logging.Info("Heap profiling saved: %s", opts.heapProfilePath)
	}

	logBackupExitStatus(stats.ExitCode)
	return stats, nil, stats.ExitCode
}

func runPreBackupChecks(opts backupModeOptions, orch *orchestrator.Orchestrator) (*orchestrator.EarlyErrorState, int) {
	preCheckDone := logging.DebugStart(opts.logger, "pre-backup checks", "")
	if err := orch.RunPreBackupChecks(opts.ctx); err != nil {
		preCheckDone(err)
		logging.Error("Pre-backup validation failed: %v", err)
		return &orchestrator.EarlyErrorState{
			Phase:     "pre_backup_checks",
			Error:     err,
			ExitCode:  types.ExitBackupError,
			Timestamp: time.Now(),
		}, types.ExitBackupError.Int()
	}
	preCheckDone(nil)
	fmt.Println()
	return nil, types.ExitSuccess.Int()
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
	fmt.Println()
	logging.Info("=== Backup Statistics ===")
	logging.Info("Files collected: %d", stats.FilesCollected)
	if stats.FilesFailed > 0 {
		logging.Warning("Files failed: %d", stats.FilesFailed)
	}
	logging.Info("Directories created: %d", stats.DirsCreated)
	logging.Info("Data collected: %s", formatBytes(stats.BytesCollected))
	logging.Info("Archive size: %s", formatBytes(stats.ArchiveSize))
	logCompressionRatio(stats)
	logging.Info("Compression used: %s (level %d, mode %s)", stats.Compression, stats.CompressionLevel, stats.CompressionMode)
	if stats.RequestedCompression != stats.Compression {
		logging.Info("Requested compression: %s", stats.RequestedCompression)
	}
	logging.Info("Duration: %s", formatDuration(stats.Duration))
	logBackupArtifactPaths(stats)
	fmt.Println()
}

func logCompressionRatio(stats *orchestrator.BackupStats) {
	switch {
	case stats.CompressionSavingsPercent > 0:
		logging.Info("Compression ratio: %.1f%%", stats.CompressionSavingsPercent)
	case stats.CompressionRatioPercent > 0:
		logging.Info("Compression ratio: %.1f%%", stats.CompressionRatioPercent)
	case stats.BytesCollected > 0:
		ratio := float64(stats.ArchiveSize) / float64(stats.BytesCollected) * 100
		logging.Info("Compression ratio: %.1f%%", ratio)
	default:
		logging.Info("Compression ratio: N/A")
	}
}

func logBackupArtifactPaths(stats *orchestrator.BackupStats) {
	if stats.BundleCreated {
		logging.Info("Bundle path: %s", stats.ArchivePath)
		logging.Info("Bundle contents: archive + checksum + metadata")
		return
	}

	logging.Info("Archive path: %s", stats.ArchivePath)
	if stats.ManifestPath != "" {
		logging.Info("Manifest path: %s", stats.ManifestPath)
	}
	if stats.Checksum != "" {
		logging.Info("Archive checksum (SHA256): %s", stats.Checksum)
	}
}

func logBackupExitStatus(exitCode int) {
	status := notify.StatusFromExitCode(exitCode)
	statusLabel := strings.ToUpper(status.String())
	emoji := notify.GetStatusEmoji(status)
	logging.Info("Exit status: %s %s (code=%d)", emoji, statusLabel, exitCode)
}
