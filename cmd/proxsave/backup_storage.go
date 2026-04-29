// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"time"

	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

type backupStorageState struct {
	localFS     *storage.FilesystemInfo
	secondaryFS *storage.FilesystemInfo
	cloudFS     *storage.FilesystemInfo
}

func initializeBackupStorage(opts backupModeOptions, orch *orchestrator.Orchestrator, checker *checks.Checker) (backupStorageState, *orchestrator.EarlyErrorState, int) {
	cfg := opts.cfg
	logger := opts.logger
	state := backupStorageState{}

	logging.Step("Initializing storage backends")
	storageDone := logging.DebugStart(logger, "storage init", "primary=%s secondary=%v cloud=%v", cfg.BackupPath, cfg.SecondaryEnabled, cfg.CloudEnabled)

	localBackend, localFS, storageFailureMessage, err := initializePrimaryStorage(opts)
	if err != nil {
		storageDone(err)
		logging.Error("%s: %v", storageFailureMessage, err)
		return state, &orchestrator.EarlyErrorState{
			Phase:     "storage_init",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}, types.ExitConfigError.Int()
	}
	state.localFS = localFS
	registerPrimaryStorage(opts, orch, localBackend, localFS)

	state.secondaryFS = initializeSecondaryStorage(opts, orch)
	state.cloudFS = initializeCloudStorage(opts, orch, checker)
	storageDone(nil)

	fmt.Println()
	return state, nil, types.ExitSuccess.Int()
}

func initializePrimaryStorage(opts backupModeOptions) (storage.Storage, *storage.FilesystemInfo, string, error) {
	cfg := opts.cfg
	logger := opts.logger

	logging.DebugStep(logger, "storage init", "primary backend")
	localBackend, err := storage.NewLocalStorage(cfg, logger)
	if err != nil {
		return nil, nil, "Failed to initialize local storage", err
	}
	localFS, err := detectFilesystemInfo(opts.ctx, localBackend, cfg.BackupPath, logger)
	if err != nil {
		return nil, nil, "Failed to prepare primary storage", err
	}

	logging.DebugStep(logger, "storage init", "primary filesystem=%s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))
	logging.Info("Path Primary: %s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))
	return localBackend, localFS, "", nil
}

func registerPrimaryStorage(opts backupModeOptions, orch *orchestrator.Orchestrator, localBackend storage.Storage, localFS *storage.FilesystemInfo) {
	cfg := opts.cfg
	logger := opts.logger

	localStats := fetchStorageStats(opts.ctx, localBackend, logger, "Local storage")
	localBackups := fetchBackupList(opts.ctx, localBackend)
	logging.DebugStep(logger, "storage init", "primary stats=%v backups=%d", localStats != nil, len(localBackups))

	localAdapter := orchestrator.NewStorageAdapter(localBackend, logger, cfg)
	localAdapter.SetFilesystemInfo(localFS)
	localAdapter.SetInitialStats(localStats)
	orch.RegisterStorageTarget(localAdapter)
	logStorageInitSummary(formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, localStats, localBackups))
}

func initializeSecondaryStorage(opts backupModeOptions, orch *orchestrator.Orchestrator) *storage.FilesystemInfo {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.SecondaryEnabled {
		logging.Skip("Path Secondary: disabled")
		return nil
	}

	logging.DebugStep(logger, "storage init", "secondary backend")
	secondaryBackend, err := storage.NewSecondaryStorage(cfg, logger)
	if err != nil {
		logging.Warning("Failed to initialize secondary storage: %v", err)
		logging.Info("Path Secondary: %s", formatDetailedFilesystemLabel(cfg.SecondaryPath, nil))
		return nil
	}

	secondaryFS, _ := detectFilesystemInfo(opts.ctx, secondaryBackend, cfg.SecondaryPath, logger)
	logging.DebugStep(logger, "storage init", "secondary filesystem=%s", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS))
	logging.Info("Path Secondary: %s", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS))
	secondaryStats := fetchStorageStats(opts.ctx, secondaryBackend, logger, "Secondary storage")
	secondaryBackups := fetchBackupList(opts.ctx, secondaryBackend)
	logging.DebugStep(logger, "storage init", "secondary stats=%v backups=%d", secondaryStats != nil, len(secondaryBackups))
	secondaryAdapter := orchestrator.NewStorageAdapter(secondaryBackend, logger, cfg)
	secondaryAdapter.SetFilesystemInfo(secondaryFS)
	secondaryAdapter.SetInitialStats(secondaryStats)
	orch.RegisterStorageTarget(secondaryAdapter)
	logStorageInitSummary(formatStorageInitSummary("Secondary storage", cfg, storage.LocationSecondary, secondaryStats, secondaryBackups))
	return secondaryFS
}

func initializeCloudStorage(opts backupModeOptions, orch *orchestrator.Orchestrator, checker *checks.Checker) *storage.FilesystemInfo {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.CloudEnabled {
		logging.Skip("Path Cloud: disabled")
		return nil
	}

	logging.DebugStep(logger, "storage init", "cloud backend")
	cloudBackend, err := storage.NewCloudStorage(cfg, logger)
	if err != nil {
		logging.Warning("Failed to initialize cloud storage: %v", err)
		logging.Info("Path Cloud: %s", formatDetailedFilesystemLabel(cfg.CloudRemote, nil))
		logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, nil, nil))
		return nil
	}

	cloudFS, _ := detectFilesystemInfo(opts.ctx, cloudBackend, cfg.CloudRemote, logger)
	if cloudFS == nil {
		logging.DebugStep(logger, "storage init", "cloud unavailable, disabling")
		cfg.CloudEnabled = false
		cfg.CloudLogPath = ""
		if checker != nil {
			checker.DisableCloud()
		}
		logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, nil, nil))
		logging.Skip("Path Cloud: disabled")
		return nil
	}

	logging.DebugStep(logger, "storage init", "cloud filesystem=%s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
	logging.Info("Path Cloud: %s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
	cloudStats := fetchStorageStats(opts.ctx, cloudBackend, logger, "Cloud storage")
	cloudBackups := fetchBackupList(opts.ctx, cloudBackend)
	logging.DebugStep(logger, "storage init", "cloud stats=%v backups=%d", cloudStats != nil, len(cloudBackups))
	cloudAdapter := orchestrator.NewStorageAdapter(cloudBackend, logger, cfg)
	cloudAdapter.SetFilesystemInfo(cloudFS)
	cloudAdapter.SetInitialStats(cloudStats)
	orch.RegisterStorageTarget(cloudAdapter)
	logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, cloudStats, cloudBackups))
	return cloudFS
}
