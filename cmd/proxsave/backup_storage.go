// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/safefs"
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
			ExitCode:  types.ExitStorageError,
			Timestamp: time.Now(),
		}, types.ExitStorageError.Int()
	}
	state.localFS = localFS
	registerPrimaryStorage(opts, orch, localBackend, localFS)

	state.secondaryFS = initializeSecondaryStorage(opts, orch, checker)
	state.cloudFS = initializeCloudStorage(opts, orch, checker)
	storageDone(nil)

	fmt.Println()
	return state, nil, types.ExitSuccess.Int()
}

func initializePrimaryStorage(opts backupModeOptions) (storage.Storage, *storage.FilesystemInfo, string, error) {
	cfg := opts.cfg
	logger := opts.logger

	logging.DebugStep(logger, "storage init", "primary backend")
	// Retention needs the name this run writes under, not just os.Hostname: archives
	// carry what "hostname -f" returned, while os.Hostname reports the kernel short
	// name, and an archive is only this host's own if it matches one of the two.
	localBackend, err := storage.NewLocalStorage(cfg, logger, opts.hostname)
	if err != nil {
		return nil, nil, "Failed to initialize local storage", err
	}
	localFS, err := detectFilesystemInfo(opts.ctx, localBackend, cfg.BackupPath, logger)
	if err != nil {
		return nil, nil, "Failed to prepare primary storage", err
	}

	logging.DebugStep(logger, "storage init", "primary filesystem=%s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))
	logStoragePath("Primary", cfg.BackupPath, localFS)
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
	localOwned, localOwnedKnown := startupOwnedBackups(opts.ctx, localBackend, localStats, localBackups)
	logging.DebugStep(logger, "storage init", "primary owned=%d known=%v", localOwned, localOwnedKnown)
	localAdapter.SetInitialOwnedBackups(localOwned, localOwnedKnown)
	orch.RegisterStorageTarget(localAdapter)
	logStorageInitSummary(formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, localStats, localBackups))
}

func initializeSecondaryStorage(opts backupModeOptions, orch *orchestrator.Orchestrator, checker *checks.Checker) *storage.FilesystemInfo {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.SecondaryEnabled {
		logging.Skip("Path Secondary: disabled")
		return nil
	}

	logging.DebugStep(logger, "storage init", "secondary backend")
	secondaryBackend, err := storage.NewSecondaryStorage(cfg, logger, opts.hostname)
	if err != nil {
		// The destination cannot be used for this run: it is disabled exactly like an
		// unreachable cloud, so the later steps (disk space, [6], log copy) skip it
		// instead of failing on it one by one.
		logging.DebugStep(logger, "storage init", "secondary unavailable, disabling: %v", err)
		path := cfg.SecondaryPath
		cfg.SecondaryEnabled = false
		cfg.SecondaryLogPath = ""
		if checker != nil {
			checker.DisableSecondary()
		}
		logStorageNotInitialized("Secondary", "Secondary storage", path, "Secondary storage", safefs.SystemErrorText(err))
		return nil
	}

	secondaryFS, _ := detectFilesystemInfo(opts.ctx, secondaryBackend, cfg.SecondaryPath, logger)
	logging.DebugStep(logger, "storage init", "secondary filesystem=%s", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS))
	logStoragePath("Secondary", cfg.SecondaryPath, secondaryFS)
	secondaryStats := fetchStorageStats(opts.ctx, secondaryBackend, logger, "Secondary storage")
	secondaryBackups := fetchBackupList(opts.ctx, secondaryBackend)
	logging.DebugStep(logger, "storage init", "secondary stats=%v backups=%d", secondaryStats != nil, len(secondaryBackups))
	secondaryAdapter := orchestrator.NewStorageAdapter(secondaryBackend, logger, cfg)
	secondaryAdapter.SetFilesystemInfo(secondaryFS)
	secondaryAdapter.SetInitialStats(secondaryStats)
	secondaryOwned, secondaryOwnedKnown := startupOwnedBackups(opts.ctx, secondaryBackend, secondaryStats, secondaryBackups)
	logging.DebugStep(logger, "storage init", "secondary owned=%d known=%v", secondaryOwned, secondaryOwnedKnown)
	secondaryAdapter.SetInitialOwnedBackups(secondaryOwned, secondaryOwnedKnown)
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
	cloudBackend, err := storage.NewCloudStorage(cfg, logger, opts.hostname)
	if err != nil {
		logging.DebugStep(logger, "storage init", "cloud backend unavailable, disabling: %v", err)
		remote := cfg.CloudRemote
		disableCloudForRun(cfg, checker)
		logStorageNotInitialized("Cloud", "Cloud storage", remote, "Cloud storage", safefs.SystemErrorText(err))
		return nil
	}

	cloudFS, err := detectFilesystemInfo(opts.ctx, cloudBackend, cfg.CloudRemote, logger)
	if cloudFS == nil {
		// The remote did not answer: the block shows the path, the filesystem nobody
		// could detect, rclone's own last line as the cause, then the "✗" outcome and
		// the SKIP. The full error chain stays in DEBUG.
		cause := "filesystem detection unavailable"
		if err != nil {
			cause = storage.ErrorCause(err)
		}
		logging.DebugStep(logger, "storage init", "cloud unavailable, disabling: %v", err)
		remote := cfg.CloudRemote
		disableCloudForRun(cfg, checker)
		logStorageNotInitialized("Cloud", "Cloud storage", remote, "Cloud remote", cause)
		return nil
	}

	logging.DebugStep(logger, "storage init", "cloud filesystem=%s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
	logStoragePath("Cloud", cfg.CloudRemote, cloudFS)
	cloudStats := fetchStorageStats(opts.ctx, cloudBackend, logger, "Cloud storage")
	cloudBackups := fetchBackupList(opts.ctx, cloudBackend)
	logging.DebugStep(logger, "storage init", "cloud stats=%v backups=%d", cloudStats != nil, len(cloudBackups))
	cloudAdapter := orchestrator.NewStorageAdapter(cloudBackend, logger, cfg)
	cloudAdapter.SetFilesystemInfo(cloudFS)
	cloudAdapter.SetInitialStats(cloudStats)
	cloudOwned, cloudOwnedKnown := startupOwnedBackups(opts.ctx, cloudBackend, cloudStats, cloudBackups)
	logging.DebugStep(logger, "storage init", "cloud owned=%d known=%v", cloudOwned, cloudOwnedKnown)
	cloudAdapter.SetInitialOwnedBackups(cloudOwned, cloudOwnedKnown)
	orch.RegisterStorageTarget(cloudAdapter)
	logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, cloudStats, cloudBackups))
	return cloudFS
}

// disableCloudForRun turns the cloud destination off for the rest of the run: no
// upload at [6], no log copy, no disk-space check.
func disableCloudForRun(cfg *config.Config, checker *checks.Checker) {
	cfg.CloudEnabled = false
	cfg.CloudLogPath = ""
	if checker != nil {
		checker.DisableCloud()
	}
}

// startupOwnedBackups counts the archives this host owns in the startup listing,
// with the meaning a successful run's count has (discussion #292). A failed run
// reports it for the locations it never reached (StorageAdapter.applyInitialStats).
//
// known is false unless the two startup reads agree. GetStats lists the location
// itself and counts that listing, while fetchBackupList returns nil on a failed
// listing, which is also what an empty location returns: scoping that nil would
// report "0 owned" beside a location GetStats saw full. A listing that changed
// between the two reads is refused the same way, and the caller keeps the unscoped
// total.
func startupOwnedBackups(ctx context.Context, backend storage.Storage, stats *storage.StorageStats, backups []*types.BackupMetadata) (int, bool) {
	if backend == nil || stats == nil || len(backups) != stats.TotalBackups {
		return 0, false
	}
	return storage.CountOwnedBackups(ctx, backend, backups)
}
