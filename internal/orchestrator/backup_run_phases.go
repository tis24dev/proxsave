// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/types"
)

type backupRunContext struct {
	ctx             context.Context
	envInfo         *environment.EnvironmentInfo
	hostname        string
	proxmoxType     types.ProxmoxType
	startTime       time.Time
	timestamp       string
	normalizedLevel int
	collectorConfig *backup.CollectorConfig
	stats           *BackupStats
}

type backupWorkspace struct {
	registry *TempDirRegistry
	fs       FS
	tempRoot string
	tempDir  string
}

type backupArtifacts struct {
	archiver     *backup.Archiver
	archivePath  string
	checksumPath string
	manifestPath string
	bundlePath   string
}

func (o *Orchestrator) newBackupRunContext(ctx context.Context, envInfo *environment.EnvironmentInfo, hostname string) *backupRunContext {
	if ctx == nil {
		ctx = context.Background()
	}
	if envInfo == nil {
		envInfo = o.envInfo
	} else {
		o.SetEnvironmentInfo(envInfo)
	}

	pType := types.ProxmoxUnknown
	if envInfo != nil {
		pType = envInfo.Type
	}

	startTime := o.startTime
	if startTime.IsZero() {
		startTime = o.now()
		o.startTime = startTime
	}

	return &backupRunContext{
		ctx:             ctx,
		envInfo:         envInfo,
		hostname:        hostname,
		proxmoxType:     pType,
		startTime:       startTime,
		timestamp:       startTime.Format("20060102-150405"),
		normalizedLevel: normalizeCompressionLevel(o.compressionType, o.compressionLevel),
	}
}

func (o *Orchestrator) initBackupRun(run *backupRunContext) *BackupStats {
	fmt.Println()
	o.logStep(1, "Initializing backup statistics and temporary workspace")
	run.stats = InitializeBackupStats(
		run.hostname,
		run.envInfo,
		o.version,
		run.startTime,
		o.cfg,
		o.compressionType,
		o.compressionMode,
		run.normalizedLevel,
		o.compressionThreads,
		o.backupPath,
		o.serverID,
		o.serverMAC,
	)
	if logFile := o.logger.GetLogFilePath(); logFile != "" {
		run.stats.LogFilePath = logFile
	}
	if o.versionUpdateAvailable || o.updateCurrentVersion != "" || o.updateLatestVersion != "" {
		run.stats.NewVersionAvailable = o.versionUpdateAvailable
		run.stats.CurrentVersion = o.updateCurrentVersion
		run.stats.LatestVersion = o.updateLatestVersion
	}
	return run.stats
}

func (o *Orchestrator) exportBackupMetrics(run *backupRunContext, runErr error) {
	stats := run.stats
	if !o.shouldExportBackupMetrics(stats) {
		return
	}

	o.ensureBackupStatsTiming(stats)
	stats.ExitCode = backupMetricsExitCode(stats, runErr)
	o.exportPrometheusBackupMetrics(stats)
}

func (o *Orchestrator) finalizeFailedBackupStats(run *backupRunContext, runErr error) {
	stats := run.stats
	if runErr == nil || stats == nil {
		return
	}

	o.ensureBackupStatsTiming(stats)
	o.parseFailedBackupLogCounts(stats)
	stats.ExitCode = backupFailureExitCode(runErr)
}

func (o *Orchestrator) prepareBackupWorkspace(run *backupRunContext, workspace *backupWorkspace) error {
	o.logger.Debug("Creating temporary directory for collection output")
	workspace.tempRoot = filepath.Join("/tmp", "proxsave")
	if err := workspace.fs.MkdirAll(workspace.tempRoot, 0o755); err != nil {
		return fmt.Errorf("temp directory creation failed - path: %s: %w", workspace.tempRoot, err)
	}

	tempDir, err := workspace.fs.MkdirTemp(workspace.tempRoot, fmt.Sprintf("proxsave-%s-%s-", run.hostname, run.timestamp))
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}
	workspace.tempDir = tempDir

	if o.dryRun {
		o.logger.Info("[DRY RUN] Temporary directory would be: %s", workspace.tempDir)
	} else {
		o.logger.Debug("Using temporary directory: %s", workspace.tempDir)
	}
	return nil
}

func (o *Orchestrator) cleanupBackupWorkspace(workspace *backupWorkspace) {
	if workspace.registry == nil {
		if cleanupErr := workspace.fs.RemoveAll(workspace.tempDir); cleanupErr != nil {
			o.logger.Warning("Failed to remove temp directory %s: %v", workspace.tempDir, cleanupErr)
		}
		return
	}
	o.logger.Debug("Temporary workspace preserved at %s (will be removed at the next startup)", workspace.tempDir)
}

func (o *Orchestrator) markBackupWorkspace(workspace *backupWorkspace) error {
	markerPath := filepath.Join(workspace.tempDir, ".proxsave-marker")
	markerContent := fmt.Sprintf(
		"Created by PID %d on %s UTC\n",
		os.Getpid(),
		o.now().UTC().Format("2006-01-02 15:04:05"),
	)
	return workspace.fs.WriteFile(markerPath, []byte(markerContent), 0600)
}

func (o *Orchestrator) registerBackupWorkspace(workspace *backupWorkspace) {
	if workspace.registry == nil {
		return
	}
	if err := workspace.registry.Register(workspace.tempDir); err != nil {
		o.logger.Debug("Failed to register temp directory %s: %v", workspace.tempDir, err)
	}
}

func (o *Orchestrator) collectBackupData(run *backupRunContext, workspace *backupWorkspace) error {
	fmt.Println()
	o.logStep(2, "Collection of configuration files and optimizations")
	o.logger.Info("Collecting configuration files...")
	o.logger.Debug("Collector dry-run=%v excludePatterns=%d", o.dryRun, len(o.excludePatterns))

	collectorConfig := o.buildBackupCollectorConfig()
	run.collectorConfig = collectorConfig

	if err := collectorConfig.Validate(); err != nil {
		return &BackupError{Phase: "config", Err: err, Code: types.ExitConfigError}
	}

	collector, err := o.runBackupCollector(run, workspace, collectorConfig)
	if err != nil {
		return &BackupError{Phase: "collection", Err: err, Code: types.ExitCollectionError}
	}

	collStats := collector.GetStats()
	o.applyBackupCollectionStats(run.stats, collStats, collector)
	o.writeBackupCollectionMetadata(workspace.tempDir, run.hostname, run.stats, collector)
	o.logBackupCollectionSummary(collStats)

	if err := o.validateCollectedBackupSize(run.stats); err != nil {
		return err
	}

	return o.applyBackupOptimizations(run.ctx, workspace.tempDir)
}

func (o *Orchestrator) validateCollectedBackupSize(stats *BackupStats) error {
	if o.checker == nil || stats.BytesCollected <= 0 {
		return nil
	}

	o.logger.Debug("Running disk-space validation for estimated data size")
	result := o.checker.CheckDiskSpaceForEstimate(estimatedBackupSizeGB(stats.BytesCollected))
	if result.Passed {
		o.logger.Debug("Disk check passed: %s", result.Message)
		return nil
	}

	return backupDiskValidationError(result.Message, result.Error)
}

func (o *Orchestrator) createBackupArchive(run *backupRunContext, workspace *backupWorkspace) (*backupArtifacts, error) {
	fmt.Println()
	o.logStep(3, "Creation of compressed archive")
	o.logger.Info("Creating compressed archive...")
	o.logger.Debug("Archiver configuration: type=%s level=%d mode=%s threads=%d",
		o.compressionType, run.normalizedLevel, o.compressionMode, o.compressionThreads)

	ageRecipients, err := o.prepareAgeRecipients(run.ctx)
	if err != nil {
		return nil, &BackupError{Phase: "encryption", Err: err, Code: types.ExitEncryptionError}
	}

	archiverConfig := o.buildBackupArchiverConfig(run, ageRecipients)
	if err := archiverConfig.Validate(); err != nil {
		return nil, &BackupError{Phase: "config", Err: err, Code: types.ExitConfigError}
	}

	archiver := backup.NewArchiver(o.logger, archiverConfig)
	o.applyBackupArchiverStats(run.stats, archiver)
	archivePath := o.backupArchivePath(run, archiver)
	o.logResolvedBackupCompression(run.stats)

	if err := createBackupArchiveFile(run.ctx, archiver, workspace.tempDir, archivePath); err != nil {
		return nil, err
	}

	run.stats.ArchivePath = archivePath
	return &backupArtifacts{
		archiver:     archiver,
		archivePath:  archivePath,
		checksumPath: archivePath + ".sha256",
	}, nil
}

func (o *Orchestrator) verifyAndWriteBackupArtifacts(run *backupRunContext, workspace *backupWorkspace, artifacts *backupArtifacts) error {
	stats := run.stats
	if o.dryRun {
		return o.skipDryRunArtifactVerification(stats, artifacts)
	}

	fmt.Println()
	o.logStep(4, "Verification of archive and metadata generation")
	o.recordArchiveSize(stats, artifacts)

	if err := artifacts.archiver.VerifyArchive(run.ctx, artifacts.archivePath); err != nil {
		return &BackupError{Phase: "verification", Err: err, Code: types.ExitVerificationError}
	}

	checksum, err := o.generateArchiveChecksum(run.ctx, artifacts.archivePath)
	if err != nil {
		return err
	}
	stats.Checksum = checksum

	if err := o.writeArchiveChecksum(workspace, artifacts, checksum); err != nil {
		return &BackupError{
			Phase: "verification",
			Err:   err,
			Code:  types.ExitVerificationError,
		}
	}
	if err := o.writeArchiveManifest(run, artifacts, checksum); err != nil {
		return err
	}
	o.writeLegacyMetadataAlias(workspace, artifacts)
	return nil
}

func (o *Orchestrator) bundleBackupArtifacts(run *backupRunContext, workspace *backupWorkspace, artifacts *backupArtifacts) error {
	if o.dryRun {
		return nil
	}

	bundleEnabled := o.cfg != nil && o.cfg.BundleAssociatedFiles
	if !bundleEnabled {
		fmt.Println()
		o.logger.Skip("Bundling disabled")
		run.stats.EndTime = o.now()
		o.logger.Info("✓ Archive created and verified")
		return nil
	}

	fmt.Println()
	o.logStep(5, "Bundling of archive, checksum and metadata")
	o.logger.Debug("Bundling enabled: creating bundle from %s", filepath.Base(artifacts.archivePath))
	bundlePath, err := o.createBundle(run.ctx, artifacts.archivePath)
	if err != nil {
		return &BackupError{
			Phase: "archive",
			Err:   fmt.Errorf("bundle creation failed: %w", err),
			Code:  types.ExitArchiveError,
		}
	}

	if err := o.removeAssociatedFiles(artifacts.archivePath); err != nil {
		o.logger.Warning("Failed to remove raw files after bundling: %v", err)
	} else {
		o.logger.Debug("Removed raw tar/checksum/metadata after bundling")
	}

	stats := run.stats
	if info, err := workspace.fs.Stat(bundlePath); err == nil {
		stats.ArchiveSize = info.Size()
		stats.CompressedSize = info.Size()
		stats.updateCompressionMetrics()
	}
	stats.ArchivePath = bundlePath
	stats.ManifestPath = ""
	stats.BundleCreated = true
	artifacts.bundlePath = bundlePath
	artifacts.archivePath = bundlePath
	o.logger.Debug("Bundle ready: %s", filepath.Base(bundlePath))

	stats.EndTime = o.now()
	o.logger.Info("✓ Archive created and verified")
	return nil
}

func (o *Orchestrator) finalizeBackupStats(run *backupRunContext) {
	stats := run.stats
	stats.Duration = stats.EndTime.Sub(stats.StartTime)

	if stats.LogFilePath != "" {
		o.logger.Debug("Parsing log file for error/warning counts: %s", stats.LogFilePath)
		_, errorCount, warningCount := ParseLogCounts(stats.LogFilePath, 0)
		stats.ErrorCount = errorCount
		stats.WarningCount = warningCount
		if errorCount > 0 || warningCount > 0 {
			o.logger.Debug("Found %d errors and %d warnings in log file", errorCount, warningCount)
		}
	} else {
		o.logger.Debug("No log file path specified, error/warning counts will be 0")
	}

	switch {
	case stats.ErrorCount > 0:
		stats.ExitCode = types.ExitBackupError.Int()
	case stats.WarningCount > 0:
		stats.ExitCode = types.ExitGenericError.Int()
	default:
		stats.ExitCode = types.ExitSuccess.Int()
	}
	o.logger.Debug("Aggregated exit code based on log analysis: %d", stats.ExitCode)
}

func (o *Orchestrator) dispatchBackupArtifacts(run *backupRunContext) error {
	if len(o.storageTargets) == 0 {
		fmt.Println()
		o.logStep(6, "No storage targets registered - skipping")
	} else if o.dryRun {
		fmt.Println()
		o.logStep(6, "Storage dispatch skipped (dry run mode)")
	} else {
		fmt.Println()
		o.logStep(6, "Dispatching archive to %d storage target(s)", len(o.storageTargets))
		o.logGlobalRetentionPolicy()
	}

	if o.dryRun {
		return nil
	}

	o.logger.Debug("Dispatching archive to %d storage targets", len(o.storageTargets))
	return o.dispatchPostBackup(run.ctx, run.stats)
}
