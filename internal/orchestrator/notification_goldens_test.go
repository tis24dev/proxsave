package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Characterization lock for the inputs of every notification channel and of the
// dashboard outcome screen. Each case builds the run's BackupStats the way a run does
// (InitializeBackupStats, the real StorageAdapter.Sync over fakes that fail on command,
// the real log parse, applyIssueExitCode) and hands it to the real dispatchNotifications,
// where one NotificationAdapter per channel runs the real
// convertBackupStatsToNotificationData. What each channel receives is pinned as JSON,
// in dispatch order, so a channel sent later sees what an earlier one recorded
// (the Email outcome Telegram prints).
//
// The JSON files are the inputs of two consumers that cannot import this package:
// internal/notify (TestNotificationChannelGoldens renders every channel from them) and
// cmd/proxsave (TestBackupOutcomeGoldens renders the dashboard outcome from stats.json
// and outcome.json). These goldens record what the code does TODAY, defects included.
// Regenerate deliberately, this package first:
//
//	go test ./internal/orchestrator/ -run TestNotificationGoldens -update
//	go test ./internal/notify/ -run TestNotificationChannelGoldens -update
//	go test ./cmd/proxsave/ -run TestBackupOutcomeGoldens -update
const notifyGoldenDir = "testdata/notification_goldens"

// notifyGoldenStart is the run's start. UTC, so every rendered date is the same on every
// machine whatever TZ says.
var notifyGoldenStart = time.Date(2026, time.October, 4, 2, 0, 0, 0, time.UTC)

const (
	notifyGoldenHost          = "pve-golden"
	notifyGoldenServerID      = "7302915846102738"
	notifyGoldenMAC           = "bc:24:11:5e:3a:01"
	notifyGoldenVersion       = "0.41.0"
	notifyGoldenLatestVersion = "0.42.0"
	// notifyGoldenRunDuration is how long steps [1]-[5] took; EndTime is the start plus this.
	notifyGoldenRunDuration = 2*time.Minute + 14*time.Second
)

// notifyGoldenLogFile is the run log path a default install writes
// (LOG_PATH=${BASE_DIR}/log, cmd/proxsave/main_runtime.go names the file). The run
// really logs to a temporary file; the stats carry this path whenever a channel or the
// screen can read it.
var notifyGoldenLogFile = "/opt/proxsave/log/backup-" + notifyGoldenHost + "-" + notifyGoldenStart.Format("20060102-150405") + ".log"

// notifyGoldenOutcome is what the dashboard outcome screen is built from besides the
// stats: the exit code runBackupModeSteps returns, the LOG_FILE the runtime exports, and
// whether the result carries the stats (an early error returns none).
type notifyGoldenOutcome struct {
	ExitCode  int    `json:"exit_code"`
	LogFile   string `json:"log_file"`
	WithStats bool   `json:"with_stats"`
}

type notifyGoldenCase struct {
	name string
	// configure turns the default-install configuration into the case's backup.env.
	configure func(cfg *config.Config)
	// startup is what cmd/proxsave does and logs before the orchestrator runs.
	startup func(logger *logging.Logger, cfg *config.Config)
	// The step [6] fakes: the Secondary retention fails, the Cloud copy fails.
	failSecondaryRetention bool
	failCloudStore         bool
	// The step [6] fakes save the archive and not one of its sidecar files: the
	// Secondary copy of a sidecar fails, the Cloud upload of a sidecar fails.
	secondarySidecarNotSaved bool
	cloudSidecarNotSaved     bool
	// runErr stops the run before step [6], with the error RunGoBackup returns.
	runErr error
	// pbs is the result the PBS block writes at step [7] (PBS_TARGET_ENABLED=true).
	pbs *block.Result
	// pbsServer runs the real PBS block, at startup and at step [7], against this server.
	pbsServer *notifyGoldenPBS
	// update is an available update: what checkForUpdates logs and the orchestrator copies.
	update bool
	// offline is a run without network: checkForUpdates could not ask for the latest
	// version, so the stats carry none.
	offline bool
	// emailFallback makes the Email channel report a delivery through the fallback.
	emailFallback bool
	// emailMethod is the delivery method the Email channel reports; empty = the relay.
	emailMethod string
	// early is an initialization failure: DispatchEarlyErrorNotification, no run.
	early    *EarlyErrorState
	wantExit int
}

// notifyGoldenConfig is a default install (internal/config/templates/backup.env) with
// every destination and every channel switched on.
func notifyGoldenConfig() *config.Config {
	return &config.Config{
		BackupPath:             "/opt/proxsave/backup",
		LogPath:                "/opt/proxsave/log",
		BundleAssociatedFiles:  true,
		RetentionPolicy:        "simple",
		LocalRetentionDays:     15,
		SecondaryRetentionDays: 15,
		CloudRetentionDays:     15,
		MaxLocalBackups:        15,
		MaxSecondaryBackups:    15,
		MaxCloudBackups:        15,
		MaxPBSTargetBackups:    15,
		SecondaryEnabled:       true,
		SecondaryPath:          "/mnt/nas-backup",
		CloudEnabled:           true,
		CloudRemote:            "gdrive:proxsave/backup",
		EmailEnabled:           true,
		EmailDeliveryMethod:    "relay",
		EmailFallbackSendmail:  true,
		TelegramEnabled:        true,
		TelegramBotType:        "centralized",
		GotifyEnabled:          true,
		WebhookEnabled:         true,
	}
}

func notifyGoldenEnv() *environment.EnvironmentInfo {
	return &environment.EnvironmentInfo{Type: types.ProxmoxVE, Version: "8.4.1", PVEVersion: "8.4.1"}
}

// notifyGoldenLogStorageNotInitialized writes what logStorageNotInitialized
// (cmd/proxsave/runtime_helpers.go) writes for a destination switched off at startup.
func notifyGoldenLogStorageNotInitialized(logger *logging.Logger, label, name, causeLine string) {
	if causeLine != "" {
		logger.Info("%s", causeLine)
	}
	logger.Warning("%s %s: not initialized", theme.SymbolError, name)
	logger.Skip("Path %s: disabled", label)
}

func notifyGoldenCases() []notifyGoldenCase {
	return []notifyGoldenCase{
		{
			name:     "01_all_ok",
			wantExit: types.ExitSuccess.Int(),
		},
		{
			name:                   "02_secondary_warning_cloud_error_email_fallback",
			failSecondaryRetention: true,
			failCloudStore:         true,
			emailFallback:          true,
			wantExit:               types.ExitGenericError.Int(),
		},
		{
			// A warning with no error anywhere: the only case where the email's status
			// sidebar takes the warning colour.
			name:                   "02b_secondary_retention_failed",
			failSecondaryRetention: true,
			wantExit:               types.ExitGenericError.Int(),
		},
		{
			// The workspace cannot be created: RunGoBackup returns a plain error, so the
			// exit code is ExitBackupError (backupFailureExitCode) and step [6] never runs.
			name: "03_stopped_before_storage",
			runErr: fmt.Errorf("failed to create temporary directory: %w", &fs.PathError{
				Op:   "mkdirtemp",
				Path: workspaceRoot + "/proxsave-" + notifyGoldenHost + "-" + notifyGoldenStart.Format("20060102-150405") + "-*",
				Err:  syscall.ENOSPC,
			}),
			wantExit: types.ExitBackupError.Int(),
		},
		{
			name: "04_destinations_and_email_off",
			configure: func(cfg *config.Config) {
				cfg.SecondaryEnabled = false
				cfg.SecondaryPath = ""
				cfg.CloudEnabled = false
				cfg.CloudRemote = ""
				cfg.EmailEnabled = false
			},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			// cmd/proxsave/backup_storage.go: the secondary directory cannot be created and
			// the cloud remote does not answer. Both are switched off for the run
			// (disableSecondaryForRun, disableCloudForRun), so the run sees them as off.
			name: "05_destinations_off_after_startup_failure",
			startup: func(logger *logging.Logger, cfg *config.Config) {
				cfg.SecondaryEnabled = false
				cfg.SecondaryLogPath = ""
				notifyGoldenLogStorageNotInitialized(logger, "Secondary", "Secondary storage", "  Directory not created: "+safefs.SystemErrorText(syscall.EACCES))
				cfg.CloudEnabled = false
				cfg.CloudLogPath = ""
				notifyGoldenLogStorageNotInitialized(logger, "Cloud", "Cloud storage", "")
			},
			wantExit: types.ExitGenericError.Int(),
		},
		{
			// RETENTION_POLICY is one setting for every destination
			// (storage.NewRetentionConfigFromConfig): GFS makes all three "N/-".
			name: "06a_gfs_retention",
			configure: func(cfg *config.Config) {
				cfg.RetentionPolicy = "gfs"
				cfg.RetentionDaily = 7
				cfg.RetentionWeekly = 4
				cfg.RetentionMonthly = 12
				cfg.RetentionYearly = 3
			},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			// Simple retention with MAX_SECONDARY_BACKUPS=0 and MAX_CLOUD_BACKUPS=0: no
			// retention pass there, "N/?".
			name: "06b_max_backups_zero",
			configure: func(cfg *config.Config) {
				cfg.SecondaryRetentionDays = 0
				cfg.MaxSecondaryBackups = 0
				cfg.CloudRetentionDays = 0
				cfg.MaxCloudBackups = 0
			},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			name: "07_pbs_target_ok",
			configure: func(cfg *config.Config) {
				cfg.PBSTargetEnabled = true
				cfg.PBSTargetStorage = "pbs-offsite"
				cfg.MaxPBSTargetBackups = 15
			},
			pbs: &block.Result{
				Name:            block.PBSName,
				Status:          block.StatusOK,
				Location:        "backup@pbs!proxsave@192.0.2.10:offsite",
				Snapshot:        "host/proxsave-" + notifyGoldenHost + "/2026-10-04T02:02:20Z",
				Backups:         12,
				MaxBackups:      15,
				RetentionPolicy: "simple",
				Deleted:         0,
				FreeBytes:       1210 << 30,
				UsedBytes:       578 << 30,
				TotalBytes:      1788 << 30,
			},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			// cmd/proxsave/backup_storage.go: the primary storage cannot be prepared, so
			// LocalStorage.DetectFilesystem fails to create BACKUP_PATH.
			name: "08_early_error",
			early: &EarlyErrorState{
				Phase: "storage_init",
				Error: &storage.StorageError{
					Location:   storage.LocationPrimary,
					Operation:  "detect_filesystem",
					Path:       "/opt/proxsave/backup",
					Err:        &fs.PathError{Op: "mkdir", Path: "/opt/proxsave/backup", Err: syscall.EROFS},
					IsCritical: true,
				},
				ExitCode:  types.ExitStorageError,
				Timestamp: notifyGoldenStart,
			},
			wantExit: types.ExitStorageError.Int(),
		},
		{
			name:   "09_update_available",
			update: true,
			startup: func(logger *logging.Logger, cfg *config.Config) {
				// What checkForUpdates (cmd/proxsave/main_update.go) logs once the run log
				// is open.
				logger.Warning("New ProxSave version %s (current %s): run 'proxsave --upgrade' to install.", notifyGoldenLatestVersion, notifyGoldenVersion)
			},
			wantExit: types.ExitGenericError.Int(),
		},
		{
			// The archive reached the cloud remote, one sidecar upload did not.
			name:                 "10_cloud_sidecar_upload_failed",
			cloudSidecarNotSaved: true,
			wantExit:             types.ExitGenericError.Int(),
		},
		{
			// The archive was copied to the secondary path, one sidecar copy failed.
			name:                     "11_secondary_sidecar_not_copied",
			secondarySidecarNotSaved: true,
			wantExit:                 types.ExitGenericError.Int(),
		},
		{
			// PBS_TARGET_ENABLED=true, the server refuses the connection at startup: the
			// block stays registered and step [7] reports the backup not saved.
			name:      "12_pbs_not_initialized",
			configure: notifyGoldenPBSConfigure,
			pbsServer: &notifyGoldenPBS{answers: map[string]notifyGoldenPBSAnswer{
				"version": {rc: 255, stderr: "Error: client error (Connect)\n" +
					"Caused by: error connecting to https://192.0.2.10:8007/ - tcp connect error: Connection refused (os error 111)\n"},
			}},
			wantExit: types.ExitGenericError.Int(),
		},
		{
			// The backup is saved, the prune is refused: no retention at all.
			name:      "13_pbs_retention_denied",
			configure: notifyGoldenPBSConfigure,
			pbsServer: &notifyGoldenPBS{answers: map[string]notifyGoldenPBSAnswer{
				"prune": {rc: 255, stderr: "Error: permission check failed - missing Datastore.Modify|Datastore.Prune on /datastore/offsite\n"},
			}},
			wantExit: types.ExitGenericError.Int(),
		},
		{
			// The collected tree holds a .pxarexclude: the backup is saved with its rules.
			name:      "14_pbs_exclusion_rules",
			configure: notifyGoldenPBSConfigure,
			pbsServer: &notifyGoldenPBS{treeFiles: []string{"etc/ssh/.pxarexclude"}},
			wantExit:  types.ExitGenericError.Int(),
		},
		{
			// GFS: the snapshot the policy drops is protected, so it is kept.
			name: "15_pbs_gfs_protected_kept",
			configure: func(cfg *config.Config) {
				notifyGoldenPBSConfigure(cfg)
				notifyGoldenPBSRetentionGFS(cfg)
			},
			pbsServer: &notifyGoldenPBS{snapshots: []notifyGoldenPBSSnapshot{
				{at: notifyGoldenPBSRunSnapshotTime},
				{at: notifyGoldenPBSRunSnapshotTime.AddDate(0, 0, -30), protected: true},
			}},
			wantExit: types.ExitGenericError.Int(),
		},
		{
			// PBS initialized at startup, the run stops before step [6] (and [7]), as in
			// 03_stopped_before_storage.
			name:      "16_pbs_stopped_before_step7",
			configure: notifyGoldenPBSConfigure,
			pbsServer: &notifyGoldenPBS{},
			runErr: fmt.Errorf("failed to create temporary directory: %w", &fs.PathError{
				Op:   "mkdirtemp",
				Path: workspaceRoot + "/proxsave-" + notifyGoldenHost + "-" + notifyGoldenStart.Format("20060102-150405") + "-*",
				Err:  syscall.ENOSPC,
			}),
			wantExit: types.ExitBackupError.Int(),
		},
		{
			// PBS_TARGET_ENABLED=false with PBS_TARGET_STORAGE still set, next to the
			// Secondary and the Cloud.
			name: "17_pbs_off_secondary_cloud_on",
			configure: func(cfg *config.Config) {
				cfg.PBSTargetEnabled = false
				cfg.PBSTargetStorage = notifyGoldenPBSStorage
			},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			// cmd/proxsave validateFutureFeatures: CLOUD_ENABLED=true with CLOUD_REMOTE
			// empty switches the cloud off for the run. It runs before the run logger
			// exists, so its WARNING goes to the process-start default logger and never
			// reaches the run log. (The configuration parser already rejects an empty
			// CLOUD_REMOTE with CLOUD_ENABLED=true, so backup.env cannot get here.)
			name: "18_cloud_remote_empty",
			configure: func(cfg *config.Config) {
				cfg.CloudRemote = ""
			},
			startup: func(logger *logging.Logger, cfg *config.Config) {
				bootLogger := logging.New(types.LogLevelDebug, false)
				bootLogger.SetOutput(io.Discard)
				bootLogger.Warning("Cloud backup enabled but CLOUD_REMOTE is empty, disabling cloud storage for this run")
				cfg.CloudEnabled = false
				cfg.CloudRemote = ""
				cfg.CloudLogPath = ""
			},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			// cmd/proxsave runNetworkPreflight with no outbound connectivity: every
			// network feature is switched off for the run (disableNetworkFeaturesForRun),
			// the email goes through sendmail. The bootstrap WARNING lines are flushed
			// into the run log.
			name:    "19_cloud_off_no_network",
			offline: true,
			startup: func(logger *logging.Logger, cfg *config.Config) {
				logger.Warning("Network connectivity unavailable for: %s. %s",
					"Telegram centralized registration, Email relay delivery, Gotify notifications, Webhooks, Cloud storage (rclone)",
					"no outbound connectivity (checked 2 endpoints)")
				logger.Warning("Disabling network-dependent features for this run")
				logger.Warning("WARNING: Disabling cloud storage (rclone) due to missing network connectivity")
				cfg.CloudEnabled = false
				cfg.CloudLogPath = ""
				logger.Warning("WARNING: Disabling Telegram notifications due to missing network connectivity")
				cfg.TelegramEnabled = false
				logger.Warning("WARNING: Network unavailable; switching Email delivery to sendmail for this run")
				cfg.EmailDeliveryMethod = "sendmail"
				logger.Warning("WARNING: Disabling Gotify notifications due to missing network connectivity")
				cfg.GotifyEnabled = false
				logger.Warning("WARNING: Disabling Webhook notifications due to missing network connectivity")
				cfg.WebhookEnabled = false
			},
			emailMethod: "email-sendmail",
			wantExit:    types.ExitGenericError.Int(),
		},
	}
}

// notifyGoldenNotifier is the notify.Notifier behind each channel's real
// NotificationAdapter: it keeps the NotificationData it is handed, as JSON, and answers
// with the outcome the case asks for.
type notifyGoldenNotifier struct {
	name     string
	result   notify.NotificationResult
	calls    int
	snapshot []byte
	err      error
}

func (n *notifyGoldenNotifier) Name() string     { return n.name }
func (n *notifyGoldenNotifier) IsEnabled() bool  { return true }
func (n *notifyGoldenNotifier) IsCritical() bool { return false }

func (n *notifyGoldenNotifier) Send(ctx context.Context, data *notify.NotificationData) (*notify.NotificationResult, error) {
	n.calls++
	n.snapshot, n.err = json.MarshalIndent(data, "", "  ")
	result := n.result
	return &result, nil
}

// notifyGoldenHostPinned stands in front of a channel on the early-error path only:
// DispatchEarlyErrorNotification reads the hostname from os.Hostname and the log path
// from the open run log, the only two values that come from the machine. It writes the
// fixed ones into the stats before the real adapter converts them.
type notifyGoldenHostPinned struct {
	inner NotificationChannel
}

func (p notifyGoldenHostPinned) Name() string { return p.inner.Name() }

func (p notifyGoldenHostPinned) Notify(ctx context.Context, stats *BackupStats) error {
	stats.Hostname = notifyGoldenHost
	if stats.LogFilePath != "" {
		stats.LogFilePath = notifyGoldenLogFile
	}
	return p.inner.Notify(ctx, stats)
}

// notifyGoldenNotifiers returns the channels cmd/proxsave registers: only the enabled
// ones (initializeBackupNotifications skips a disabled channel).
func notifyGoldenNotifiers(cfg *config.Config, tc notifyGoldenCase) []*notifyGoldenNotifier {
	emailMethod := "email-relay"
	if tc.emailMethod != "" {
		emailMethod = tc.emailMethod
	}
	email := notify.NotificationResult{Success: true, Method: emailMethod}
	if tc.emailFallback {
		email = notify.NotificationResult{Success: true, UsedFallback: true, Method: "email-sendmail",
			Error: errors.New("bad request (HTTP 400): bad request")}
	}
	all := []struct {
		enabled bool
		n       *notifyGoldenNotifier
	}{
		{cfg.EmailEnabled, &notifyGoldenNotifier{name: "Email", result: email}},
		{cfg.TelegramEnabled, &notifyGoldenNotifier{name: "Telegram", result: notify.NotificationResult{Success: true, Method: "telegram"}}},
		{cfg.GotifyEnabled, &notifyGoldenNotifier{name: "Gotify", result: notify.NotificationResult{Success: true, Method: "gotify"}}},
		{cfg.WebhookEnabled, &notifyGoldenNotifier{name: "Webhook", result: notify.NotificationResult{Success: true, Method: "webhook"}}},
	}
	var out []*notifyGoldenNotifier
	for _, c := range all {
		if c.enabled {
			out = append(out, c.n)
		}
	}
	return out
}

// notifyGoldenBackends builds the step [6] fakes in the order cmd/proxsave registers the
// destinations (local, secondary, cloud), with the figures their GetStats and retention
// report. Only enabled destinations are registered, like cmd/proxsave does.
func notifyGoldenBackends(cfg *config.Config, tc notifyGoldenCase) []*backupCharBackend {
	backends := []*backupCharBackend{{
		label: "local", name: "Local Storage", location: storage.LocationPrimary, critical: true,
		fsInfo: storage.FilesystemInfo{Path: cfg.BackupPath, Type: storage.FilesystemExt4, SupportsOwnership: true, MountPoint: "/"},
		stats: storage.StorageStats{TotalBackups: 15, ListedBackups: 15, TotalSize: 15 * (21 << 20),
			AvailableSpace: 412 << 30, UsedSpace: 54 << 30, TotalSpace: 466 << 30},
		deleted: 1,
		summary: storage.RetentionSummary{BackupsDeleted: 1, BackupsRemaining: 15, LogsDeleted: 1, LogsRemaining: 15,
			HasLogInfo: true, ScopeValid: true, Owned: 15, PassCompleted: true},
	}}
	if cfg.SecondaryEnabled {
		backends = append(backends, &backupCharBackend{
			label: "secondary", name: "Secondary Storage", location: storage.LocationSecondary,
			failRetention: tc.failSecondaryRetention,
			fsInfo:        storage.FilesystemInfo{Path: cfg.SecondaryPath, Type: storage.FilesystemNFS4, IsNetworkFS: true, MountPoint: cfg.SecondaryPath},
			stats: storage.StorageStats{TotalBackups: 16, ListedBackups: 16, TotalSize: 16 * (21 << 20),
				AvailableSpace: 1843 << 30, UsedSpace: 1883 << 30, TotalSpace: 3726 << 30},
			deleted: 1,
			summary: storage.RetentionSummary{BackupsDeleted: 1, BackupsRemaining: 15, ScopeValid: true, Owned: 15, PassCompleted: true},
		})
	}
	if cfg.CloudEnabled {
		backends = append(backends, &backupCharBackend{
			label: "cloud", name: "Cloud Storage (rclone)", location: storage.LocationCloud,
			failStore: tc.failCloudStore,
			fsInfo: storage.FilesystemInfo{Path: cfg.CloudRemote, Type: storage.FilesystemType("rclone-gdrive"),
				IsNetworkFS: true, MountPoint: cfg.CloudRemote, Device: "cloud"},
			stats:   storage.StorageStats{TotalBackups: 15, ListedBackups: 15, TotalSize: 15 * (21 << 20)},
			deleted: 1,
			summary: storage.RetentionSummary{BackupsDeleted: 1, BackupsRemaining: 15, ScopeValid: true, Owned: 15, PassCompleted: true},
		})
	}
	return backends
}

// notifyGoldenArchive writes what steps [2]-[5] leave in the stats for a bundled xz
// archive: the collection figures, the archive and bundle, the end of the timed part.
func notifyGoldenArchive(stats *BackupStats) {
	stats.FilesCollected = 1832
	stats.FilesIncluded = 1832
	stats.DirsCreated = 214
	stats.BytesCollected = 178 << 20
	stats.UncompressedSize = 178 << 20
	stats.ClusterMode = "standalone"
	stats.ArchiveSize = 21 << 20
	stats.CompressedSize = 21 << 20
	stats.updateCompressionMetrics()
	stats.Checksum = "9f2c4a1d7e6b3c8f0a5d2e9b4c7f1a6d3e8b0c5f2a7d4e1b9c6f3a0d8e5b2c7f"
	stats.ArchivePath = filepath.Join(stats.LocalPath, notifyGoldenHost+"-backup-"+notifyGoldenStart.Format("20060102-150405")+".tar.xz.bundle.tar")
	stats.BundleCreated = true
	stats.EndTime = notifyGoldenStart.Add(notifyGoldenRunDuration)
	stats.Duration = stats.EndTime.Sub(stats.StartTime)
}

func notifyGoldenMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(data, '\n')
}

// runNotifyGoldenCase runs one case and returns its files, keyed by name.
func runNotifyGoldenCase(t *testing.T, tc notifyGoldenCase) map[string][]byte {
	t.Helper()
	cfg := notifyGoldenConfig()
	if tc.configure != nil {
		tc.configure(cfg)
	}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	runLog := filepath.Join(t.TempDir(), filepath.Base(notifyGoldenLogFile))
	if err := logger.OpenLogFile(runLog); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() { _ = logger.CloseLogFile() })

	if tc.startup != nil {
		tc.startup(logger, cfg)
	}
	var pveDir, pbsTree string
	if tc.pbsServer != nil {
		pveDir = tc.pbsServer.install(t)
		pbsTree = tc.pbsServer.tree(t)
	}

	o := &Orchestrator{
		logger:               logger,
		cfg:                  cfg,
		clock:                &backupCharClock{now: notifyGoldenStart.Add(notifyGoldenRunDuration)},
		version:              notifyGoldenVersion,
		serverID:             notifyGoldenServerID,
		serverMAC:            notifyGoldenMAC,
		storageTargets:       make([]StorageTarget, 0),
		notificationChannels: make([]NotificationChannel, 0),
	}
	o.SetEnvironmentInfo(notifyGoldenEnv())
	ctx := context.Background()
	if tc.pbsServer != nil {
		notifyGoldenInitPBS(t, ctx, logger, cfg, o, pveDir)
	}

	notifiers := notifyGoldenNotifiers(cfg, tc)
	files := map[string][]byte{}
	var stats *BackupStats
	outcome := notifyGoldenOutcome{LogFile: notifyGoldenLogFile}

	if tc.early != nil {
		for _, n := range notifiers {
			o.RegisterNotificationChannel(notifyGoldenHostPinned{inner: NewNotificationAdapter(n, logger)})
		}
		stats = o.DispatchEarlyErrorNotification(ctx, tc.early)
		if stats == nil {
			t.Fatal("DispatchEarlyErrorNotification returned nil stats")
		}
		stats.Hostname = notifyGoldenHost
		stats.LogFilePath = notifyGoldenLogFile
		// finishBackupMode returns the early error's exit code and no stats.
		outcome.ExitCode = tc.early.ExitCode.Int()
	} else {
		stats = InitializeBackupStats(notifyGoldenHost, notifyGoldenEnv(), notifyGoldenVersion, notifyGoldenStart, cfg,
			types.CompressionXZ, "ultra", 9, 0, cfg.BackupPath, notifyGoldenServerID, notifyGoldenMAC)
		stats.LogFilePath = runLog
		// initBackupRun copies what checkForUpdates found; an up-to-date check carries
		// the same version twice.
		stats.CurrentVersion = notifyGoldenVersion
		stats.LatestVersion = notifyGoldenVersion
		if tc.update {
			stats.NewVersionAvailable = true
			stats.LatestVersion = notifyGoldenLatestVersion
		}
		if tc.offline {
			stats.LatestVersion = ""
		}

		backendClock := &backupCharClock{now: notifyGoldenStart}
		rec := &backupCharRecorder{}
		for _, b := range notifyGoldenBackends(cfg, tc) {
			b.rec = rec
			b.clock = backendClock
			info := b.fsInfo
			initial := b.stats
			var backend storage.Storage = b
			switch {
			case tc.secondarySidecarNotSaved && b.location == storage.LocationSecondary:
				backend = &notifyGoldenSidecarBackend{backupCharBackend: b, issues: []storage.StoreIssue{storage.StoreIssueSidecarNotSaved}}
			case tc.cloudSidecarNotSaved && b.location == storage.LocationCloud:
				backend = &notifyGoldenSidecarBackend{backupCharBackend: b, primarySaved: true}
			}
			adapter := NewStorageAdapter(backend, logger, cfg)
			adapter.SetFilesystemInfo(&info)
			adapter.SetInitialStats(&initial)
			adapter.SetInitialOwnedBackups(b.summary.Owned, true)
			o.RegisterStorageTarget(adapter)
		}

		if tc.runErr != nil {
			// RunGoBackup's deferred failure path, then handleBackupRunError's line.
			o.finalizeFailedBackupStats(&backupRunContext{ctx: ctx, stats: stats}, tc.runErr)
			logger.Error("Backup orchestration failed: %v", tc.runErr)
		} else {
			notifyGoldenArchive(stats)
			if err := o.syncStorageTargets(ctx, stats); err != nil {
				t.Fatalf("syncStorageTargets: %v", err)
			}
			if tc.pbs != nil {
				result := *tc.pbs
				stats.PBSTarget = &result
			}
			if tc.pbsServer != nil {
				notifyGoldenRunPBSStep(t, ctx, o, stats, pbsTree)
			}
		}

		for _, n := range notifiers {
			o.RegisterNotificationChannel(NewNotificationAdapter(n, logger))
		}
		// startNotificationGroup with no NOTIFY_ON configured: snapshot, exit code, dispatch.
		o.snapshotPreNotificationIssues(stats)
		stats.LogFilePath = notifyGoldenLogFile
		applyIssueExitCode(stats)
		o.dispatchNotifications(ctx, stats)
		if tc.runErr == nil {
			// exportBackupMetrics re-reads the closed log on a successful run.
			stats.LogFilePath = runLog
			o.finalizeSuccessIssueStats(stats)
			stats.LogFilePath = notifyGoldenLogFile
		}
		outcome.ExitCode = stats.ExitCode
		outcome.WithStats = true
	}

	if outcome.ExitCode != tc.wantExit {
		t.Fatalf("exit code %d, want %d", outcome.ExitCode, tc.wantExit)
	}
	for _, n := range notifiers {
		if n.calls == 0 {
			continue
		}
		if n.calls != 1 || n.err != nil {
			t.Fatalf("%s: calls=%d marshal error=%v", n.name, n.calls, n.err)
		}
		files[strings.ToLower(n.name)+".json"] = append(n.snapshot, '\n')
	}
	files["stats.json"] = notifyGoldenMarshal(t, stats)
	files["outcome.json"] = notifyGoldenMarshal(t, outcome)
	return files
}

// TestNotificationGoldens pins, per case, the NotificationData each channel receives,
// the stats the run ends with and the outcome the dashboard is built from.
func TestNotificationGoldens(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	cases := notifyGoldenCases()
	names := make([]string, 0, len(cases))
	for _, tc := range cases {
		names = append(names, tc.name)
		t.Run(tc.name, func(t *testing.T) {
			files := runNotifyGoldenCase(t, tc)
			assertNotifyGoldenDir(t, filepath.Join(notifyGoldenDir, tc.name), files)
		})
	}
	assertNotifyGoldenCaseList(t, names)
}

// assertNotifyGoldenDir compares (or, with -update, rewrites) one case directory: every
// file the case produced, and no other.
func assertNotifyGoldenDir(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	if *updateBackupGoldens {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("clear %s: %v", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s (run with -update to create): %v", dir, err)
	}
	var onDisk, produced []string
	for _, e := range entries {
		onDisk = append(onDisk, e.Name())
	}
	for name := range files {
		produced = append(produced, name)
	}
	sort.Strings(produced)
	if strings.Join(onDisk, ",") != strings.Join(produced, ",") {
		t.Fatalf("%s holds %v, the case produces %v", dir, onDisk, produced)
	}
	for _, name := range produced {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(want) != string(files[name]) {
			t.Fatalf("golden mismatch for %s/%s\n--- want ---\n%s\n--- got ---\n%s", dir, name, want, files[name])
		}
	}
}

// assertNotifyGoldenCaseList fails on a case directory no case produces any more, which
// the two consumers would otherwise keep rendering.
func assertNotifyGoldenCaseList(t *testing.T, names []string) {
	t.Helper()
	if *updateBackupGoldens {
		entries, err := os.ReadDir(notifyGoldenDir)
		if err != nil {
			t.Fatalf("read %s: %v", notifyGoldenDir, err)
		}
		for _, e := range entries {
			if !slices.Contains(names, e.Name()) {
				if err := os.RemoveAll(filepath.Join(notifyGoldenDir, e.Name())); err != nil {
					t.Fatalf("remove stale case %s: %v", e.Name(), err)
				}
			}
		}
		return
	}
	entries, err := os.ReadDir(notifyGoldenDir)
	if err != nil {
		t.Fatalf("read %s: %v", notifyGoldenDir, err)
	}
	for _, e := range entries {
		if !slices.Contains(names, e.Name()) {
			t.Fatalf("%s/%s belongs to no case", notifyGoldenDir, e.Name())
		}
	}
}
