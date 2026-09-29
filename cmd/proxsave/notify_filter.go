package main

import (
	"context"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// NOTIFY_ON below always lets a clean run go silent, and silence is only safe when something
// else alarms when a run never happens: the Healthchecks monitor. So a run applies the threshold
// only when that monitor is confirmed to reach the operator, and notifies every outcome
// otherwise:
//   - centralized: the relay reports the alive and backup checks covered by a verified DOWN
//     route (state ready) and has applied this exact policy to the notify checks;
//   - self: both ping URLs valid, the daemon transmitting, and no notify URL configured (a
//     notify check the operator runs on a period would go DOWN on every filtered run).
// The decision is taken at initialization and taken again before dispatch when the relay's
// answer has expired.

// Healthchecks status words shown on the INFO line (maintainer-approved set).
const (
	hcStatusReady           = "ready"
	hcStatusNotConfigured   = "not configured"
	hcStatusNotVerified     = "not verified"
	hcStatusDegraded        = "degraded"
	hcStatusUnknown         = "unknown"
	hcStatusDisabled        = "disabled"
	hcStatusNotTransmitting = "not transmitting"
	hcStatusSelf            = "self"
)

// Healthchecks section outcome at initialization, recorded by initializeHealthcheckSection.
const (
	hcSectionDisabled        = "disabled"
	hcSectionNotTransmitting = "not_transmitting"
	hcSectionInitialized     = "initialized"
)

// notifyFilterValidity is how long a relay answer may be reused for the decision before dispatch.
const notifyFilterValidity = 120 * time.Second

var fetchDeliveryStatus = health.FetchDeliveryStatus

type notifyFilterDecision struct {
	requested string
	status    string
	effective string
	reason    string
	readAt    time.Time
}

func deliveryStatusWord(state string) string {
	switch state {
	case "ready":
		return hcStatusReady
	case "not_configured":
		return hcStatusNotConfigured
	case "unverified":
		return hcStatusNotVerified
	case "degraded":
		return hcStatusDegraded
	default:
		return hcStatusUnknown
	}
}

func selfNotifyURLConfigured(cfg *config.Config) bool {
	for _, v := range []string{cfg.HealthcheckNotifyEmailURL, cfg.HealthcheckNotifyEmailID,
		cfg.HealthcheckNotifyTelegramURL, cfg.HealthcheckNotifyTelegramID,
		cfg.HealthcheckNotifyGotifyURL, cfg.HealthcheckNotifyGotifyID,
		cfg.HealthcheckNotifyWebhookURL, cfg.HealthcheckNotifyWebhookID} {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// decideNotifyFilter takes the decision; op names the DEBUG operation ("notifications init" or
// "notifications dispatch").
func decideNotifyFilter(ctx context.Context, cfg *config.Config, logger *logging.Logger, section, op string) notifyFilterDecision {
	d := notifyFilterDecision{requested: negotiatedNotifyOn(cfg), effective: config.NotifyOnAlways}
	switch {
	case cfg == nil || section == hcSectionDisabled:
		d.status, d.reason = hcStatusDisabled, "healthchecks_disabled"
	case section == hcSectionNotTransmitting:
		d.status, d.reason = hcStatusNotTransmitting, "not_transmitting"
	case cfg.HealthcheckMode == config.HealthcheckModeSelf:
		d.status = hcStatusSelf
		logging.DebugStep(logger, op, "healthchecks mode=self notify_urls=%t", selfNotifyURLConfigured(cfg))
		if selfNotifyURLConfigured(cfg) {
			d.reason = "self_notify_urls_configured"
		} else {
			d.effective = d.requested
		}
	default:
		secret, _ := identity.LoadNotifySecret(cfg.BaseDir)
		st, err := fetchDeliveryStatus(ctx, nil, cfg.ServerAPIHost, cfg.ServerID, secret)
		d.readAt = time.Now()
		if err != nil {
			d.status, d.reason = hcStatusUnknown, "delivery_status_unavailable"
			logging.DebugStep(logger, op, "healthchecks delivery unavailable: %v", err)
			break
		}
		d.status = deliveryStatusWord(st.State)
		confirmed := st.PolicyConfirmed(d.requested, enabledNotifyChannels(cfg))
		logging.DebugStep(logger, op, "healthchecks delivery state=%s alive_routes=%d/%d backup_routes=%d/%d policy_confirmed=%t reasons=%s",
			st.State, st.Checks.Alive.VerifiedDownRoutes, st.Checks.Alive.ConfiguredDownRoutes,
			st.Checks.Backup.VerifiedDownRoutes, st.Checks.Backup.ConfiguredDownRoutes, confirmed,
			strings.Join(st.ReasonCodes, ","))
		switch {
		case st.State != "ready":
			d.reason = "alerts_not_verified"
		case !confirmed:
			d.reason = "policy_unconfirmed"
		default:
			d.effective = d.requested
		}
	}
	if d.requested == config.NotifyOnAlways {
		d.effective, d.reason = config.NotifyOnAlways, ""
	}
	if d.effective != d.requested {
		logging.DebugStep(logger, op, "notification filter fallback=%s reason=%s", d.effective, d.reason)
	}
	return d
}

// logNotifyFilterInit writes the three INFO lines of the initialization block and hands the
// decision to the orchestrator, with the refresh used before dispatch.
func logNotifyFilterInit(opts backupModeOptions, orch *orchestrator.Orchestrator, section string) {
	cfg, logger := opts.cfg, opts.logger
	source := "default"
	if cfg != nil && cfg.NotifyOnSource != "" {
		source = cfg.NotifyOnSource
	}
	requested := negotiatedNotifyOn(cfg)
	logging.Info("Notification setting: NOTIFY_ON=%s", requested)
	logging.DebugStep(logger, "notifications init", "notify_on=%s source=%s", requested, source)
	if cfg != nil && cfg.HealthcheckEnabled {
		logging.DebugStep(logger, "notifications init", "healthchecks mode=%s", cfg.HealthcheckMode)
	}
	d := decideNotifyFilter(opts.ctx, cfg, logger, section, "notifications init")
	logging.Info("Healthchecks status: %s", d.status)
	logging.Info("Notification filter: %s", d.effective)
	if orch == nil {
		return
	}
	orch.SetNotifyFilter(func(ctx context.Context) string {
		if d.readAt.IsZero() || time.Since(d.readAt) < notifyFilterValidity {
			return d.effective
		}
		d = decideNotifyFilter(ctx, cfg, logger, section, "notifications dispatch")
		return d.effective
	})
}
