package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/serverbot"
)

// healthchecksSectionName is the SINGLE source of truth for the Phase-7 section name.
// It MUST be used for BOTH the dispatchNotifications entry (extensions.go) AND
// HealthchecksChannel.Name(); a mismatch would double-handle the section (the entries
// loop warns "enabled but not initialized" while the remainder loop also dispatches).
const healthchecksSectionName = "Healthchecks"

// HealthchecksChannel renders the always-visible "Healthchecks" line in Phase 7. It is
// a NotificationChannel in name only: it SENDS nothing. It reports the centralized/self
// monitoring state -- DECOUPLED from Telegram (gated on HEALTHCHECK_ENABLED and reading
// the identity secret directly, so disabling Telegram never hides it) -- and surfaces
// the portal magic-link.
//
// It is the SOLE display boundary for the magic-link: every link it prints passes
// through serverbot.SanitizeLoginURL, and it never registers the link as a log secret
// (it must stay visible). It NEVER aborts the run and never escalates a transport
// hiccup to WARNING/ERROR: the "active" state comes from the on-disk secret (the
// daemon's Reporter is the real pinger), and a failed best-effort mint degrades to a
// quiet Info line.
type HealthchecksChannel struct {
	cfg    *config.Config
	logger *logging.Logger

	// Seams for tests (nil-safe): loadSecret reads the on-disk per-server relay secret
	// (presence == "centralized monitoring provisioned"); mintLink best-effort fetches a
	// fresh portal magic-link (login=1) when this run's relay did not already capture one.
	loadSecret func(baseDir string) (string, error)
	mintLink   func(ctx context.Context, serverAPIHost, serverID, secret string) (string, error)
}

// NewHealthchecksChannel builds the Phase-7 healthchecks section from config.
func NewHealthchecksChannel(cfg *config.Config, logger *logging.Logger) *HealthchecksChannel {
	return &HealthchecksChannel{
		cfg:    cfg,
		logger: logger,
		loadSecret: func(baseDir string) (string, error) {
			return identity.LoadNotifySecret(baseDir)
		},
		mintLink: func(ctx context.Context, serverAPIHost, serverID, secret string) (string, error) {
			c, err := health.FetchCentralizedConfig(ctx, nil, serverAPIHost, serverID, secret, true)
			return c.LoginURL, err
		},
	}
}

// Name is the section label; MUST equal the dispatchNotifications entry (both use
// healthchecksSectionName) so the section is dispatched exactly once.
func (h *HealthchecksChannel) Name() string { return healthchecksSectionName }

// Notify reports the monitoring state and (centralized) the portal link. Errors are
// never returned as fatal: it always returns nil, and every failure degrades to a
// quiet Info -- it must not break a successful backup over a cosmetic section.
func (h *HealthchecksChannel) Notify(ctx context.Context, stats *BackupStats) error {
	h.info("%s: starting", healthchecksSectionName)
	if h.cfg == nil {
		return nil
	}

	if h.cfg.HealthcheckMode == "self" {
		h.info("✓ %s: self-hosted monitoring configured", healthchecksSectionName)
		setHealthcheckStatus(stats, "self")
		return nil
	}

	// Centralized. The presence of the on-disk relay secret is the source of truth for
	// "provisioned" -- NO mandatory per-run fetch (the daemon's Reporter already pings).
	secret := ""
	if h.loadSecret != nil {
		secret, _ = h.loadSecret(h.cfg.BaseDir)
	}
	if secret == "" {
		h.warn("%s: not configured (pair with the bot to provision centralized monitoring)", healthchecksSectionName)
		setHealthcheckStatus(stats, "not-configured")
		return nil
	}

	h.info("✓ %s: active (reporting to the monitor)", healthchecksSectionName)
	setHealthcheckStatus(stats, "active")

	// Portal magic-link: prefer the one THIS run's relay already captured (no network);
	// else best-effort mint one (the server returns it only until the user's first
	// login, so this self-limits). A mint failure is a QUIET Info, never a WARNING.
	link := ""
	if stats != nil {
		link = stats.HealthcheckLink
	}
	if link == "" && h.mintLink != nil {
		if minted, err := h.mintLink(ctx, h.cfg.TelegramServerAPIHost, h.cfg.ServerID, secret); err == nil {
			link = minted
		} else {
			h.info("%s: portal link not available this run", healthchecksSectionName)
		}
	}
	// SOLE display boundary: sanitize the RAW link before printing; never register it as
	// a secret (it must stay visible).
	if safe := serverbot.SanitizeLoginURL(link); safe != "" {
		h.info("Monitoring portal (single-use link, open it to set a password and configure alerts): %s", safe)
	}
	return nil
}

func (h *HealthchecksChannel) info(format string, args ...interface{}) {
	if h.logger != nil {
		h.logger.Info(format, args...)
	}
}

func (h *HealthchecksChannel) warn(format string, args ...interface{}) {
	if h.logger != nil {
		h.logger.Warning(format, args...)
	}
}

func setHealthcheckStatus(stats *BackupStats, state string) {
	if stats != nil {
		stats.HealthcheckStatus = state
	}
}
