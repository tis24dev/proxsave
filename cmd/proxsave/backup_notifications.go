// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func initializeBackupNotifications(opts backupModeOptions, orch *orchestrator.Orchestrator) {
	logger := opts.logger

	logging.Step("Initializing notification channels")
	notifyDone := logging.DebugStart(logger, "notifications init", "")
	initializeEmailNotification(opts, orch)
	initializeTelegramNotification(opts, orch)
	initializeGotifyNotification(opts, orch)
	initializeWebhookNotification(opts, orch)
	notifyDone(nil)

	fmt.Println()
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
		ServerAPIHost: cfg.TelegramServerAPIHost,
		ServerID:      cfg.ServerID,
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

func logBackupRuntimeSummary(cfg *config.Config, storageState backupStorageState) {
	logBackupStorageSummary(cfg, storageState)
	logBackupLogSummary(cfg)
	logBackupNotificationSummary(cfg)
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

func logBackupNotificationSummary(cfg *config.Config) {
	logging.Info("Notification configuration:")
	logging.Info("  Telegram: %v", cfg.TelegramEnabled)
	logging.Info("  Email: %v", cfg.EmailEnabled)
	logging.Info("  Gotify: %v", cfg.GotifyEnabled)
	logging.Info("  Webhook: %v", cfg.WebhookEnabled)
	logging.Info("  Metrics: %v", cfg.MetricsEnabled)
	fmt.Println()
}
