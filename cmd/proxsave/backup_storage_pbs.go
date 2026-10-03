package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// pbsStorageName is the subject of the startup outcome ("✓ PBS storage: initialized").
const pbsStorageName = "PBS storage"

// initializePBSTarget opens the PBS block of "Initializing storage backends", after the
// cloud: the storage ID read from the configuration, its retention, then what the check
// finds (a configuration fact under the path, or the server's answer under "Checking PBS
// storage accessibility..."). A storage that is not initialized is not switched off: the
// block is registered all the same and step [7] reports the backup not saved. It returns
// nil when PBS_TARGET_ENABLED=false.
func initializePBSTarget(opts backupModeOptions, orch *orchestrator.Orchestrator, checker *checks.Checker) *block.PBS {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.PBSTargetEnabled {
		logging.Skip("Path PBS: disabled")
		return nil
	}

	logging.DebugStep(logger, "storage init", "pbs storage %s", cfg.PBSTargetStorage)
	logging.Info("Path PBS: %s", cfg.PBSTargetStorage)
	retention := block.PBSRetentionFromConfig(cfg)
	isPVE := opts.envInfo != nil && opts.envInfo.Type.SupportsPVE()
	ctx := opts.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	pbs, report := block.InitPBS(ctx, block.PBSOptions{
		PVEConfigPath:  cfg.PVEConfigPath,
		StorageID:      cfg.PBSTargetStorage,
		Hostname:       opts.hostname,
		IsPVEHost:      isPVE,
		Retention:      retention,
		EncryptArchive: cfg.EncryptArchive,
		Logger:         logger,
	})
	logPBSRetentionPolicy(retention)

	switch {
	case report.Fact != nil:
		logging.DebugStep(logger, "storage init", "pbs not initialized: %s", report.Fact.Line())
		logging.Info("  %s", report.Fact.Line())
		logging.Warning("%s %s: not initialized", theme.SymbolError, pbsStorageName)
	case report.Cause != nil:
		logging.DebugStep(logger, "storage init", "pbs not initialized: server %s", report.Cause.Text)
		logging.Info("Checking PBS storage accessibility...")
		logging.Info("  %s", report.Cause.Capitalized())
		logging.Warning("%s %s: not initialized", theme.SymbolError, pbsStorageName)
	default:
		logging.DebugStep(logger, "storage init", "pbs initialized, own snapshots=%d", len(report.Backups))
		logging.Info("Checking PBS storage accessibility...")
		logging.Info("  Accessible")
		logStorageInitSummary(formatPBSInitSummary(retention, report.Backups))
	}

	if checker != nil {
		var available func() (float64, string)
		if pbs.Initialized() {
			available = func() (float64, string) {
				gb, cause := pbs.AvailableGB(ctx)
				if cause != nil {
					return 0, cause.Text
				}
				return gb, ""
			}
		}
		checker.SetPBS(cfg.PBSTargetStorage, cfg.MinDiskPBSGB, available)
	}
	orch.RegisterBackupBlock(pbs)
	return pbs
}

// logPBSRetentionPolicy is the configuration line under "Path PBS: <storage>", in the
// form of the other destinations: simple prints it, GFS keeps its limits in DEBUG.
func logPBSRetentionPolicy(rc storage.RetentionConfig) {
	if rc.Policy == "gfs" {
		rc = storage.EffectiveGFSRetentionConfig(rc)
		logging.Debug("storage init: pbs retention policy=gfs daily=%d weekly=%d monthly=%d yearly=%d",
			rc.Daily, rc.Weekly, rc.Monthly, rc.Yearly)
		return
	}
	logging.Info("  Retention policy: simple (keep %d newest)", rc.MaxBackups)
}

// formatPBSInitSummary closes the block of an initialized PBS storage: this host's
// snapshots, the GFS tiers when the policy is GFS, then the outcome.
func formatPBSInitSummary(rc storage.RetentionConfig, snapshots []block.Snapshot) (string, bool) {
	result := fmt.Sprintf("  Backups: %d", len(snapshots))
	if rc.Policy == "gfs" {
		rc = storage.EffectiveGFSRetentionConfig(rc)
		backups := make([]*types.BackupMetadata, 0, len(snapshots))
		for _, s := range snapshots {
			backups = append(backups, &types.BackupMetadata{BackupFile: s.Name(), Timestamp: time.Unix(s.BackupTime, 0)})
		}
		result += formatGFSTierLines(rc, len(snapshots), backups)
	}
	result += fmt.Sprintf("\n%s %s: initialized", theme.SymbolSuccess, pbsStorageName)
	return result, false
}

// logPBSStorageSummary is the PBS line of "Storage configuration:", after the cloud.
func logPBSStorageSummary(cfg *config.Config, pbs *block.PBS) {
	switch {
	case !cfg.PBSTargetEnabled:
		logging.Skip("  PBS storage: disabled")
	case pbs.Initialized():
		logging.Info("  PBS storage: %s [pbs]", cfg.PBSTargetStorage)
	default:
		logging.Info("  PBS storage: %s [not initialized]", cfg.PBSTargetStorage)
	}
}

// logPBSLogSummary is the PBS line of "Log configuration:", after the cloud: the log is
// attached to the snapshot of the run.
func logPBSLogSummary(cfg *config.Config, pbs *block.PBS) {
	switch {
	case !cfg.PBSTargetEnabled:
		logging.Skip("  PBS: disabled")
	case pbs.Initialized():
		logging.Info("  PBS: %s", cfg.PBSTargetStorage)
	default:
		logging.Info("  PBS: not initialized")
	}
}
