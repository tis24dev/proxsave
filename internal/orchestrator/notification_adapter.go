package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

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

func (n *NotificationAdapter) Name() string {
	return n.notifier.Name()
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

	// The Telegram relay path prints its own two-line result (server acceptance +
	// real Telegram delivery); everything else keeps the generic single line.
	if n.notifier.Name() == "Telegram" && n.logTelegramOutcome(result) {
		// handled by the relay-aware two-line logger
	} else if !result.Success {
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

	n.warnOnInconsistentUsageStats("local", stats.LocalUsedSpace, stats.LocalTotalSpace)
	localFree := formatBytesHR(stats.LocalFreeSpace)
	localUsed := formatBytesHR(stats.LocalUsedSpace)
	localPercent := formatPercentString(calculateUsagePercent(stats.LocalUsedSpace, stats.LocalTotalSpace))

	secondaryFree := ""
	secondaryUsed := ""
	secondaryPercent := ""
	if stats.SecondaryEnabled {
		n.warnOnInconsistentUsageStats("secondary", stats.SecondaryUsedSpace, stats.SecondaryTotalSpace)
		secondaryFree = formatBytesHR(stats.SecondaryFreeSpace)
		secondaryUsed = formatBytesHR(stats.SecondaryUsedSpace)
		secondaryPercent = formatPercentString(calculateUsagePercent(stats.SecondaryUsedSpace, stats.SecondaryTotalSpace))
	}

	// Issue counts and categories are snapshotted immediately before the
	// notification group starts, so all notifiers see the same pre-notification
	// totals. Re-parsing the log here per-notifier would over-count warnings
	// emitted by earlier notifiers in the dispatch chain.
	errorCount := stats.ErrorCount
	warningCount := stats.WarningCount
	logCategories := append([]notify.LogCategory(nil), stats.LogCategories...)
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
		LocalUsagePercent:  calculateUsagePercent(stats.LocalUsedSpace, stats.LocalTotalSpace),

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
		SecondaryUsagePercent:  calculateUsagePercent(stats.SecondaryUsedSpace, stats.SecondaryTotalSpace),

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

		ScriptVersion:       stats.ScriptVersion,
		NewVersionAvailable: stats.NewVersionAvailable,
		CurrentVersion:      stats.CurrentVersion,
		LatestVersion:       stats.LatestVersion,
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

// calculateUsagePercent calculates the usage percentage from used and total bytes.
func calculateUsagePercent(usedBytes, totalBytes uint64) float64 {
	if totalBytes == 0 {
		return 0.0
	}
	if usedBytes >= totalBytes {
		return 100.0
	}
	return (float64(usedBytes) / float64(totalBytes)) * 100.0
}

func (n *NotificationAdapter) warnOnInconsistentUsageStats(location string, usedBytes, totalBytes uint64) {
	if n == nil || n.logger == nil {
		return
	}
	if totalBytes == 0 {
		if usedBytes > 0 {
			n.logger.Warning("%s storage usage stats inconsistent: used=%d total=%d; reporting 0%% usage for display because total capacity is unknown", location, usedBytes, totalBytes)
		}
		return
	}
	if usedBytes <= totalBytes {
		return
	}
	n.logger.Warning("%s storage usage stats inconsistent: used=%d total=%d; clamping percentage to 100%% for display", location, usedBytes, totalBytes)
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
		if sub := telegramDeliverySubstate(result); sub != "" {
			statusDetail = statusDetail + ", " + sub
		}
		if statusDetail != "" {
			stats.TelegramStatus = fmt.Sprintf("%s (%s)", base, statusDetail)
		} else {
			stats.TelegramStatus = base
		}
		// Dual-write (S3): the relay may have piggybacked a fresh portal magic-link on
		// the /api/notify response. Stash it RAW for the S4 healthchecks section; it is
		// sanitized (serverbot.SanitizeLoginURL) only at that display boundary.
		if result != nil && result.Metadata != nil {
			if link, ok := result.Metadata["login_url"].(string); ok && link != "" {
				stats.HealthcheckLink = link
			}
		}
	case "Email":
		stats.EmailStatus = describeNotificationSeverity(result)
	}

	// Capture EVERY channel's outcome (incl. Gotify/Webhook, which have no legacy status
	// field) as a severity keyed by display name, for the per-channel healthchecks sensors
	// the daemon pings (Fase 2B / R4). describeNotificationSeverity is channel-agnostic.
	setNotifyResult(stats, n.notifier.Name(), describeNotificationSeverity(result))
}

// setNotifyResult records channel -> severity into stats.NotifyResults, lazily allocating
// the map. Safe on a nil stats / empty name.
func setNotifyResult(stats *BackupStats, name, severity string) {
	if stats == nil || name == "" {
		return
	}
	if stats.NotifyResults == nil {
		stats.NotifyResults = make(map[string]string)
	}
	stats.NotifyResults[name] = severity
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

// logTelegramOutcome prints the two-response result for the Telegram RELAY path:
// line 1 = accepted by the ProxSave server, line 2 = delivered to Telegram. It
// returns false (logging nothing) when the notification did NOT go through the relay
// (personal mode / legacy direct bot-token send), so the caller falls back to the
// generic single line. Every second-line non-success is WARNING level with a ❌/⚠️
// in the text: the delivery outcome must be VISIBLE but must never block the backup
// (the exit code is frozen before the notification phase; a WARNING is counted as a
// warning, never an error, in the run summary).
func (n *NotificationAdapter) logTelegramOutcome(result *notify.NotificationResult) bool {
	if result == nil || result.Metadata == nil {
		return false
	}
	accVal, ok := result.Metadata["relay_accepted"]
	if !ok {
		return false // no relay path involved -> generic single line
	}
	name := n.notifier.Name()
	accepted, _ := accVal.(bool)

	// First response: did the ProxSave server accept it?
	if !accepted {
		if result.Error != nil {
			n.logger.Warning("❌ %s: could not send to ProxSave server: %v", name, result.Error)
		} else {
			n.logger.Warning("❌ %s: could not send to ProxSave server", name)
		}
		return true // no second line: nothing was accepted
	}
	// First-line latency is the time to server ACCEPTANCE (recorded before any poll),
	// falling back to the total duration if absent.
	acceptDur := result.Duration
	if d, ok := result.Metadata["relay_accept_duration"].(time.Duration); ok {
		acceptDur = d
	}
	n.logger.Info("✓ %s: sent to ProxSave server (in %v)", name, acceptDur)

	// Second response: did Telegram confirm delivery?
	state, _ := result.Metadata["telegram_state"].(string)
	switch state {
	case "delivered":
		if id, ok := result.Metadata["telegram_message_id"].(int64); ok && id != 0 {
			n.logger.Debug("Telegram: message_id=%d", id)
		}
		n.logger.Info("✓ %s: delivered to Telegram", name)
	case "failed":
		reason, _ := result.Metadata["telegram_reason"].(string)
		n.logger.Warning("❌ %s: not delivered (%s)", name, mapTelegramReason(reason))
	case "pending":
		n.logger.Warning("⚠️ %s: accepted; delivery in progress (auto-retry)", name)
	case "unconfirmed":
		// Confirmation disabled by config: accepted is enough, stay quiet.
		n.logger.Debug("Telegram: delivery confirmation disabled (accepted by server)")
	default: // "unknown" or missing
		n.logger.Warning("⚠️ %s: accepted; delivery not confirmed", name)
	}
	return true
}

// mapTelegramReason turns a server-side outbox reason into user-facing copy.
func mapTelegramReason(reason string) string {
	switch {
	case reason == "":
		return "rejected by Telegram"
	case reason == "http_403":
		return "bot blocked by the user"
	case reason == "http_400", reason == "http_404":
		return "invalid chat"
	case reason == "http_413":
		return "message too long"
	case strings.HasPrefix(reason, "gave_up_after_ttl"):
		return "Telegram unreachable too long (expired)"
	case strings.HasPrefix(reason, "gave_up_after_"):
		return "too many failed attempts"
	default:
		return reason
	}
}

// telegramDeliverySubstate is the short delivery tag appended to stats.TelegramStatus
// (which is echoed into the notification body). Empty for the non-relay path.
func telegramDeliverySubstate(result *notify.NotificationResult) string {
	if result == nil || result.Metadata == nil {
		return ""
	}
	// Gate on the VALUE, not mere presence: a relay POST that failed sets
	// relay_accepted=false with no telegram_state and must NOT be labelled
	// "delivery unconfirmed" (it was never accepted).
	if accepted, _ := result.Metadata["relay_accepted"].(bool); !accepted {
		return ""
	}
	switch s, _ := result.Metadata["telegram_state"].(string); s {
	case "delivered":
		return "delivered"
	case "failed":
		reason, _ := result.Metadata["telegram_reason"].(string)
		return "not delivered: " + mapTelegramReason(reason)
	case "pending":
		return "queued"
	case "unconfirmed":
		return "" // confirmation disabled -> no substate noise in the body
	default:
		return "delivery unconfirmed"
	}
}
