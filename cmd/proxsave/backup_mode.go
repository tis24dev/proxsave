// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

type backupModeOptions struct {
	ctx              context.Context
	cfg              *config.Config
	logger           *logging.Logger
	envInfo          *environment.EnvironmentInfo
	unprivilegedInfo environment.UnprivilegedContainerInfo
	updateInfo       *UpdateInfo
	toolVersion      string
	dryRun           bool
	startTime        time.Time
	heapProfilePath  string
	serverIDValue    string
	serverMACValue   string
}

type backupModeResult struct {
	orch            *orchestrator.Orchestrator
	earlyErrorState *orchestrator.EarlyErrorState
	supportStats    *orchestrator.BackupStats
	exitCode        int
}

func runBackupMode(opts backupModeOptions) backupModeResult {
	orch, earlyErrorState, exitCode := initializeBackupOrchestrator(opts)
	if earlyErrorState != nil {
		return finishBackupMode(orch, earlyErrorState, nil, exitCode)
	}

	verifyBackupDirectories(opts.cfg, opts.logger)

	checker, earlyErrorState, exitCode := configurePreBackupChecker(opts, orch)
	if earlyErrorState != nil {
		return finishBackupMode(orch, earlyErrorState, nil, exitCode)
	}

	defer func() {
		if err := orch.ReleaseBackupLock(); err != nil {
			logging.Warning("Failed to release backup lock: %v", err)
		}
	}()

	storageState, earlyErrorState, exitCode := initializeBackupStorage(opts, orch, checker)
	if earlyErrorState != nil {
		return finishBackupMode(orch, earlyErrorState, nil, exitCode)
	}

	initializeBackupNotifications(opts, orch)
	logBackupRuntimeSummary(opts.cfg, storageState)

	stats, earlyErrorState, exitCode := runConfiguredBackup(opts, orch)
	return finishBackupMode(orch, earlyErrorState, stats, exitCode)
}

func finishBackupMode(orch *orchestrator.Orchestrator, earlyErrorState *orchestrator.EarlyErrorState, stats *orchestrator.BackupStats, exitCode int) backupModeResult {
	return backupModeResult{
		orch:            orch,
		earlyErrorState: earlyErrorState,
		supportStats:    stats,
		exitCode:        exitCode,
	}
}

func initializeBackupOrchestrator(opts backupModeOptions) (*orchestrator.Orchestrator, *orchestrator.EarlyErrorState, int) {
	logger := opts.logger

	logging.Step("Initializing backup orchestrator")
	orchInitDone := logging.DebugStart(logger, "orchestrator init", "dry_run=%v", opts.dryRun)
	orch := orchestrator.New(logger, opts.dryRun)
	configureBackupOrchestrator(opts, orch)

	if earlyErrorState, exitCode := ensureBackupAgeRecipientsReady(opts, orch, orchInitDone); earlyErrorState != nil {
		return orch, earlyErrorState, exitCode
	}
	orchInitDone(nil)

	logging.Info("✓ Orchestrator initialized")
	fmt.Println()
	return orch, nil, types.ExitSuccess.Int()
}

func configureBackupOrchestrator(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	cfg := opts.cfg
	orch.SetUnprivilegedContainerContext(opts.unprivilegedInfo.Detected, opts.unprivilegedInfo.Details)
	orch.SetVersion(opts.toolVersion)
	orch.SetConfig(cfg)
	orch.SetIdentity(opts.serverIDValue, opts.serverMACValue)
	orch.SetEnvironmentInfo(opts.envInfo)
	orch.SetStartTime(opts.startTime)
	if opts.updateInfo != nil {
		orch.SetUpdateInfo(opts.updateInfo.NewVersion, opts.updateInfo.Current, opts.updateInfo.Latest)
	}

	orch.SetBackupConfig(
		cfg.BackupPath,
		cfg.LogPath,
		cfg.CompressionType,
		cfg.CompressionLevel,
		cfg.CompressionThreads,
		cfg.CompressionMode,
		buildBackupExcludePatterns(cfg),
	)
	orch.SetOptimizationConfig(backupOptimizationConfig(cfg))
}

func backupOptimizationConfig(cfg *config.Config) backup.OptimizationConfig {
	return backup.OptimizationConfig{
		EnableChunking:            cfg.EnableSmartChunking,
		EnableDeduplication:       cfg.EnableDeduplication,
		EnablePrefilter:           cfg.EnablePrefilter,
		ChunkSizeBytes:            int64(cfg.ChunkSizeMB) * bytesPerMegabyte,
		ChunkThresholdBytes:       int64(cfg.ChunkThresholdMB) * bytesPerMegabyte,
		PrefilterMaxFileSizeBytes: int64(cfg.PrefilterMaxFileSizeMB) * bytesPerMegabyte,
	}
}

func ensureBackupAgeRecipientsReady(opts backupModeOptions, orch *orchestrator.Orchestrator, orchInitDone func(error)) (*orchestrator.EarlyErrorState, int) {
	err := orch.EnsureAgeRecipientsReady(opts.ctx)
	if err == nil {
		return nil, types.ExitSuccess.Int()
	}

	orchInitDone(err)
	if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
		logging.Warning("Encryption setup aborted by user. Exiting...")
		return backupAgeRecipientEarlyError(err, types.ExitGenericError), types.ExitGenericError.Int()
	}

	logging.Error("ERROR: %v", err)
	return backupAgeRecipientEarlyError(err, types.ExitConfigError), types.ExitConfigError.Int()
}

func backupAgeRecipientEarlyError(err error, exitCode types.ExitCode) *orchestrator.EarlyErrorState {
	return &orchestrator.EarlyErrorState{
		Phase:     "encryption_setup",
		Error:     err,
		ExitCode:  exitCode,
		Timestamp: time.Now(),
	}
}

func buildBackupExcludePatterns(cfg *config.Config) []string {
	excludePatterns := append([]string(nil), cfg.ExcludePatterns...)
	excludePatterns = addPathExclusion(excludePatterns, cfg.BackupPath)
	if cfg.SecondaryEnabled {
		excludePatterns = addPathExclusion(excludePatterns, cfg.SecondaryPath)
	}
	if cfg.CloudEnabled && isLocalPath(cfg.CloudRemote) {
		excludePatterns = addPathExclusion(excludePatterns, cfg.CloudRemote)
	}
	return excludePatterns
}

func verifyBackupDirectories(cfg *config.Config, logger *logging.Logger) {
	logging.Step("Verifying directory structure")
	checkDir := func(name, path string) {
		ensureDirectoryExists(logger, name, path)
	}

	checkDir("Backup directory", cfg.BackupPath)
	checkDir("Log directory", cfg.LogPath)
	if cfg.SecondaryEnabled {
		secondaryLogPath := strings.TrimSpace(cfg.SecondaryLogPath)
		if secondaryLogPath != "" {
			checkDir("Secondary log directory", secondaryLogPath)
		} else {
			logging.Warning("✗ Secondary log directory not configured (secondary storage enabled)")
		}
	}
	if cfg.CloudEnabled {
		logCloudLogDirectory(cfg)
	}
	checkDir("Lock directory", cfg.LockPath)
}

func logCloudLogDirectory(cfg *config.Config) {
	cloudLogPath := strings.TrimSpace(cfg.CloudLogPath)
	if cloudLogPath == "" {
		logging.Warning("✗ Cloud log directory not configured (cloud storage enabled)")
		return
	}
	if strings.Contains(cloudLogPath, ":") {
		logging.Info("Cloud log path (legacy): %s", cloudLogPath)
		return
	}

	remoteName := extractRemoteName(cfg.CloudRemote)
	if remoteName != "" {
		logging.Info("Cloud log path: %s (using remote: %s)", cloudLogPath, remoteName)
	} else {
		logging.Warning("Cloud log path %s requires CLOUD_REMOTE to be set", cloudLogPath)
	}
}

func configurePreBackupChecker(opts backupModeOptions, orch *orchestrator.Orchestrator) (*checks.Checker, *orchestrator.EarlyErrorState, int) {
	cfg := opts.cfg
	logger := opts.logger

	logging.Debug("Configuring pre-backup validation checks...")
	checkerConfig := checks.GetDefaultCheckerConfig(cfg.BackupPath, cfg.LogPath, cfg.LockPath)
	checkerConfig.SecondaryEnabled = cfg.SecondaryEnabled
	if cfg.SecondaryEnabled && strings.TrimSpace(cfg.SecondaryPath) != "" {
		checkerConfig.SecondaryPath = cfg.SecondaryPath
	} else {
		checkerConfig.SecondaryPath = ""
	}
	checkerConfig.CloudEnabled = cfg.CloudEnabled
	if cfg.CloudEnabled && strings.TrimSpace(cfg.CloudRemote) != "" {
		if isLocalPath(cfg.CloudRemote) {
			checkerConfig.CloudPath = cfg.CloudRemote
		} else {
			checkerConfig.CloudPath = ""
			logging.Info("Skipping cloud disk-space check: %s is a remote rclone path (no local mount detected)", cfg.CloudRemote)
		}
	} else {
		checkerConfig.CloudPath = ""
	}
	checkerConfig.MinDiskPrimaryGB = cfg.MinDiskPrimaryGB
	checkerConfig.MinDiskSecondaryGB = cfg.MinDiskSecondaryGB
	checkerConfig.MinDiskCloudGB = cfg.MinDiskCloudGB
	checkerConfig.FsIoTimeout = time.Duration(cfg.FsIoTimeoutSeconds) * time.Second
	checkerConfig.DryRun = opts.dryRun
	checkerDone := logging.DebugStart(logger, "pre-backup check config", "dry_run=%v", opts.dryRun)
	if err := checkerConfig.Validate(); err != nil {
		checkerDone(err)
		logging.Error("Invalid checker configuration: %v", err)
		return nil, &orchestrator.EarlyErrorState{
			Phase:     "checker_config",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}, types.ExitConfigError.Int()
	}
	checkerDone(nil)
	checker := checks.NewChecker(logger, checkerConfig)
	orch.SetChecker(checker)

	logging.Info("✓ Pre-backup checks configured")
	fmt.Println()
	return checker, nil, types.ExitSuccess.Int()
}
