package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// SecondaryStorage implements the Storage interface for secondary (remote) filesystem storage
// This is typically a network mount (NFS/CIFS) or another local path
// All errors from secondary storage are NON-FATAL - they log warnings but don't abort the backup
type SecondaryStorage struct {
	config     *config.Config
	logger     *logging.Logger
	basePath   string
	fsDetector *FilesystemDetector
	fsInfo     *FilesystemInfo
	lastRet    RetentionSummary
}

// NewSecondaryStorage creates a new secondary storage instance
func NewSecondaryStorage(cfg *config.Config, logger *logging.Logger) (*SecondaryStorage, error) {
	return &SecondaryStorage{
		config:     cfg,
		logger:     logger,
		basePath:   cfg.SecondaryPath,
		fsDetector: NewFilesystemDetector(logger, WithIOTimeout(fsIoTimeout(cfg))),
	}, nil
}

// Name returns the storage backend name
func (s *SecondaryStorage) Name() string {
	return "Secondary Storage"
}

// Location returns the backup location type
func (s *SecondaryStorage) Location() BackupLocation {
	return LocationSecondary
}

// IsEnabled returns true if secondary storage is configured
func (s *SecondaryStorage) IsEnabled() bool {
	return s.config.SecondaryEnabled && s.basePath != ""
}

// IsCritical returns false because secondary storage is non-critical
// Failures in secondary storage should NOT abort the backup
func (s *SecondaryStorage) IsCritical() bool {
	return false
}

// DetectFilesystem detects the filesystem type for the secondary path
func (s *SecondaryStorage) DetectFilesystem(ctx context.Context) (info *FilesystemInfo, err error) {
	done := logging.DebugStart(s.logger, "secondary detect filesystem", "path=%s", s.basePath)
	defer func() { done(err) }()
	// Ensure directory exists (bounded: secondary is typically an NFS/CIFS mount).
	if err := safefs.MkdirAll(ctx, s.basePath, 0700, fsIoTimeout(s.config)); err != nil {
		// Non-critical error - log warning and return
		s.logger.Warning("WARNING: Cannot create secondary backup directory %s: %v", s.basePath, err)
		s.logger.Warning("WARNING: Secondary backup will be skipped")
		return nil, &StorageError{
			Location:    LocationSecondary,
			Operation:   "detect_filesystem",
			Path:        s.basePath,
			Err:         fmt.Errorf("failed to create directory: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	fsInfo, err := s.fsDetector.DetectFilesystem(ctx, s.basePath)
	if err != nil {
		// Non-critical error - log warning
		s.logger.Warning("WARNING: Failed to detect filesystem type for secondary storage %s: %v", s.basePath, err)
		s.logger.Warning("WARNING: Will attempt to copy files anyway, but ownership may not be preserved")
		// Create minimal fsInfo with unknown type
		fsInfo = &FilesystemInfo{
			Path:              s.basePath,
			Type:              FilesystemUnknown,
			SupportsOwnership: false,
		}
	}

	s.fsInfo = fsInfo
	return fsInfo, nil
}

// Store copies a backup file to secondary storage using an atomic Go-based copy
func (s *SecondaryStorage) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) (err error) {
	done := logging.DebugStart(s.logger, "secondary store", "file=%s", filepath.Base(backupFile))
	defer func() { done(err) }()
	s.logger.Debug("Secondary storage: preparing to store %s", filepath.Base(backupFile))
	// Check context
	if err := ctx.Err(); err != nil {
		s.logger.Debug("Secondary storage: store aborted due to context cancellation")
		return err
	}

	bundleEnabled := s.config != nil && s.config.BundleAssociatedFiles
	sourceFile := backupFile
	if bundleEnabled {
		sourceFile = bundlePathFor(sourceFile)
	}

	// Verify source file exists (bounded against a dead/stale mount).
	if _, err := safefs.Stat(ctx, sourceFile, fsIoTimeout(s.config)); err != nil {
		s.logger.Debug("Secondary storage: source file %s not found", sourceFile)
		s.logger.Warning("WARNING: Secondary storage - backup file not found: %s: %v", sourceFile, err)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        sourceFile,
			Err:         fmt.Errorf("source file not found: %w", err),
			IsCritical:  false,
			Recoverable: false,
		}
	}

	// Ensure destination directory exists (bounded against a dead/stale mount).
	if err := safefs.MkdirAll(ctx, s.basePath, 0700, fsIoTimeout(s.config)); err != nil {
		s.logger.Debug("Secondary storage: failed to create destination folder %s", s.basePath)
		s.logger.Warning("WARNING: Secondary storage - failed to create destination directory %s: %v", s.basePath, err)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        s.basePath,
			Err:         fmt.Errorf("failed to create destination directory: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Determine destination filename
	destFile := filepath.Join(s.basePath, filepath.Base(sourceFile))

	s.logger.Debug("Secondary Storage: Start copy...")
	s.logger.Debug("Copying backup to secondary storage: %s -> %s", filepath.Base(sourceFile), s.basePath)

	if err := s.copyFile(ctx, sourceFile, destFile); err != nil {
		s.logger.Warning("WARNING: Secondary Storage: File copy failed for %s: %v", filepath.Base(sourceFile), err)
		s.logger.Warning("WARNING: Secondary Storage: Backup not saved to %s", s.basePath)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        sourceFile,
			Err:         fmt.Errorf("copy failed: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Copy associated files if not bundled
	if !bundleEnabled {
		associatedFiles := []string{
			backupFile + ".sha256",
			backupFile + ".metadata",
			backupFile + ".metadata.sha256",
		}
		failedAssoc := make([]string, 0)

		for _, srcFile := range associatedFiles {
			if _, err := safefs.Stat(ctx, srcFile, fsIoTimeout(s.config)); err != nil {
				continue // Skip if doesn't exist
			}

			destAssocFile := filepath.Join(s.basePath, filepath.Base(srcFile))
			if err := s.copyFile(ctx, srcFile, destAssocFile); err != nil {
				s.logger.Warning("WARNING: Secondary Storage: Failed to copy associated file %s: %v",
					filepath.Base(srcFile), err)
				failedAssoc = append(failedAssoc, filepath.Base(srcFile))
				// Continue with other files
			}
		}

		if len(failedAssoc) > 0 {
			s.logger.Warning("WARNING: Secondary Storage: %d associated file(s) failed to copy: %v",
				len(failedAssoc), failedAssoc)
		}
	}

	// Set permissions on destination (best effort)
	if s.fsInfo != nil && s.fsInfo.SupportsOwnership {
		if err := s.fsDetector.SetPermissions(ctx, destFile, 0, 0, 0600, s.fsInfo); err != nil {
			s.logger.Warning("WARNING: Secondary storage - failed to set permissions on %s: %v",
				filepath.Base(destFile), err)
			// Not critical - continue
		}
	}

	s.logger.Debug("✓ Secondary Storage: File copied")

	if count := s.countBackups(ctx); count >= 0 {
		s.logger.Debug("Secondary storage: current backups detected after copy: %d", count)
	} else {
		s.logger.Debug("Secondary storage: unable to count backups after copy (see previous log for details)")
	}

	return nil
}

// countBackups lists current backups on secondary storage for logging/diagnostic purposes.
func (s *SecondaryStorage) countBackups(ctx context.Context) int {
	backups, err := s.List(ctx)
	if err != nil {
		s.logger.Debug("Secondary storage: failed to list backups for recount: %v", err)
		return -1
	}
	return len(backups)
}

// secondaryCloseSourceFile closes the read source after a copy. It is a seam so
// a test can prove that a close failure on the success path (after the
// destination has been durably renamed) is treated as best-effort read-side
// cleanup and never turns a committed copy into a reported failure.
var secondaryCloseSourceFile = func(f *os.File) error { return f.Close() }

// copyFile copies a file using Go's io.Copy
func (s *SecondaryStorage) copyFile(ctx context.Context, src, dest string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Bound the leaf metadata/open/finalize syscalls AND the byte-transfer loop
	// (via safefs.CopyBounded below) so a dead/stale secondary mount cannot wedge
	// any of them in an uninterruptible syscall.
	to := fsIoTimeout(s.config)

	sourceInfo, err := safefs.Stat(ctx, src, to)
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", src, err)
	}

	destDir := filepath.Dir(dest)
	if err := safefs.MkdirAll(ctx, destDir, 0700, to); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	tempFile, err := safefs.CreateTemp(ctx, destDir, fmt.Sprintf(".tmp-%s-", filepath.Base(dest)), to)
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", destDir, err)
	}
	tempName := tempFile.Name()
	defer func() {
		if tempFile != nil {
			if _, closeErr := safefs.Run(ctx, "secondary-close-temp", tempName, to, func() (struct{}, error) {
				return struct{}{}, tempFile.Close()
			}); closeErr != nil && err == nil {
				err = fmt.Errorf("failed to close temporary file %s: %w", tempName, closeErr)
			}
		}
		if tempName != "" {
			if removeErr := safefs.Remove(ctx, tempName, to); removeErr != nil && err == nil && !os.IsNotExist(removeErr) {
				err = fmt.Errorf("failed to remove temporary file %s: %w", tempName, removeErr)
			}
		}
	}()

	start := time.Now()
	sourceFile, err := safefs.Open(ctx, src, to)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer func() {
		if sourceFile == nil { // abandoned copy worker may still hold this fd
			return
		}
		// Best-effort read-side cleanup: on the success path the destination has
		// already been durably renamed into place before this runs, so a failed or
		// timed-out close of the read-only source must NOT turn a committed copy
		// into a reported failure (that would drive misleading retries / duplicate
		// backups). On the error paths err is already set, so this never masks a
		// pre-commit failure either.
		if _, closeErr := safefs.Run(ctx, "secondary-close-src", src, to, func() (struct{}, error) {
			return struct{}{}, secondaryCloseSourceFile(sourceFile)
		}); closeErr != nil {
			s.logger.Debug("Secondary storage: failed to close source file %s: %v", src, closeErr)
		}
	}()

	// Stream the bytes under a per-chunk stall budget so a mount dying mid-copy
	// cannot wedge Read/Write in an uninterruptible (D-state) syscall. The copy
	// bypasses the shared safefs limiter (it is sequential, so it self-throttles
	// to one in-flight worker and must not erode the slot budget the critical
	// paths rely on). On a stalled chunk the worker is abandoned and may still
	// hold these handles, so on abandonment we drop them and skip the closes.
	written, copyErr := safefs.CopyBounded(ctx, tempFile, sourceFile, 1024*1024, to, "secondary-copy", src)
	if copyErr != nil {
		if safefs.IsAbandoned(copyErr) {
			tempFile = nil
			sourceFile = nil
		}
		return fmt.Errorf("stream copy %s -> %s: %w", src, dest, copyErr)
	}

	if _, err := safefs.Run(ctx, "secondary-sync-temp", tempName, to, func() (struct{}, error) {
		return struct{}{}, tempFile.Sync()
	}); err != nil {
		return fmt.Errorf("failed to sync temporary file %s: %w", tempName, err)
	}
	_, closeErr := safefs.Run(ctx, "secondary-close-temp", tempName, to, func() (struct{}, error) {
		return struct{}{}, tempFile.Close()
	})
	tempFile = nil
	if closeErr != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tempName, closeErr)
	}

	if err := safefs.Chmod(ctx, tempName, sourceInfo.Mode(), to); err != nil {
		s.logger.Debug("Secondary storage: unable to mirror permissions on %s: %v", tempName, err)
	}
	if _, err := safefs.Run(ctx, "secondary-chtimes", tempName, to, func() (struct{}, error) {
		return struct{}{}, os.Chtimes(tempName, sourceInfo.ModTime(), sourceInfo.ModTime())
	}); err != nil {
		s.logger.Debug("Secondary storage: unable to mirror timestamps on %s: %v", tempName, err)
	}

	if err := safefs.Rename(ctx, tempName, dest, to); err != nil {
		return fmt.Errorf("failed to finalize copy to %s: %w", dest, err)
	}
	tempName = ""

	elapsed := time.Since(start)
	var rateStr string
	if elapsed > 0 {
		rate := float64(written) / elapsed.Seconds()
		if rate < 0 {
			rate = 0
		}
		rateStr = fmt.Sprintf("%s/s", utils.FormatBytes(int64(rate)))
	} else {
		rateStr = "n/a"
	}
	s.logger.Debug("Copied %s (%s) to %s in %s (avg %s)", filepath.Base(src), utils.FormatBytes(written), dest, elapsed.Truncate(time.Millisecond), rateStr)
	return nil
}

// List returns all backups in secondary storage
func (s *SecondaryStorage) List(ctx context.Context) (backups []*types.BackupMetadata, err error) {
	done := logging.DebugStart(s.logger, "secondary list", "path=%s", s.basePath)
	defer func() { done(err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Find all backup files (legacy + Go naming)
	globPatterns := []string{
		filepath.Join(s.basePath, "proxmox-backup-*.tar.*"), // Legacy Bash naming
		filepath.Join(s.basePath, "*-backup-*.tar*"),        // Go pipeline naming (bundle included)
	}

	timeout := fsIoTimeout(s.config)
	var matches []string
	seen := make(map[string]struct{})
	for _, pattern := range globPatterns {
		patternMatches, err := safefs.Run(ctx, "secondary-glob", s.basePath, timeout, func() ([]string, error) {
			return filepath.Glob(pattern)
		})
		if err != nil {
			s.logger.Warning("WARNING: Secondary storage - failed to list backups: %v", err)
			return nil, &StorageError{
				Location:    LocationSecondary,
				Operation:   "list",
				Path:        s.basePath,
				Err:         err,
				IsCritical:  false,
				Recoverable: true,
			}
		}
		for _, match := range patternMatches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			matches = append(matches, match)
		}
	}

	backups = nil

	// Filter and parse backup files
	for _, match := range matches {
		// Skip associated sidecars (checksum/metadata/manifest); shared predicate.
		if isBackupSidecar(match) {
			continue
		}

		// When bundling is enabled, skip standalone files that have a corresponding bundle
		if s.config != nil && s.config.BundleAssociatedFiles {
			if !strings.HasSuffix(match, ".bundle.tar") {
				// This is a standalone file, check if bundle exists
				bundlePath := match + ".bundle.tar"
				if _, err := safefs.Stat(ctx, bundlePath, timeout); err == nil {
					// Bundle exists, skip the standalone file
					s.logger.Debug("Skipping standalone file %s (bundle exists at %s)",
						filepath.Base(match), filepath.Base(bundlePath))
					continue
				}
			}
		}

		// Get file info (bounded against a dead/stale mount).
		stat, err := safefs.Stat(ctx, match, timeout)
		if err != nil {
			continue
		}

		backups = append(backups, &types.BackupMetadata{
			BackupFile: match,
			Timestamp:  stat.ModTime(),
			Size:       stat.Size(),
		})
	}

	// Sort by timestamp (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return backups, nil
}

// Delete removes a backup file and its associated files
func (s *SecondaryStorage) Delete(ctx context.Context, backupFile string) (err error) {
	done := logging.DebugStart(s.logger, "secondary delete", "file=%s", backupFile)
	defer func() { done(err) }()
	_, err = s.deleteBackupInternal(ctx, backupFile)
	return err
}

func (s *SecondaryStorage) deleteBackupInternal(ctx context.Context, backupFile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.logger.Debug("Deleting secondary backup: %s", backupFile)

	basePath, _ := trimBundleSuffix(backupFile)
	filesToDelete := buildBackupCandidatePaths(basePath, s.config.BundleAssociatedFiles)

	// Delete all files; collect real removal failures (not "already gone") and
	// track whether the data archive itself (not just a sidecar) failed, so the
	// caller never counts a backup whose archive remains on disk as deleted
	// (PS-BH-001), while a sidecar-only failure still counts (the archive IS gone).
	timeout := fsIoTimeout(s.config)
	var failedFiles []string
	dataFailed := false
	for _, f := range filesToDelete {
		if f == "" {
			continue
		}
		s.logger.Debug("Removing file: %s", f)
		if err := safefs.Remove(ctx, f, timeout); err != nil {
			if os.IsNotExist(err) {
				s.logger.Debug("Secondary storage: file already removed %s", f)
				continue
			}
			s.logger.Warning("WARNING: Secondary storage - failed to remove %s: %v", f, err)
			failedFiles = append(failedFiles, f)
			if !isBackupSidecar(f) {
				dataFailed = true
			}
		}
	}

	// Best-effort: delete associated secondary log file for this backup
	logDeleted := s.deleteAssociatedLog(backupFile)

	if len(failedFiles) > 0 {
		if !dataFailed {
			return logDeleted, fmt.Errorf("%w: %v", errBackupSidecarDeleteOnly, failedFiles)
		}
		return logDeleted, fmt.Errorf("failed to remove %d file(s): %v", len(failedFiles), failedFiles)
	}

	s.logger.Debug("Deleted secondary backup: %s", filepath.Base(backupFile))
	return logDeleted, nil
}

// deleteAssociatedLog attempts to remove the secondary log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (s *SecondaryStorage) deleteAssociatedLog(backupFile string) bool {
	if s == nil || s.config == nil {
		return false
	}

	logPath := strings.TrimSpace(s.config.SecondaryLogPath)
	if logPath == "" {
		return false
	}

	host, ts, ok := extractLogKeyFromBackup(backupFile)
	if !ok {
		return false
	}

	logName := fmt.Sprintf("backup-%s-%s.log", host, ts)
	fullPath := filepath.Join(logPath, logName)

	if err := os.Remove(fullPath); err != nil {
		if !os.IsNotExist(err) {
			s.logger.Debug("Secondary logs: failed to delete %s: %v", logName, err)
		}
		return false
	}

	s.logger.Debug("Secondary logs: deleted log file %s", logName)
	return true
}

func (s *SecondaryStorage) countLogFiles() int {
	if s == nil || s.config == nil {
		return -1
	}
	logPath := strings.TrimSpace(s.config.SecondaryLogPath)
	if logPath == "" {
		return 0
	}
	pattern := filepath.Join(logPath, "backup-*.log")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		s.logger.Debug("Secondary logs: failed to count log files: %v", err)
		return -1
	}
	return len(matches)
}

// ApplyRetention removes old backups according to retention policy
// Supports both simple (count-based) and GFS (time-distributed) policies
func (s *SecondaryStorage) ApplyRetention(ctx context.Context, config RetentionConfig) (deleted int, err error) {
	done := logging.DebugStart(s.logger, "secondary retention", "policy=%s max=%d", config.Policy, config.MaxBackups)
	defer func() { done(err) }()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// List all backups
	s.logger.Debug("Secondary storage: listing backups for retention policy '%s'", config.Policy)
	backups, err := s.List(ctx)
	if err != nil {
		s.logger.Warning("WARNING: Secondary storage - failed to list backups for retention: %v", err)
		return 0, &StorageError{
			Location:    LocationSecondary,
			Operation:   "apply_retention",
			Path:        s.basePath,
			Err:         err,
			IsCritical:  false,
			Recoverable: true,
		}
	}

	if len(backups) == 0 {
		s.logger.Debug("Secondary storage: no backups to apply retention")
		return 0, nil
	}

	// Apply appropriate retention policy
	if config.Policy == "gfs" {
		return s.applyGFSRetention(ctx, backups, config)
	}
	return s.applySimpleRetention(ctx, backups, config.MaxBackups)
}

// applyGFSRetention applies GFS (Grandfather-Father-Son) retention policy
func (s *SecondaryStorage) applyGFSRetention(ctx context.Context, backups []*types.BackupMetadata, config RetentionConfig) (int, error) {
	config = EffectiveGFSRetentionConfig(config)
	s.logger.Debug("Applying GFS retention policy (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
		config.Daily, config.Weekly, config.Monthly, config.Yearly)

	initialLogs := s.countLogFiles()
	logsDeleted := 0

	// Classify backups according to GFS scheme
	classification := ClassifyBackupsGFS(backups, config)

	// Get statistics
	stats := GetRetentionStats(classification)
	s.logger.Debug("GFS classification → daily: %d/%d, weekly: %d/%d, monthly: %d/%d, yearly: %d/%d, to_delete: %d",
		stats[CategoryDaily], config.Daily,
		stats[CategoryWeekly], config.Weekly,
		stats[CategoryMonthly], config.Monthly,
		stats[CategoryYearly], config.Yearly,
		stats[CategoryDelete])

	// Delete backups marked for deletion
	deleted := 0
	for backup, category := range classification {
		if category != CategoryDelete {
			continue
		}

		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		s.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := s.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			if !errors.Is(err, errBackupSidecarDeleteOnly) {
				s.logger.Warning("WARNING: Secondary storage - failed to delete %s: %v", backup.BackupFile, err)
				continue
			}
			// Archive removed, only sidecar(s) failed: count as deleted but warn.
			s.logger.Warning("WARNING: Secondary storage - %s archive removed but sidecar cleanup failed: %v", backup.BackupFile, err)
		}

		deleted++
		if logDeleted {
			logsDeleted++
		}
	}

	remaining := len(backups) - deleted
	if remaining < 0 {
		remaining = 0
	}

	if logsRemaining, ok := computeRemaining(initialLogs, logsDeleted); ok {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// applySimpleRetention applies simple count-based retention policy
func (s *SecondaryStorage) applySimpleRetention(ctx context.Context, backups []*types.BackupMetadata, maxBackups int) (int, error) {
	if maxBackups <= 0 {
		s.logger.Debug("Retention disabled for secondary storage (maxBackups = %d)", maxBackups)
		return 0, nil
	}

	totalBackups := len(backups)
	if totalBackups <= maxBackups {
		s.logger.Debug("Secondary storage: %d backups (within retention limit of %d)", totalBackups, maxBackups)
		return 0, nil
	}

	// Calculate how many to delete
	toDelete := totalBackups - maxBackups
	s.logger.Info("Applying simple retention policy: %d backups found, limit is %d, deleting %d oldest",
		totalBackups, maxBackups, toDelete)
	s.logger.Info("Simple retention → current: %d, limit: %d, to_delete: %d",
		totalBackups, maxBackups, toDelete)

	// Delete oldest backups (already sorted newest first)
	initialLogs := s.countLogFiles()
	logsDeleted := 0
	deleted := 0
	for i := totalBackups - 1; i >= maxBackups; i-- {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		backup := backups[i]
		s.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := s.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			if !errors.Is(err, errBackupSidecarDeleteOnly) {
				s.logger.Warning("WARNING: Secondary storage - failed to delete %s: %v", backup.BackupFile, err)
				continue
			}
			// Archive removed, only sidecar(s) failed: count as deleted but warn.
			s.logger.Warning("WARNING: Secondary storage - %s archive removed but sidecar cleanup failed: %v", backup.BackupFile, err)
		}

		deleted++
		if logDeleted {
			logsDeleted++
		}
	}

	remaining := totalBackups - deleted
	if remaining < 0 {
		remaining = 0
	}

	if logsRemaining, ok := computeRemaining(initialLogs, logsDeleted); ok {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// VerifyUpload is not applicable for secondary storage
func (s *SecondaryStorage) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	return true, nil
}

// LastRetentionSummary returns the latest retention summary.
func (s *SecondaryStorage) LastRetentionSummary() RetentionSummary {
	return s.lastRet
}

// GetStats returns storage statistics
func (s *SecondaryStorage) GetStats(ctx context.Context) (stats *StorageStats, err error) {
	done := logging.DebugStart(s.logger, "secondary stats", "path=%s", s.basePath)
	defer func() { done(err) }()
	backups, err := s.List(ctx)
	if err != nil {
		s.logger.Warning("WARNING: Secondary storage - failed to get stats: %v", err)
		return nil, err
	}

	stats = &StorageStats{
		TotalBackups: len(backups),
	}

	if s.fsInfo != nil {
		stats.FilesystemType = s.fsInfo.Type
	}

	var totalSize int64
	var oldest, newest *time.Time

	for _, backup := range backups {
		totalSize += backup.Size

		if oldest == nil || backup.Timestamp.Before(*oldest) {
			t := backup.Timestamp
			oldest = &t
		}
		if newest == nil || backup.Timestamp.After(*newest) {
			t := backup.Timestamp
			newest = &t
		}
	}

	stats.TotalSize = totalSize
	stats.OldestBackup = oldest
	stats.NewestBackup = newest

	// Get available/total space using statfs (bounded against a dead/stale mount).
	if stat, err := safefs.Statfs(ctx, s.basePath, fsIoTimeout(s.config)); err == nil {
		total, available, used := safefs.SpaceUsageFromStatfs(stat)
		stats.AvailableSpace = available
		stats.TotalSpace = total
		stats.UsedSpace = used
	}

	return stats, nil
}
