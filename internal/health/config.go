package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/serverbot"
)

// fetchTimeout bounds the centralized-config GET; the daemon retries with backoff.
const fetchTimeout = 5 * time.Second

var (
	// ErrHCAuth: the per-server secret was rejected (missing/stale). Definitive:
	// re-fetch will keep failing until the secret is re-provisioned.
	ErrHCAuth = errors.New("healthcheck config: auth rejected")
	// ErrHCUnknown: the server does not know this server_id (not registered).
	ErrHCUnknown = errors.New("healthcheck config: server unknown")
	// ErrHCNotReady: provisioning has not completed yet; retry later.
	ErrHCNotReady = errors.New("healthcheck config: provisioning not ready")
	// ErrHCDisabled: healthcheck provisioning is turned off on the server.
	ErrHCDisabled = errors.New("healthcheck config: disabled on server")
)

// CentralizedConfig is the proxsave_server's answer for a client's two ping URLs.
// LoginURL is a fresh single-use portal magic-link, populated only when the caller
// requested it (includeLogin) AND the server minted one; otherwise it stays "".
type CentralizedConfig struct {
	Mode        string `json:"mode"`
	AliveURL    string `json:"alive_ping_url"`
	BackupURL   string `json:"backup_ping_url"`
	ProjectCode string `json:"project_code"`
	LoginURL    string `json:"login_url"`
	// Checks carries additive, OPTIONAL per-sensor ping URLs beyond the two frozen
	// alive/backup keys (Fase 1: {"updates":"<ping-url>"}). An old server omits it; the
	// completeness check below still requires only alive+backup, so a new client against
	// an old server simply resolves no updates URL. Omitted when empty.
	Checks map[string]string `json:"checks,omitempty"`
}

// Check-name vocabulary, shared by the Reporter url map, the Status.Records-derived
// sensor names, and the CentralizedConfig.Checks wire keys. The rule is: the wire/client
// key is the server hc slug with the leading "proxsave-" stripped once. alive/backup ride
// the FROZEN top-level alive_ping_url/backup_ping_url wire fields, NOT the Checks map, so
// their keys are internal-only (the url map and Has*URL accessors). updates + notify-<ch>
// travel inside Checks.
const (
	CheckKeyAlive        = "alive"
	CheckKeyBackup       = "backup"
	CheckKeyUpdates      = "updates"
	CheckKeyNotifyPrefix = "notify-" // per-notification-channel check key prefix (notify-email, ...)
	SensorProxsavePrefix = "proxsave-"
)

// CheckKeyNotify returns the Checks/url-map key for a notification channel (lowercased),
// e.g. CheckKeyNotify("email") == "notify-email". The server hc slug is
// SensorProxsavePrefix+CheckKeyNotify(ch) = "proxsave-notify-email".
func CheckKeyNotify(channel string) string {
	return CheckKeyNotifyPrefix + strings.ToLower(strings.TrimSpace(channel))
}

// serverError is the {"error":...} envelope the proxsave_server returns on failure.
type serverError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// FetchCentralizedConfig asks the proxsave_server for this client's two healthchecks
// ping URLs, reusing the SAME identity the client already uses for /api/notify:
// GET {serverAPIHost}/api/healthcheck/config?server_id=<id> with X-Server-Auth =
// the per-server secret and X-Proxsave-Version. On the server side this lazily
// provisions/self-heals, so a first call after pairing returns ready URLs.
//
// serverAPIHost is the base (e.g. https://bot.proxsave.dev); serverID is the
// client's identity; secret is the raw notify secret (no signing, mirroring the
// existing notify calls). Returns a typed error on the known failure codes so the
// daemon can decide retry vs. give-up. When includeLogin is set, the request asks
// the server (?login=1) to also mint and return a portal magic-link (LoginURL) -
// used by the install-time setup screen; the daemon's poll passes false so it
// never triggers the server-side mint.
func FetchCentralizedConfig(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string, includeLogin bool) (CentralizedConfig, error) {
	q := url.Values{"server_id": {serverID}}
	if includeLogin {
		q.Set("login", "1")
	}
	// Transport + auth (host normalize, X-Server-Auth, X-Proxsave-Version, timeout,
	// bounded read, error redaction) is the shared serverbot brick; the endpoint
	// vocabulary below (status map + typed errors + completeness check) stays here.
	resp, err := serverbot.New(serverAPIHost, client, nil).Do(ctx, serverbot.Request{
		Method:   http.MethodGet,
		Path:     "/api/healthcheck/config",
		Query:    q,
		Secret:   secret,
		Timeout:  fetchTimeout,
		MaxBytes: 8192,
	})
	if err != nil {
		// Transport failure (build/dial/read), already URL-stripped + secret-masked by
		// serverbot. Note: unlike the pre-serverbot code (which swallowed a body-read
		// error and still returned the typed sentinel), a body-read failure now surfaces
		// as this transport error -> the caller retries instead of treating it as
		// definitive. Benign: the affected 4xx/503 bodies are tiny, so the window (status
		// received but body transfer broken) is vanishingly small, and retry is the safer
		// degradation.
		return CentralizedConfig{}, err
	}

	switch resp.Status {
	case http.StatusOK:
		var cfg CentralizedConfig
		if err := resp.JSON(&cfg); err != nil {
			return CentralizedConfig{}, fmt.Errorf("healthcheck config: bad JSON: %w", err)
		}
		if cfg.AliveURL == "" || cfg.BackupURL == "" {
			return CentralizedConfig{}, fmt.Errorf("healthcheck config: incomplete response")
		}
		return cfg, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return CentralizedConfig{}, ErrHCAuth
	case http.StatusNotFound:
		return CentralizedConfig{}, ErrHCUnknown
	case http.StatusServiceUnavailable:
		// The server returns HC_DISABLED (feature off) or HC_NOT_READY (provisioning
		// not done yet). Distinguish so the daemon logs the right thing.
		var e serverError
		_ = resp.JSON(&e)
		if e.Error == "HC_DISABLED" {
			return CentralizedConfig{}, ErrHCDisabled
		}
		return CentralizedConfig{}, ErrHCNotReady
	default:
		return CentralizedConfig{}, fmt.Errorf("healthcheck config: HTTP %d", resp.Status)
	}
}
