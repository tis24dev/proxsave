package storage

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// LocalStorage implements the Storage interface for local filesystem storage
type LocalStorage struct {
	config     *config.Config
	logger     *logging.Logger
	basePath   string
	fsDetector *FilesystemDetector
	fsInfo     *FilesystemInfo
	lastRet    RetentionSummary
}

// NewLocalStorage creates a new local storage instance
func NewLocalStorage(cfg *config.Config, logger *logging.Logger) (*LocalStorage, error) {
	return &LocalStorage{
		config:     cfg,
		logger:     logger,
		basePath:   cfg.BackupPath,
		fsDetector: NewFilesystemDetector(logger),
	}, nil
}

// Name returns the storage backend name
func (l *LocalStorage) Name() string {
	return "Local Storage"
}

// Location returns the backup location type
func (l *LocalStorage) Location() BackupLocation {
	return LocationPrimary
}

// IsEnabled returns true if local storage is configured
func (l *LocalStorage) IsEnabled() bool {
	return l.basePath != ""
}

// IsCritical returns true because local storage is critical
func (l *LocalStorage) IsCritical() bool {
	return true
}

// DetectFilesystem detects the filesystem type for the backup path
func (l *LocalStorage) DetectFilesystem(ctx context.Context) (*FilesystemInfo, error) {
	// Ensure directory exists
	if err := os.MkdirAll(l.basePath, 0700); err != nil {
		return nil, &StorageError{
			Location:   LocationPrimary,
			Operation:  "detect_filesystem",
			Path:       l.basePath,
			Err:        err,
			IsCritical: true,
		}
	}

	fsInfo, err := l.fsDetector.DetectFilesystem(ctx, l.basePath)
	if err != nil {
		return nil, &StorageError{
			Location:   LocationPrimary,
			Operation:  "detect_filesystem",
			Path:       l.basePath,
			Err:        err,
			IsCritical: true,
		}
	}

	l.fsInfo = fsInfo
	return fsInfo, nil
}

// Store stores a backup file to local storage
// For local storage, this mainly involves setting proper permissions
func (l *LocalStorage) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	l.logger.Debug("Local storage: preparing to store %s", filepath.Base(backupFile))
	// Check context
	if err := ctx.Err(); err != nil {
		l.logger.Debug("Local storage: store aborted due to context cancellation")
		return err
	}

	// Verify file exists
	if _, err := os.Stat(backupFile); err != nil {
		l.logger.Debug("Local storage: source file %s not found", backupFile)
		return &StorageError{
			Location:   LocationPrimary,
			Operation:  "store",
			Path:       backupFile,
			Err:        fmt.Errorf("backup file not found: %w", err),
			IsCritical: true,
		}
	}

	// Set proper permissions on the backup file
	l.logger.Debug("Local storage: setting ownership/permissions on %s", filepath.Base(backupFile))
	if err := l.fsDetector.SetPermissions(ctx, backupFile, 0, 0, 0600, l.fsInfo); err != nil {
		l.logger.Warning("Failed to set permissions on %s: %v", backupFile, err)
		// Not critical - continue
	}

	l.logger.Debug("Backup stored successfully in local storage: %s", backupFile)

	if count := l.countBackups(ctx); count >= 0 {
		l.logger.Debug("Local storage: current backups detected after archive creation: %d", count)
	} else {
		l.logger.Debug("Local storage: unable to count backups after archive creation (see previous log for details)")
	}

	return nil
}

func (l *LocalStorage) countBackups(ctx context.Context) int {
	backups, err := l.List(ctx)
	if err != nil {
		l.logger.Debug("Local storage: failed to list backups for recount: %v", err)
		return -1
	}
	return len(backups)
}

// List returns all backups in local storage
func (l *LocalStorage) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Find all backup files (legacy "proxmox-backup-*.tar.*" or new "*-backup-*.tar*")
	globPatterns := []string{
		filepath.Join(l.basePath, "proxmox-backup-*.tar.*"), // Legacy Bash naming
		filepath.Join(l.basePath, "*-backup-*.tar*"),        // Go pipeline naming (+ bundle)
	}

	var matches []string
	seen := make(map[string]struct{})
	for _, pattern := range globPatterns {
		patternMatches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, &StorageError{
				Location:   LocationPrimary,
				Operation:  "list",
				Path:       l.basePath,
				Err:        err,
				IsCritical: true,
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
		// Skip associated files (.sha256, .metadata)
		if strings.HasSuffix(match, ".sha256") ||
			strings.HasSuffix(match, ".metadata") {
			continue
		}

		// When bundling is enabled, skip standalone files that have a corresponding bundle
		if l.config != nil && l.config.BundleAssociatedFiles {
			if !strings.HasSuffix(match, ".bundle.tar") {
				// This is a standalone file, check if bundle exists
				bundlePath := match + ".bundle.tar"
				if _, err := os.Stat(bundlePath); err == nil {
					// Bundle exists, skip the standalone file
					l.logger.Debug("Skipping standalone file %s (bundle exists at %s)",
						filepath.Base(match), filepath.Base(bundlePath))
					continue
				}
			}
		}

		l.logger.Debug("Local storage: processing candidate %s (BundleAssociatedFiles=%v)",
			match, l.config != nil && l.config.BundleAssociatedFiles)

		// Parse metadata if available
		metadata, err := l.loadMetadata(match)
		if err != nil {
			l.logger.Warning("Failed to load metadata for %s: %v", match, err)
			// Create minimal metadata from filename
			metadata = &types.BackupMetadata{
				BackupFile: match,
			}
			if stat, statErr := os.Stat(match); statErr == nil {
				metadata.Timestamp = stat.ModTime()
				metadata.Size = stat.Size()
			} else {
				l.logger.Debug("Failed to stat %s after metadata load failure: %v", match, statErr)
			}
		}

		backups = append(backups, metadata)
	}

	// Sort by timestamp (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return backups, nil
}

// loadMetadata loads metadata for a backup file
func (l *LocalStorage) loadMetadata(backupFile string) (*types.BackupMetadata, error) {
	l.logger.Debug("Local storage: loadMetadata called for %s (isBundle=%v)",
		backupFile, strings.HasSuffix(backupFile, ".bundle.tar"))

	if strings.HasSuffix(backupFile, ".bundle.tar") {
		l.logger.Debug("Local storage: delegating to loadMetadataFromBundle(%s)", backupFile)
		return l.loadMetadataFromBundle(backupFile)
	}

	// When bundles are enabled, prefer reading metadata from the bundle
	if l != nil && l.config != nil && l.config.BundleAssociatedFiles {
		bundlePath := backupFile + ".bundle.tar"
		l.logger.Debug("Local storage: BundleAssociatedFiles=true, checking bundle %s", bundlePath)
		if _, err := os.Stat(bundlePath); err == nil {
			l.logger.Debug("Local storage: using metadata from bundle %s for %s", bundlePath, backupFile)
			return l.loadMetadataFromBundle(bundlePath)
		} else {
			l.logger.Debug("Local storage: bundle %s not found (%v) — falling back to sidecar metadata", bundlePath, err)
		}
	}

	metadataFile := backupFile + ".metadata"
	l.logger.Debug("Local storage: using sidecar metadata file %s", metadataFile)
	if _, err := os.Stat(metadataFile); err != nil {
		l.logger.Debug("Local storage: sidecar metadata %s missing/inaccessible: %v", metadataFile, err)
		return nil, err
	}

	manifest, err := backup.LoadManifest(metadataFile)
	if err != nil {
		return nil, err
	}

	metadata := &types.BackupMetadata{
		BackupFile:  backupFile,
		Timestamp:   manifest.CreatedAt,
		Size:        manifest.ArchiveSize,
		Checksum:    manifest.SHA256,
		ProxmoxType: types.ProxmoxType(manifest.ProxmoxType),
		Compression: types.CompressionType(manifest.CompressionType),
		Version:     manifest.ScriptVersion,
	}

	if metadata.Timestamp.IsZero() || metadata.Size == 0 {
		if stat, statErr := os.Stat(backupFile); statErr == nil {
			if metadata.Timestamp.IsZero() {
				metadata.Timestamp = stat.ModTime()
			}
			if metadata.Size == 0 {
				metadata.Size = stat.Size()
			}
		}
	}

	return metadata, nil
}

func (l *LocalStorage) loadMetadataFromBundle(bundlePath string) (*types.BackupMetadata, error) {
	l.logger.Debug("Local storage: loadMetadataFromBundle called for %s", bundlePath)

	file, err := os.Open(bundlePath)
	if err != nil {
		l.logger.Debug("Local storage: failed to open bundle %s: %v", bundlePath, err)
		return nil, err
	}
	defer file.Close()

	tr := tar.NewReader(file)
	expectedName := strings.TrimSuffix(filepath.Base(bundlePath), ".bundle.tar") + ".metadata"
	l.logger.Debug("Local storage: expecting metadata entry %s in bundle %s", expectedName, filepath.Base(bundlePath))

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			l.logger.Warning("Local storage: metadata %s not found inside bundle %s", expectedName, filepath.Base(bundlePath))
			return nil, fmt.Errorf("metadata %s not found in bundle %s", expectedName, filepath.Base(bundlePath))
		}
		if err != nil {
			return nil, fmt.Errorf("read bundle %s: %w", filepath.Base(bundlePath), err)
		}

		if filepath.Base(hdr.Name) != expectedName {
			continue
		}

		var manifest backup.Manifest
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			return nil, fmt.Errorf("parse manifest from bundle %s: %w", filepath.Base(bundlePath), err)
		}

		metadata := &types.BackupMetadata{
			BackupFile:  bundlePath,
			Timestamp:   manifest.CreatedAt,
			Size:        manifest.ArchiveSize,
			Checksum:    manifest.SHA256,
			ProxmoxType: types.ProxmoxType(manifest.ProxmoxType),
			Compression: types.CompressionType(manifest.CompressionType),
			Version:     manifest.ScriptVersion,
		}

		if metadata.Timestamp.IsZero() || metadata.Size == 0 {
			if stat, statErr := os.Stat(bundlePath); statErr == nil {
				if metadata.Timestamp.IsZero() {
					metadata.Timestamp = stat.ModTime()
				}
				if metadata.Size == 0 {
					metadata.Size = stat.Size()
				}
			}
		}

		return metadata, nil
	}
}

// Delete removes a backup file and its associated files
func (l *LocalStorage) Delete(ctx context.Context, backupFile string) error {
	_, err := l.deleteBackupInternal(ctx, backupFile)
	return err
}

func (l *LocalStorage) deleteBackupInternal(ctx context.Context, backupFile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	l.logger.Debug("Local storage: deleting backup %s", filepath.Base(backupFile))

	basePath, _ := trimBundleSuffix(backupFile)
	filesToDelete := buildBackupCandidatePaths(basePath, l.config.BundleAssociatedFiles)

	// Delete all files
	for _, f := range filesToDelete {
		if f == "" {
			continue
		}
		l.logger.Debug("Local storage: removing file %s", f)
		if err := os.Remove(f); err != nil {
			if os.IsNotExist(err) {
				l.logger.Debug("Local storage: file already removed %s", f)
				continue
			}
			l.logger.Warning("Failed to remove %s: %v", f, err)
			// Continue with other files
		}
	}

	// Best-effort: delete associated local log file for this backup
	logDeleted := l.deleteAssociatedLog(backupFile)

	l.logger.Debug("Local storage: deleted backup and associated files: %s", filepath.Base(backupFile))
	return logDeleted, nil
}

// deleteAssociatedLog attempts to remove the local log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (l *LocalStorage) deleteAssociatedLog(backupFile string) bool {
	if l == nil || l.config == nil {
		return false
	}
	logPath := strings.TrimSpace(l.config.LogPath)
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
			l.logger.Debug("Local logs: failed to delete %s: %v", logName, err)
		}
		return false
	}

	l.logger.Debug("Local logs: deleted log file %s", logName)
	return true
}

func (l *LocalStorage) countLogFiles() int {
	if l == nil || l.config == nil {
		return -1
	}
	logPath := strings.TrimSpace(l.config.LogPath)
	if logPath == "" {
		return 0
	}
	pattern := filepath.Join(logPath, "backup-*.log")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		l.logger.Debug("Local logs: failed to count log files: %v", err)
		return -1
	}
	return len(matches)
}

// ApplyRetention removes old backups according to retention policy
// Supports both simple (count-based) and GFS (time-distributed) policies
func (l *LocalStorage) ApplyRetention(ctx context.Context, config RetentionConfig) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// List all backups
	l.logger.Debug("Local storage: listing backups for retention policy '%s'", config.Policy)
	backups, err := l.List(ctx)
	if err != nil {
		return 0, &StorageError{
			Location:   LocationPrimary,
			Operation:  "apply_retention",
			Path:       l.basePath,
			Err:        err,
			IsCritical: true,
		}
	}

	if len(backups) == 0 {
		l.logger.Debug("Local storage: no backups to apply retention")
		return 0, nil
	}

	// Apply appropriate retention policy
	if config.Policy == "gfs" {
		return l.applyGFSRetention(ctx, backups, config)
	}
	return l.applySimpleRetention(ctx, backups, config.MaxBackups)
}

// applyGFSRetention applies GFS (Grandfather-Father-Son) retention policy
func (l *LocalStorage) applyGFSRetention(ctx context.Context, backups []*types.BackupMetadata, config RetentionConfig) (int, error) {
	l.logger.Debug("Applying GFS retention policy (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
		config.Daily, config.Weekly, config.Monthly, config.Yearly)

	initialLogs := l.countLogFiles()
	logsDeleted := 0

	// Classify backups according to GFS scheme
	classification := ClassifyBackupsGFS(backups, config)

	// Get statistics
	stats := GetRetentionStats(classification)
	kept := len(backups) - stats[CategoryDelete]
	l.logger.Debug("GFS classification → daily: %d/%d, weekly: %d/%d, monthly: %d/%d, yearly: %d/%d, kept: %d, to_delete: %d",
		stats[CategoryDaily], config.Daily,
		stats[CategoryWeekly], config.Weekly,
		stats[CategoryMonthly], config.Monthly,
		stats[CategoryYearly], config.Yearly,
		kept,
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

		l.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := l.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			l.logger.Warning("Failed to delete %s: %v", backup.BackupFile, err)
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
		l.logger.Debug("Local storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		l.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		l.logger.Debug("Local storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		l.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// applySimpleRetention applies simple count-based retention policy
func (l *LocalStorage) applySimpleRetention(ctx context.Context, backups []*types.BackupMetadata, maxBackups int) (int, error) {
	if maxBackups <= 0 {
		l.logger.Debug("Retention disabled for local storage (maxBackups = %d)", maxBackups)
		return 0, nil
	}

	totalBackups := len(backups)
	if totalBackups <= maxBackups {
		l.logger.Debug("Local storage: %d backups (within retention limit of %d)", totalBackups, maxBackups)
		return 0, nil
	}

	// Calculate how many to delete
	toDelete := totalBackups - maxBackups
	l.logger.Info("Applying simple retention policy: %d backups found, limit is %d, deleting %d oldest",
		totalBackups, maxBackups, toDelete)
	l.logger.Info("Simple retention → current: %d, limit: %d, to_delete: %d",
		totalBackups, maxBackups, toDelete)

	// Delete oldest backups (already sorted newest first)
	initialLogs := l.countLogFiles()
	logsDeleted := 0
	deleted := 0
	for i := totalBackups - 1; i >= maxBackups; i-- {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		backup := backups[i]
		l.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := l.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			l.logger.Warning("Failed to delete %s: %v", backup.BackupFile, err)
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
		l.logger.Debug("Local storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		l.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		l.logger.Debug("Local storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		l.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// LastRetentionSummary returns information about the latest retention run.
func (l *LocalStorage) LastRetentionSummary() RetentionSummary {
	return l.lastRet
}

// VerifyUpload is not applicable for local storage
func (l *LocalStorage) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	return true, nil
}

// GetStats returns storage statistics
func (l *LocalStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	backups, err := l.List(ctx)
	if err != nil {
		return nil, err
	}

	stats := &StorageStats{
		TotalBackups: len(backups),
	}

	if l.fsInfo != nil {
		stats.FilesystemType = l.fsInfo.Type
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
	if err := syscall.Statfs(l.basePath, &stat); err == nil {
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
