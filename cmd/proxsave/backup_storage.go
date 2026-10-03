// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tis24dev/proxsave/internal/block"
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
	// pbs is the PBS block, registered initialized or not; nil when
	// PBS_TARGET_ENABLED=false.
	pbs *block.PBS
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
	state.pbs = initializePBSTarget(opts, orch, checker)
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
	logStoragePath("Primary", cfg.BackupPath, cfg, storage.LocationPrimary)
	localFS, err := detectFilesystemInfo(opts.ctx, localBackend, cfg.BackupPath, logger)
	if err != nil {
		return nil, nil, "Failed to prepare primary storage", err
	}

	logging.DebugStep(logger, "storage init", "primary filesystem=%s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))
	logStorageFilesystem(localFS, nil)
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
	logStorageInitSummary(formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, localStats, startupOwnedListing(opts.ctx, localBackend, localBackups)))
}

// detectionFailureReporter is implemented by a backend whose DetectFilesystem can fall
// back to an unknown filesystem and keep the copy going (SecondaryStorage).
type detectionFailureReporter interface {
	DetectionFailure() error
}

func initializeSecondaryStorage(opts backupModeOptions, orch *orchestrator.Orchestrator, checker *checks.Checker) *storage.FilesystemInfo {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.SecondaryEnabled {
		logging.Skip("Path Secondary: disabled")
		return nil
	}

	logging.DebugStep(logger, "storage init", "secondary backend")
	logStoragePath("Secondary", cfg.SecondaryPath, cfg, storage.LocationSecondary)
	secondaryBackend, err := storage.NewSecondaryStorage(cfg, logger, opts.hostname)
	if err != nil {
		// The destination cannot be used for this run: it is disabled exactly like an
		// unreachable cloud, so the later steps (disk space, [6], log copy) skip it
		// instead of failing on it one by one.
		logging.DebugStep(logger, "storage init", "secondary unavailable, disabling: %v", err)
		disableSecondaryForRun(cfg, checker)
		logStorageNotInitialized("Secondary", "Secondary storage", "  "+storage.CapitalizedCause(err))
		return nil
	}

	secondaryFS, detectErr := detectFilesystemInfo(opts.ctx, secondaryBackend, cfg.SecondaryPath, logger)
	var missingErr *storage.DirectoryMissingError
	if errors.As(detectErr, &missingErr) {
		// A dry run creates no destination directory: nothing can be copied there.
		logging.DebugStep(logger, "storage init", "secondary directory missing in dry run, disabling: %v", detectErr)
		disableSecondaryForRun(cfg, checker)
		logStorageNotInitialized("Secondary", "Secondary storage", "  Directory: missing, not created in dry run")
		return nil
	}
	var dirErr *storage.DirectoryError
	if errors.As(detectErr, &dirErr) {
		// The backup directory cannot be created: nothing can be copied there.
		logging.DebugStep(logger, "storage init", "secondary directory not created, disabling: %v", detectErr)
		disableSecondaryForRun(cfg, checker)
		logStorageNotInitialized("Secondary", "Secondary storage", "  Directory not created: "+safefs.SystemErrorText(dirErr.Err))
		return nil
	}
	if detectErr == nil {
		var backend storage.Storage = secondaryBackend
		if reporter, ok := backend.(detectionFailureReporter); ok {
			detectErr = reporter.DetectionFailure()
		}
	}
	logging.DebugStep(logger, "storage init", "secondary filesystem=%s detect_err=%v", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS), detectErr)
	var problems []string
	if problem := logStorageFilesystem(secondaryFS, detectErr); problem != "" {
		problems = append(problems, problem)
	}
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
	logStorageInitSummary(formatStorageInitSummary("Secondary storage", cfg, storage.LocationSecondary, secondaryStats, startupOwnedListing(opts.ctx, secondaryBackend, secondaryBackups), problems...))
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
	logStoragePath("Cloud", cfg.CloudRemote, cfg, storage.LocationCloud)
	cloudBackend, err := storage.NewCloudStorage(cfg, logger, opts.hostname)
	if err != nil {
		logging.DebugStep(logger, "storage init", "cloud backend unavailable, disabling: %v", err)
		disableCloudForRun(cfg, checker)
		logStorageNotInitialized("Cloud", "Cloud storage", "  "+storage.CapitalizedCause(err))
		return nil
	}

	// DetectFilesystem prints "Checking cloud remote accessibility..." and, under it,
	// what the check found ("  Accessible", "  Timed out after 30s", ...).
	cloudFS, err := detectFilesystemInfo(opts.ctx, cloudBackend, cfg.CloudRemote, logger)
	if cloudFS == nil {
		// The remote did not answer: the fact is already under the check, the block
		// closes with the "✗" outcome and the SKIP. The full chain stays in DEBUG.
		logging.DebugStep(logger, "storage init", "cloud unavailable, disabling: %v", err)
		disableCloudForRun(cfg, checker)
		logStorageNotInitialized("Cloud", "Cloud storage", "")
		return nil
	}

	// A remote has no filesystem of its own to show: what the backend reports stays in
	// DEBUG, and the block goes on with what the check found ("  Accessible"). A local
	// directory has one, and the owner and mode of the copies depend on it: its facts
	// are the Secondary's.
	logging.DebugStep(logger, "storage init", "cloud filesystem=%s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
	var problems []string
	if _, local := storage.LocalCloudRemote(cfg.CloudRemote); local {
		var detectErr error
		var backend storage.Storage = cloudBackend
		if reporter, ok := backend.(detectionFailureReporter); ok {
			detectErr = reporter.DetectionFailure()
		}
		logging.DebugStep(logger, "storage init", "cloud local directory detect_err=%v", detectErr)
		if problem := logStorageFilesystem(cloudFS, detectErr); problem != "" {
			problems = append(problems, problem)
		}
	}
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
	cloudOwnedBackups := startupOwnedListing(opts.ctx, cloudBackend, cloudBackups)
	var checked storage.Storage = cloudBackend
	if reporter, ok := checked.(dryRunCheckReporter); ok && reporter.NotCheckedInDryRun() {
		// A dry run could check this remote only by writing to it: it is used, not checked.
		logging.DebugStep(logger, "storage init", "cloud not checked in dry run (listing not permitted)")
		logStorageInitSummary(formatNotCheckedInitSummary("Cloud storage", cfg, storage.LocationCloud, cloudStats, cloudOwnedBackups))
		return cloudFS
	}
	logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, cloudStats, cloudOwnedBackups, problems...))
	return cloudFS
}

// dryRunCheckReporter is implemented by a backend whose accessibility check a dry run
// can leave undone (CloudStorage: listing not permitted, write test skipped).
type dryRunCheckReporter interface {
	NotCheckedInDryRun() bool
}

// disableSecondaryForRun turns the secondary destination off for the rest of the run:
// no copy at [6], no log copy, no disk-space check.
func disableSecondaryForRun(cfg *config.Config, checker *checks.Checker) {
	cfg.SecondaryEnabled = false
	cfg.SecondaryLogPath = ""
	if checker != nil {
		checker.DisableSecondary()
	}
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
// itself and records how many archives that listing held (ListedBackups), while
// fetchBackupList returns nil on a failed listing, which is also what an empty
// location returns: scoping that nil would report "0 owned" beside a location GetStats
// saw full. A listing that changed between the two reads is refused the same way, and
// the caller keeps the GetStats count.
func startupOwnedBackups(ctx context.Context, backend storage.Storage, stats *storage.StorageStats, backups []*types.BackupMetadata) (int, bool) {
	if backend == nil || stats == nil || len(backups) != stats.ListedBackups {
		return 0, false
	}
	return storage.CountOwnedBackups(ctx, backend, backups)
}

// startupOwnedListing narrows the startup listing to this host's own backups, by the
// rule retention prunes by, so the storage-init GFS tiers count what "  Backups: <N>"
// counts. A backend this package cannot attribute keeps the listing as it is.
func startupOwnedListing(ctx context.Context, backend storage.Storage, backups []*types.BackupMetadata) []*types.BackupMetadata {
	owned, ok := storage.OwnedBackups(ctx, backend, backups)
	if !ok {
		return backups
	}
	return owned
}
