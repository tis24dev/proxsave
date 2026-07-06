// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func initializeBackupNotifications(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	logger := opts.logger

	// Register notifier secrets so they are scrubbed from every log line
	// (defense-in-depth on top of the per-notifier source redaction).
	registerNotificationSecrets(logger, opts.cfg)

	logging.Step("Initializing notification channels")
	notifyDone := logging.DebugStart(logger, "notifications init", "")
	initializeEmailNotification(opts, orch)
	initializeTelegramNotification(opts, orch)
	initializeGotifyNotification(opts, orch)
	initializeWebhookNotification(opts, orch)
	initializeHealthcheckSection(opts, orch)
	notifyDone(nil)

	fmt.Println()
}

// registerNotificationSecrets registers the notifier credentials with the logger
// so they are masked out of any log line. The public Cloudflare relay
// worker token / HMAC secret are intentionally NOT registered (documented
// shared anti-abuse credentials, not confidential).
func registerNotificationSecrets(logger *logging.Logger, cfg *config.Config) {
	if logger == nil || cfg == nil {
		return
	}
	logger.RegisterSecret(cfg.TelegramBotToken)
	logger.RegisterSecret(cfg.TelegramNotifySecret)
	logger.RegisterSecret(cfg.GotifyToken)
	if cfg.WebhookEnabled {
		for _, ep := range cfg.BuildWebhookConfig().Endpoints {
			logger.RegisterSecret(ep.URL)
			logger.RegisterSecret(ep.Auth.Token)
			logger.RegisterSecret(ep.Auth.Secret)
			logger.RegisterSecret(ep.Auth.Pass)
		}
	}
}

func initializeEmailNotification(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.EmailEnabled {
		logging.DebugStep(logger, "notifications init", "email disabled")
		logging.Skip("Email: disabled")
		return
	}

	logging.DebugStep(logger, "notifications init", "email enabled")
	emailConfig := notify.EmailConfig{
		Enabled:          true,
		DeliveryMethod:   notify.EmailDeliveryMethod(cfg.EmailDeliveryMethod),
		FallbackSendmail: cfg.EmailFallbackSendmail,
		Recipient:        cfg.EmailRecipient,
		From:             cfg.EmailFrom,
		CloudRelayConfig: notify.CloudRelayConfig{
			WorkerURL:   cfg.CloudflareWorkerURL,
			WorkerToken: cfg.CloudflareWorkerToken,
			HMACSecret:  cfg.CloudflareHMACSecret,
			Timeout:     cfg.WorkerTimeout,
			MaxRetries:  cfg.WorkerMaxRetries,
			RetryDelay:  cfg.WorkerRetryDelay,
		},
	}
	emailNotifier, err := notify.NewEmailNotifier(emailConfig, opts.envInfo.Type, logger)
	if err != nil {
		logging.Warning("Failed to initialize Email notifier: %v", err)
		return
	}
	emailAdapter := orchestrator.NewNotificationAdapter(emailNotifier, logger)
	orch.RegisterNotificationChannel(emailAdapter)
	logging.Info("✓ Email initialized (method: %s)", cfg.EmailDeliveryMethod)
}

// initializeHealthcheckSection verifies the healthchecks config at run start and
// registers the Phase-7 section, EXACTLY like the other notification channels: it prints
// a real init line (SKIP when disabled, a WARNING that disables the section on a config
// problem, or a "✓ initialized" when usable) instead of being silent. This is a
// CONFIG-only check (no network); the REAL transmission state is reported later by the
// Phase-7 section from the daemon status file. Registering only when usable keeps a
// disabled/broken section out of the dispatch, and the Phase-7 entries loop renders the
// matching "disabled" / "enabled but not initialized" line just as it does for the others.
func initializeHealthcheckSection(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	cfg := opts.cfg
	logger := opts.logger
	if cfg == nil || !cfg.HealthcheckEnabled {
		logging.DebugStep(logger, "notifications init", "healthchecks disabled")
		logging.Skip("Healthchecks: disabled")
		return
	}
	// Verify config, then that the monitoring daemon (the ONLY pinger) is actually alive -
	// a valid config is worthless if the daemon is down. On ANY problem, switch the
	// channel to disabled EXACTLY like checkTelegramServerStatus does for a failed
	// centralized handshake (main_identity.go): so the whole flow treats it as disabled.
	if problem := healthcheckConfigProblem(cfg); problem != "" {
		disableHealthchecks(cfg, logger, problem)
		return
	}
	if problem := healthcheckDaemonProblem(cfg, logger); problem != "" {
		disableHealthchecks(cfg, logger, problem)
		return
	}
	logging.DebugStep(logger, "notifications init", "healthchecks enabled (mode=%s, daemon up)", cfg.HealthcheckMode)
	orch.RegisterNotificationChannel(orchestrator.NewHealthchecksChannel(cfg, logger))
	logging.Info("✓ Healthchecks initialized (mode: %s)", cfg.HealthcheckMode)
}

// disableHealthchecks switches the section to disabled with a reason, mirroring
// checkTelegramServerStatus (cmd/proxsave/main_identity.go): a WARNING naming the
// problem, a clean "Healthchecks: disabled" SKIP, and flipping cfg.HealthcheckEnabled so
// the downstream Phase-7 dispatch entries loop renders "Healthchecks: disabled" instead
// of "enabled but not initialized". cfg is the same pointer the dispatcher reads.
func disableHealthchecks(cfg *config.Config, logger *logging.Logger, reason string) {
	logging.DebugStep(logger, "notifications init", "healthchecks disabled: %s", reason)
	logging.Warning("Healthchecks: %s", reason)
	logging.Skip("Healthchecks: disabled")
	cfg.HealthcheckEnabled = false
}

// healthcheckDaemonProblem verifies the monitoring daemon is actually alive by reading
// its persisted heartbeat (the daemon records its first beat immediately on startup, so
// a fresh beat proves it is running). Returns a specific reason when the daemon is not
// usable (unreadable status, not running, or down/stuck), or "" when it is up. It never
// transmits - it only inspects the local status file the daemon writes.
func healthcheckDaemonProblem(cfg *config.Config, logger *logging.Logger) string {
	st, err := health.LoadStatus(cfg.BaseDir)
	if err != nil {
		logging.DebugStep(logger, "notifications init", "healthchecks status unreadable: %v", err)
		return "status file unreadable"
	}
	d := health.Diagnose(st, cfg.HealthcheckHeartbeatInterval, time.Now())
	// Shape-only debug, mirroring the Phase-7 section's diagnose line so the init verdict
	// and the run-time section can be cross-read at debug (no URL/secret, just state).
	logging.DebugStep(logger, "notifications init",
		"healthchecks daemon diagnose state=%s daemon_up=%t hb_age=%s", d.State, d.DaemonUp, d.HbAge)
	if d.DaemonUp {
		return ""
	}
	if d.State == health.TxStale {
		return "daemon stale (last beat " + health.HumanizeAge(d.HbAge) + ")"
	}
	return "daemon not running"
}

// healthcheckConfigProblem returns a short reason when the healthcheck config is
// structurally unusable, or "" when it is fine. Config-only (mirrors how the other
// notifiers validate at init) - it never touches the network or the on-disk secret;
// runtime provisioning/transmission is the Phase-7 section's job (the daemon status file).
func healthcheckConfigProblem(cfg *config.Config) string {
	switch cfg.HealthcheckMode {
	case "self":
		// Self mode needs a check to ping: a full alive URL, or an alive check id (the
		// ping endpoint defaults to a public host, so the id/URL is the discriminator).
		// The service-alive (heartbeat / dead-man) check is the mandatory one; a
		// backup-only self config has no liveness signal. Name it precisely so the
		// message is not mistaken for "no monitoring at all".
		if cfg.HealthcheckAliveURL == "" && cfg.HealthcheckAliveID == "" {
			return "no alive check configured"
		}
	default: // centralized
		if strings.TrimSpace(cfg.ServerID) == "" {
			return "no SERVER_ID"
		}
	}
	return ""
}

func initializeTelegramNotification(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.TelegramEnabled {
		logging.DebugStep(logger, "notifications init", "telegram disabled")
		logging.Skip("Telegram: disabled")
		return
	}

	logging.DebugStep(logger, "notifications init", "telegram enabled (mode=%s)", cfg.TelegramBotType)
	telegramConfig := notify.TelegramConfig{
		Enabled:       true,
		Mode:          notify.TelegramMode(cfg.TelegramBotType),
		BotToken:      cfg.TelegramBotToken,
		ChatID:        cfg.TelegramChatID,
		ServerAPIHost: cfg.ServerAPIHost,
		ServerID:      cfg.ServerID,
		NotifySecret:  cfg.TelegramNotifySecret,
		BaseDir:       cfg.BaseDir,

		ConfirmDelivery: cfg.TelegramConfirmDelivery,
		ConfirmTimeout:  time.Duration(cfg.TelegramConfirmTimeoutS) * time.Second,
		ConfirmInterval: time.Duration(cfg.TelegramConfirmIntervalS) * time.Second,
	}
	telegramNotifier, err := notify.NewTelegramNotifier(telegramConfig, logger)
	if err != nil {
		logging.Warning("Failed to initialize Telegram notifier: %v", err)
		return
	}
	telegramAdapter := orchestrator.NewNotificationAdapter(telegramNotifier, logger)
	orch.RegisterNotificationChannel(telegramAdapter)
	logging.Info("✓ Telegram initialized (mode: %s)", cfg.TelegramBotType)
}

func initializeGotifyNotification(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.GotifyEnabled {
		logging.DebugStep(logger, "notifications init", "gotify disabled")
		logging.Skip("Gotify: disabled")
		return
	}

	logging.DebugStep(logger, "notifications init", "gotify enabled")
	gotifyConfig := notify.GotifyConfig{
		Enabled:         true,
		ServerURL:       cfg.GotifyServerURL,
		Token:           cfg.GotifyToken,
		PrioritySuccess: cfg.GotifyPrioritySuccess,
		PriorityWarning: cfg.GotifyPriorityWarning,
		PriorityFailure: cfg.GotifyPriorityFailure,
	}
	gotifyNotifier, err := notify.NewGotifyNotifier(gotifyConfig, logger)
	if err != nil {
		logging.Warning("Failed to initialize Gotify notifier: %v", err)
		return
	}
	gotifyAdapter := orchestrator.NewNotificationAdapter(gotifyNotifier, logger)
	orch.RegisterNotificationChannel(gotifyAdapter)
	logging.Info("✓ Gotify initialized")
}

func initializeWebhookNotification(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	cfg := opts.cfg
	logger := opts.logger
	if !cfg.WebhookEnabled {
		logging.DebugStep(logger, "notifications init", "webhook disabled")
		logging.Skip("Webhook: disabled")
		return
	}

	logging.DebugStep(logger, "notifications init", "webhook enabled")
	logging.Debug("Initializing webhook notifier...")
	webhookConfig := cfg.BuildWebhookConfig()
	logging.Debug("Webhook config built: %d endpoints configured", len(webhookConfig.Endpoints))

	webhookNotifier, err := notify.NewWebhookNotifier(webhookConfig, logger)
	if err != nil {
		logging.Warning("Failed to initialize Webhook notifier: %v", err)
		return
	}
	logging.Debug("Creating webhook notification adapter...")
	webhookAdapter := orchestrator.NewNotificationAdapter(webhookNotifier, logger)

	logging.Debug("Registering webhook notification channel with orchestrator...")
	orch.RegisterNotificationChannel(webhookAdapter)
	logging.Info("✓ Webhook initialized (%d endpoint(s))", len(webhookConfig.Endpoints))
}

func logBackupRuntimeSummary(cfg *config.Config, logger *logging.Logger, storageState backupStorageState) {
	logBackupStorageSummary(cfg, storageState)
	logBackupLogSummary(cfg)
	logBackupNotificationSummary(cfg, logger)
}

func logBackupStorageSummary(cfg *config.Config, storageState backupStorageState) {
	logging.Info("Storage configuration:")
	logging.Info("  Primary: %s", formatStorageLabel(cfg.BackupPath, storageState.localFS))
	if cfg.SecondaryEnabled {
		logging.Info("  Secondary storage: %s", formatStorageLabel(cfg.SecondaryPath, storageState.secondaryFS))
	} else {
		logging.Skip("  Secondary storage: disabled")
	}
	if cfg.CloudEnabled {
		logging.Info("  Cloud storage: %s", formatStorageLabel(cfg.CloudRemote, storageState.cloudFS))
	} else {
		logging.Skip("  Cloud storage: disabled")
	}
	fmt.Println()
}

func logBackupLogSummary(cfg *config.Config) {
	logging.Info("Log configuration:")
	logging.Info("  Primary: %s", cfg.LogPath)
	if cfg.SecondaryEnabled {
		if strings.TrimSpace(cfg.SecondaryLogPath) != "" {
			logging.Info("  Secondary: %s", cfg.SecondaryLogPath)
		} else {
			logging.Skip("  Secondary: disabled (log path not configured)")
		}
	} else {
		logging.Skip("  Secondary: disabled")
	}
	if cfg.CloudEnabled {
		if strings.TrimSpace(cfg.CloudLogPath) != "" {
			logging.Info("  Cloud: %s", cfg.CloudLogPath)
		} else {
			logging.Skip("  Cloud: disabled (log path not configured)")
		}
	} else {
		logging.Skip("  Cloud: disabled")
	}
	fmt.Println()
}

// logBackupNotificationSummary prints the EFFECTIVE per-channel state for THIS run, not
// the raw config: every flag has already absorbed the network-preflight, Telegram-handshake
// and Healthchecks-daemon flips, all of which run earlier on this same cfg pointer (proof:
// runNetworkPreflight and initializeServerIdentity precede runBackupMode, and
// initializeBackupNotifications precedes this summary). Healthchecks is listed so the set is
// not silently short a channel; Metrics has no runtime flip, so its effective value always
// equals the configured one.
func logBackupNotificationSummary(cfg *config.Config, logger *logging.Logger) {
	done := logging.DebugStart(logger, "notification summary", "effective post-flip state")
	logging.Info("Notification configuration:")
	logging.Info("  Telegram: %v", cfg.TelegramEnabled)
	logging.Info("  Email: %v", cfg.EmailEnabled)
	logging.Info("  Gotify: %v", cfg.GotifyEnabled)
	logging.Info("  Webhook: %v", cfg.WebhookEnabled)
	logging.Info("  Healthchecks: %v", cfg.HealthcheckEnabled)
	logging.Info("  Metrics: %v", cfg.MetricsEnabled)
	logging.DebugStep(logger, "notification summary",
		"telegram=%t email=%t gotify=%t webhook=%t healthchecks=%t metrics=%t",
		cfg.TelegramEnabled, cfg.EmailEnabled, cfg.GotifyEnabled, cfg.WebhookEnabled, cfg.HealthcheckEnabled, cfg.MetricsEnabled)
	done(nil)
	fmt.Println()
}
