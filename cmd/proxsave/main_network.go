// Package main contains the proxsave command entrypoint.
package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

type networkFeatureDisablement struct {
	enabled func(*config.Config) bool
	apply   func(*config.Config, networkWarningFunc)
}

type networkWarningFunc func(string, ...interface{})

// featuresNeedNetwork returns whether current configuration requires outbound network, and human reasons.
func featuresNeedNetwork(cfg *config.Config) (bool, []string) {
	reasons := []string{}
	// Telegram (any mode uses network)
	if cfg.TelegramEnabled {
		if strings.EqualFold(cfg.TelegramBotType, "centralized") {
			reasons = append(reasons, "Telegram centralized registration")
		} else {
			reasons = append(reasons, "Telegram personal notifications")
		}
	}
	// Email via relay
	if cfg.EmailEnabled && strings.EqualFold(cfg.EmailDeliveryMethod, "relay") {
		reasons = append(reasons, "Email relay delivery")
	}
	// Gotify
	if cfg.GotifyEnabled {
		reasons = append(reasons, "Gotify notifications")
	}
	// Webhooks
	if cfg.WebhookEnabled {
		reasons = append(reasons, "Webhooks")
	}
	// Cloud uploads via rclone
	if cfg.CloudEnabled {
		reasons = append(reasons, "Cloud storage (rclone)")
	}
	return len(reasons) > 0, reasons
}

// disableNetworkFeaturesForRun disables all network-dependent features when connectivity is unavailable.
func disableNetworkFeaturesForRun(cfg *config.Config, bootstrap *logging.BootstrapLogger) {
	if cfg == nil {
		return
	}
	warn := networkWarning(bootstrap)
	for _, disablement := range networkFeatureDisablements() {
		if disablement.enabled(cfg) {
			disablement.apply(cfg, warn)
		}
	}
}

func networkWarning(bootstrap *logging.BootstrapLogger) networkWarningFunc {
	return func(format string, args ...interface{}) {
		if bootstrap != nil {
			bootstrap.Warning(format, args...)
			return
		}
		logging.Warning(format, args...)
	}
}

func networkFeatureDisablements() []networkFeatureDisablement {
	return []networkFeatureDisablement{
		{enabled: cloudNetworkEnabled, apply: disableCloudNetworkFeature},
		{enabled: telegramNetworkEnabled, apply: disableTelegramNetworkFeature},
		{enabled: emailRelayNetworkEnabled, apply: disableEmailRelayNetworkFeature},
		{enabled: gotifyNetworkEnabled, apply: disableGotifyNetworkFeature},
		{enabled: webhookNetworkEnabled, apply: disableWebhookNetworkFeature},
	}
}

func cloudNetworkEnabled(cfg *config.Config) bool { return cfg.CloudEnabled }

func telegramNetworkEnabled(cfg *config.Config) bool { return cfg.TelegramEnabled }

func emailRelayNetworkEnabled(cfg *config.Config) bool {
	return cfg.EmailEnabled && strings.EqualFold(cfg.EmailDeliveryMethod, "relay")
}

func gotifyNetworkEnabled(cfg *config.Config) bool { return cfg.GotifyEnabled }

func webhookNetworkEnabled(cfg *config.Config) bool { return cfg.WebhookEnabled }

func disableCloudNetworkFeature(cfg *config.Config, warn networkWarningFunc) {
	warn("WARNING: Disabling cloud storage (rclone) due to missing network connectivity")
	cfg.CloudEnabled = false
	cfg.CloudLogPath = ""
}

func disableTelegramNetworkFeature(cfg *config.Config, warn networkWarningFunc) {
	warn("WARNING: Disabling Telegram notifications due to missing network connectivity")
	cfg.TelegramEnabled = false
}

func disableEmailRelayNetworkFeature(cfg *config.Config, warn networkWarningFunc) {
	if cfg.EmailFallbackSendmail {
		warn("WARNING: Network unavailable; switching Email delivery to sendmail for this run")
		cfg.EmailDeliveryMethod = "sendmail"
		return
	}
	warn("WARNING: Disabling Email relay notifications due to missing network connectivity")
	cfg.EmailEnabled = false
}

func disableGotifyNetworkFeature(cfg *config.Config, warn networkWarningFunc) {
	warn("WARNING: Disabling Gotify notifications due to missing network connectivity")
	cfg.GotifyEnabled = false
}

func disableWebhookNetworkFeature(cfg *config.Config, warn networkWarningFunc) {
	warn("WARNING: Disabling Webhook notifications due to missing network connectivity")
	cfg.WebhookEnabled = false
}
