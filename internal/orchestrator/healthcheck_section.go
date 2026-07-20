package orchestrator

import (
	"context"
	"time"

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
// the identity secret directly, so disabling Telegram never hides it).
//
// It does NOT display the portal magic-link: it only CAPTURES the one this run's relay
// already fetched, or best-effort MINTS a fresh one, and STORES the RAW link on
// stats.HealthcheckLink. The link is NEVER registered as a log secret (it must stay
// visible), and it is carried RAW so the SOLE display boundary can sanitize it once.
// That display boundary (serverbot.SanitizeLoginURL + the "Healthchecks Portal" line) now
// lives in the backup epilogue (logMonitoringPortalLink in cmd/proxsave), which prints
// it right after the Server MAC Address line. This channel NEVER aborts the run and
// never escalates a transport hiccup to WARNING/ERROR by itself.
//
// The reported transmission state is REAL, not cosmetic: the DAEMON (a separate process,
// cmd/proxsave/daemon.go) is the only pinger, and it persists every ping outcome to the
// on-disk status file. This section runs inside the RUN process and transmits nothing,
// so it can only READ that file and report what the daemon actually managed to send. A
// missing/empty/corrupt file reads as "nothing transmitted yet" (honest for a first run
// or a stopped daemon), never a false success. A failed best-effort mint of the portal
// link degrades to a quiet Info line.
type HealthchecksChannel struct {
	cfg    *config.Config
	logger *logging.Logger

	// Seams for tests (nil-safe): loadSecret reads the on-disk per-server relay secret
	// (presence == "centralized monitoring provisioned"); mintLink best-effort fetches a
	// fresh portal magic-link (login=1) when this run's relay did not already capture one;
	// loadStatus reads the daemon's persisted ping outcomes; now clocks the freshness of
	// those outcomes (injectable so the staleness branches are deterministic in tests).
	loadSecret func(baseDir string) (string, error)
	mintLink   func(ctx context.Context, serverAPIHost, serverID, secret string) (string, error)
	loadStatus func(baseDir string) (health.Status, error)
	now        func() time.Time
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
		loadStatus: health.LoadStatus,
		now:        time.Now,
	}
}

// Name is the section label; MUST equal the dispatchNotifications entry (both use
// healthchecksSectionName) so the section is dispatched exactly once.
func (h *HealthchecksChannel) Name() string { return healthchecksSectionName }

// Notify reports the monitoring state and (centralized) captures/mints and STORES the
// RAW portal link on stats.HealthcheckLink (it does not display it; the epilogue does).
// Errors are never returned as fatal: it always returns nil, and every failure degrades
// to a quiet Info -- it must not break a successful backup over a cosmetic section.
func (h *HealthchecksChannel) Notify(ctx context.Context, stats *BackupStats) error {
	h.info("%s: starting", healthchecksSectionName)
	if h.cfg == nil {
		return nil
	}

	if h.cfg.HealthcheckMode == config.HealthcheckModeSelf {
		h.info("✓ %s: self-hosted", healthchecksSectionName)
		setHealthcheckStatus(stats, config.HealthcheckModeSelf)
		return nil
	}

	// Centralized. The presence of the on-disk relay secret is the source of truth for
	// "provisioned" -- NO mandatory per-run fetch (the daemon's Reporter already pings).
	secret := ""
	if h.loadSecret != nil {
		secret, _ = h.loadSecret(h.cfg.BaseDir)
	}
	if secret == "" {
		h.warn("⚠️ %s: not configured", healthchecksSectionName)
		setHealthcheckStatus(stats, "not-configured")
		return nil
	}

	// Provisioned: report the REAL transmission status read from the daemon status file,
	// refined with the systemd state so a running-but-silent daemon is not called "down".
	h.renderTransmissionStatus(ctx, stats)

	// Portal magic-link: prefer the one THIS run's relay already captured (no network);
	// else best-effort mint one (the server returns it only until the user's first
	// login, so this self-limits). A mint failure is a QUIET Info, never a WARNING.
	link := ""
	if stats != nil {
		link = stats.HealthcheckLink
	}
	if link == "" && h.mintLink != nil {
		// Network op: wrap in a shape-only debug envelope (no URL, no secret).
		done := logging.DebugStart(h.logger, "hc portal mint", "have_capture=false")
		minted, err := h.mintLink(ctx, h.cfg.ServerAPIHost, h.cfg.ServerID, secret)
		done(err)
		if err == nil {
			link = minted
		} else {
			h.info("%s: portal link unavailable", healthchecksSectionName)
		}
	}
	// STORE the link on stats so a MINTED link is carried to the epilogue (a captured link
	// is already there, validated by the dual-write gate). Validate the minted link at this
	// capture boundary (TrustedLoginURL: sanitize AND require the bot-server's own
	// registrable domain) so a hostile mint response cannot surface a foreign phishing host
	// to root. A foreign/unsafe link is dropped. The SOLE display boundary (sanitize +
	// print) is logMonitoringPortalLink in cmd/proxsave. Never register the link as a secret.
	if link != "" && stats != nil {
		if trusted := serverbot.TrustedLoginURL(link, defaultServerAPIHost); trusted != "" {
			stats.HealthcheckLink = trusted
		}
	}
	return nil
}

// renderTransmissionStatus reads the daemon status file and reports the latest REAL
// transmission outcome. It NEVER prints the success glyph unless a fresh heartbeat is
// backed by an ok-or-absent last backup outcome; anything else is an honest WARNING.
func (h *HealthchecksChannel) renderTransmissionStatus(ctx context.Context, stats *BackupStats) {
	// systemd is the authoritative existence signal; probe it up front so even an
	// unreadable status file still yields a real verdict (installed/active) instead of a
	// bare "unreadable" - matching the run-start init check exactly.
	presence := probeDaemonPresence(ctx)

	var st health.Status
	if h.loadStatus != nil {
		// File read op: wrap in a shape-only debug envelope (mode only, never a path
		// secret). Reads are tolerant: missing/empty file yields a zero Status + nil err.
		done := logging.DebugStart(h.logger, "hc status read", "mode=%s", h.cfg.HealthcheckMode)
		loaded, err := h.loadStatus(h.cfg.BaseDir)
		done(err)
		if err != nil {
			// A corrupt/UNREADABLE file (bad JSON, permissions) can neither prove nor
			// disprove transmission from the file alone. When systemd was probed it still
			// speaks (installed/active/running-not-reporting), keeping this line consistent
			// with the init check; only when systemd is unavailable is the state truly
			// unknown. The raw error (may carry the file path) stays debug-only.
			h.debug("%s: status read failed (%v)", healthchecksSectionName, err)
			if !presence.Probed {
				h.warn("⚠️ %s: status file unreadable", healthchecksSectionName)
				setHealthcheckStatus(stats, "status-unreadable")
				return
			}
			d := health.RefineWithPresence(health.Diagnosis{State: health.TxNoHeartbeat}, presence)
			h.renderTransmissionState(d, stats)
			return
		}
		st = loaded
	}

	// SINGLE source of truth: the SAME health.Diagnose the run-start init check uses, then
	// refined with the authoritative systemd state so the init verdict and this line can
	// never disagree and a running-but-silent daemon reads as such, not as "down".
	d := health.Diagnose(st, h.cfg.HealthcheckHeartbeatInterval, h.clock())
	d = health.RefineWithPresence(d, presence)
	h.renderTransmissionState(d, stats)
}

// renderTransmissionState renders ONE synthetic WARNING/OK line for a (presence-refined)
// diagnosis. Split out so the normal and unreadable-but-systemd-probed paths render the
// exact same vocabulary.
func (h *HealthchecksChannel) renderTransmissionState(d health.Diagnosis, stats *BackupStats) {
	// Shape-only debug: the diagnosed state + booleans, never a URL/secret.
	h.debug("%s: diagnose state=%s daemon_up=%t hb_age=%s has_outcome=%t",
		healthchecksSectionName, d.State, d.DaemonUp, d.HbAge, d.HasOutcome)

	// Each state is a DISTINCT, single-cause, SYNTHETIC line - a bare fact, never an
	// instruction or a parenthetical explanation. d.Err is already Reporter-redacted.
	switch d.State {
	case health.TxNotInstalled:
		h.warn("⚠️ %s: daemon not installed", healthchecksSectionName)
		setHealthcheckStatus(stats, "not-installed")
	case health.TxNotActive:
		h.warn("⚠️ %s: daemon not running", healthchecksSectionName)
		setHealthcheckStatus(stats, "not-active")
	case health.TxRunningNoReport:
		h.warn("⚠️ %s: daemon running, not reporting", healthchecksSectionName)
		setHealthcheckStatus(stats, "running-not-reporting")
	case health.TxNoHeartbeat:
		h.warn("⚠️ %s: daemon not running", healthchecksSectionName)
		setHealthcheckStatus(stats, "daemon-down")
	case health.TxStale:
		h.warn("⚠️ %s: daemon stale (last beat %s)", healthchecksSectionName, health.HumanizeAge(d.HbAge))
		setHealthcheckStatus(stats, "stale")
	case health.TxNotProvisioned:
		h.warn("⚠️ %s: not provisioned (no ping URL)", healthchecksSectionName)
		setHealthcheckStatus(stats, "not-provisioned")
	case health.TxUnreachable:
		h.warn("⚠️ %s: monitor unreachable: %s", healthchecksSectionName, orNA(d.Err))
		setHealthcheckStatus(stats, "unreachable")
	case health.TxTransmitFailed:
		h.warn("⚠️ %s: last outcome not transmitted: %s", healthchecksSectionName, orNA(d.Err))
		setHealthcheckStatus(stats, "transmit-failed")
	default: // health.TxTransmitting - the ONLY success glyph
		h.info("✓ %s: transmitting (last beat %s)", healthchecksSectionName, health.HumanizeAge(d.HbAge))
		setHealthcheckStatus(stats, "transmitting")
	}
}

// clock returns the current time via the injectable seam (defaults to time.Now).
func (h *HealthchecksChannel) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// orNA renders a possibly-empty error string as "unspecified error" so a WARNING line
// never trails off into an empty value (defensive: a non-no_url failure always carries
// an Err today, but a future recorder that forgets one must not print a blank reason).
func orNA(s string) string {
	if s == "" {
		return "unspecified error"
	}
	return s
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

func (h *HealthchecksChannel) debug(format string, args ...interface{}) {
	if h.logger != nil {
		h.logger.Debug(format, args...)
	}
}

func setHealthcheckStatus(stats *BackupStats, state string) {
	if stats != nil {
		stats.HealthcheckStatus = state
	}
}
