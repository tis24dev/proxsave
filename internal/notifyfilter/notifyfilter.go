// Package notifyfilter decides the NOTIFY_ON threshold a run applies. It is shared by the run
// (cmd/proxsave), which filters the notifications with it, and by the Healthchecks check screen,
// which shows the same decision, so the two can never give different answers.
//
// NOTIFY_ON below always lets a clean run go silent, and silence is only safe when something
// else alarms when a run never happens: the Healthchecks monitor. So a run applies the threshold
// only when that monitor is confirmed to reach the operator, and notifies every outcome
// otherwise:
//   - centralized: the relay reports the alive and backup checks covered by a verified DOWN
//     route (state ready) and has applied this exact policy to the notify checks;
//   - self: both ping URLs valid, the daemon transmitting, and no notify URL configured (a
//     notify check the operator runs on a period would go DOWN on every filtered run).
package notifyfilter

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
)

// Healthchecks status words shown on the run's INFO line (maintainer-approved set).
const (
	StatusReady           = "ready"
	StatusNotConfigured   = "not configured"
	StatusNotVerified     = "not verified"
	StatusDegraded        = "degraded"
	StatusUnknown         = "unknown"
	StatusDisabled        = "disabled"
	StatusNotTransmitting = "not transmitting"
	StatusSelf            = "self"
)

// How the Healthchecks section stands when the decision is taken.
const (
	SectionDisabled        = "disabled"
	SectionNotTransmitting = "not_transmitting"
	SectionInitialized     = "initialized"
)

// Reasons a decision falls back to always (DEBUG vocabulary).
const (
	ReasonHealthchecksDisabled = "healthchecks_disabled"
	ReasonNotTransmitting      = "not_transmitting"
	ReasonSelfNotifyChecks     = "self_notify_urls_configured"
	ReasonStatusUnavailable    = "delivery_status_unavailable"
	ReasonAlertsNotVerified    = "alerts_not_verified"
	ReasonPolicyUnconfirmed    = "policy_unconfirmed"
)

// Validity is how long a relay answer may be reused for the decision before dispatch, at most: the
// relay's own validity (valid_for_seconds less age_seconds) can make it shorter.
const Validity = 120 * time.Second

// Seams for tests.
var (
	FetchDeliveryStatus = health.FetchDeliveryStatus
	// Now is the clock the relay answer's age is measured with.
	Now = time.Now
)

// Decision is the threshold a run applies and why.
type Decision struct {
	Requested string // NOTIFY_ON as negotiated: the configured value when valid, always otherwise
	Status    string // Healthchecks status word
	Effective string // the threshold applied
	Reason    string // why Effective differs from Requested, "" when it does not
	ReadAt    time.Time
	ValidFor  time.Duration
}

// Negotiated is the NOTIFY_ON value the daemon sends to the relay and the run requests: the
// configured one when valid, always otherwise.
func Negotiated(cfg *config.Config) string {
	if cfg == nil || !config.IsValidNotifyOn(cfg.NotifyOn) || cfg.NotifyOn == "" {
		return config.NotifyOnAlways
	}
	return cfg.NotifyOn
}

// EnabledChannels returns the lowercased notification-channel names enabled in cfg, sorted: the
// authoritative set the daemon sends to the relay. Always non-nil.
func EnabledChannels(cfg *config.Config) []string {
	out := []string{}
	if cfg == nil {
		return out
	}
	if cfg.EmailEnabled {
		out = append(out, "email")
	}
	if cfg.TelegramEnabled {
		out = append(out, "telegram")
	}
	if cfg.GotifyEnabled {
		out = append(out, "gotify")
	}
	if cfg.WebhookEnabled {
		out = append(out, "webhook")
	}
	sort.Strings(out)
	return out
}

// SelfNotifyChecksConfigured reports whether any HEALTHCHECK_NOTIFY_* variable is set.
func SelfNotifyChecksConfigured(cfg *config.Config) bool {
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

func statusWord(state string) string {
	switch state {
	case "ready":
		return StatusReady
	case "not_configured":
		return StatusNotConfigured
	case "unverified":
		return StatusNotVerified
	case "degraded":
		return StatusDegraded
	default:
		return StatusUnknown
	}
}

// Decide takes the decision; op names the DEBUG operation ("notifications init" or
// "notifications dispatch"). A nil logger writes no DEBUG line.
func Decide(ctx context.Context, cfg *config.Config, logger *logging.Logger, section, op string) Decision {
	d := Decision{Requested: Negotiated(cfg), Effective: config.NotifyOnAlways}
	switch {
	case cfg == nil || section == SectionDisabled:
		d.Status, d.Reason = StatusDisabled, ReasonHealthchecksDisabled
	case section == SectionNotTransmitting:
		d.Status, d.Reason = StatusNotTransmitting, ReasonNotTransmitting
	case cfg.HealthcheckMode == config.HealthcheckModeSelf:
		d.Status = StatusSelf
		logging.DebugStep(logger, op, "healthchecks mode=self notify_urls=%t", SelfNotifyChecksConfigured(cfg))
		if SelfNotifyChecksConfigured(cfg) {
			d.Reason = ReasonSelfNotifyChecks
		} else {
			d.Effective = d.Requested
		}
	default:
		secret, _ := identity.LoadNotifySecret(cfg.BaseDir)
		st, err := FetchDeliveryStatus(ctx, nil, cfg.ServerAPIHost, cfg.ServerID, secret)
		d.ReadAt, d.ValidFor = Now(), Validity
		if err != nil {
			d.Status, d.Reason = StatusUnknown, ReasonStatusUnavailable
			logging.DebugStep(logger, op, "healthchecks delivery unavailable: %v", err)
			break
		}
		d.Status = statusWord(st.State)
		d.ValidFor = st.Remaining(Validity)
		confirmed := st.PolicyConfirmed(d.Requested, EnabledChannels(cfg))
		logging.DebugStep(logger, op, "healthchecks delivery state=%s alive_routes=%d/%d backup_routes=%d/%d policy_confirmed=%t reasons=%s",
			st.State, st.Checks.Alive.VerifiedDownRoutes, st.Checks.Alive.ConfiguredDownRoutes,
			st.Checks.Backup.VerifiedDownRoutes, st.Checks.Backup.ConfiguredDownRoutes, confirmed,
			strings.Join(st.ReasonCodes, ","))
		switch {
		case st.State != "ready":
			d.Reason = ReasonAlertsNotVerified
		case !confirmed:
			d.Reason = ReasonPolicyUnconfirmed
		default:
			d.Effective = d.Requested
		}
	}
	if d.Requested == config.NotifyOnAlways {
		d.Effective, d.Reason = config.NotifyOnAlways, ""
	}
	if d.Effective != d.Requested {
		logging.DebugStep(logger, op, "notification filter fallback=%s reason=%s", d.Effective, d.Reason)
	}
	return d
}
