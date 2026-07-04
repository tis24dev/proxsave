package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/version"
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
type CentralizedConfig struct {
	Mode        string `json:"mode"`
	AliveURL    string `json:"alive_ping_url"`
	BackupURL   string `json:"backup_ping_url"`
	ProjectCode string `json:"project_code"`
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
// daemon can decide retry vs. give-up.
func FetchCentralizedConfig(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string) (CentralizedConfig, error) {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	endpoint := strings.TrimRight(serverAPIHost, "/") + "/api/healthcheck/config?server_id=" + url.QueryEscape(serverID)

	reqCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return CentralizedConfig{}, err
	}
	req.Header.Set("X-Server-Auth", secret)
	req.Header.Set("X-Proxsave-Version", version.String())

	resp, err := client.Do(req)
	if err != nil {
		return CentralizedConfig{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	switch resp.StatusCode {
	case http.StatusOK:
		var cfg CentralizedConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
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
		_ = json.Unmarshal(raw, &e)
		if e.Error == "HC_DISABLED" {
			return CentralizedConfig{}, ErrHCDisabled
		}
		return CentralizedConfig{}, ErrHCNotReady
	default:
		return CentralizedConfig{}, fmt.Errorf("healthcheck config: HTTP %d", resp.StatusCode)
	}
}
