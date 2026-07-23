package notify

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/serverbot"
)

// provisionTimeout bounds the relay-provision POST; callers retry on the next run.
const provisionTimeout = 5 * time.Second

// relayProvisionMaxRetryAfter bounds a server-supplied Retry-After. The relay's
// longest rolling admission window is 24h; a larger value is treated as 24h so a
// malformed response cannot suppress self-healing indefinitely.
const relayProvisionMaxRetryAfter = 24 * time.Hour

// RelayProvisionRateLimitError is returned for HTTP 429. RetryAfter is zero when
// the header is absent/invalid; callers then keep their existing local retry floor.
// A typed error lets the daemon honor server backpressure without exposing or
// logging the untrusted response body.
type RelayProvisionRateLimitError struct {
	RetryAfter time.Duration
}

func (e *RelayProvisionRateLimitError) Error() string {
	if e != nil && e.RetryAfter > 0 {
		return fmt.Sprintf("relay provision: rate limited (HTTP 429; retry after %s)", e.RetryAfter)
	}
	return "relay provision: rate limited (HTTP 429)"
}

func parseRelayProvisionRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > int64(relayProvisionMaxRetryAfter/time.Second) {
			return relayProvisionMaxRetryAfter
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	d := when.Sub(now)
	if d <= 0 {
		return 0
	}
	if d > relayProvisionMaxRetryAfter {
		return relayProvisionMaxRetryAfter
	}
	return d
}

// relayProvisionResponse is the JSON of POST /api/relay/provision: a 201 carries
// notify_secret; a 200 carries status=="already_provisioned". The RELAY_* error field
// is deliberately NOT decoded here (untrusted text); the status code drives the logic.
type relayProvisionResponse struct {
	NotifySecret string `json:"notify_secret"`
	Status       string `json:"status"`
}

// provisionViaRelay POSTs the dedicated, Telegram-independent provisioning endpoint
// POST /api/relay/provision (unauthenticated: the endpoint ISSUES the per-server token)
// with {server_id} plus the shared X-Proxsave-Version header. It returns:
//
//	(secret, false, nil) on 201 with a notify_secret          -> adopt + confirm
//	("", true, nil)      on 200 already_provisioned            -> nothing to adopt
//	("", false, err)     on 429 (retryable) / any other code / transport error
//
// The untrusted response body is NEVER embedded in the returned error (only the status
// code), and the issued secret is registered with the logger's masker before any later
// log line can preview it.
func provisionViaRelay(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) (string, bool, error) {
	resp, err := serverbot.New(serverAPIHost, http.DefaultClient, logger).Do(ctx, serverbot.Request{
		Method: http.MethodPost,
		Path:   "/api/relay/provision",
		Body: struct {
			ServerID string `json:"server_id"`
		}{ServerID: serverID},
		Timeout:  provisionTimeout,
		MaxBytes: 8192,
	})
	if err != nil {
		return "", false, fmt.Errorf("relay provision: request failed: %w", err)
	}
	switch resp.Status {
	case http.StatusCreated: // 201: fresh token issued
		var body relayProvisionResponse
		if jerr := resp.JSON(&body); jerr != nil {
			return "", false, fmt.Errorf("relay provision: bad 201 JSON: %w", jerr)
		}
		secret := strings.TrimSpace(body.NotifySecret)
		if secret == "" {
			return "", false, fmt.Errorf("relay provision: 201 without notify_secret")
		}
		if logger != nil {
			logger.RegisterSecret(secret)
		}
		return secret, false, nil
	case http.StatusOK: // 200: already provisioned + confirmed server-side
		var body relayProvisionResponse
		if jerr := resp.JSON(&body); jerr != nil {
			return "", false, fmt.Errorf("relay provision: bad 200 JSON: %w", jerr)
		}
		if body.Status != "already_provisioned" {
			return "", false, fmt.Errorf("relay provision: unexpected 200 status")
		}
		// No new token is minted. We have nothing to adopt this run; a lost local
		// secret is not recoverable here (the row self-heals via purge + recreation
		// later, or a fresh recreation re-issues). Do NOT try to read a token that
		// is not in the body.
		return "", true, nil
	case http.StatusTooManyRequests: // 429: rolling/total admission cap
		// A TEMPORARY refusal. Persist nothing; the caller retries on the next run.
		return "", false, &RelayProvisionRateLimitError{
			RetryAfter: parseRelayProvisionRetryAfter(
				resp.Header.Get("Retry-After"), time.Now()),
		}
	default:
		// 400 / 422 / 426 / 500 / 503 / ... The body is untrusted, so the error
		// carries only the status code.
		return "", false, fmt.Errorf("relay provision: unexpected status %d", resp.Status)
	}
}

// ProvisionRelaySecret provisions this host's per-server relay token WITHOUT any Telegram
// pairing, via the dedicated POST /api/relay/provision endpoint: on a 201 it persists the
// issued token (immutable identity file) and confirms it (/api/confirm-secret) so the
// centralized healthcheck fetch and the notify relay can authenticate. This is the generic
// provisioning entry point used by the healthcheck self-heal hooks and the SERVER_PARKED
// recovery path.
//
// The legacy /api/get-chat-id handshake is intentionally NOT used here: for a chat-less
// (Telegram-independent) server it returns 409 before ever issuing a token, so Option A
// could never provision through it. get-chat-id remains the Telegram/v0.29 path unchanged.
//
// Returns provisioned=true once the secret is on disk (a confirm failure is NON-FATAL: the
// hash the server stored on issuance already authenticates the fetch, and the server
// re-confirms on the next run). Every other outcome returns (false, err-or-nil):
//   - a non-201 refusal (429 retryable, or another code) -> (false, err)
//   - a 200 already_provisioned (nothing to adopt)        -> (false, nil)
//   - a 201 with no token                                 -> (false, err)
//   - an empty baseDir                                    -> (false, err)
//   - an issued secret < identity.NotifySecretMinLen runes -> (false, err) [defensive floor]
//   - a persist failure                                   -> (false, err)
//
// Callers MUST treat a non-nil err as "retry later" and NEVER block on it.
func ProvisionRelaySecret(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) (bool, error) {
	if strings.TrimSpace(baseDir) == "" {
		return false, fmt.Errorf("relay provision: empty baseDir, cannot persist relay secret")
	}
	// Serialize issue -> persist -> confirm across processes. Hook a (installer) and
	// hook b (an enable-now daemon) can run concurrently against the same server_id; two
	// DISTINCT minted secrets would strand the host (last-write-wins on disk vs the
	// server's confirm-locks-reissue). An exclusive advisory lock on the identity dir
	// makes exactly one minter win; the loser adopts the winner's on-disk secret below.
	unlock, err := identity.LockNotifySecret(baseDir)
	if err != nil {
		return false, fmt.Errorf("relay provision: lock: %w", err)
	}
	defer unlock()
	// Re-check UNDER the lock: if a concurrent minter already persisted a secret, adopt it
	// instead of minting a competing one. A real read error (the file exists but could not
	// be read) is surfaced rather than swallowed; missing/empty/malformed yields ("", nil)
	// per the loader contract, so we fall through and provision.
	if s, err := identity.LoadNotifySecret(baseDir); err != nil {
		return false, fmt.Errorf("relay provision: re-check load: %w", err)
	} else if strings.TrimSpace(s) != "" {
		logTelegramRegistrationDebug(logger, "relay provision: secret already present under lock (adopting; nothing to provision)")
		return false, nil
	}

	secret, alreadyProvisioned, err := provisionViaRelay(ctx, serverAPIHost, serverID, logger)
	if err != nil {
		return false, err
	}
	if alreadyProvisioned {
		logTelegramRegistrationDebug(logger, "relay provision: server reports already provisioned (nothing to adopt)")
		return false, nil
	}
	// Defensive length floor (shared with the persistence sink): an issued secret shorter
	// than identity.NotifySecretMinLen is not masked in logs, so refuse it rather than write
	// an unmaskable value to disk. (Server format is 19 chars, so a real secret never trips
	// this.)
	if len([]rune(secret)) < identity.NotifySecretMinLen {
		return false, fmt.Errorf("relay provision: issued secret too short (<%d runes); refusing to persist", identity.NotifySecretMinLen)
	}
	if err := identity.PersistNotifySecret(ctx, baseDir, secret, logger); err != nil {
		return false, fmt.Errorf("relay provision: persist failed: %w", err)
	}
	if err := confirmTelegramRelaySecret(ctx, http.DefaultClient, serverAPIHost, serverID, secret, logger); err != nil {
		// Non-fatal: the secret IS persisted and the issuance hash already authenticates the
		// fetch; the server re-confirms on the next run. confirmTelegramRelaySecret already
		// redacts the secret from its error text.
		logTelegramRegistrationDebug(logger, "relay provision: confirm failed (non-fatal, secret persisted): %v", err)
		return true, nil
	}
	logTelegramRegistrationDebug(logger, "relay provision: secret persisted and confirmed")
	return true, nil
}
