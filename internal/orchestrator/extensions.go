package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// StorageTarget rappresenta una destinazione esterna (es. storage secondario, cloud).
type StorageTarget interface {
	Sync(ctx context.Context, stats *BackupStats) error
}

// NotificationChannel rappresenta un canale di notifica (es. Telegram, email).
type NotificationChannel interface {
	Notify(ctx context.Context, stats *BackupStats) error
}

// RegisterStorageTarget aggiunge una destinazione da eseguire dopo il backup.
func (o *Orchestrator) RegisterStorageTarget(target StorageTarget) {
	if target == nil {
		return
	}
	o.storageTargets = append(o.storageTargets, target)
}

// RegisterNotificationChannel aggiunge un canale di notifica da eseguire dopo il backup.
func (o *Orchestrator) RegisterNotificationChannel(channel NotificationChannel) {
	if channel == nil {
		return
	}
	o.notificationChannels = append(o.notificationChannels, channel)
}

func (o *Orchestrator) dispatchNotifications(ctx context.Context, stats *BackupStats) {
	if o == nil || o.logger == nil {
		return
	}

	type notifierEntry struct {
		name    string
		enabled bool
	}

	cfg := o.cfg

	// If email notifications are disabled in configuration, reflect this explicitly
	// in the aggregated backup stats so that downstream channels (e.g. Telegram)
	// render the Email status as "disabled" (➖) instead of an optimistic default.
	if stats != nil && cfg != nil && !cfg.EmailEnabled {
		stats.EmailStatus = "disabled"
	}

	entries := []notifierEntry{
		{name: "Email", enabled: cfg != nil && cfg.EmailEnabled},
		{name: "Telegram", enabled: cfg != nil && cfg.TelegramEnabled},
		{name: "Gotify", enabled: cfg != nil && cfg.GotifyEnabled},
		{name: "Webhook", enabled: cfg != nil && cfg.WebhookEnabled},
	}

	channelIndex := 0
	nextChannel := func() NotificationChannel {
		if channelIndex >= len(o.notificationChannels) {
			return nil
		}
		ch := o.notificationChannels[channelIndex]
		channelIndex++
		return ch
	}

	for _, entry := range entries {
		if !entry.enabled {
			o.logger.Skip("%s: disabled", entry.name)
			continue
		}
		if channel := nextChannel(); channel != nil {
			_ = channel.Notify(ctx, stats) // Ignore errors - notifications are non-critical
		}
	}

	// Dispatch any remaining channels (custom or future ones)
	for channelIndex < len(o.notificationChannels) {
		if channel := nextChannel(); channel != nil {
			_ = channel.Notify(ctx, stats)
		}
	}
}

// DispatchEarlyErrorNotification sends notifications for errors that occurred before backup started
// This creates a minimal BackupStats with error information for notification purposes
func (o *Orchestrator) DispatchEarlyErrorNotification(ctx context.Context, earlyErr *EarlyErrorState) *BackupStats {
	if o == nil || o.logger == nil || earlyErr == nil || !earlyErr.HasError() {
		return nil
	}

	o.logger.Info("Sending notifications for early error: %s phase", earlyErr.Phase)

	// Get hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
		o.logger.Warning("Failed to get hostname: %v", err)
	}

	// Create minimal stats with error information
	stats := &BackupStats{
		Hostname:     hostname,
		Timestamp:    earlyErr.Timestamp,
		StartTime:    earlyErr.Timestamp,
		EndTime:      earlyErr.Timestamp,
		ExitCode:     earlyErr.ExitCode.Int(),
		ErrorCount:   1,
		WarningCount: 0,
		LocalStatus:  "error",
	}

	phaseLabel := describeEarlyErrorPhase(earlyErr.Phase)
	errorMessage := ""
	if earlyErr.Error != nil {
		errorMessage = earlyErr.Error.Error()
	}
	if phaseLabel != "" && errorMessage != "" {
		stats.LocalStatusSummary = fmt.Sprintf("%s: %s", phaseLabel, errorMessage)
	} else if errorMessage != "" {
		stats.LocalStatusSummary = errorMessage
	} else {
		stats.LocalStatusSummary = "Initialization error"
	}

	// Try to populate version info from orchestrator
	if o.version != "" {
		stats.Version = o.version
	}
	if o.proxmoxVersion != "" {
		stats.ProxmoxVersion = o.proxmoxVersion
	}

	// Set log file path if logger has one
	if logPath := o.logger.GetLogFilePath(); logPath != "" {
		stats.LogFilePath = logPath
	}

	// Dispatch notifications with minimal stats
	o.dispatchNotifications(ctx, stats)

	return stats
}

// dispatchNotificationsAndLogs esegue la fase di notifiche e gestione file di log.
// Viene usata sia nel percorso di successo che in quello di errore, così che
// le notifiche vengano comunque inviate e il log venga sempre chiuso/ruotato.
func (o *Orchestrator) dispatchNotificationsAndLogs(ctx context.Context, stats *BackupStats) {
	if o == nil || o.logger == nil {
		return
	}

	// Log explicit SKIP lines for disabled storage tiers so that
	// Local / Secondary / Cloud all appear grouped with storage operations.
	if o.logger != nil && stats != nil {
		if !stats.SecondaryEnabled {
			o.logger.Skip("Secondary Storage: disabled")
		}
		if !stats.CloudEnabled {
			o.logger.Skip("Cloud Storage: disabled")
		}
	}

	// Phase 2: Notifications (non-critical - failures don't abort backup)
	// Notification errors are logged but never propagated
	fmt.Println()
	o.logStep(7, "Notifications - dispatching channels")
	o.dispatchNotifications(ctx, stats)
}

func (o *Orchestrator) dispatchPostBackup(ctx context.Context, stats *BackupStats) error {
	if o == nil {
		return nil
	}
	// Phase 1: Storage operations (critical - failures abort backup)
	for _, target := range o.storageTargets {
		if err := target.Sync(ctx, stats); err != nil {
			return &BackupError{
				Phase: "storage",
				Err:   fmt.Errorf("storage target failed: %w", err),
				Code:  types.ExitStorageError,
			}
		}
	}

	// Phase 2 + 3: Notifications and log management (non-critical)
	o.FinalizeAfterRun(ctx, stats)
	return nil
}

// FinalizeAfterRun dispatches notifications (when applicable) and ensures the log
// file is closed/copied to the configured destinations. Safe to call multiple times.
func (o *Orchestrator) FinalizeAfterRun(ctx context.Context, stats *BackupStats) {
	if o == nil {
		return
	}

	if !o.dryRun && stats != nil {
		o.dispatchNotificationsAndLogs(ctx, stats)
	}

	o.FinalizeAndCloseLog(ctx)
}

// FinalizeAndCloseLog closes the active log file (if any) and copies it to
// secondary/cloud storage destinations.
func (o *Orchestrator) FinalizeAndCloseLog(ctx context.Context) {
	if o == nil || o.logger == nil {
		return
	}

	logFilePath := o.logger.GetLogFilePath()
	if logFilePath == "" {
		o.logger.Debug("No log file to close (logging to stdout only)")
		return
	}

	fmt.Println()
	o.logStep(8, "Log file management")
	o.logger.Info("Closing log file: %s", logFilePath)
	if err := o.logger.CloseLogFile(); err != nil {
		o.logger.Warning("Failed to close log file: %v", err)
		return
	}
	o.logger.Debug("Log file closed successfully")

	// Copy log to secondary and cloud storage
	if err := o.dispatchLogFile(ctx, logFilePath); err != nil {
		o.logger.Warning("Log file dispatch failed: %v", err)
	}
}

// dispatchLogFile copies the log file to secondary and cloud storage
func (o *Orchestrator) dispatchLogFile(ctx context.Context, logFilePath string) error {
	if o.cfg == nil {
		return nil
	}

	fs := o.filesystem()
	logFileName := filepath.Base(logFilePath)
	o.logger.Info("Dispatching log file: %s", logFileName)

	// Copy to secondary storage
	if o.cfg.SecondaryEnabled && o.cfg.SecondaryLogPath != "" {
		secondaryLogPath := filepath.Join(o.cfg.SecondaryLogPath, logFileName)
		o.logger.Debug("Copying log to secondary: %s", secondaryLogPath)

		if err := fs.MkdirAll(o.cfg.SecondaryLogPath, 0755); err != nil {
			o.logger.Warning("Failed to create secondary log directory: %v", err)
		} else {
			if err := copyFile(fs, logFilePath, secondaryLogPath); err != nil {
				o.logger.Warning("Failed to copy log to secondary: %v", err)
			} else {
				o.logger.Info("✓ Log copied to secondary: %s", secondaryLogPath)
			}
		}
	}

	// Copy to cloud storage
	if o.cfg.CloudEnabled {
		if cloudBase := strings.TrimSpace(o.cfg.CloudLogPath); cloudBase != "" {
			destination := buildCloudLogDestination(cloudBase, logFileName)
			o.logger.Debug("Copying log to cloud: %s", destination)

			if err := o.copyLogToCloud(ctx, logFilePath, destination); err != nil {
				o.logger.Warning("Failed to copy log to cloud: %v", err)
			} else {
				o.logger.Info("✓ Log copied to cloud: %s", destination)
			}
		}
	}

	return nil
}

// copyLogToCloud copies a log file to cloud storage using rclone
func (o *Orchestrator) copyLogToCloud(ctx context.Context, sourcePath, destPath string) error {
	if !strings.Contains(destPath, ":") {
		return fmt.Errorf("cloud log path must include an rclone remote (es. remote:/logs): %s", destPath)
	}

	client, err := storage.NewCloudStorage(o.cfg, o.logger)
	if err != nil {
		return fmt.Errorf("failed to initialize cloud storage: %w", err)
	}

	return client.UploadToRemotePath(ctx, sourcePath, destPath, true)
}

func buildCloudLogDestination(basePath, fileName string) string {
	base := strings.TrimSpace(basePath)
	if base == "" {
		return fileName
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, ":") {
		return base + fileName
	}
	if strings.Contains(base, ":") {
		return base + "/" + fileName
	}
	return filepath.Join(base, fileName)
}

func describeEarlyErrorPhase(phase string) string {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "encryption_setup":
		return "Encryption setup failed"
	case "checker_config":
		return "Checker configuration failed"
	case "storage_init":
		return "Storage initialization failed"
	case "pre_backup_checks":
		return "Pre-backup checks failed"
	default:
		if phase == "" {
			return "Initialization failed"
		}
		return fmt.Sprintf("%s failed", phase)
	}
}
