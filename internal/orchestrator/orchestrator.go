package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/metrics"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// BackupError represents a backup error with specific phase and exit code
type BackupError struct {
	Phase string         // "collection", "archive", "compression", "verification"
	Err   error          // Underlying error
	Code  types.ExitCode // Specific exit code
}

func (e *BackupError) Error() string {
	return fmt.Sprintf("%s phase failed: %v", e.Phase, e.Err)
}

func (e *BackupError) Unwrap() error {
	return e.Err
}

// EarlyErrorState represents an error that occurred before the backup started
// This is used to send notifications for initialization/configuration errors
type EarlyErrorState struct {
	Phase     string         // e.g., "config", "security", "encryption", "storage_init"
	Error     error          // The actual error that occurred
	ExitCode  types.ExitCode // Exit code to return
	Timestamp time.Time      // When the error occurred
}

// HasError returns true if an error has been recorded
func (e *EarlyErrorState) HasError() bool {
	return e.Error != nil
}

// BackupStats contains statistics from backup operations
type BackupStats struct {
	Hostname                  string
	ProxmoxType               types.ProxmoxType
	ProxmoxVersion            string
	BundleCreated             bool
	Timestamp                 time.Time
	Version                   string
	StartTime                 time.Time
	EndTime                   time.Time
	FilesCollected            int
	FilesFailed               int
	DirsCreated               int
	BytesCollected            int64
	ArchiveSize               int64
	UncompressedSize          int64
	CompressedSize            int64
	Duration                  time.Duration
	ArchivePath               string
	RequestedCompression      types.CompressionType
	RequestedCompressionMode  string
	Compression               types.CompressionType
	CompressionLevel          int
	CompressionMode           string
	CompressionThreads        int
	CompressionRatio          float64
	CompressionRatioPercent   float64
	CompressionSavingsPercent float64
	ReportPath                string
	ManifestPath              string
	Checksum                  string
	LocalPath                 string
	SecondaryPath             string
	CloudPath                 string
	LocalStatus               string
	LocalStatusSummary        string // Summary message for early errors
	SecondaryStatus           string
	CloudStatus               string

	// System identification
	ServerID  string
	ServerMAC string

	// Cluster mode (only meaningful for PVE)
	ClusterMode string // "cluster" or "standalone"

	// File counts for notifications
	FilesIncluded int
	FilesMissing  int

	// Storage statistics
	SecondaryEnabled    bool
	LocalBackups        int
	LocalFreeSpace      uint64
	LocalTotalSpace     uint64
	SecondaryBackups    int
	SecondaryFreeSpace  uint64
	SecondaryTotalSpace uint64
	CloudEnabled        bool
	CloudBackups        int
	MaxLocalBackups     int
	MaxSecondaryBackups int
	MaxCloudBackups     int

	// Retention policy info (for notifications)
	LocalRetentionPolicy       string
	LocalGFSDaily              int
	LocalGFSWeekly             int
	LocalGFSMonthly            int
	LocalGFSYearly             int
	LocalGFSCurrentDaily       int
	LocalGFSCurrentWeekly      int
	LocalGFSCurrentMonthly     int
	LocalGFSCurrentYearly      int
	SecondaryRetentionPolicy   string
	SecondaryGFSDaily          int
	SecondaryGFSWeekly         int
	SecondaryGFSMonthly        int
	SecondaryGFSYearly         int
	SecondaryGFSCurrentDaily   int
	SecondaryGFSCurrentWeekly  int
	SecondaryGFSCurrentMonthly int
	SecondaryGFSCurrentYearly  int
	CloudRetentionPolicy       string
	CloudGFSDaily              int
	CloudGFSWeekly             int
	CloudGFSMonthly            int
	CloudGFSYearly             int
	CloudGFSCurrentDaily       int
	CloudGFSCurrentWeekly      int
	CloudGFSCurrentMonthly     int
	CloudGFSCurrentYearly      int

	// Error/warning counts
	ErrorCount   int
	WarningCount int
	LogFilePath  string

	// Exit code
	ExitCode       int
	ScriptVersion  string
	TelegramStatus string
	EmailStatus    string
}

// Orchestrator coordinates the backup process using Go components
type Orchestrator struct {
	checker              *checks.Checker
	logger               *logging.Logger
	cfg                  *config.Config
	prompter             Prompter
	fs                   FS
	system               SystemDetector
	clock                TimeProvider
	cmdRunner            CommandRunner
	version              string
	proxmoxVersion       string
	dryRun               bool
	forceNewAgeRecipient bool
	ageRecipientCache    []age.Recipient

	// Backup configuration
	backupPath         string
	logPath            string
	compressionType    types.CompressionType
	compressionLevel   int
	compressionThreads int
	compressionMode    string
	excludePatterns    []string
	optimizationCfg    backup.OptimizationConfig

	storageTargets       []StorageTarget
	notificationChannels []NotificationChannel
	tempRegistry         *TempDirRegistry

	// Identity
	serverID  string
	serverMAC string

	startTime time.Time
}

const tempDirCleanupAge = 24 * time.Hour

// New creates a new Orchestrator
func New(logger *logging.Logger, dryRun bool) *Orchestrator {
	deps := defaultDeps(logger, dryRun)
	setRestoreDeps(deps.FS, deps.Time, deps.Prompter, deps.Command, deps.System)
	return &Orchestrator{
		logger:               logger,
		dryRun:               dryRun,
		storageTargets:       make([]StorageTarget, 0),
		notificationChannels: make([]NotificationChannel, 0),
		prompter:             deps.Prompter,
		fs:                   deps.FS,
		system:               deps.System,
		clock:                deps.Time,
		cmdRunner:            deps.Command,
	}
}

func (o *Orchestrator) logStep(step int, format string, args ...interface{}) {
	if o == nil || o.logger == nil {
		return
	}
	message := format
	if len(args) > 0 {
		message = fmt.Sprintf(format, args...)
	}
	o.logger.Step("%s", message)
}

func (o *Orchestrator) logGlobalRetentionPolicy() {
	if o == nil || o.logger == nil || o.cfg == nil {
		return
	}

	// If GFS is enabled globally, policy is the same for all storage paths
	if o.cfg.IsGFSRetentionEnabled() {
		rc := storage.NewRetentionConfigFromConfig(o.cfg, storage.LocationPrimary)
		rc = storage.NormalizeGFSRetentionConfig(o.logger, "All Storage", rc)
		o.logger.Info("  Policy: GFS (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
			rc.Daily, rc.Weekly, rc.Monthly, rc.Yearly)
		return
	}

	// Simple (count-based) retention: may vary per path, summarize compactly
	local := o.cfg.LocalRetentionDays
	secondary := o.cfg.SecondaryRetentionDays
	cloud := o.cfg.CloudRetentionDays

	if local == 0 && secondary == 0 && cloud == 0 {
		o.logger.Info("  Policy: simple (disabled)")
		return
	}

	parts := make([]string, 0, 3)
	if local > 0 {
		parts = append(parts, fmt.Sprintf("local=%d", local))
	}
	if secondary > 0 {
		parts = append(parts, fmt.Sprintf("secondary=%d", secondary))
	}
	if cloud > 0 {
		parts = append(parts, fmt.Sprintf("cloud=%d", cloud))
	}

	o.logger.Info("  Policy: simple (%s)", strings.Join(parts, ", "))
}

func (o *Orchestrator) SetForceNewAgeRecipient(force bool) {
	o.forceNewAgeRecipient = force
	if force {
		o.ageRecipientCache = nil
	}
}

func (o *Orchestrator) SetProxmoxVersion(version string) {
	o.proxmoxVersion = strings.TrimSpace(version)
}

// SetStartTime injects the timestamp to reuse across logs/backups.
func (o *Orchestrator) SetStartTime(t time.Time) {
	o.startTime = t
}

func (o *Orchestrator) now() time.Time {
	if o != nil && o.clock != nil {
		return o.clock.Now()
	}
	return time.Now()
}

func (o *Orchestrator) filesystem() FS {
	if o != nil && o.fs != nil {
		return o.fs
	}
	return osFS{}
}

// SetConfig attaches the loaded configuration to the orchestrator
func (o *Orchestrator) SetConfig(cfg *config.Config) {
	o.cfg = cfg
	o.ageRecipientCache = nil
}

// SetVersion sets the current tool version (for metadata reporting)
func (o *Orchestrator) SetVersion(version string) {
	o.version = version
}

// SetChecker sets the pre-backup checker
func (o *Orchestrator) SetChecker(checker *checks.Checker) {
	o.checker = checker
}

// SetIdentity configures server identity information for downstream consumers.
func (o *Orchestrator) SetIdentity(serverID, serverMAC string) {
	o.serverID = strings.TrimSpace(serverID)
	o.serverMAC = strings.TrimSpace(serverMAC)
}

// RunPreBackupChecks performs all pre-backup validation checks
func (o *Orchestrator) RunPreBackupChecks(ctx context.Context) error {
	if o.checker == nil {
		o.logger.Debug("No checker configured, skipping pre-backup checks")
		return nil
	}

	o.logger.Step("Pre-backup validation checks")

	results, err := o.checker.RunAllChecks(ctx)

	// Log all check results
	for _, result := range results {
		if result.Passed {
			if result.Name == "Disk Space (Estimated)" {
				o.logger.Debug("✓ %s: %s", result.Name, result.Message)
			} else {
				o.logger.Info("✓ %s: %s", result.Name, result.Message)
			}
		} else {
			o.logger.Error("✗ %s: %s", result.Name, result.Message)
		}
	}

	if err != nil {
		o.logger.Error("Pre-backup checks failed: %v", err)
		return fmt.Errorf("pre-backup checks failed: %w", err)
	}

	o.logger.Info("All pre-backup checks passed")
	return nil
}

// ReleaseBackupLock releases the backup lock file
func (o *Orchestrator) ReleaseBackupLock() error {
	if o.checker == nil {
		return nil
	}
	return o.checker.ReleaseLock()
}

// SetBackupConfig configures paths and compression for Go-based backup
func (o *Orchestrator) SetBackupConfig(backupPath, logPath string, compression types.CompressionType, level int, threads int, mode string, excludePatterns []string) {
	o.backupPath = backupPath
	o.logPath = logPath
	o.compressionType = compression
	o.compressionLevel = level
	o.compressionThreads = threads
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "standard"
	}
	o.compressionMode = mode
	o.excludePatterns = append([]string(nil), excludePatterns...)
}

// SetOptimizationConfig configures optional preprocessing (chunking/dedup/prefilter)
func (o *Orchestrator) SetOptimizationConfig(cfg backup.OptimizationConfig) {
	o.optimizationCfg = cfg
}

// SetTempDirRegistry allows callers (main/tests) to inject a custom registry.
func (o *Orchestrator) SetTempDirRegistry(reg *TempDirRegistry) {
	o.tempRegistry = reg
}

func (o *Orchestrator) describeTelegramConfig() string {
	return describeTelegramStatus(o.cfg)
}

func (o *Orchestrator) ensureTempRegistry() *TempDirRegistry {
	if o.tempRegistry != nil {
		return o.tempRegistry
	}

	registryPath := resolveRegistryPath()
	registry, err := NewTempDirRegistry(o.logger, registryPath)
	if err != nil {
		o.logger.Debug("Temp dir registry disabled: %v", err)
		return nil
	}
	o.tempRegistry = registry
	return registry
}

// RunGoBackup performs the entire backup using Go components (collector + archiver)
func (o *Orchestrator) RunGoBackup(ctx context.Context, pType types.ProxmoxType, hostname string) (stats *BackupStats, err error) {
	o.logger.Info("Starting Go-based backup orchestration for %s", pType)

	// Unified cleanup of previous execution artifacts
	registry := o.cleanupPreviousExecutionArtifacts()
	fs := o.filesystem()

	startTime := o.startTime
	if startTime.IsZero() {
		startTime = o.now()
		o.startTime = startTime
	}
	normalizedLevel := normalizeCompressionLevel(o.compressionType, o.compressionLevel)

	fmt.Println()
	o.logStep(1, "Initializing backup statistics and temporary workspace")
	stats = InitializeBackupStats(
		hostname,
		pType,
		o.proxmoxVersion,
		o.version,
		startTime,
		o.cfg,
		o.compressionType,
		o.compressionMode,
		normalizedLevel,
		o.compressionThreads,
		o.backupPath,
		o.serverID,
		o.serverMAC,
	)
	// Get log file path from logger (more reliable than env var)
	if logFile := o.logger.GetLogFilePath(); logFile != "" {
		stats.LogFilePath = logFile
	}

	metricsStats := stats
	defer func() {
		if metricsStats == nil || o.cfg == nil || !o.cfg.MetricsEnabled || o.dryRun {
			return
		}

		if metricsStats.EndTime.IsZero() {
			metricsStats.EndTime = o.now()
		}
		if metricsStats.Duration == 0 && !metricsStats.StartTime.IsZero() {
			metricsStats.Duration = metricsStats.EndTime.Sub(metricsStats.StartTime)
		}

		if err != nil {
			var backupErr *BackupError
			if errors.As(err, &backupErr) {
				metricsStats.ExitCode = backupErr.Code.Int()
			} else {
				metricsStats.ExitCode = types.ExitGenericError.Int()
			}
		} else if metricsStats.ExitCode == 0 {
			metricsStats.ExitCode = types.ExitSuccess.Int()
		}

		if m := metricsStats.toPrometheusMetrics(); m != nil {
			exporter := metrics.NewPrometheusExporter(o.cfg.MetricsPath, o.logger)
			if exportErr := exporter.Export(m); exportErr != nil {
				o.logger.Warning("Failed to export Prometheus metrics: %v", exportErr)
			}
		}
	}()

	// Ensure that, in case of failure, we still perform log parsing,
	// derive an exit code and dispatch notifications/log rotation.
	defer func() {
		if err == nil || stats == nil {
			return
		}

		// Ensure end time and duration are set
		if stats.EndTime.IsZero() {
			stats.EndTime = o.now()
		}
		if stats.Duration == 0 && !stats.StartTime.IsZero() {
			stats.Duration = stats.EndTime.Sub(stats.StartTime)
		}

		// Parse log file to populate error/warning counts
		if stats.LogFilePath != "" {
			o.logger.Debug("Parsing log file for error/warning counts after failure: %s", stats.LogFilePath)
			_, errorCount, warningCount := ParseLogCounts(stats.LogFilePath, 0)
			stats.ErrorCount = errorCount
			stats.WarningCount = warningCount
			if errorCount > 0 || warningCount > 0 {
				o.logger.Debug("Found %d errors and %d warnings in log file (failure path)", errorCount, warningCount)
			}
		} else {
			o.logger.Debug("No log file path specified, error/warning counts will be 0 (failure path)")
		}

		// Derive exit code from the error when possible
		var backupErr *BackupError
		if errors.As(err, &backupErr) {
			stats.ExitCode = backupErr.Code.Int()
		} else {
			stats.ExitCode = types.ExitBackupError.Int()
		}

	}()

	o.logger.Debug("Creating temporary directory for collection output")
	// Create temporary directory for collection (outside backup path)
	timestampStr := startTime.Format("20060102-150405")
	tempRoot := filepath.Join("/tmp", "proxsave")
	if err := fs.MkdirAll(tempRoot, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create temporary root directory: %w", err)
	}
	tempDir, err := fs.MkdirTemp(tempRoot, fmt.Sprintf("proxsave-%s-%s-", hostname, timestampStr))
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}
	if o.dryRun {
		o.logger.Info("[DRY RUN] Temporary directory would be: %s", tempDir)
	} else {
		o.logger.Debug("Using temporary directory: %s", tempDir)
	}
	defer func() {
		if registry == nil {
			if cleanupErr := fs.RemoveAll(tempDir); cleanupErr != nil {
				o.logger.Warning("Failed to remove temp directory %s: %v", tempDir, cleanupErr)
			}
			return
		}
		o.logger.Debug("Temporary workspace preserved at %s (will be removed at the next startup)", tempDir)
	}()

	// Create marker file for parity with Bash cleanup guarantees
	markerPath := filepath.Join(tempDir, ".proxsave-marker")
	markerContent := fmt.Sprintf(
		"Created by PID %d on %s UTC\n",
		os.Getpid(),
		o.now().UTC().Format("2006-01-02 15:04:05"),
	)
	if err := fs.WriteFile(markerPath, []byte(markerContent), 0600); err != nil {
		return stats, fmt.Errorf("failed to create temp marker file: %w", err)
	}

	if registry != nil {
		if err := registry.Register(tempDir); err != nil {
			o.logger.Debug("Failed to register temp directory %s: %v", tempDir, err)
		}
	}

	// Step 1: Collect configuration files
	fmt.Println()
	o.logStep(2, "Collection of configuration files and optimizations")
	o.logger.Info("Collecting configuration files...")
	o.logger.Debug("Collector dry-run=%v excludePatterns=%d", o.dryRun, len(o.excludePatterns))
	collectorConfig := backup.GetDefaultCollectorConfig()
	collectorConfig.ExcludePatterns = append([]string(nil), o.excludePatterns...)
	if o.cfg != nil {
		applyCollectorOverrides(collectorConfig, o.cfg)
		if len(o.cfg.BackupBlacklist) > 0 {
			collectorConfig.ExcludePatterns = append(collectorConfig.ExcludePatterns, o.cfg.BackupBlacklist...)
		}
	}

	if err := collectorConfig.Validate(); err != nil {
		return stats, &BackupError{
			Phase: "config",
			Err:   err,
			Code:  types.ExitConfigError,
		}
	}

	collector := backup.NewCollector(o.logger, collectorConfig, tempDir, pType, o.dryRun)

	o.logger.Debug("Starting collector run (type=%s)", pType)
	if err := collector.CollectAll(ctx); err != nil {
		// Return collection-specific error
		return stats, &BackupError{
			Phase: "collection",
			Err:   err,
			Code:  types.ExitCollectionError,
		}
	}

	// Get collection statistics
	collStats := collector.GetStats()
	stats.FilesCollected = int(collStats.FilesProcessed)
	stats.FilesFailed = int(collStats.FilesFailed)
	stats.DirsCreated = int(collStats.DirsCreated)
	stats.BytesCollected = collStats.BytesCollected
	stats.FilesIncluded = int(collStats.FilesProcessed)
	stats.FilesMissing = int(collStats.FilesFailed)
	stats.UncompressedSize = collStats.BytesCollected
	if pType == types.ProxmoxVE {
		if collector.IsClusteredPVE() {
			stats.ClusterMode = "cluster"
		} else {
			stats.ClusterMode = "standalone"
		}
	}

	if err := o.writeBackupMetadata(tempDir, stats); err != nil {
		o.logger.Debug("Failed to write backup metadata: %v", err)
	}

	o.logger.Info("Collection completed: %d files (%s), %d failed, %d dirs created",
		collStats.FilesProcessed,
		backup.FormatBytes(collStats.BytesCollected),
		collStats.FilesFailed,
		collStats.DirsCreated)

	// Additional disk space check using estimated size and safety factor
	if o.checker != nil && stats.BytesCollected > 0 {
		o.logger.Debug("Running disk-space validation for estimated data size")
		estimatedSizeGB := float64(stats.BytesCollected) / (1024.0 * 1024.0 * 1024.0)
		// Ensure we always reserve at least a small amount
		if estimatedSizeGB < 0.001 {
			estimatedSizeGB = 0.001
		}
		result := o.checker.CheckDiskSpaceForEstimate(estimatedSizeGB)
		if result.Passed {
			o.logger.Debug("Disk check passed: %s", result.Message)
		} else {
			errMsg := result.Message
			diskErr := result.Error
			if errMsg == "" && diskErr != nil {
				errMsg = diskErr.Error()
			}
			if errMsg == "" {
				errMsg = "insufficient disk space"
			}
			if diskErr == nil {
				diskErr = errors.New(errMsg)
			}
			return stats, &BackupError{
				Phase: "disk",
				Err:   fmt.Errorf("disk space validation failed: %w", diskErr),
				Code:  types.ExitDiskSpaceError,
			}
		}
	}

	if o.optimizationCfg.Enabled() {
		fmt.Println()
		o.logger.Step("Backup optimizations on collected data")
		if err := backup.ApplyOptimizations(ctx, o.logger, tempDir, o.optimizationCfg); err != nil {
			o.logger.Warning("Backup optimizations completed with warnings: %v", err)
		}
	} else {
		o.logger.Debug("Skipping optimization step (all features disabled)")
	}

	// Step 2: Create archive
	fmt.Println()
	o.logStep(3, "Creation of compressed archive")
	o.logger.Info("Creating compressed archive...")
	o.logger.Debug("Archiver configuration: type=%s level=%d mode=%s threads=%d",
		o.compressionType, normalizedLevel, o.compressionMode, o.compressionThreads)

	// Generate archive filename
	archiveBasename := fmt.Sprintf("%s-backup-%s", hostname, timestampStr)

	ageRecipients, err := o.prepareAgeRecipients(ctx)
	if err != nil {
		return stats, &BackupError{
			Phase: "config",
			Err:   err,
			Code:  types.ExitConfigError,
		}
	}

	archiverConfig := BuildArchiverConfig(
		o.compressionType,
		normalizedLevel,
		o.compressionThreads,
		o.compressionMode,
		o.dryRun,
		o.cfg != nil && o.cfg.EncryptArchive,
		ageRecipients,
	)

	if err := archiverConfig.Validate(); err != nil {
		return stats, &BackupError{
			Phase: "config",
			Err:   err,
			Code:  types.ExitConfigError,
		}
	}

	archiver := backup.NewArchiver(o.logger, archiverConfig)
	effectiveCompression := archiver.ResolveCompression()
	stats.Compression = effectiveCompression
	stats.CompressionLevel = archiver.CompressionLevel()
	stats.CompressionMode = archiver.CompressionMode()
	stats.CompressionThreads = archiver.CompressionThreads()
	archiveExt := archiver.GetArchiveExtension()
	archivePath := filepath.Join(o.backupPath, archiveBasename+archiveExt)
	if stats.RequestedCompression != stats.Compression {
		o.logger.Info("Using %s compression (requested %s)", stats.Compression, stats.RequestedCompression)
	}

	if err := archiver.CreateArchive(ctx, tempDir, archivePath); err != nil {
		phase := "archive"
		code := types.ExitArchiveError
		var compressionErr *backup.CompressionError
		if errors.As(err, &compressionErr) {
			phase = "compression"
			code = types.ExitCompressionError
		}

		return stats, &BackupError{
			Phase: phase,
			Err:   err,
			Code:  code,
		}
	}

	stats.ArchivePath = archivePath
	checksumPath := archivePath + ".sha256"

	// Get archive size
	if !o.dryRun {
		fmt.Println()
		o.logStep(4, "Verification of archive and metadata generation")
		if size, err := archiver.GetArchiveSize(archivePath); err == nil {
			stats.ArchiveSize = size
			stats.CompressedSize = size
			stats.updateCompressionMetrics()
			o.logger.Debug("Archive created: %s (%s)", archivePath, backup.FormatBytes(size))
		} else {
			o.logger.Warning("Failed to get archive size: %v", err)
		}

		// Verify archive (skipped internally when encryption is enabled)
		if err := archiver.VerifyArchive(ctx, archivePath); err != nil {
			// Return verification-specific error
			return stats, &BackupError{
				Phase: "verification",
				Err:   err,
				Code:  types.ExitVerificationError,
			}
		}

		// Generate checksum and manifest for the archive
		checksum, err := backup.GenerateChecksum(ctx, o.logger, archivePath)
		if err != nil {
			return stats, &BackupError{
				Phase: "verification",
				Err:   fmt.Errorf("checksum generation failed: %w", err),
				Code:  types.ExitVerificationError,
			}
		}
		stats.Checksum = checksum

		checksumContent := fmt.Sprintf("%s  %s\n", checksum, filepath.Base(archivePath))
		if err := fs.WriteFile(checksumPath, []byte(checksumContent), 0640); err != nil {
			o.logger.Warning("Failed to write checksum file %s: %v", checksumPath, err)
		} else {
			o.logger.Debug("Checksum file written to %s", checksumPath)
		}

		manifestPath := archivePath + ".manifest.json"
		manifestCreatedAt := stats.Timestamp
		encryptionMode := "none"
		if o.cfg != nil && o.cfg.EncryptArchive {
			encryptionMode = "age"
		}
		targets := make([]string, 0, 1)
		if stats.ProxmoxType != "" {
			targets = append(targets, string(stats.ProxmoxType))
		}
		manifest := &backup.Manifest{
			ArchivePath:      archivePath,
			ArchiveSize:      stats.ArchiveSize,
			SHA256:           checksum,
			CreatedAt:        manifestCreatedAt,
			CompressionType:  string(stats.Compression),
			CompressionLevel: stats.CompressionLevel,
			CompressionMode:  stats.CompressionMode,
			ProxmoxType:      string(stats.ProxmoxType),
			ProxmoxTargets:   targets,
			ProxmoxVersion:   stats.ProxmoxVersion,
			Hostname:         stats.Hostname,
			ScriptVersion:    stats.ScriptVersion,
			EncryptionMode:   encryptionMode,
			ClusterMode:      stats.ClusterMode,
		}

		if err := backup.CreateManifest(ctx, o.logger, manifest, manifestPath); err != nil {
			return stats, &BackupError{
				Phase: "verification",
				Err:   fmt.Errorf("manifest creation failed: %w", err),
				Code:  types.ExitVerificationError,
			}
		}
		stats.ManifestPath = manifestPath

		// Maintain Bash-compatible metadata filename for downstream tooling
		metadataAlias := archivePath + ".metadata"
		if err := copyFile(fs, manifestPath, metadataAlias); err != nil {
			o.logger.Warning("Failed to write legacy metadata file %s: %v", metadataAlias, err)
		} else {
			o.logger.Debug("Legacy metadata file written to %s", metadataAlias)
		}

		// Create bundle (if requested) before dispatching to other storage targets
		bundleEnabled := o.cfg != nil && o.cfg.BundleAssociatedFiles
		if bundleEnabled {
			fmt.Println()
			o.logStep(5, "Bundling of archive, checksum and metadata")
			o.logger.Debug("Bundling enabled: creating bundle from %s", filepath.Base(archivePath))
			bundlePath, err := o.createBundle(ctx, archivePath)
			if err != nil {
				return stats, &BackupError{
					Phase: "archive",
					Err:   fmt.Errorf("bundle creation failed: %w", err),
					Code:  types.ExitArchiveError,
				}
			}

			if err := o.removeAssociatedFiles(archivePath); err != nil {
				o.logger.Warning("Failed to remove raw files after bundling: %v", err)
			} else {
				o.logger.Debug("Removed raw tar/checksum/metadata after bundling")
			}

			if info, err := fs.Stat(bundlePath); err == nil {
				stats.ArchiveSize = info.Size()
				stats.CompressedSize = info.Size()
				stats.updateCompressionMetrics()
			}
			stats.ArchivePath = bundlePath
			stats.ManifestPath = ""
			stats.BundleCreated = true
			archivePath = bundlePath
			o.logger.Debug("Bundle ready: %s", filepath.Base(bundlePath))
		} else {
			fmt.Println()
			o.logger.Skip("Bundling disabled")
		}

		stats.EndTime = o.now()

		o.logger.Info("✓ Archive created and verified")
	} else {
		fmt.Println()
		o.logStep(4, "Verification skipped (dry run mode)")
		o.logger.Info("[DRY RUN] Would create archive: %s", archivePath)
		stats.EndTime = o.now()
	}

	stats.Duration = stats.EndTime.Sub(stats.StartTime)

	// Parse log file to populate error/warning counts before dispatch
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

	// Determine aggregated exit code (similar to legacy Bash logic)
	switch {
	case stats.ErrorCount > 0:
		stats.ExitCode = types.ExitBackupError.Int()
	case stats.WarningCount > 0:
		stats.ExitCode = types.ExitGenericError.Int()
	default:
		stats.ExitCode = types.ExitSuccess.Int()
	}
	o.logger.Debug("Aggregated exit code based on log analysis: %d", stats.ExitCode)

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

	if !o.dryRun {
		o.logger.Debug("Dispatching archive to %d storage targets", len(o.storageTargets))
		if err := o.dispatchPostBackup(ctx, stats); err != nil {
			return stats, err
		}
	}

	fmt.Println()
	o.logger.Debug("Go backup completed in %s", backup.FormatDuration(stats.Duration))

	return stats, nil
}

func normalizeCompressionLevel(comp types.CompressionType, level int) int {
	const defaultLevel = 6

	switch comp {
	case types.CompressionGzip:
		if level < 1 || level > 9 {
			return defaultLevel
		}
	case types.CompressionXZ:
		if level < 0 || level > 9 {
			return defaultLevel
		}
	case types.CompressionZstd:
		if level < 1 || level > 22 {
			return defaultLevel
		}
	case types.CompressionNone:
		return 0
	default:
		return level
	}
	return level
}

func (s *BackupStats) updateCompressionMetrics() {
	if s == nil {
		return
	}

	if s.UncompressedSize <= 0 || s.CompressedSize <= 0 {
		s.CompressionRatio = 0
		s.CompressionRatioPercent = 0
		s.CompressionSavingsPercent = 0
		return
	}

	ratio := float64(s.CompressedSize) / float64(s.UncompressedSize)
	if ratio < 0 {
		ratio = 0
	}

	s.CompressionRatio = ratio
	s.CompressionRatioPercent = ratio * 100

	savings := (1 - ratio) * 100
	if savings < 0 {
		savings = 0
	}
	s.CompressionSavingsPercent = savings
}

func (s *BackupStats) toPrometheusMetrics() *metrics.BackupMetrics {
	if s == nil {
		return nil
	}

	return &metrics.BackupMetrics{
		Hostname:       s.Hostname,
		ProxmoxType:    s.ProxmoxType.String(),
		ProxmoxVersion: s.ProxmoxVersion,
		ScriptVersion:  s.ScriptVersion,
		StartTime:      s.StartTime,
		EndTime:        s.EndTime,
		Duration:       s.Duration,
		ExitCode:       s.ExitCode,
		ErrorCount:     s.ErrorCount,
		WarningCount:   s.WarningCount,
		LocalBackups:   s.LocalBackups,
		SecBackups:     s.SecondaryBackups,
		CloudBackups:   s.CloudBackups,
		BytesCollected: s.BytesCollected,
		ArchiveSize:    s.ArchiveSize,
		FilesCollected: s.FilesCollected,
		FilesFailed:    s.FilesFailed,
	}
}

func (o *Orchestrator) createBundle(ctx context.Context, archivePath string) (string, error) {
	logger := o.logger
	fs := o.filesystem()
	dir := filepath.Dir(archivePath)
	base := filepath.Base(archivePath)

	associated := []string{
		base,
		base + ".sha256",
		base + ".metadata",
	}

	metaChecksum := base + ".metadata.sha256"
	if _, err := fs.Stat(filepath.Join(dir, metaChecksum)); err == nil {
		associated = append(associated, metaChecksum)
	}

	for _, file := range associated[:3] {
		if _, err := fs.Stat(filepath.Join(dir, file)); err != nil {
			return "", fmt.Errorf("associated file not found: %s: %w", file, err)
		}
	}

	bundlePath := archivePath + ".bundle.tar"
	logger.Debug("Creating bundle with native Go tar: %s (files: %v)", bundlePath, associated)

	// Create tar archive using native Go archive/tar
	outFile, err := fs.Create(bundlePath)
	if err != nil {
		return "", fmt.Errorf("failed to create bundle file: %w", err)
	}
	defer outFile.Close()

	tw := tar.NewWriter(outFile)
	defer tw.Close()

	// Add each associated file to the tar archive
	for _, filename := range associated {
		filePath := filepath.Join(dir, filename)

		// Get file info
		fileInfo, err := fs.Stat(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to stat %s: %w", filename, err)
		}

		// Create tar header
		header, err := tar.FileInfoHeader(fileInfo, "")
		if err != nil {
			return "", fmt.Errorf("failed to create tar header for %s: %w", filename, err)
		}
		header.Name = filename // Use basename only (no directory prefix)

		// Write header
		if err := tw.WriteHeader(header); err != nil {
			return "", fmt.Errorf("failed to write tar header for %s: %w", filename, err)
		}

		// Write file content
		file, err := fs.Open(filePath)
		if err != nil {
			return "", fmt.Errorf("failed to open %s: %w", filename, err)
		}

		if _, err := io.Copy(tw, file); err != nil {
			file.Close()
			return "", fmt.Errorf("failed to write %s to tar: %w", filename, err)
		}
		file.Close()
	}

	// Close tar writer to flush
	if err := tw.Close(); err != nil {
		return "", fmt.Errorf("failed to finalize tar archive: %w", err)
	}

	if err := outFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close bundle file: %w", err)
	}

	// Verify bundle was created
	if _, err := fs.Stat(bundlePath); err != nil {
		return "", fmt.Errorf("bundle file not created: %w", err)
	}

	logger.Debug("Bundle created successfully: %s", bundlePath)
	return bundlePath, nil
}

func (o *Orchestrator) removeAssociatedFiles(archivePath string) error {
	logger := o.logger
	fs := o.filesystem()
	files := []string{
		archivePath,
		archivePath + ".sha256",
		archivePath + ".metadata",
		archivePath + ".metadata.sha256",
		archivePath + ".manifest.json",
	}

	for _, f := range files {
		if err := fs.Remove(f); err != nil {
			if os.IsNotExist(err) { // fs.Remove should still return os.ErrNotExist
				continue
			}
			return fmt.Errorf("remove %s: %w", filepath.Base(f), err)
		}
		logger.Debug("Removed raw artifact: %s", filepath.Base(f))
	}
	return nil
}

// Legacy compatibility wrapper for callers that used the package-level createBundle function.
func createBundle(ctx context.Context, logger *logging.Logger, archivePath string) (string, error) {
	o := &Orchestrator{logger: logger, fs: osFS{}, clock: realTimeProvider{}}
	return o.createBundle(ctx, archivePath)
}

// encryptArchive was replaced by streaming encryption inside the archiver.

// SaveStatsReport writes a JSON report with backup statistics to the log directory.
func (o *Orchestrator) SaveStatsReport(stats *BackupStats) error {
	if stats == nil {
		return fmt.Errorf("stats cannot be nil")
	}
	fs := o.filesystem()

	if o.logPath == "" || stats.Timestamp.IsZero() {
		return nil
	}

	timestampStr := stats.Timestamp.Format("20060102-150405")
	reportPath := filepath.Join(o.logPath, fmt.Sprintf("backup-stats-%s.json", timestampStr))
	stats.ReportPath = reportPath

	if o.dryRun {
		o.logger.Info("[DRY RUN] Would write stats report: %s", reportPath)
		return nil
	}

	if err := fs.MkdirAll(o.logPath, 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	file, err := fs.OpenFile(reportPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("create stats report: %w", err)
	}
	defer file.Close()

	durationSeconds := stats.Duration.Seconds()
	compressionRatio := stats.CompressionRatio
	if compressionRatio == 0 && stats.BytesCollected > 0 {
		if stats.BytesCollected > 0 {
			compressionRatio = float64(stats.ArchiveSize) / float64(stats.BytesCollected)
		}
	}
	compressionRatioPercent := stats.CompressionRatioPercent
	if compressionRatioPercent == 0 && compressionRatio > 0 {
		compressionRatioPercent = compressionRatio * 100
	}
	compressionSavingsPercent := stats.CompressionSavingsPercent
	if compressionSavingsPercent == 0 && compressionRatioPercent > 0 {
		savings := 100 - compressionRatioPercent
		if savings < 0 {
			savings = 0
		}
		compressionSavingsPercent = savings
	}

	payload := struct {
		Hostname           string                `json:"hostname"`
		ProxmoxType        types.ProxmoxType     `json:"proxmox_type"`
		Timestamp          string                `json:"timestamp"`
		StartTime          time.Time             `json:"start_time"`
		EndTime            time.Time             `json:"end_time"`
		DurationSeconds    float64               `json:"duration_seconds"`
		DurationHuman      string                `json:"duration_human"`
		FilesCollected     int                   `json:"files_collected"`
		FilesFailed        int                   `json:"files_failed"`
		DirsCreated        int                   `json:"directories_created"`
		BytesCollected     int64                 `json:"bytes_collected"`
		BytesCollectedStr  string                `json:"bytes_collected_human"`
		ArchivePath        string                `json:"archive_path"`
		ArchiveSize        int64                 `json:"archive_size"`
		ArchiveSizeStr     string                `json:"archive_size_human"`
		RequestedComp      types.CompressionType `json:"requested_compression"`
		RequestedCompMode  string                `json:"requested_compression_mode"`
		Compression        types.CompressionType `json:"compression"`
		CompressionLevel   int                   `json:"compression_level"`
		CompressionMode    string                `json:"compression_mode"`
		CompressionThreads int                   `json:"compression_threads"`
		CompressionRatio   float64               `json:"compression_ratio"`
		CompressionPct     float64               `json:"compression_ratio_percent"`
		CompressionSavings float64               `json:"compression_savings_percent"`
		Checksum           string                `json:"checksum"`
		ManifestPath       string                `json:"manifest_path"`
	}{
		Hostname:           stats.Hostname,
		ProxmoxType:        stats.ProxmoxType,
		Timestamp:          stats.Timestamp.Format("20060102-150405"),
		StartTime:          stats.StartTime,
		EndTime:            stats.EndTime,
		DurationSeconds:    durationSeconds,
		DurationHuman:      backup.FormatDuration(stats.Duration),
		FilesCollected:     stats.FilesCollected,
		FilesFailed:        stats.FilesFailed,
		DirsCreated:        stats.DirsCreated,
		BytesCollected:     stats.BytesCollected,
		BytesCollectedStr:  backup.FormatBytes(stats.BytesCollected),
		ArchivePath:        stats.ArchivePath,
		ArchiveSize:        stats.ArchiveSize,
		ArchiveSizeStr:     backup.FormatBytes(stats.ArchiveSize),
		RequestedComp:      stats.RequestedCompression,
		RequestedCompMode:  stats.RequestedCompressionMode,
		Compression:        stats.Compression,
		CompressionLevel:   stats.CompressionLevel,
		CompressionMode:    stats.CompressionMode,
		CompressionThreads: stats.CompressionThreads,
		CompressionRatio:   compressionRatio,
		CompressionPct:     compressionRatioPercent,
		CompressionSavings: compressionSavingsPercent,
		Checksum:           stats.Checksum,
		ManifestPath:       stats.ManifestPath,
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return fmt.Errorf("write stats report: %w", err)
	}

	o.logger.Debug("Backup stats written to %s", reportPath)
	return nil
}

// cleanupPreviousExecutionArtifacts performs unified cleanup of old JSON stats and orphaned temp directories
// Returns the TempDirRegistry for use by the caller
func (o *Orchestrator) cleanupPreviousExecutionArtifacts() *TempDirRegistry {
	// Check if there's anything to clean
	hasStatsFiles := false
	fs := o.filesystem()

	// Check for JSON stats files
	if o.logPath != "" {
		pattern := filepath.Join(o.logPath, "backup-stats-*.json")
		matches, err := filepath.Glob(pattern)
		if err == nil && len(matches) > 0 {
			hasStatsFiles = true
		}
	}

	// Get temp directory registry
	registry := o.ensureTempRegistry()

	removedFiles := 0
	failedFiles := 0
	removedDirs := 0
	cleanupStarted := false

	// Phase 1: Cleanup JSON stats files
	if hasStatsFiles {
		pattern := filepath.Join(o.logPath, "backup-stats-*.json")
		matches, _ := filepath.Glob(pattern)

		if len(matches) > 0 {
			if !cleanupStarted {
				o.logger.Debug("Starting cleanup of previous execution files...")
				cleanupStarted = true
			}
			o.logger.Debug("Found %d stats file(s) from previous execution", len(matches))

			for _, file := range matches {
				filename := filepath.Base(file)
				if err := fs.Remove(file); err != nil {
					o.logger.Debug("Failed to remove file %s: %v", filename, err)
					failedFiles++
				} else {
					o.logger.Debug("Cleanup file %s executed", filename)
					removedFiles++
				}
			}
		}
	}

	// Phase 2: Cleanup orphaned temp directories
	if registry != nil {
		if !cleanupStarted {
			o.logger.Debug("Starting cleanup of previous execution files...")
			cleanupStarted = true
		}
		o.logger.Debug("Checking for orphaned temp directories older than %s", tempDirCleanupAge)

		// CleanupOrphaned now returns the count of directories removed
		count, err := registry.CleanupOrphaned(tempDirCleanupAge)
		if err != nil {
			o.logger.Debug("Temp dir cleanup skipped: %v", err)
		} else {
			removedDirs = count
		}
	}

	// Final summary - only show if cleanup was actually performed
	if cleanupStarted {
		if removedFiles > 0 || removedDirs > 0 {
			totalRemoved := removedFiles + removedDirs
			if failedFiles > 0 {
				o.logger.Info("Cleanup of previous execution files completed with errors (%d item(s) removed: %d file(s), %d dir(s); %d failed)", totalRemoved, removedFiles, removedDirs, failedFiles)
			} else {
				o.logger.Info("Cleanup of previous execution files completed successfully (%d item(s) removed: %d file(s), %d dir(s))", totalRemoved, removedFiles, removedDirs)
			}
		}
	}

	return registry
}

func (o *Orchestrator) writeBackupMetadata(tempDir string, stats *BackupStats) error {
	fs := o.filesystem()
	infoDir := filepath.Join(tempDir, "var/lib/proxsave-info")
	if err := fs.MkdirAll(infoDir, 0755); err != nil {
		return err
	}

	version := strings.TrimSpace(stats.Version)
	if version == "" {
		version = "0.0.0"
	}

	builder := strings.Builder{}
	builder.WriteString("# ProxSave Metadata\n")
	builder.WriteString("# This file enables selective restore functionality in newer restore scripts\n")
	builder.WriteString(fmt.Sprintf("VERSION=%s\n", version))
	builder.WriteString(fmt.Sprintf("BACKUP_TYPE=%s\n", stats.ProxmoxType.String()))
	builder.WriteString(fmt.Sprintf("TIMESTAMP=%s\n", stats.Timestamp))
	builder.WriteString(fmt.Sprintf("HOSTNAME=%s\n", stats.Hostname))
	if stats.ClusterMode != "" {
		builder.WriteString(fmt.Sprintf("PVE_CLUSTER_MODE=%s\n", stats.ClusterMode))
	}
	builder.WriteString("SUPPORTS_SELECTIVE_RESTORE=true\n")
	builder.WriteString("BACKUP_FEATURES=selective_restore,category_mapping,version_detection,auto_directory_creation\n")

	target := filepath.Join(infoDir, "backup_metadata.txt")
	if err := fs.WriteFile(target, []byte(builder.String()), 0640); err != nil {
		return err
	}
	return nil
}

func applyCollectorOverrides(cc *backup.CollectorConfig, cfg *config.Config) {
	cc.BackupVMConfigs = cfg.BackupVMConfigs
	cc.BackupClusterConfig = cfg.BackupClusterConfig
	cc.BackupPVEFirewall = cfg.BackupPVEFirewall
	cc.BackupVZDumpConfig = cfg.BackupVZDumpConfig
	cc.BackupPVEACL = cfg.BackupPVEACL
	cc.BackupPVEJobs = cfg.BackupPVEJobs
	cc.BackupPVESchedules = cfg.BackupPVESchedules
	cc.BackupPVEReplication = cfg.BackupPVEReplication
	cc.BackupPVEBackupFiles = cfg.BackupPVEBackupFiles
	cc.BackupSmallPVEBackups = cfg.BackupSmallPVEBackups
	cc.MaxPVEBackupSizeBytes = cfg.MaxPVEBackupSizeBytes
	cc.PVEBackupIncludePattern = cfg.PVEBackupIncludePattern
	cc.BackupCephConfig = cfg.BackupCephConfig
	cc.CephConfigPath = cfg.CephConfigPath

	cc.BackupDatastoreConfigs = cfg.BackupDatastoreConfigs
	cc.BackupUserConfigs = cfg.BackupUserConfigs
	cc.BackupRemoteConfigs = cfg.BackupRemoteConfigs
	cc.BackupSyncJobs = cfg.BackupSyncJobs
	cc.BackupVerificationJobs = cfg.BackupVerificationJobs
	cc.BackupTapeConfigs = cfg.BackupTapeConfigs
	cc.BackupPruneSchedules = cfg.BackupPruneSchedules
	cc.BackupPxarFiles = cfg.BackupPxarFiles

	cc.BackupNetworkConfigs = cfg.BackupNetworkConfigs
	cc.BackupAptSources = cfg.BackupAptSources
	cc.BackupCronJobs = cfg.BackupCronJobs
	cc.BackupSystemdServices = cfg.BackupSystemdServices
	cc.BackupSSLCerts = cfg.BackupSSLCerts
	cc.BackupSysctlConfig = cfg.BackupSysctlConfig
	cc.BackupKernelModules = cfg.BackupKernelModules
	cc.BackupFirewallRules = cfg.BackupFirewallRules
	cc.BackupInstalledPackages = cfg.BackupInstalledPackages
	cc.BackupScriptDir = cfg.BackupScriptDir
	cc.BackupCriticalFiles = cfg.BackupCriticalFiles
	cc.BackupSSHKeys = cfg.BackupSSHKeys
	cc.BackupZFSConfig = cfg.BackupZFSConfig
	cc.BackupRootHome = cfg.BackupRootHome
	cc.BackupScriptRepository = cfg.BackupScriptRepository
	cc.BackupUserHomes = cfg.BackupUserHomes
	cc.BackupConfigFile = cfg.BackupConfigFile
	cc.ScriptRepositoryPath = cfg.BaseDir
	if cfg.PxarDatastoreConcurrency > 0 {
		cc.PxarDatastoreConcurrency = cfg.PxarDatastoreConcurrency
	}
	if cfg.PxarIntraConcurrency > 0 {
		cc.PxarIntraConcurrency = cfg.PxarIntraConcurrency
	}
	if cfg.PxarScanFanoutLevel > 0 {
		cc.PxarScanFanoutLevel = cfg.PxarScanFanoutLevel
	}
	if cfg.PxarScanMaxRoots > 0 {
		cc.PxarScanMaxRoots = cfg.PxarScanMaxRoots
	}
	cc.PxarStopOnCap = cfg.PxarStopOnCap
	if cfg.PxarEnumWorkers > 0 {
		cc.PxarEnumWorkers = cfg.PxarEnumWorkers
	}
	if cfg.PxarEnumBudgetMs >= 0 {
		cc.PxarEnumBudgetMs = cfg.PxarEnumBudgetMs
	}
	cc.PxarFileIncludePatterns = append([]string(nil), cfg.PxarFileIncludePatterns...)
	cc.PxarFileExcludePatterns = append([]string(nil), cfg.PxarFileExcludePatterns...)

	cc.CustomBackupPaths = append([]string(nil), cfg.CustomBackupPaths...)
	cc.BackupBlacklist = append([]string(nil), cfg.BackupBlacklist...)

	cc.ConfigFilePath = cfg.ConfigPath

	cc.PVEConfigPath = cfg.PVEConfigPath
	cc.PVEClusterPath = cfg.PVEClusterPath
	cc.CorosyncConfigPath = cfg.CorosyncConfigPath
	cc.VzdumpConfigPath = cfg.VzdumpConfigPath
	cc.PBSConfigPath = cfg.PBSConfigPath
	cc.PBSDatastorePaths = append([]string(nil), cfg.PBSDatastorePaths...)

	// Pass PBS authentication (auto-detected, zero user input)
	cc.PBSRepository = cfg.PBSRepository
	cc.PBSPassword = cfg.PBSPassword
	cc.PBSFingerprint = cfg.PBSFingerprint
}
func copyFile(fs FS, src, dest string) error {
	if fs == nil {
		fs = osFS{}
	}
	in, err := fs.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := fs.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
