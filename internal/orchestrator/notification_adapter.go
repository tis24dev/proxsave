package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
)

// NotificationAdapter adapts notify.Notifier to NotificationChannel interface
type NotificationAdapter struct {
	notifier notify.Notifier
	logger   *logging.Logger
}

// NewNotificationAdapter creates a new NotificationAdapter
func NewNotificationAdapter(notifier notify.Notifier, logger *logging.Logger) *NotificationAdapter {
	return &NotificationAdapter{
		notifier: notifier,
		logger:   logger,
	}
}

// Notify implements the NotificationChannel interface
func (n *NotificationAdapter) Notify(ctx context.Context, stats *BackupStats) error {
	n.logger.Info("%s: starting", n.notifier.Name())
	n.logger.Debug("=== NotificationAdapter.Notify() called for '%s' notifier ===", n.notifier.Name())

	if !n.notifier.IsEnabled() {
		n.logger.Debug("%s: disabled, skipping", n.notifier.Name())
		return nil
	}

	n.logger.Debug("Converting BackupStats to NotificationData...")
	n.logger.Debug("Stats summary: exit_code=%d, duration=%s, archive_size=%d bytes, hostname=%s",
		stats.ExitCode, stats.Duration, stats.ArchiveSize, stats.Hostname)

	// Convert BackupStats to NotificationData
	data := n.convertBackupStatsToNotificationData(stats)

	n.logger.Debug("NotificationData created: status=%s, hostname=%s, files_included=%d, errors=%d, warnings=%d",
		data.Status.String(), data.Hostname, data.FilesIncluded, data.ErrorCount, data.WarningCount)

	statusEmoji := notify.GetStatusEmoji(data.Status)
	n.logger.Debug("Notification subject emoji resolved: %s (status=%s, exit_code=%d)",
		statusEmoji, strings.ToUpper(data.Status.String()), data.ExitCode)

	// Send notification
	n.logger.Debug("Calling %s.Send()...", n.notifier.Name())
	result, err := n.notifier.Send(ctx, data)

	if err != nil {
		// Log error but don't abort backup (notifications are non-critical)
		n.logger.Error("❌ %s: failed: %v", n.notifier.Name(), err)
		n.recordNotifierStatus(stats, &notify.NotificationResult{
			Success: false,
			Method:  n.notifier.Name(),
			Error:   err,
		})
		return nil // Don't propagate error - notifications are non-critical
	}

	n.logger.Debug("Notifier '%s' returned result: success=%v, method=%s, duration=%s",
		n.notifier.Name(), result.Success, result.Method, result.Duration)

	// Handle three cases: success, fallback success, or failure
	if !result.Success {
		// Complete failure
		n.logger.Warning("%s: failure reported", n.notifier.Name())
		if result.Error != nil {
			n.logger.Debug("  Error: %v", result.Error)
		}
	} else if result.UsedFallback {
		// Fallback succeeded after primary method failed
		n.logger.Warning("⚠️ %s: sent via fallback in %v", n.notifier.Name(), result.Duration)
		if result.Error != nil {
			n.logger.Debug("  Primary method failed: %v", result.Error)
		}
	} else {
		// Primary method succeeded (notification pipeline completed)
		n.logger.Info("✓ %s: notification completed successfully (took %v)", n.notifier.Name(), result.Duration)
	}

	n.recordNotifierStatus(stats, result)
	return nil
}

// convertBackupStatsToNotificationData converts orchestrator BackupStats to notify.NotificationData
func (n *NotificationAdapter) convertBackupStatsToNotificationData(stats *BackupStats) *notify.NotificationData {
	// Determine overall status based on ExitCode
	var status notify.NotificationStatus
	var statusMessage string

	switch stats.ExitCode {
	case 0:
		status = notify.StatusSuccess
		statusMessage = "Backup completed successfully"
	case 1:
		status = notify.StatusWarning
		statusMessage = "Backup completed with warnings"
	default:
		status = notify.StatusFailure
		statusMessage = "Backup failed"
	}

		// Determine storage statuses
		localStatus := strings.TrimSpace(stats.LocalStatus)
		if localStatus == "" {
			switch notify.StatusFromExitCode(stats.ExitCode) {
			case notify.StatusSuccess:
				localStatus = "ok"
			case notify.StatusWarning:
				localStatus = "warning"
			default:
				localStatus = "error"
			}
	}

	secondaryStatus := strings.TrimSpace(stats.SecondaryStatus)
	if secondaryStatus == "" {
		if !stats.SecondaryEnabled {
			secondaryStatus = "disabled"
		} else {
			secondaryStatus = "ok"
		}
	}

	cloudStatus := strings.TrimSpace(stats.CloudStatus)
	if cloudStatus == "" {
		if !stats.CloudEnabled {
			cloudStatus = "disabled"
		} else {
			cloudStatus = "ok"
		}
	}

	localStatusSummary := formatBackupStatusSummary(stats.LocalRetentionPolicy, stats.LocalBackups, stats.MaxLocalBackups)
	secondaryStatusSummary := formatBackupStatusSummary(stats.SecondaryRetentionPolicy, stats.SecondaryBackups, stats.MaxSecondaryBackups)
	cloudStatusSummary := formatBackupStatusSummary(stats.CloudRetentionPolicy, stats.CloudBackups, stats.MaxCloudBackups)

	// Email/Telegram status summaries
	emailStatus := stats.EmailStatus
	if emailStatus == "" {
		emailStatus = "disabled"
	}
	telegramStatus := stats.TelegramStatus
	if telegramStatus == "" {
		telegramStatus = "N/A"
	}

	// Use precomputed compression ratio (savings %) if available
	compressionRatio := stats.CompressionSavingsPercent
	if compressionRatio <= 0 && stats.UncompressedSize > 0 {
		compressionRatio = (1.0 - float64(stats.CompressedSize)/float64(stats.UncompressedSize)) * 100.0
	}

	localFree := formatBytesHR(stats.LocalFreeSpace)
	localUsed := formatBytesHR(calculateUsedBytes(stats.LocalFreeSpace, stats.LocalTotalSpace))
	localPercent := formatPercentString(calculateUsagePercent(stats.LocalFreeSpace, stats.LocalTotalSpace))

	secondaryFree := ""
	secondaryUsed := ""
	secondaryPercent := ""
	if stats.SecondaryEnabled {
		secondaryFree = formatBytesHR(stats.SecondaryFreeSpace)
		secondaryUsed = formatBytesHR(calculateUsedBytes(stats.SecondaryFreeSpace, stats.SecondaryTotalSpace))
		secondaryPercent = formatPercentString(calculateUsagePercent(stats.SecondaryFreeSpace, stats.SecondaryTotalSpace))
	}

	// Parse log file for categories - use ParseLogCounts as primary source
	logCategories, logErrors, logWarnings := ParseLogCounts(stats.LogFilePath, 10)

	// Use parsed counts if available, otherwise fall back to stats
	errorCount := stats.ErrorCount
	warningCount := stats.WarningCount
	if logErrors > 0 || logWarnings > 0 {
		errorCount = logErrors
		warningCount = logWarnings
	}

	totalIssues := errorCount + warningCount

	// Extract filename from full path for email display
	backupFileName := stats.ArchivePath
	if lastSlash := strings.LastIndex(stats.ArchivePath, "/"); lastSlash >= 0 {
		backupFileName = stats.ArchivePath[lastSlash+1:]
	}

	return &notify.NotificationData{
		Status:        status,
		StatusMessage: statusMessage,
		ExitCode:      stats.ExitCode,

		Hostname:    stats.Hostname,
		ProxmoxType: stats.ProxmoxType,
		ServerID:    stats.ServerID,
		ServerMAC:   stats.ServerMAC,

		BackupDate:     stats.Timestamp,
		BackupDuration: stats.Duration,
		BackupFile:     stats.ArchivePath,
		BackupFileName: backupFileName,
		BackupSize:     stats.CompressedSize,
		BackupSizeHR:   formatBytes(stats.ArchiveSize), // Use ArchiveSize from stats

		CompressionType:  stats.Compression.String(),
		CompressionLevel: stats.CompressionLevel,
		CompressionMode:  stats.CompressionMode,
		CompressionRatio: compressionRatio,

		FilesIncluded: stats.FilesIncluded,
		FilesMissing:  stats.FilesMissing,

		LocalStatus:        localStatus,
		LocalStatusSummary: localStatusSummary,
		LocalCount:         stats.LocalBackups,
		LocalFree:          localFree,
		LocalUsed:          localUsed,
		LocalPercent:       localPercent,
		LocalSpaceBytes:    stats.LocalFreeSpace,
		LocalUsagePercent:  calculateUsagePercent(stats.LocalFreeSpace, stats.LocalTotalSpace),

		// Local retention info
		LocalRetentionPolicy:   stats.LocalRetentionPolicy,
		LocalRetentionLimit:    stats.MaxLocalBackups,
		LocalGFSDaily:          stats.LocalGFSDaily,
		LocalGFSWeekly:         stats.LocalGFSWeekly,
		LocalGFSMonthly:        stats.LocalGFSMonthly,
		LocalGFSYearly:         stats.LocalGFSYearly,
		LocalGFSCurrentDaily:   stats.LocalGFSCurrentDaily,
		LocalGFSCurrentWeekly:  stats.LocalGFSCurrentWeekly,
		LocalGFSCurrentMonthly: stats.LocalGFSCurrentMonthly,
		LocalGFSCurrentYearly:  stats.LocalGFSCurrentYearly,
		LocalBackups:           stats.LocalBackups,

		SecondaryEnabled:       stats.SecondaryEnabled,
		SecondaryStatus:        secondaryStatus,
		SecondaryStatusSummary: secondaryStatusSummary,
		SecondaryCount:         stats.SecondaryBackups,
		SecondaryFree:          secondaryFree,
		SecondaryUsed:          secondaryUsed,
		SecondaryPercent:       secondaryPercent,
		SecondarySpaceBytes:    stats.SecondaryFreeSpace,
		SecondaryUsagePercent:  calculateUsagePercent(stats.SecondaryFreeSpace, stats.SecondaryTotalSpace),

		// Secondary retention info
		SecondaryRetentionPolicy:   stats.SecondaryRetentionPolicy,
		SecondaryRetentionLimit:    stats.MaxSecondaryBackups,
		SecondaryGFSDaily:          stats.SecondaryGFSDaily,
		SecondaryGFSWeekly:         stats.SecondaryGFSWeekly,
		SecondaryGFSMonthly:        stats.SecondaryGFSMonthly,
		SecondaryGFSYearly:         stats.SecondaryGFSYearly,
		SecondaryGFSCurrentDaily:   stats.SecondaryGFSCurrentDaily,
		SecondaryGFSCurrentWeekly:  stats.SecondaryGFSCurrentWeekly,
		SecondaryGFSCurrentMonthly: stats.SecondaryGFSCurrentMonthly,
		SecondaryGFSCurrentYearly:  stats.SecondaryGFSCurrentYearly,
		SecondaryBackups:           stats.SecondaryBackups,

		CloudEnabled:       stats.CloudEnabled,
		CloudStatus:        cloudStatus,
		CloudStatusSummary: cloudStatusSummary,
		CloudCount:         stats.CloudBackups,

		// Cloud retention info
		CloudRetentionPolicy:   stats.CloudRetentionPolicy,
		CloudRetentionLimit:    stats.MaxCloudBackups,
		CloudGFSDaily:          stats.CloudGFSDaily,
		CloudGFSWeekly:         stats.CloudGFSWeekly,
		CloudGFSMonthly:        stats.CloudGFSMonthly,
		CloudGFSYearly:         stats.CloudGFSYearly,
		CloudGFSCurrentDaily:   stats.CloudGFSCurrentDaily,
		CloudGFSCurrentWeekly:  stats.CloudGFSCurrentWeekly,
		CloudGFSCurrentMonthly: stats.CloudGFSCurrentMonthly,
		CloudGFSCurrentYearly:  stats.CloudGFSCurrentYearly,
		CloudBackups:           stats.CloudBackups,

		EmailStatus:    emailStatus,
		TelegramStatus: telegramStatus,

		LocalPath:     stats.LocalPath,
		SecondaryPath: stats.SecondaryPath,
		CloudPath:     stats.CloudPath,

		ErrorCount:    errorCount,
		WarningCount:  warningCount,
		LogFilePath:   stats.LogFilePath,
		TotalIssues:   totalIssues,
		LogCategories: logCategories,

		ScriptVersion: stats.ScriptVersion,
	}
}

// formatBytesHR formats bytes in human-readable format (using uint64)
func formatBytesHR(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	val := float64(bytes) / float64(div)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.2f %s", val, units[exp])
}

// calculateUsagePercent calculates the usage percentage
func calculateUsagePercent(freeBytes, totalBytes uint64) float64 {
	if totalBytes == 0 {
		return 0.0
	}
	usedBytes := totalBytes - freeBytes
	return (float64(usedBytes) / float64(totalBytes)) * 100.0
}

func calculateUsedBytes(freeBytes, totalBytes uint64) uint64 {
	if totalBytes == 0 || totalBytes <= freeBytes {
		return 0
	}
	return totalBytes - freeBytes
}

func formatPercentString(percent float64) string {
	if percent <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", percent)
}

func formatBackupStatusSummary(policy string, count, max int) string {
	// GFS mode: show X/- (no fixed limit)
	if policy == "gfs" {
		return fmt.Sprintf("%d/-", count)
	}

	// Simple mode: show X/Y or X/?
	if max <= 0 {
		if count <= 0 {
			return "0/?"
		}
		return fmt.Sprintf("%d/?", count)
	}
	return fmt.Sprintf("%d/%d", count, max)
}

func (n *NotificationAdapter) recordNotifierStatus(stats *BackupStats, result *notify.NotificationResult) {
	if stats == nil {
		return
	}

	switch n.notifier.Name() {
	case "Telegram":
		base := strings.TrimSpace(stats.TelegramStatus)
		if base == "" {
			base = "unknown"
		}
		statusDetail := describeNotificationResult(result)
		if statusDetail != "" {
			stats.TelegramStatus = fmt.Sprintf("%s (%s)", base, statusDetail)
		} else {
			stats.TelegramStatus = base
		}
	case "Email":
		stats.EmailStatus = describeNotificationSeverity(result)
	}
}

func describeNotificationResult(result *notify.NotificationResult) string {
	if result == nil {
		return "unknown"
	}
	if !result.Success {
		if result.Error != nil {
			return fmt.Sprintf("failed: %v", result.Error)
		}
		return "failed"
	}
	if result.UsedFallback {
		if result.Method != "" {
			return fmt.Sprintf("sent via %s fallback", result.Method)
		}
		return "sent via fallback"
	}
	if result.Method != "" {
		return fmt.Sprintf("sent (%s)", result.Method)
	}
	return "sent"
}

func describeNotificationSeverity(result *notify.NotificationResult) string {
	if result == nil {
		return "disabled"
	}
	if !result.Success {
		return "error"
	}
	if result.UsedFallback {
		return "warning"
	}
	return "ok"
}
