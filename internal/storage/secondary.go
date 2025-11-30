package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
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
		fsDetector: NewFilesystemDetector(logger),
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
func (s *SecondaryStorage) DetectFilesystem(ctx context.Context) (*FilesystemInfo, error) {
	// Ensure directory exists
	if err := os.MkdirAll(s.basePath, 0700); err != nil {
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
func (s *SecondaryStorage) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	s.logger.Debug("Secondary storage: preparing to store %s", filepath.Base(backupFile))
	// Check context
	if err := ctx.Err(); err != nil {
		s.logger.Debug("Secondary storage: store aborted due to context cancellation")
		return err
	}

	// Verify source file exists
	if _, err := os.Stat(backupFile); err != nil {
		s.logger.Debug("Secondary storage: source file %s not found", backupFile)
		s.logger.Warning("WARNING: Secondary storage - backup file not found: %s: %v", backupFile, err)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        backupFile,
			Err:         fmt.Errorf("source file not found: %w", err),
			IsCritical:  false,
			Recoverable: false,
		}
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(s.basePath, 0700); err != nil {
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
	destFile := filepath.Join(s.basePath, filepath.Base(backupFile))

	s.logger.Debug("Secondary Storage: Start copy...")
	s.logger.Debug("Copying backup to secondary storage: %s -> %s", filepath.Base(backupFile), s.basePath)

	if err := s.copyFile(ctx, backupFile, destFile); err != nil {
		s.logger.Warning("WARNING: Secondary Storage: File copy failed for %s: %v", filepath.Base(backupFile), err)
		s.logger.Warning("WARNING: Secondary Storage: Backup not saved to %s", s.basePath)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        backupFile,
			Err:         fmt.Errorf("copy failed: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Copy associated files if not bundled
	if !s.config.BundleAssociatedFiles {
		associatedFiles := []string{
			backupFile + ".sha256",
			backupFile + ".metadata",
			backupFile + ".metadata.sha256",
		}
		failedAssoc := make([]string, 0)

		for _, srcFile := range associatedFiles {
			if _, err := os.Stat(srcFile); err != nil {
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
	} else {
		// Copy bundle file
		bundleFile := backupFile + ".bundle.tar"
		if _, err := os.Stat(bundleFile); err == nil {
			destBundle := filepath.Join(s.basePath, filepath.Base(bundleFile))
			if err := s.copyFile(ctx, bundleFile, destBundle); err != nil {
				s.logger.Warning("WARNING: Secondary Storage: Failed to copy bundle %s: %v",
					filepath.Base(bundleFile), err)
			}
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

// copyFile copies a file using Go's io.Copy
func (s *SecondaryStorage) copyFile(ctx context.Context, src, dest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	sourceInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", src, err)
	}

	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	tempFile, err := os.CreateTemp(destDir, fmt.Sprintf(".tmp-%s-", filepath.Base(dest)))
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", destDir, err)
	}
	tempName := tempFile.Name()
	defer func() {
		tempFile.Close()
		if tempName != "" {
			os.Remove(tempName)
		}
	}()

	start := time.Now()
	sourceFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer sourceFile.Close()

	buf := make([]byte, 1024*1024) // 1MB buffer
	var written int64

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		nr, er := sourceFile.Read(buf)
		if nr > 0 {
			if _, ew := tempFile.Write(buf[:nr]); ew != nil {
				return fmt.Errorf("write error during copy: %w", ew)
			}
			written += int64(nr)
		}

		if er != nil {
			if er == io.EOF {
				break
			}
			return fmt.Errorf("read error during copy: %w", er)
		}
	}

	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary file %s: %w", tempName, err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tempName, err)
	}
	tempFile = nil

	if err := os.Chmod(tempName, sourceInfo.Mode()); err != nil {
		s.logger.Debug("Secondary storage: unable to mirror permissions on %s: %v", tempName, err)
	}
	if err := os.Chtimes(tempName, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		s.logger.Debug("Secondary storage: unable to mirror timestamps on %s: %v", tempName, err)
	}

	if err := os.Rename(tempName, dest); err != nil {
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
func (s *SecondaryStorage) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Find all backup files (legacy + Go naming)
	globPatterns := []string{
		filepath.Join(s.basePath, "proxmox-backup-*.tar.*"), // Legacy Bash naming
		filepath.Join(s.basePath, "*-backup-*.tar*"),        // Go pipeline naming (bundle compreso)
	}

	var matches []string
	seen := make(map[string]struct{})
	for _, pattern := range globPatterns {
		patternMatches, err := filepath.Glob(pattern)
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

	var backups []*types.BackupMetadata

	// Filter and parse backup files
	for _, match := range matches {
		// Skip associated files
		if strings.HasSuffix(match, ".sha256") ||
			strings.HasSuffix(match, ".metadata") {
			continue
		}

		// When bundling is enabled, skip standalone files that have a corresponding bundle
		if s.config != nil && s.config.BundleAssociatedFiles {
			if !strings.HasSuffix(match, ".bundle.tar") {
				// This is a standalone file, check if bundle exists
				bundlePath := match + ".bundle.tar"
				if _, err := os.Stat(bundlePath); err == nil {
					// Bundle exists, skip the standalone file
					s.logger.Debug("Skipping standalone file %s (bundle exists at %s)",
						filepath.Base(match), filepath.Base(bundlePath))
					continue
				}
			}
		}

		// Get file info
		stat, err := os.Stat(match)
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
func (s *SecondaryStorage) Delete(ctx context.Context, backupFile string) error {
	_, err := s.deleteBackupInternal(ctx, backupFile)
	return err
}

func (s *SecondaryStorage) deleteBackupInternal(ctx context.Context, backupFile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.logger.Debug("Deleting secondary backup: %s", backupFile)

	basePath, _ := trimBundleSuffix(backupFile)
	filesToDelete := buildBackupCandidatePaths(basePath, s.config.BundleAssociatedFiles)

	// Delete all files (non-critical errors)
	for _, f := range filesToDelete {
		if f == "" {
			continue
		}
		s.logger.Debug("Removing file: %s", f)
		if err := os.Remove(f); err != nil {
			if os.IsNotExist(err) {
				s.logger.Debug("Secondary storage: file already removed %s", f)
				continue
			}
			s.logger.Warning("WARNING: Secondary storage - failed to remove %s: %v", f, err)
			// Continue with other files
		}
	}

	// Best-effort: delete associated secondary log file for this backup
	logDeleted := s.deleteAssociatedLog(backupFile)

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
func (s *SecondaryStorage) ApplyRetention(ctx context.Context, config RetentionConfig) (int, error) {
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
			s.logger.Warning("WARNING: Secondary storage - failed to delete %s: %v", backup.BackupFile, err)
			continue
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
			s.logger.Warning("WARNING: Secondary storage - failed to delete %s: %v", backup.BackupFile, err)
			continue
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
func (s *SecondaryStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	backups, err := s.List(ctx)
	if err != nil {
		s.logger.Warning("WARNING: Secondary storage - failed to get stats: %v", err)
		return nil, err
	}

	stats := &StorageStats{
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

	// Get available/total space using statfs
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.basePath, &stat); err == nil {
		available := int64(stat.Bavail) * int64(stat.Bsize)
		total := int64(stat.Blocks) * int64(stat.Bsize)
		if available < 0 {
			available = 0
		}
		if total < 0 {
			total = 0
		}
		stats.AvailableSpace = available
		stats.TotalSpace = total
		used := total - available
		if used < 0 {
			used = 0
		}
		stats.UsedSpace = used
	}

	return stats, nil
}
