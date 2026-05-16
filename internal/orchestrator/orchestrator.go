package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
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
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/metrics"
	"github.com/tis24dev/proxsave/internal/notify"
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
	ProxmoxTargets            []string
	ProxmoxVersion            string
	PVEVersion                string
	PBSVersion                string
	BundleCreated             bool
	Timestamp                 time.Time
	Version                   string
	StartTime                 time.Time
	EndTime                   time.Time
	FilesCollected            int
	FilesFailed               int
	FilesNotFound             int
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
	LocalUsedSpace      uint64
	LocalTotalSpace     uint64
	SecondaryBackups    int
	SecondaryFreeSpace  uint64
	SecondaryUsedSpace  uint64
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
	ErrorCount    int
	WarningCount  int
	LogFilePath   string
	LogCategories []notify.LogCategory

	// Exit code
	ExitCode       int
	ScriptVersion  string
	TelegramStatus string
	EmailStatus    string

	// Version update information
	NewVersionAvailable bool
	CurrentVersion      string
	LatestVersion       string
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
	envInfo              *environment.EnvironmentInfo
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

	// Version update information (from CLI)
	versionUpdateAvailable bool
	updateCurrentVersion   string
	updateLatestVersion    string

	// Unprivileged container context (computed once by CLI and injected into collectors).
	unprivilegedContainerDetector func() (bool, string)
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
	o.logger.Step("[%d] %s", step, message)
}

// SetUpdateInfo records version update information discovered by the CLI layer.
// This allows the orchestrator to propagate structured update data into BackupStats
// and, transitively, into notifications/metrics.
func (o *Orchestrator) SetUpdateInfo(newVersion bool, current, latest string) {
	if o == nil {
		return
	}
	o.versionUpdateAvailable = newVersion
	o.updateCurrentVersion = strings.TrimSpace(current)
	o.updateLatestVersion = strings.TrimSpace(latest)
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

// SetEnvironmentInfo attaches the detected environment model for downstream consumers.
func (o *Orchestrator) SetEnvironmentInfo(info *environment.EnvironmentInfo) {
	if o == nil || info == nil {
		return
	}
	copied := *info
	o.envInfo = &copied
	if strings.TrimSpace(copied.Version) != "" {
		o.proxmoxVersion = strings.TrimSpace(copied.Version)
	}
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

	// Helper to log result immediately after each check
	logResult := func(result checks.CheckResult) {
		if result.Passed {
			if result.Name == "Disk Space (Estimated)" {
				o.logger.Debug("✓ %s: %s", result.Name, result.Message)
			} else {
				o.logger.Info("✓ %s: %s", result.Name, result.Message)
			}
		} else {
			if result.Code != "" {
				o.logger.Error("✗ %s (%s): %s", result.Name, result.Code, result.Message)
			} else {
				o.logger.Error("✗ %s: %s", result.Name, result.Message)
			}
		}
	}

	// 1. Check directories FIRST - they must exist for all other checks
	dirResult := o.checker.CheckDirectories()
	logResult(dirResult)
	if !dirResult.Passed {
		return fmt.Errorf("pre-backup checks failed: %s", dirResult.Message)
	}

	// 1.5. Check temp directory - verify /tmp/proxsave is usable
	tempDirResult := o.checker.CheckTempDirectory()
	logResult(tempDirResult)
	if !tempDirResult.Passed {
		return fmt.Errorf("pre-backup checks failed: %s", tempDirResult.Message)
	}

	// 2. Check disk space - now that we know directories exist
	diskResult := o.checker.CheckDiskSpace()
	logResult(diskResult)
	if !diskResult.Passed {
		return fmt.Errorf("pre-backup checks failed: %s", diskResult.Message)
	}

	// 3. Check permissions - verify we can write to directories
	if !o.checker.ShouldSkipPermissionCheck() {
		permResult := o.checker.CheckPermissions()
		logResult(permResult)
		if !permResult.Passed {
			return fmt.Errorf("pre-backup checks failed: %s", permResult.Message)
		}
	}

	// 4. Check lock file LAST - only after all other prerequisites are met
	lockResult := o.checker.CheckLockFile()
	logResult(lockResult)
	if !lockResult.Passed {
		return fmt.Errorf("pre-backup checks failed: %s", lockResult.Message)
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

// SetUnprivilegedContainerContext injects the precomputed unprivileged-container
// detection result into the orchestrator so that collectors can reuse it without
// re-reading /proc and related files.
//
// The "details" string is intended for DEBUG logs.
func (o *Orchestrator) SetUnprivilegedContainerContext(detected bool, details string) {
	if o == nil {
		return
	}
	d := detected
	s := strings.TrimSpace(details)
	o.unprivilegedContainerDetector = func() (bool, string) {
		return d, s
	}
}

func (o *Orchestrator) collectorDeps() backup.CollectorDeps {
	if o == nil || o.unprivilegedContainerDetector == nil {
		return backup.CollectorDeps{}
	}
	return backup.CollectorDeps{
		DetectUnprivilegedContainer: o.unprivilegedContainerDetector,
	}
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
func (o *Orchestrator) RunGoBackup(ctx context.Context, envInfo *environment.EnvironmentInfo, hostname string) (stats *BackupStats, err error) {
	run := o.newBackupRunContext(ctx, envInfo, hostname)
	done := logging.DebugStart(o.logger, "backup run", "type=%s hostname=%s", run.proxmoxType, hostname)
	defer func() { done(err) }()
	o.logger.Info("Starting Go-based backup orchestration for %s", run.proxmoxType)

	workspace := &backupWorkspace{
		registry: o.cleanupPreviousExecutionArtifacts(),
		fs:       o.filesystem(),
	}
	stats = o.initBackupRun(run)
	defer func() {
		o.exportBackupMetrics(run, err)
	}()
	defer func() {
		o.finalizeFailedBackupStats(run, err)
	}()

	if err := o.prepareBackupWorkspace(run, workspace); err != nil {
		return stats, err
	}
	defer func() {
		o.cleanupBackupWorkspace(workspace)
	}()
	if err := o.markBackupWorkspace(workspace); err != nil {
		return stats, fmt.Errorf("failed to create temp marker file: %w", err)
	}
	o.registerBackupWorkspace(workspace)

	if err := o.collectBackupData(run, workspace); err != nil {
		return stats, err
	}
	artifacts, err := o.createBackupArchive(run, workspace)
	if err != nil {
		return stats, err
	}
	if err := o.verifyAndWriteBackupArtifacts(run, workspace, artifacts); err != nil {
		return stats, err
	}
	if err := o.bundleBackupArtifacts(run, workspace, artifacts); err != nil {
		return stats, err
	}
	o.finalizeBackupStats(run)
	if err := o.dispatchBackupArtifacts(run); err != nil {
		return stats, err
	}

	fmt.Println()
	o.logger.Debug("Go backup completed in %s", backup.FormatDuration(run.stats.Duration))

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

func (o *Orchestrator) createBundle(ctx context.Context, archivePath string) (bundlePath string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	logger := o.logger
	fs := o.filesystem()
	dir := filepath.Dir(archivePath)
	base := filepath.Base(archivePath)
	done := logging.DebugStart(logger, "bundle create", "archive=%s", archivePath)
	defer func() { done(err) }()

	associated := []string{
		base + ".metadata",
		base + ".sha256",
		base,
	}

	metaChecksum := base + ".metadata.sha256"
	if _, err := fs.Stat(filepath.Join(dir, metaChecksum)); err == nil {
		associated = append([]string{associated[0], metaChecksum}, associated[1:]...)
	}

	for _, file := range associated[:3] {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := fs.Stat(filepath.Join(dir, file)); err != nil {
			return "", fmt.Errorf("associated file not found: %s: %w", file, err)
		}
	}

	bundlePath = archivePath + ".bundle.tar"
	logger.Debug("Creating bundle with native Go tar: %s (files: %v)", bundlePath, associated)

	// Write to a temporary file in the target directory and rename on success.
	outFile, err := fs.CreateTemp(dir, fmt.Sprintf("%s.tmp-*", filepath.Base(bundlePath)))
	if err != nil {
		return "", fmt.Errorf("failed to create temp bundle file: %w", err)
	}
	tempBundle := outFile.Name()
	var tw *tar.Writer
	removeTemp := true
	defer func() {
		if tw != nil {
			_ = tw.Close()
			tw = nil
		}
		if outFile != nil {
			_ = outFile.Close()
			outFile = nil
		}
		if removeTemp {
			_ = fs.Remove(tempBundle)
		}
	}()

	tw = tar.NewWriter(outFile)

	// Add each associated file to the tar archive
	for _, filename := range associated {
		if err := ctx.Err(); err != nil {
			return "", err
		}
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

		if _, err := io.Copy(tw, &contextReader{ctx: ctx, r: file}); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("failed to write %s to tar: %w", filename, err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("failed to close %s: %w", filename, err)
		}
	}

	// Close tar writer to flush
	if err := tw.Close(); err != nil {
		tw = nil
		return "", fmt.Errorf("failed to finalize tar archive: %w", err)
	}
	tw = nil

	if err := outFile.Sync(); err != nil {
		return "", fmt.Errorf("failed to sync bundle file: %w", err)
	}

	if err := outFile.Close(); err != nil {
		outFile = nil
		return "", fmt.Errorf("failed to close bundle file: %w", err)
	}
	outFile = nil

	if err := fs.Rename(tempBundle, bundlePath); err != nil {
		return "", fmt.Errorf("failed to rename temp bundle file: %w", err)
	}
	removeTemp = false
	if err := syncDirectoryWithDeps(fs, dir); err != nil {
		_ = fs.Remove(bundlePath)
		return "", fmt.Errorf("failed to sync bundle directory: %w", err)
	}

	// Verify bundle was created
	if _, err := fs.Stat(bundlePath); err != nil {
		_ = fs.Remove(bundlePath)
		return "", fmt.Errorf("bundle file not created: %w", err)
	}

	logger.Debug("Bundle created successfully: %s", bundlePath)
	return bundlePath, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr *contextReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, err
	}
	return cr.r.Read(p)
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

// encryptArchive was replaced by streaming encryption inside the archiver.

// SaveStatsReport writes a JSON report with backup statistics to the log directory.
func (o *Orchestrator) SaveStatsReport(stats *BackupStats) (err error) {
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
	defer closeIntoErr(&err, file, "close stats report")

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

// cleanupPreviousExecutionArtifacts performs unified cleanup of old JSON stats, pprof files,
// and orphaned temp directories. Returns the TempDirRegistry for use by the caller.
func (o *Orchestrator) cleanupPreviousExecutionArtifacts() *TempDirRegistry {
	fs := o.filesystem()

	// Discover JSON stats files and pprof profiles from previous runs
	var statsFiles []string
	var cpuProfiles []string
	var heapProfiles []string
	if o.logPath != "" {
		if matches, err := filepath.Glob(filepath.Join(o.logPath, "backup-stats-*.json")); err == nil {
			statsFiles = matches
		}

		if matches, err := filepath.Glob(filepath.Join(o.logPath, "cpu-*.pprof")); err == nil {
			cpuProfiles = matches
		}
	}

	// Heap profiles are written under /tmp/proxsave
	if matches, err := filepath.Glob(filepath.Join("/tmp", "proxsave", "heap-*.pprof")); err == nil {
		heapProfiles = matches
	}

	// Get temp directory registry
	registry := o.ensureTempRegistry()

	removedFiles := 0
	failedFiles := 0
	removedDirs := 0
	cleanupStarted := false

	// Phase 1: Cleanup JSON stats files
	if len(statsFiles) > 0 {
		if !cleanupStarted {
			o.logger.Debug("Starting cleanup of previous execution files...")
			cleanupStarted = true
		}
		o.logger.Debug("Found %d stats file(s) from previous execution", len(statsFiles))

		for _, file := range statsFiles {
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

	// Phase 2: Cleanup CPU pprof files under log path
	if len(cpuProfiles) > 0 {
		if !cleanupStarted {
			o.logger.Debug("Starting cleanup of previous execution files...")
			cleanupStarted = true
		}
		o.logger.Debug("Found %d CPU profile file(s) from previous execution", len(cpuProfiles))

		for _, file := range cpuProfiles {
			filename := filepath.Base(file)
			if err := fs.Remove(file); err != nil {
				o.logger.Debug("Failed to remove CPU profile %s: %v", filename, err)
				failedFiles++
			} else {
				o.logger.Debug("Cleanup CPU profile %s executed", filename)
				removedFiles++
			}
		}
	}

	// Phase 3: Cleanup heap pprof files under /tmp/proxsave
	if len(heapProfiles) > 0 {
		if !cleanupStarted {
			o.logger.Debug("Starting cleanup of previous execution files...")
			cleanupStarted = true
		}
		o.logger.Debug("Found %d heap profile file(s) from previous execution", len(heapProfiles))

		for _, file := range heapProfiles {
			filename := filepath.Base(file)
			if err := fs.Remove(file); err != nil {
				o.logger.Debug("Failed to remove heap profile %s: %v", filename, err)
				failedFiles++
			} else {
				o.logger.Debug("Cleanup heap profile %s executed", filename)
				removedFiles++
			}
		}
	}

	// Phase 4: Cleanup orphaned temp directories
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
	if o.dryRun {
		return nil
	}
	infoDir := filepath.Join(tempDir, "var/lib/proxsave-info")

	version := strings.TrimSpace(stats.Version)
	if version == "" {
		version = "0.0.0"
	}

	builder := strings.Builder{}
	builder.WriteString("# ProxSave Metadata\n")
	builder.WriteString("# This file enables selective restore functionality in newer restore scripts\n")
	fmt.Fprintf(&builder, "VERSION=%s\n", version)
	fmt.Fprintf(&builder, "BACKUP_TYPE=%s\n", stats.ProxmoxType.String())
	if len(stats.ProxmoxTargets) > 0 {
		fmt.Fprintf(&builder, "BACKUP_TARGETS=%s\n", strings.Join(stats.ProxmoxTargets, ","))
	}
	fmt.Fprintf(&builder, "TIMESTAMP=%s\n", stats.Timestamp)
	fmt.Fprintf(&builder, "HOSTNAME=%s\n", stats.Hostname)
	if strings.TrimSpace(stats.PVEVersion) != "" {
		fmt.Fprintf(&builder, "PVE_VERSION=%s\n", strings.TrimSpace(stats.PVEVersion))
	}
	if strings.TrimSpace(stats.PBSVersion) != "" {
		fmt.Fprintf(&builder, "PBS_VERSION=%s\n", strings.TrimSpace(stats.PBSVersion))
	}
	if stats.ClusterMode != "" {
		fmt.Fprintf(&builder, "PVE_CLUSTER_MODE=%s\n", stats.ClusterMode)
	}
	builder.WriteString("SUPPORTS_SELECTIVE_RESTORE=true\n")
	builder.WriteString("BACKUP_FEATURES=selective_restore,category_mapping,version_detection,auto_directory_creation\n")

	target := filepath.Join(infoDir, "backup_metadata.txt")
	patterns := append([]string(nil), o.excludePatterns...)
	if o.cfg != nil && len(o.cfg.BackupBlacklist) > 0 {
		patterns = append(patterns, o.cfg.BackupBlacklist...)
	}
	if excluded, pattern := backup.FindExcludeMatch(patterns, target, tempDir, ""); excluded {
		o.logger.Debug("Skipping backup metadata %s (matches pattern %s)", target, pattern)
		return nil
	}

	if err := fs.MkdirAll(infoDir, 0755); err != nil {
		return err
	}
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
	cc.PveshTimeoutSeconds = cfg.PveshTimeoutSeconds
	cc.FsIoTimeoutSeconds = cfg.FsIoTimeoutSeconds

	cc.BackupDatastoreConfigs = cfg.BackupDatastoreConfigs
	cc.BackupPBSS3Endpoints = cfg.BackupPBSS3Endpoints
	cc.BackupPBSNodeConfig = cfg.BackupPBSNodeConfig
	cc.BackupPBSAcmeAccounts = cfg.BackupPBSAcmeAccounts
	cc.BackupPBSAcmePlugins = cfg.BackupPBSAcmePlugins
	cc.BackupPBSMetricServers = cfg.BackupPBSMetricServers
	cc.BackupPBSTrafficControl = cfg.BackupPBSTrafficControl
	cc.BackupPBSNotifications = cfg.BackupPBSNotifications
	cc.BackupPBSNotificationsPriv = cfg.BackupPBSNotificationsPriv
	cc.BackupUserConfigs = cfg.BackupUserConfigs
	cc.BackupRemoteConfigs = cfg.BackupRemoteConfigs
	cc.BackupSyncJobs = cfg.BackupSyncJobs
	cc.BackupVerificationJobs = cfg.BackupVerificationJobs
	cc.BackupTapeConfigs = cfg.BackupTapeConfigs
	cc.BackupPBSNetworkConfig = cfg.BackupPBSNetworkConfig
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
func copyFile(fs FS, src, dest string) (err error) {
	if fs == nil {
		fs = osFS{}
	}
	in, err := fs.Open(src)
	if err != nil {
		return err
	}
	defer closeIntoErr(&err, in, "close source file")

	out, err := fs.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return err
	}
	defer closeIntoErr(&err, out, "close destination file")

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
