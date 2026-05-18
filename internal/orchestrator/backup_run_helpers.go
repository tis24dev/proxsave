// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/metrics"
	"github.com/tis24dev/proxsave/internal/types"
)

func (o *Orchestrator) shouldExportBackupMetrics(stats *BackupStats) bool {
	return stats != nil && o.cfg != nil && o.cfg.MetricsEnabled && !o.dryRun
}

func (o *Orchestrator) ensureBackupStatsTiming(stats *BackupStats) {
	if stats.EndTime.IsZero() {
		stats.EndTime = o.now()
	}
	if stats.Duration == 0 && !stats.StartTime.IsZero() {
		stats.Duration = stats.EndTime.Sub(stats.StartTime)
	}
}

func backupMetricsExitCode(stats *BackupStats, runErr error) int {
	if runErr == nil {
		if stats.ExitCode == 0 {
			return types.ExitSuccess.Int()
		}
		return stats.ExitCode
	}

	var backupErr *BackupError
	if errors.As(runErr, &backupErr) {
		return backupErr.Code.Int()
	}
	return types.ExitGenericError.Int()
}

func (o *Orchestrator) exportPrometheusBackupMetrics(stats *BackupStats) {
	m := stats.toPrometheusMetrics()
	if m == nil {
		return
	}

	exporter := metrics.NewPrometheusExporter(o.cfg.MetricsPath, o.logger)
	if err := exporter.Export(m); err != nil {
		o.logger.Warning("Failed to export Prometheus metrics: %v", err)
	}
}

func (o *Orchestrator) parseFailedBackupLogCounts(stats *BackupStats) {
	if stats.LogFilePath == "" {
		o.logger.Debug("No log file path specified, error/warning counts will be 0 (failure path)")
		return
	}

	o.logger.Debug("Parsing log file for error/warning counts after failure: %s", stats.LogFilePath)
	o.refreshLogIssuesFromFile(stats, false)
	if stats.ErrorCount > 0 || stats.WarningCount > 0 {
		o.logger.Debug("Found %d errors and %d warnings in log file (failure path)", stats.ErrorCount, stats.WarningCount)
	}
}

func backupFailureExitCode(runErr error) int {
	var backupErr *BackupError
	if errors.As(runErr, &backupErr) {
		return backupErr.Code.Int()
	}
	return types.ExitBackupError.Int()
}

func (o *Orchestrator) buildBackupCollectorConfig() *backup.CollectorConfig {
	collectorConfig := backup.GetDefaultCollectorConfig()
	collectorConfig.ExcludePatterns = append([]string(nil), o.excludePatterns...)
	if o.cfg == nil {
		return collectorConfig
	}

	applyCollectorOverrides(collectorConfig, o.cfg)
	if len(o.cfg.BackupBlacklist) > 0 {
		collectorConfig.ExcludePatterns = append(collectorConfig.ExcludePatterns, o.cfg.BackupBlacklist...)
	}
	return collectorConfig
}

func (o *Orchestrator) runBackupCollector(run *backupRunContext, workspace *backupWorkspace, collectorConfig *backup.CollectorConfig) (*backup.Collector, error) {
	collector := backup.NewCollectorWithDeps(o.logger, collectorConfig, workspace.tempDir, run.proxmoxType, o.dryRun, o.collectorDeps())
	o.logger.Debug("Starting collector run (type=%s)", run.proxmoxType)
	if err := collector.CollectAll(run.ctx); err != nil {
		return nil, err
	}
	return collector, nil
}

func (o *Orchestrator) applyBackupCollectionStats(stats *BackupStats, collStats *backup.CollectionStats, collector *backup.Collector) {
	stats.FilesCollected = int(collStats.FilesProcessed)
	stats.FilesFailed = int(collStats.FilesFailed)
	stats.FilesNotFound = int(collStats.FilesNotFound)
	stats.DirsCreated = int(collStats.DirsCreated)
	stats.BytesCollected = collStats.BytesCollected
	stats.FilesIncluded = int(collStats.FilesProcessed)
	stats.FilesMissing = int(collStats.FilesNotFound)
	stats.UncompressedSize = collStats.BytesCollected
	if stats.ProxmoxType.SupportsPVE() {
		stats.ClusterMode = standaloneClusterMode(collector)
	}
}

func standaloneClusterMode(collector *backup.Collector) string {
	if collector.IsClusteredPVE() {
		return "cluster"
	}
	return "standalone"
}

func (o *Orchestrator) writeBackupCollectionMetadata(tempDir, hostname string, stats *BackupStats, collector *backup.Collector) {
	if err := o.writeBackupMetadata(tempDir, stats); err != nil {
		o.logger.Debug("Failed to write backup metadata: %v", err)
	}
	if err := collector.WriteManifest(hostname); err != nil {
		o.logger.Debug("Failed to write backup manifest: %v", err)
	}
}

func (o *Orchestrator) logBackupCollectionSummary(collStats *backup.CollectionStats) {
	o.logger.Info("Collection completed: %d files (%s), %d failed, %d dirs created",
		collStats.FilesProcessed,
		backup.FormatBytes(collStats.BytesCollected),
		collStats.FilesFailed,
		collStats.DirsCreated)
}

func (o *Orchestrator) applyBackupOptimizations(ctx context.Context, tempDir string) error {
	if !o.optimizationCfg.Enabled() {
		o.logger.Debug("Skipping optimization step (all features disabled)")
		return nil
	}

	fmt.Println()
	o.logger.Step("Backup optimizations on collected data")
	if err := backup.ApplyOptimizations(ctx, o.logger, tempDir, o.optimizationCfg); err != nil {
		o.logger.Warning("Backup optimizations completed with warnings: %v", err)
	}
	return nil
}

func estimatedBackupSizeGB(bytesCollected int64) float64 {
	estimatedSizeGB := float64(bytesCollected) / (1024.0 * 1024.0 * 1024.0)
	if estimatedSizeGB < 0.001 {
		return 0.001
	}
	return estimatedSizeGB
}

func backupDiskValidationError(message string, diskErr error) error {
	errMsg := message
	if errMsg == "" && diskErr != nil {
		errMsg = diskErr.Error()
	}
	if errMsg == "" {
		errMsg = "insufficient disk space"
	}
	if diskErr == nil {
		diskErr = errors.New(errMsg)
	}
	return &BackupError{
		Phase: "disk",
		Err:   fmt.Errorf("disk space validation failed: %w", diskErr),
		Code:  types.ExitDiskSpaceError,
	}
}

func (o *Orchestrator) buildBackupArchiverConfig(run *backupRunContext, ageRecipients []age.Recipient) *backup.ArchiverConfig {
	return BuildArchiverConfig(
		o.compressionType,
		run.normalizedLevel,
		o.compressionThreads,
		o.compressionMode,
		o.dryRun,
		o.cfg != nil && o.cfg.EncryptArchive,
		ageRecipients,
		run.collectorConfig.ExcludePatterns,
	)
}

func (o *Orchestrator) applyBackupArchiverStats(stats *BackupStats, archiver *backup.Archiver) {
	stats.Compression = archiver.ResolveCompression()
	stats.CompressionLevel = archiver.CompressionLevel()
	stats.CompressionMode = archiver.CompressionMode()
	stats.CompressionThreads = archiver.CompressionThreads()
}

func (o *Orchestrator) backupArchivePath(run *backupRunContext, archiver *backup.Archiver) string {
	archiveBasename := fmt.Sprintf("%s-backup-%s", run.hostname, run.timestamp)
	return filepath.Join(o.backupPath, archiveBasename+archiver.GetArchiveExtension())
}

func (o *Orchestrator) logResolvedBackupCompression(stats *BackupStats) {
	if stats.RequestedCompression != stats.Compression {
		o.logger.Info("Using %s compression (requested %s)", stats.Compression, stats.RequestedCompression)
	}
}

func createBackupArchiveFile(ctx context.Context, archiver *backup.Archiver, tempDir, archivePath string) error {
	if err := archiver.CreateArchive(ctx, tempDir, archivePath); err != nil {
		return backupArchiveCreationError(err)
	}
	return nil
}

func backupArchiveCreationError(err error) error {
	phase := "archive"
	code := types.ExitArchiveError
	var compressionErr *backup.CompressionError
	if errors.As(err, &compressionErr) {
		phase = "compression"
		code = types.ExitCompressionError
	}
	return &BackupError{Phase: phase, Err: err, Code: code}
}

func (o *Orchestrator) skipDryRunArtifactVerification(stats *BackupStats, artifacts *backupArtifacts) error {
	fmt.Println()
	o.logStep(4, "Verification skipped (dry run mode)")
	o.logger.Info("[DRY RUN] Would create archive: %s", artifacts.archivePath)
	stats.EndTime = o.now()
	return nil
}

func (o *Orchestrator) recordArchiveSize(stats *BackupStats, artifacts *backupArtifacts) {
	size, err := artifacts.archiver.GetArchiveSize(artifacts.archivePath)
	if err != nil {
		o.logger.Warning("Failed to get archive size: %v", err)
		return
	}

	stats.ArchiveSize = size
	stats.CompressedSize = size
	stats.updateCompressionMetrics()
	o.logger.Debug("Archive created: %s (%s)", artifacts.archivePath, backup.FormatBytes(size))
}

func (o *Orchestrator) generateArchiveChecksum(ctx context.Context, archivePath string) (string, error) {
	checksum, err := backup.GenerateChecksum(ctx, o.logger, archivePath)
	if err != nil {
		return "", &BackupError{
			Phase: "verification",
			Err:   fmt.Errorf("checksum generation failed: %w", err),
			Code:  types.ExitVerificationError,
		}
	}
	return checksum, nil
}

func (o *Orchestrator) writeArchiveChecksum(workspace *backupWorkspace, artifacts *backupArtifacts, checksum string) error {
	checksumContent := fmt.Sprintf("%s  %s\n", checksum, filepath.Base(artifacts.archivePath))
	if err := workspace.fs.WriteFile(artifacts.checksumPath, []byte(checksumContent), 0o640); err != nil {
		return fmt.Errorf("write checksum file %s: %w", artifacts.checksumPath, err)
	}
	o.logger.Debug("Checksum file written to %s", artifacts.checksumPath)
	return nil
}

func (o *Orchestrator) writeArchiveManifest(run *backupRunContext, artifacts *backupArtifacts, checksum string) error {
	manifestPath := artifacts.archivePath + ".manifest.json"
	manifest := o.newArchiveManifest(run.stats, artifacts.archivePath, checksum)
	if err := backup.CreateManifest(run.ctx, o.logger, manifest, manifestPath); err != nil {
		return &BackupError{
			Phase: "verification",
			Err:   fmt.Errorf("manifest creation failed: %w", err),
			Code:  types.ExitVerificationError,
		}
	}
	run.stats.ManifestPath = manifestPath
	artifacts.manifestPath = manifestPath
	return nil
}

func (o *Orchestrator) newArchiveManifest(stats *BackupStats, archivePath, checksum string) *backup.Manifest {
	return &backup.Manifest{
		ArchivePath:      archivePath,
		ArchiveSize:      stats.ArchiveSize,
		SHA256:           checksum,
		CreatedAt:        stats.Timestamp,
		CompressionType:  string(stats.Compression),
		CompressionLevel: stats.CompressionLevel,
		CompressionMode:  stats.CompressionMode,
		ProxmoxType:      string(stats.ProxmoxType),
		ProxmoxTargets:   append([]string(nil), stats.ProxmoxTargets...),
		ProxmoxVersion:   stats.ProxmoxVersion,
		PVEVersion:       stats.PVEVersion,
		PBSVersion:       stats.PBSVersion,
		Hostname:         stats.Hostname,
		ScriptVersion:    stats.ScriptVersion,
		EncryptionMode:   o.archiveEncryptionMode(),
		ClusterMode:      stats.ClusterMode,
	}
}

func (o *Orchestrator) archiveEncryptionMode() string {
	if o.cfg != nil && o.cfg.EncryptArchive {
		return "age"
	}
	return "none"
}

func (o *Orchestrator) writeLegacyMetadataAlias(workspace *backupWorkspace, artifacts *backupArtifacts) {
	metadataAlias := artifacts.archivePath + ".metadata"
	if err := copyFile(workspace.fs, artifacts.manifestPath, metadataAlias); err != nil {
		o.logger.Warning("Failed to write legacy metadata file %s: %v", metadataAlias, err)
	} else {
		o.logger.Debug("Legacy metadata file written to %s", metadataAlias)
	}
}
