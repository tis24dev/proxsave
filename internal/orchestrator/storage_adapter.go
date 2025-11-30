package orchestrator

import (
	"context"
	"fmt"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// StorageAdapter adapts a storage.Storage backend to the StorageTarget interface
type StorageAdapter struct {
	backend      storage.Storage
	logger       *logging.Logger
	config       *config.Config // Main configuration for retention policy
	fsInfo       *storage.FilesystemInfo
	initialStats *storage.StorageStats
}

// NewStorageAdapter creates a new storage adapter
func NewStorageAdapter(backend storage.Storage, logger *logging.Logger, cfg *config.Config) *StorageAdapter {
	return &StorageAdapter{
		backend: backend,
		logger:  logger,
		config:  cfg,
	}
}

// SetFilesystemInfo preloads filesystem info detected earlier.
func (s *StorageAdapter) SetFilesystemInfo(info *storage.FilesystemInfo) {
	if info != nil {
		s.fsInfo = info
	}
}

// SetInitialStats caches storage stats gathered during initialization.
func (s *StorageAdapter) SetInitialStats(stats *storage.StorageStats) {
	s.initialStats = stats
}

// Sync implements the StorageTarget interface
// It performs filesystem detection, stores the backup, and applies retention
func (s *StorageAdapter) Sync(ctx context.Context, stats *BackupStats) error {
	// Check if backend is enabled
	if !s.backend.IsEnabled() {
		s.logger.Debug("%s is disabled, skipping", s.backend.Name())
		s.setStorageStatus(stats, "disabled")
		return nil
	}

	s.logger.Debug("Starting %s operations...", s.backend.Name())

	// Assume success unless operations report otherwise
	s.setStorageStatus(stats, "ok")

	// Step 1: Detect filesystem and log in real-time
	var err error
	fsInfo := s.fsInfo
	hasWarnings := false
	hasErrors := false
	if fsInfo == nil {
		fsInfo, err = s.backend.DetectFilesystem(ctx)
		if err != nil {
			if s.backend.IsCritical() {
				s.setStorageStatus(stats, "error")
				return fmt.Errorf("%s filesystem detection failed (CRITICAL): %w", s.backend.Name(), err)
			}
			s.logger.Warning("WARNING: %s filesystem detection failed: %v", s.backend.Name(), err)
			s.logger.Warning("WARNING: %s operations will be skipped", s.backend.Name())
			s.setStorageStatus(stats, "error")
			return nil
		}
		s.fsInfo = fsInfo
	}

	// Step 2: Prepare backup metadata
	metadata := &types.BackupMetadata{
		BackupFile:  stats.ArchivePath,
		Timestamp:   stats.StartTime,
		Size:        stats.ArchiveSize,
		Checksum:    stats.Checksum,
		ProxmoxType: stats.ProxmoxType,
		Compression: stats.Compression,
		Version:     stats.Version,
	}

	// Step 3: Store backup
	s.logger.Step("%s: Storing backup", s.backend.Name())
	if err := s.backend.Store(ctx, stats.ArchivePath, metadata); err != nil {
		// Check if error is critical
		if s.backend.IsCritical() {
			s.setStorageStatus(stats, "error")
			return fmt.Errorf("%s store operation failed (CRITICAL): %w", s.backend.Name(), err)
		}

		// Non-critical error - log warning and continue
		s.logger.Warning("WARNING: %s store operation failed: %v", s.backend.Name(), err)
		s.logger.Warning("WARNING: Backup was not saved to %s", s.backend.Name())
		hasErrors = true
		// Don't return error - continue with retention
	} else {
		s.logger.Info("✓ %s: Backup stored successfully", s.backend.Name())
	}

	// Step 4: Apply retention policy
	retentionConfig := storage.NewRetentionConfigFromConfig(s.config, s.backend.Location())
	if retentionConfig.Policy == "gfs" {
		// Enforce GFS-specific rules (e.g. minimum DAILY=1) once per backend.
		retentionConfig = storage.NormalizeGFSRetentionConfig(s.logger, s.backend.Name(), retentionConfig)
	}
	if retentionConfig.MaxBackups > 0 || retentionConfig.Policy == "gfs" {
		if retentionConfig.Policy == "gfs" {
			s.logger.Info("%s: Applying GFS retention policy...", s.backend.Name())
		} else {
			s.logger.Info("%s: Applying retention policy...", s.backend.Name())
		}
		s.logRetentionPolicyDetails(retentionConfig)

		s.logCurrentBackupCount()
		deleted, err := s.backend.ApplyRetention(ctx, retentionConfig)
		if err != nil {
			// Check if error is critical
			if s.backend.IsCritical() {
				s.setStorageStatus(stats, "error")
				return fmt.Errorf("%s retention failed (CRITICAL): %w", s.backend.Name(), err)
			}

			// Non-critical error - log warning and continue
			s.logger.Warning("WARNING: %s retention failed: %v", s.backend.Name(), err)
			hasWarnings = true
		} else if deleted > 0 {
			if reporter, ok := s.backend.(storage.RetentionReporter); ok {
				summary := reporter.LastRetentionSummary()
				backupsDeleted := summary.BackupsDeleted
				if backupsDeleted == 0 {
					backupsDeleted = deleted
				}
				logSuffix := ""
				if summary.LogsDeleted > 0 {
					logSuffix = fmt.Sprintf(" (logs deleted: %d)", summary.LogsDeleted)
				}
				s.logger.Info("✓ %s: Deleted %d old backups%s", s.backend.Name(), backupsDeleted, logSuffix)
			} else {
				s.logger.Info("✓ %s: Deleted %d old backups", s.backend.Name(), deleted)
			}
		}
	}

	// Step 5: Get and log statistics
	storageStats, err := s.backend.GetStats(ctx)
	if err != nil {
		s.logger.Debug("%s: Failed to get statistics: %v", s.backend.Name(), err)
	} else {
		s.logger.Info("%s statistics:", s.backend.Name())
		s.logger.Info("  Total backups: %d", storageStats.TotalBackups)
		if storageStats.TotalSize > 0 {
			s.logger.Info("  Total size: %s", formatBytes(storageStats.TotalSize))
		}
		if fsInfo != nil {
			s.logger.Info("  Filesystem: %s", fsInfo.Type)
		}

		if stats != nil {
			s.applyStorageStats(storageStats, retentionConfig, stats)
		}
	}

	if hasWarnings {
		s.logger.Warning("✗ %s operations completed with warnings", s.backend.Name())
	} else {
		s.logger.Info("✓ %s operations completed", s.backend.Name())
	}
	s.finalizeStorageStatus(stats, hasErrors, hasWarnings)
	return nil
}

func (s *StorageAdapter) logCurrentBackupCount() {
	listable, ok := s.backend.(interface {
		List(context.Context) ([]*types.BackupMetadata, error)
	})
	if !ok {
		return
	}

	// Il backend cloud logga già il conteggio corrente durante ApplyRetention
	// riutilizzando la stessa lista; evitiamo una seconda chiamata rclone lsl.
	if s.backend.Location() == storage.LocationCloud {
		return
	}

	backups, err := listable.List(context.Background())
	if err != nil {
		s.logger.Debug("%s: Unable to count backups prior to retention: %v", s.backend.Name(), err)
		return
	}
	s.logger.Debug("%s: Current backups detected: %d", s.backend.Name(), len(backups))
}

func (s *StorageAdapter) logRetentionPolicyDetails(cfg storage.RetentionConfig) {
	if s.logger == nil {
		return
	}
	if cfg.Policy == "gfs" {
		s.logger.Debug("  Policy: GFS (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
			cfg.Daily, cfg.Weekly, cfg.Monthly, cfg.Yearly)
		return
	}
	if cfg.MaxBackups > 0 {
		s.logger.Debug("  Policy: simple (keep %d newest)", cfg.MaxBackups)
	} else {
		s.logger.Debug("  Policy: simple (disabled)")
	}
}

// formatBytes formats bytes in human-readable format
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func (s *StorageAdapter) applyStorageStats(storageStats *storage.StorageStats, retentionConfig storage.RetentionConfig, stats *BackupStats) {
	if storageStats == nil || stats == nil {
		return
	}

	// Get current GFS stats if in GFS mode
	var gfsStats map[storage.RetentionCategory]int
	if retentionConfig.Policy == "gfs" {
		// Get backups list for classification
		if listable, ok := s.backend.(interface {
			List(context.Context) ([]*types.BackupMetadata, error)
		}); ok {
			backups, err := listable.List(context.Background())
			if err == nil && len(backups) > 0 {
				classification := storage.ClassifyBackupsGFS(backups, retentionConfig)
				gfsStats = storage.GetRetentionStats(classification)
			}
		}
	}

	switch s.backend.Location() {
	case storage.LocationPrimary:
		stats.LocalBackups = storageStats.TotalBackups
		stats.LocalFreeSpace = clampInt64ToUint64(storageStats.AvailableSpace)
		stats.LocalTotalSpace = clampInt64ToUint64(storageStats.TotalSpace)
		// Populate retention info
		stats.LocalRetentionPolicy = retentionConfig.Policy
		if retentionConfig.Policy == "gfs" {
			stats.LocalGFSDaily = retentionConfig.Daily
			stats.LocalGFSWeekly = retentionConfig.Weekly
			stats.LocalGFSMonthly = retentionConfig.Monthly
			stats.LocalGFSYearly = retentionConfig.Yearly
			if gfsStats != nil {
				stats.LocalGFSCurrentDaily = gfsStats[storage.CategoryDaily]
				stats.LocalGFSCurrentWeekly = gfsStats[storage.CategoryWeekly]
				stats.LocalGFSCurrentMonthly = gfsStats[storage.CategoryMonthly]
				stats.LocalGFSCurrentYearly = gfsStats[storage.CategoryYearly]
			}
		}
	case storage.LocationSecondary:
		if !stats.SecondaryEnabled {
			stats.SecondaryEnabled = true
		}
		stats.SecondaryBackups = storageStats.TotalBackups
		stats.SecondaryFreeSpace = clampInt64ToUint64(storageStats.AvailableSpace)
		stats.SecondaryTotalSpace = clampInt64ToUint64(storageStats.TotalSpace)
		// Populate retention info
		stats.SecondaryRetentionPolicy = retentionConfig.Policy
		if retentionConfig.Policy == "gfs" {
			stats.SecondaryGFSDaily = retentionConfig.Daily
			stats.SecondaryGFSWeekly = retentionConfig.Weekly
			stats.SecondaryGFSMonthly = retentionConfig.Monthly
			stats.SecondaryGFSYearly = retentionConfig.Yearly
			if gfsStats != nil {
				stats.SecondaryGFSCurrentDaily = gfsStats[storage.CategoryDaily]
				stats.SecondaryGFSCurrentWeekly = gfsStats[storage.CategoryWeekly]
				stats.SecondaryGFSCurrentMonthly = gfsStats[storage.CategoryMonthly]
				stats.SecondaryGFSCurrentYearly = gfsStats[storage.CategoryYearly]
			}
		}
	case storage.LocationCloud:
		if !stats.CloudEnabled {
			stats.CloudEnabled = true
		}
		stats.CloudBackups = storageStats.TotalBackups
		// Populate retention info
		stats.CloudRetentionPolicy = retentionConfig.Policy
		if retentionConfig.Policy == "gfs" {
			stats.CloudGFSDaily = retentionConfig.Daily
			stats.CloudGFSWeekly = retentionConfig.Weekly
			stats.CloudGFSMonthly = retentionConfig.Monthly
			stats.CloudGFSYearly = retentionConfig.Yearly
			if gfsStats != nil {
				stats.CloudGFSCurrentDaily = gfsStats[storage.CategoryDaily]
				stats.CloudGFSCurrentWeekly = gfsStats[storage.CategoryWeekly]
				stats.CloudGFSCurrentMonthly = gfsStats[storage.CategoryMonthly]
				stats.CloudGFSCurrentYearly = gfsStats[storage.CategoryYearly]
			}
		}
	}
}

func clampInt64ToUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func (s *StorageAdapter) finalizeStorageStatus(stats *BackupStats, hasErrors, hasWarnings bool) {
	if stats == nil {
		return
	}
	switch {
	case hasErrors:
		s.setStorageStatus(stats, "error")
	case hasWarnings:
		s.setStorageStatus(stats, "warning")
	default:
		s.setStorageStatus(stats, "ok")
	}
}

func (s *StorageAdapter) setStorageStatus(stats *BackupStats, status string) {
	if stats == nil || s == nil || s.backend == nil {
		return
	}
	switch s.backend.Location() {
	case storage.LocationSecondary:
		stats.SecondaryStatus = status
	case storage.LocationCloud:
		stats.CloudStatus = status
	case storage.LocationPrimary:
		stats.LocalStatus = status
	default:
		stats.LocalStatus = status
	}
}
