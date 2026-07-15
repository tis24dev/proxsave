package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// fsIoTimeout converts the configured FS_IO_TIMEOUT into a per-operation safefs
// budget. A non-positive value (the explicit FS_IO_TIMEOUT=0 opt-out, or an unset
// cfg) yields 0, which safefs treats as unbounded (legacy behaviour).
func (o *Orchestrator) fsIoTimeout() time.Duration {
	if o == nil || o.cfg == nil || o.cfg.FsIoTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(o.cfg.FsIoTimeoutSeconds) * time.Second
}

// boundedCopyFile copies src to dest, bounding the whole open(src)+open(dest)+
// io.Copy+Sync as a single unit via safefs so a dead/stale mount on EITHER endpoint
// cannot wedge the caller. It delegates to the shared copyFile (preserving the FS
// seam) and leaves that generic helper untouched. timeout<=0 means unbounded
// (FS_IO_TIMEOUT=0 opt-out). On timeout safefs abandons the worker goroutine (the
// kernel call is not cancelled; fds are reclaimed at process exit) and returns a
// *safefs.TimeoutError wrapping safefs.ErrTimeout. Intended for best-effort
// shipping of the (small) run log, not for completeness-critical restore copies.
func boundedCopyFile(ctx context.Context, fs FS, src, dest string, timeout time.Duration) error {
	if timeout <= 0 {
		return copyFile(fs, src, dest)
	}
	_, err := safefs.Run(ctx, "logcopy", dest, timeout, func() (struct{}, error) {
		return struct{}{}, copyFile(fs, src, dest)
	})
	return err
}

// StorageTarget represents an external destination (e.g., secondary storage, cloud).
type StorageTarget interface {
	Sync(ctx context.Context, stats *BackupStats) error
}

// NotificationChannel represents a notification channel (e.g., Telegram, email).
type NotificationChannel interface {
	Name() string
	Notify(ctx context.Context, stats *BackupStats) error
}

// RegisterStorageTarget adds a destination to run after the backup.
func (o *Orchestrator) RegisterStorageTarget(target StorageTarget) {
	if target == nil {
		return
	}
	o.storageTargets = append(o.storageTargets, target)
}

// RegisterNotificationChannel adds a notification channel to run after the backup.
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

	// The whole notification subsystem (notifiers, adapter, serverbot client) shares
	// this one logger instance. Bracket the sequential dispatch so any error/critical
	// it logs is reclassified as a NOTIFY-ERR (display-error, warning-weight): a channel
	// outage stays warning, never escalates the run to error. One governed boundary
	// instead of patching each notifier's ~dozen error sites.
	o.logger.EnterNotifyErrorScope()
	defer o.logger.ExitNotifyErrorScope()

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
		// R3 (Fase 1): Healthchecks stays LAST in this dispatch order; do not reorder it
		// without re-checking the magic-link capture + outcome semantics below.
		// Always-visible healthchecks status; LAST so the Telegram relay above has
		// already captured any portal magic-link onto stats.HealthcheckLink. Gated on
		// HEALTHCHECK_ENABLED (independent of Telegram). Same const as Name() so it is
		// dispatched exactly once.
		{name: healthchecksSectionName, enabled: cfg != nil && cfg.HealthcheckEnabled},
	}

	channelsByName := make(map[string]NotificationChannel, len(o.notificationChannels))
	for _, ch := range o.notificationChannels {
		if ch == nil {
			continue
		}
		name := strings.TrimSpace(ch.Name())
		if name == "" {
			continue
		}
		channelsByName[name] = ch
	}

	usedChannels := make(map[NotificationChannel]bool, len(o.notificationChannels))

	for _, entry := range entries {
		if !entry.enabled {
			o.logger.Skip("%s: disabled", entry.name)
			continue
		}

		channel, ok := channelsByName[entry.name]
		if !ok || channel == nil {
			if entry.name == "Email" && cfg != nil {
				method := strings.TrimSpace(cfg.EmailDeliveryMethod)
				if method == "" {
					method = "relay"
				}
				o.logger.Warning("%s: enabled but not initialized (EMAIL_DELIVERY_METHOD=%q; allowed: relay|sendmail|pmf)", entry.name, method)

				if stats != nil && strings.TrimSpace(stats.EmailStatus) == "" {
					stats.EmailStatus = "error"
				}
			} else {
				o.logger.Warning("%s: enabled but not initialized", entry.name)
			}
			// An enabled channel that failed to initialize sent nothing: record an "error"
			// outcome so its per-channel healthchecks sensor goes DOWN instead of silently
			// absent. The Healthchecks section entry is a reporting surface, not a notify
			// channel, so it never gets a per-channel result.
			if entry.name != healthchecksSectionName {
				setNotifyResult(stats, entry.name, "error")
			}
			continue
		}

		usedChannels[channel] = true
		_ = channel.Notify(ctx, stats) // Ignore errors - notifications are non-critical
	}

	// Dispatch any remaining channels (custom or future ones) that weren't part of the fixed list above.
	for _, ch := range o.notificationChannels {
		if ch == nil || usedChannels[ch] {
			continue
		}
		_ = ch.Notify(ctx, stats)
	}

	// Hand the per-channel outcomes to the daemon (Fase 2B): env-gated, so only the daemon's
	// supervised child (which has EnvRunID set) writes; a bare `proxsave --backup` no-ops.
	o.persistNotifyResults(stats)
}

// persistNotifyResults writes the per-channel notify outcomes to the handoff file the daemon
// reads after the child exits, to ping one healthchecks check per channel. Best-effort and
// env-gated on EnvRunID (set only by the daemon on its child), so a non-daemon run leaves no
// stray file. Writes an empty results object when nothing was recorded so the daemon can tell
// "child ran, nothing to report" from "child crashed" (a missing/stale file).
func (o *Orchestrator) persistNotifyResults(stats *BackupStats) {
	if o.cfg == nil || strings.TrimSpace(o.cfg.BaseDir) == "" || stats == nil {
		return
	}
	rid := strings.TrimSpace(os.Getenv(health.EnvRunID))
	if rid == "" {
		return
	}
	if err := health.WriteNotifyResults(o.cfg.BaseDir, rid, time.Now().Unix(), stats.NotifyResults); err != nil {
		o.logger.Debug("notify results handoff write failed: %v", err)
	}
}

// startNotificationGroup is the notification boundary. Keep the issue snapshot
// immediately adjacent to dispatchNotifications; no logging belongs between them.
func (o *Orchestrator) startNotificationGroup(ctx context.Context, stats *BackupStats) {
	if o == nil {
		return
	}
	o.snapshotPreNotificationIssues(stats)
	applyIssueExitCode(stats)
	o.dispatchNotifications(ctx, stats)
}

func (o *Orchestrator) snapshotPreNotificationIssues(stats *BackupStats) {
	o.refreshLogIssuesFromFile(stats, true)
}

func (o *Orchestrator) refreshLogIssuesFromFile(stats *BackupStats, includeCategories bool) {
	if stats == nil || strings.TrimSpace(stats.LogFilePath) == "" {
		return
	}

	categoryLimit := 0
	if includeCategories {
		categoryLimit = 10
	}
	categories, errorCount, warningCount, notifyCount := ParseLogCounts(stats.LogFilePath, categoryLimit)
	stats.ErrorCount = errorCount
	stats.WarningCount = warningCount
	stats.NotifyCount = notifyCount
	if includeCategories {
		stats.LogCategories = categories
	} else {
		stats.LogCategories = nil
	}
}

func applyIssueExitCode(stats *BackupStats) {
	if stats == nil {
		return
	}

	if stats.ErrorCount > 0 {
		if stats.ExitCode == types.ExitSuccess.Int() || stats.ExitCode == types.ExitGenericError.Int() {
			stats.ExitCode = types.ExitBackupError.Int()
		}
		return
	}

	// Warnings and notify (notification/communication) issues are warning-weight:
	// they promote a clean run to the generic exit code but never to backup error.
	// The error branch above already returned for real errors, so a notify issue can
	// never mask one.
	if (stats.WarningCount > 0 || stats.NotifyCount > 0) && stats.ExitCode == types.ExitSuccess.Int() {
		stats.ExitCode = types.ExitGenericError.Int()
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
		Failed:       true, // an early-init failure IS a failed run (status=error)
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
		stats.ScriptVersion = o.version
	}
	if o.envInfo != nil {
		stats.ProxmoxType = o.envInfo.Type
		stats.ProxmoxTargets = append([]string(nil), o.envInfo.Type.Targets()...)
		stats.ProxmoxVersion = o.envInfo.Version
		stats.PVEVersion = o.envInfo.PVEVersion
		stats.PBSVersion = o.envInfo.PBSVersion
	}
	if o.proxmoxVersion != "" {
		stats.ProxmoxVersion = o.proxmoxVersion
	}
	stats.ServerID = strings.TrimSpace(o.serverID)
	stats.ServerMAC = strings.TrimSpace(o.serverMAC)

	// Set log file path if logger has one
	if logPath := o.logger.GetLogFilePath(); logPath != "" {
		stats.LogFilePath = logPath
	}

	// Export a Prometheus "fail" metric (status=error) so textfile-based alerting
	// fires on early-init failures too; otherwise the textfile keeps the last
	// successful run's metrics. Self-gated by MetricsEnabled && !dryRun.
	if o.shouldExportBackupMetrics(stats) {
		o.ensureBackupStatsTiming(stats)
		o.exportPrometheusBackupMetrics(stats)
	}

	// Honor dry-run like the normal finalize path: never send real notifications.
	if o.dryRun {
		o.logger.Info("[DRY RUN] Would send early-error notification: %s", stats.LocalStatusSummary)
		return stats
	}

	// Dispatch notifications with minimal stats. Early errors are already
	// represented in stats and may not be present in the log file yet.
	o.dispatchNotifications(ctx, stats)

	return stats
}

// dispatchNotificationsAndLogs runs the notification phase and log-file management.
// It is used both on success and on error paths, so notifications still go out
// and the log is always closed/rotated.
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
	o.startNotificationGroup(ctx, stats)
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

	// All log-dispatch FS ops are bounded by FS_IO_TIMEOUT so a dead/stale mount on
	// the source (LOG_PATH), the secondary destination (SecondaryLogPath), or the
	// cloud local source cannot wedge the finalize in an uninterruptible (D-state)
	// syscall. On timeout: warn and skip that destination (best-effort). Background
	// ctx (not the run ctx): the log must ship at shutdown even when the run was
	// cancelled (Ctrl+C); FS_IO_TIMEOUT alone bounds it.
	timeout := o.fsIoTimeout()

	// Copy to secondary storage. Bound the dir-create and the copy: both the source
	// read (LOG_PATH) and the destination write (SecondaryLogPath) may be dead mounts.
	if o.cfg.SecondaryEnabled && o.cfg.SecondaryLogPath != "" {
		secondaryLogPath := filepath.Join(o.cfg.SecondaryLogPath, logFileName)
		o.logger.Debug("Copying log to secondary: %s", secondaryLogPath)

		_, mkErr := safefs.Run(context.Background(), "logmkdir", o.cfg.SecondaryLogPath, timeout, func() (struct{}, error) {
			return struct{}{}, fs.MkdirAll(o.cfg.SecondaryLogPath, 0755)
		})
		switch {
		case mkErr != nil && errors.Is(mkErr, safefs.ErrTimeout):
			o.logger.Warning("Skipping secondary log copy: creating %s timed out after %s (dead/stale mount?)", o.cfg.SecondaryLogPath, timeout)
		case mkErr != nil:
			o.logger.Warning("Failed to create secondary log directory: %v", mkErr)
		default:
			switch err := boundedCopyFile(context.Background(), fs, logFilePath, secondaryLogPath, timeout); {
			case err == nil:
				o.logger.Info("✓ Log copied to secondary: %s", secondaryLogPath)
			case errors.Is(err, safefs.ErrTimeout):
				o.logger.Warning("Skipping secondary log copy: copy to %s timed out after %s (dead/stale mount?)", secondaryLogPath, timeout)
			default:
				o.logger.Warning("Failed to copy log to secondary: %v", err)
			}
		}
	}

	// Copy to cloud storage. rclone reads the LOCAL source log; a dead/stale LOG_PATH
	// mount can wedge that read in an uninterruptible syscall that rclone's own
	// --timeout (remote IO) and the deadline-less ctx do not bound. Probe the source
	// with a bounded stat first and skip the cloud copy if it is unreachable.
	if o.cfg.CloudEnabled {
		if cloudBase := strings.TrimSpace(o.cfg.CloudLogPath); cloudBase != "" {
			destination := buildCloudLogDestination(cloudBase, logFileName, o.cfg.CloudRemote)

			_, probeErr := safefs.Run(context.Background(), "logstat", logFilePath, timeout, func() (struct{}, error) {
				_, e := fs.Stat(logFilePath)
				return struct{}{}, e
			})
			switch {
			case probeErr != nil && errors.Is(probeErr, safefs.ErrTimeout):
				o.logger.Warning("Skipping cloud log copy: source log %s unreachable after %s (dead/stale mount?)", logFilePath, timeout)
			case probeErr != nil:
				o.logger.Warning("Skipping cloud log copy: cannot stat source log %s: %v", logFilePath, probeErr)
			default:
				o.logger.Debug("Copying log to cloud: %s", destination)
				// Detach the upload from the (possibly cancelled) run ctx: like the
				// secondary copy above, the log must still ship at shutdown after a
				// Ctrl+C. UploadToRemotePath bounds a deadline-less ctx itself, so a
				// stalled rclone cannot hang here.
				upload := o.copyLogToCloud
				if o.copyLogToCloudFn != nil {
					upload = o.copyLogToCloudFn
				}
				if err := upload(context.Background(), logFilePath, destination); err != nil {
					o.logger.Warning("Failed to copy log to cloud: %v", err)
				} else {
					o.logger.Info("✓ Log copied to cloud: %s", destination)
				}
			}
		}
	}

	return nil
}

// resolveCloudPath normalizes a cloud path by prepending the remote name if not present.
// Supports both new style (/path) and legacy style (remote:/path).
func resolveCloudPath(path, cloudRemote string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	// If it already contains ":", it's a legacy full path - use as-is
	if strings.Contains(path, ":") {
		return path
	}
	// Otherwise, use CLOUD_REMOTE
	remote := strings.TrimSpace(cloudRemote)
	if remote == "" {
		return path // fallback, shouldn't happen
	}
	// Extract just the remote name (without any legacy path component)
	if idx := strings.Index(remote, ":"); idx != -1 {
		remote = remote[:idx]
	}
	// Keep the path as-is (including leading slash if present)
	return remote + ":" + path
}

// copyLogToCloud copies a log file to cloud storage using rclone
func (o *Orchestrator) copyLogToCloud(ctx context.Context, sourcePath, destPath string) error {
	// Normalize path using CLOUD_REMOTE if needed
	destPath = resolveCloudPath(destPath, o.cfg.CloudRemote)

	if !strings.Contains(destPath, ":") {
		return fmt.Errorf("CLOUD_LOG_PATH requires CLOUD_REMOTE to be set: %s", destPath)
	}

	client, err := storage.NewCloudStorage(o.cfg, o.logger)
	if err != nil {
		return fmt.Errorf("failed to initialize cloud storage: %w", err)
	}

	return client.UploadToRemotePath(ctx, sourcePath, destPath, true)
}

func buildCloudLogDestination(basePath, fileName, cloudRemote string) string {
	// Normalize path using cloudRemote if basePath doesn't contain ":"
	base := resolveCloudPath(basePath, cloudRemote)
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
