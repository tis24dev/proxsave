package orchestrator

import (
	"context"
	"fmt"
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
// the identity secret directly, so disabling Telegram never hides it) -- and surfaces
// the portal magic-link.
//
// It is the SOLE display boundary for the magic-link: every link it prints passes
// through serverbot.SanitizeLoginURL, and it never registers the link as a log secret
// (it must stay visible). It NEVER aborts the run and never escalates a transport
// hiccup to WARNING/ERROR by itself.
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

	// Provisioned: report the REAL transmission status read from the daemon status file.
	h.renderTransmissionStatus(stats)

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

// renderTransmissionStatus reads the daemon status file and reports the latest REAL
// transmission outcome. It NEVER prints the success glyph unless a fresh heartbeat is
// backed by an ok-or-absent last backup outcome; anything else is an honest WARNING.
func (h *HealthchecksChannel) renderTransmissionStatus(stats *BackupStats) {
	var st health.Status
	if h.loadStatus != nil {
		// File read op: wrap in a shape-only debug envelope (mode only, never a path
		// secret). Reads are tolerant: missing/empty file yields a zero Status + nil err.
		done := logging.DebugStart(h.logger, "hc status read", "mode=%s", h.cfg.HealthcheckMode)
		loaded, err := h.loadStatus(h.cfg.BaseDir)
		done(err)
		if err != nil {
			// A corrupt/UNREADABLE file (bad JSON, permissions) is its OWN condition: we
			// can neither prove nor disprove transmission, so it must NOT fall through and
			// claim "daemon not running". Near-unreachable (writes are atomic tmp+rename;
			// missing/empty is the nil-error daemon-down path below). The raw error (may
			// carry the file path) stays debug-only; the WARNING keeps it out.
			h.debug("%s: status read failed (%v)", healthchecksSectionName, err)
			h.warn("⚠️ %s: monitoring status unavailable - could not read the status file", healthchecksSectionName)
			setHealthcheckStatus(stats, "status-unreadable")
			return
		}
		st = loaded
	}

	staleAfter := heartbeatStaleAfter(h.cfg.HealthcheckHeartbeatInterval)
	// Shape-only debug: booleans + threshold + the heartbeat reason code (a small enum,
	// never a URL/secret), so a support log shows exactly which branch was taken.
	hbReason, hbOK := "", false
	if st.Heartbeat != nil {
		hbReason, hbOK = st.Heartbeat.Reason, st.Heartbeat.OK
	}
	h.debug("%s: status shape heartbeat=%t hb_ok=%t hb_reason=%q run_finished=%t run_hang=%t stale_after=%s",
		healthchecksSectionName, st.Heartbeat != nil, hbOK, hbReason, st.RunFinished != nil, st.RunHang != nil, staleAfter)

	// Each condition below is a DISTINCT, single-cause line - never a confusing "A or B"
	// guess. Because the daemon records its very first beat immediately (even when it
	// cannot yet resolve a URL), a MISSING heartbeat record means the daemon is not
	// running, full stop; it is no longer conflated with "not provisioned" or "first run".
	if st.Heartbeat == nil {
		h.warn("⚠️ %s: the monitoring daemon is not running - no heartbeat recorded (start it to begin monitoring)", healthchecksSectionName)
		setHealthcheckStatus(stats, "daemon-down")
		return
	}

	now := h.clock()
	hbAge := now.Sub(time.Unix(st.Heartbeat.TS, 0))

	// Stale heartbeat: the last beat is older than 2x the configured interval, so the
	// daemon stopped beating (down, crashed, or wedged) after having run before.
	if hbAge > staleAfter {
		h.debug("%s: heartbeat age exceeds threshold (stale)", healthchecksSectionName)
		h.warn("⚠️ %s: heartbeat stale (last %s) - the monitoring daemon may have stopped", healthchecksSectionName, humanizeAge(hbAge))
		setHealthcheckStatus(stats, "stale")
		return
	}

	// Fresh beat that did NOT transmit. Two genuinely different causes, each its own line:
	if !st.Heartbeat.OK {
		// (a) no_url: the daemon is alive and beating but has no ping URL yet (centralized
		// pairing still pending, or the server was unreachable when it tried to resolve the
		// URLs). Not a monitor failure - it simply is not provisioned yet.
		if st.Heartbeat.Reason == health.ReasonNoURL {
			h.debug("%s: fresh heartbeat, reason=no_url (not provisioned)", healthchecksSectionName)
			h.warn("⚠️ %s: the monitoring daemon is running but not provisioned yet - no ping URL resolved (pairing pending, or the monitor was unreachable)", healthchecksSectionName)
			setHealthcheckStatus(stats, "not-provisioned")
			return
		}
		// (b) a real transmit failure: the daemon keeps a fresh TS forever, so the stale
		// branch never rescues this; the OK flag is the only live signal the monitor is
		// down RIGHT NOW. Err was already redacted by the Reporter (no URL/secret).
		h.debug("%s: fresh heartbeat but transmit failed=true", healthchecksSectionName)
		h.warn("⚠️ %s: the monitoring daemon is running but the monitor is unreachable: %s (last beat %s)", healthchecksSectionName, orNA(st.Heartbeat.Err), humanizeAge(hbAge))
		setHealthcheckStatus(stats, "unreachable")
		return
	}

	// Latest backup outcome = the NEWER of RunFinished/RunHang. Only its transmission
	// result matters: an old failure that a newer run superseded is not a live problem.
	outcome := newerPing(st.RunFinished, st.RunHang)
	if outcome != nil && !outcome.OK {
		outAge := now.Sub(time.Unix(outcome.TS, 0))
		h.debug("%s: last backup outcome failed=true", healthchecksSectionName)
		// outcome.Err was already redacted by the daemon's Reporter (redactURLErr): it
		// carries no URL or secret, so it is safe to surface verbatim here.
		h.warn("⚠️ %s: last backup outcome NOT transmitted: %s (%s)", healthchecksSectionName, orNA(outcome.Err), humanizeAge(outAge))
		setHealthcheckStatus(stats, "transmit-failed")
		return
	}

	// Healthy: fresh heartbeat, last outcome ok or absent. This is the ONLY success glyph.
	tail := ""
	if outcome != nil {
		tail = fmt.Sprintf(", last backup outcome %s", humanizeAge(now.Sub(time.Unix(outcome.TS, 0))))
	}
	h.info("✓ %s: transmitting to the monitor (heartbeat %s%s)", healthchecksSectionName, humanizeAge(hbAge), tail)
	setHealthcheckStatus(stats, "transmitting")
}

// clock returns the current time via the injectable seam (defaults to time.Now).
func (h *HealthchecksChannel) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// heartbeatStaleAfter returns the age past which the daemon heartbeat is treated as
// stale: 2x the configured interval. WHY the guards: an unset/zero interval falls back
// to the config default (5m), and a very small interval is floored so 2x stays at least
// ~2m -- a heartbeat that merely slipped one tick must not read as "daemon down".
func heartbeatStaleAfter(interval time.Duration) time.Duration {
	const floor = time.Minute
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if interval < floor {
		interval = floor
	}
	return 2 * interval
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

// newerPing returns whichever record has the larger timestamp (nil-tolerant). Used to
// pick the most recent backup outcome between RunFinished and RunHang.
func newerPing(a, b *health.PingRecord) *health.PingRecord {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.TS > a.TS:
		return b
	default:
		return a
	}
}

// humanizeAge renders an age as a coarse single-unit "<n><unit> ago" string. It is
// intentionally approximate: the exact value is debug-only, the human-facing line just
// needs to convey freshness. A sub-second or negative age (clock skew) reads "just now".
func humanizeAge(d time.Duration) string {
	if d < time.Second {
		return "just now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
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
