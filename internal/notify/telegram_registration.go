package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
)

func logTelegramRegistrationDebug(logger *logging.Logger, format string, args ...interface{}) {
	if logger != nil {
		logger.Debug(format, args...)
	}
}

func truncateTelegramRegistrationBody(body []byte, max int) string {
	if max <= 0 {
		return ""
	}
	text := strings.TrimSpace(string(body))
	if len(text) > max {
		return text[:max] + "...(truncated)"
	}
	return text
}

// TelegramRegistrationStatus represents the result of the handshake with the centralized Telegram server.
type TelegramRegistrationStatus struct {
	Code    int
	Message string
	Error   error
}

// StatusCodeMissingServerID is a sentinel (non-HTTP, negative) status Code set when
// the local server identity is missing, so the classifier can give identity-specific
// guidance instead of generic connectivity copy. Real transport failures keep Code 0.
const StatusCodeMissingServerID = -1

// TelegramProvisionOutcome records what the best-effort persist+confirm phase did
// after a 200. It NEVER alters the returned registration Status. NotApplicable is
// the zero value, so a stub building TelegramRegistrationResult{Status:...} on a
// 200 classifies as a clean link.
type TelegramProvisionOutcome int

const (
	TelegramProvisionNotApplicable TelegramProvisionOutcome = iota // non-200; also the 200-only stub default (see type doc)
	TelegramProvisionNoToken                                       // 200, server issued no token
	TelegramProvisionConfirmed                                     // persisted AND confirmed
	TelegramProvisionPersistFailed                                 // empty baseDir, or persist failed -> no confirm
	TelegramProvisionConfirmFailed                                 // persisted, confirm POST failed
)

// TelegramRegistrationResult bundles the public status with the provision outcome.
type TelegramRegistrationResult struct {
	Status    TelegramRegistrationStatus
	Provision TelegramProvisionOutcome
}

// parseTelegramNotifySecret lifts the one-time relay secret out of a get-chat-id
// body. Best-effort: a non-JSON or secret-less body yields "".
func parseTelegramNotifySecret(body []byte) string {
	var parsed struct {
		NotifySecret string `json:"notify_secret"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.NotifySecret)
}

// checkTelegramRegistrationWithSecret is the single shared implementation of the
// get-chat-id handshake (5s timeout, X-Proxsave-Version header). It returns the
// public status AND, on a 200, the notify_secret parsed from the body. The secret
// is registered with the logger BEFORE the body-preview line below, so the masker
// scrubs it from every subsequent log line for ALL callers. When provision is
// true it also sends the provision-intent header so the server may (re)issue a
// token; the bare status probe passes false and never churns the relay secret.
func checkTelegramRegistrationWithSecret(ctx context.Context, serverAPIHost, serverID string, provision bool, logger *logging.Logger) (TelegramRegistrationStatus, string) {
	logTelegramRegistrationDebug(logger, "Telegram registration: start (serverAPIHost=%q serverID=%q len=%d)", serverAPIHost, serverID, len(serverID))

	if serverID == "" {
		logTelegramRegistrationDebug(logger, "Telegram registration: missing serverID (empty string)")
		return TelegramRegistrationStatus{
			Code:    StatusCodeMissingServerID,
			Message: "SERVER_ID not available",
			Error:   fmt.Errorf("server ID missing"),
		}, ""
	}

	baseHost := strings.TrimRight(serverAPIHost, "/")
	escapedServerID := url.QueryEscape(serverID)
	apiURL := fmt.Sprintf("%s/api/get-chat-id?server_id=%s", baseHost, escapedServerID)
	logTelegramRegistrationDebug(logger, "Telegram registration: apiURL=%q (baseHost=%q escapedServerID=%q)", apiURL, baseHost, escapedServerID)

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	logTelegramRegistrationDebug(logger, "Telegram registration: request timeout=%s", (5 * time.Second).String())

	req, err := http.NewRequestWithContext(reqCtx, "GET", apiURL, nil)
	if err != nil {
		logTelegramRegistrationDebug(logger, "Telegram registration: failed to create request: %v", err)
		return TelegramRegistrationStatus{Message: "Failed to create HTTP request", Error: err}, ""
	}
	pv := setProxsaveVersionHeader(req) // keep X-Proxsave-Version on ALL get-chat-id requests
	if provision {
		req.Header.Set(proxsaveProvisionHeader, "1")
	}
	logTelegramRegistrationDebug(logger, "Telegram registration: provisionIntent=%v X-Proxsave-Version=%q", provision, pv)
	logTelegramRegistrationDebug(logger, "Telegram registration: performing HTTP request (method=%s host=%s path=%s)", req.Method, req.URL.Host, req.URL.Path)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logTelegramRegistrationDebug(logger, "Telegram registration: request failed: %v", err)
		return TelegramRegistrationStatus{Message: "Connection failed", Error: err}, ""
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	// SECRET-IN-LOG GUARD: on a 200, parse + register the one-time secret BEFORE
	// the body-preview line so the masker scrubs it (and every later line).
	var secret string
	if resp.StatusCode == http.StatusOK {
		secret = parseTelegramNotifySecret(body)
		if secret != "" && logger != nil {
			logger.RegisterSecret(secret)
		}
		logTelegramRegistrationDebug(logger, "Telegram registration: 200 body parsed (notifySecretPresent=%v len=%d)", secret != "", len(secret))
	}

	logTelegramRegistrationDebug(logger, "Telegram registration: response status=%d bodyBytes=%d bodyPreview=%q", resp.StatusCode, len(body), truncateTelegramRegistrationBody(body, 200))

	switch resp.StatusCode {
	case 200:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 200 (active)")
		return TelegramRegistrationStatus{Code: 200, Message: "200 - Registration active"}, secret
	case 403:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 403 (bot not started / first contact)")
		return TelegramRegistrationStatus{Code: 403, Message: "403 - Start the bot and send the Server ID", Error: fmt.Errorf("%s", body)}, ""
	case 409:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 409 (missing registration)")
		return TelegramRegistrationStatus{Code: 409, Message: "409 - Registration missing on the bot", Error: fmt.Errorf("%s", body)}, ""
	case 422:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 422 (invalid server ID)")
		return TelegramRegistrationStatus{Code: 422, Message: "422 - Invalid Server ID", Error: fmt.Errorf("%s", body)}, ""
	case 426:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 426 (upgrade required)")
		return TelegramRegistrationStatus{
			Code:    426,
			Message: "426 - Upgrade ProxSave to v0.28.0 or later to complete pairing",
			Error:   fmt.Errorf("%s", body),
		}, ""
	default:
		logTelegramRegistrationDebug(logger, "Telegram registration: unexpected status %d", resp.StatusCode)
		return TelegramRegistrationStatus{
			Code:    resp.StatusCode,
			Message: fmt.Sprintf("%d - Unexpected response: %s", resp.StatusCode, string(body)),
			Error:   fmt.Errorf("unexpected status %d", resp.StatusCode),
		}, ""
	}
}

// CheckTelegramRegistration checks the registration status on the centralized
// server. Signature and behavior are unchanged; it is the bare status probe and
// sends NO provision-intent header, so it never issues/re-issues the relay secret
// (see CheckTelegramRegistrationAndProvision).
func CheckTelegramRegistration(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) TelegramRegistrationStatus {
	status, _ := checkTelegramRegistrationWithSecret(ctx, serverAPIHost, serverID, false, logger)
	return status
}

// CheckTelegramRegistrationAndProvision performs the same handshake AS a real
// provisioning call (provision-intent header set) and, on a 200 that carries a
// notify_secret, OVERWRITES the persisted secret and CONFIRMS it back to the
// server so the subsequent Send in this same run relays through the server
// instead of leaking the bot token. The server returns a token ONLY when it wants
// the client to (re)adopt it, so there is no idempotent skip. The returned
// Status is identical to CheckTelegramRegistration's; persist and confirm are
// best-effort and NEVER change the returned Status. The Provision field records
// what the persist+confirm phase did so callers (the install Check UIs) can show
// a distinct message without altering the registration status.
func CheckTelegramRegistrationAndProvision(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) TelegramRegistrationResult {
	status, secret := checkTelegramRegistrationWithSecret(ctx, serverAPIHost, serverID, true, logger)
	res := TelegramRegistrationResult{Status: status, Provision: TelegramProvisionNotApplicable}

	if status.Code != 200 {
		logTelegramRegistrationDebug(logger, "Telegram registration: skip provisioning (statusCode=%d)", status.Code)
		return res
	}
	if secret == "" {
		res.Provision = TelegramProvisionNoToken
		logTelegramRegistrationDebug(logger, "Telegram registration: 200 without token (nothing to provision)")
		return res
	}
	if strings.TrimSpace(baseDir) == "" {
		res.Provision = TelegramProvisionPersistFailed
		logTelegramRegistrationDebug(logger, "Telegram registration: 200 with token but empty baseDir (cannot persist)")
		return res
	}
	// Adopt-on-token-present: OVERWRITE (the server returns a token ONLY when it
	// wants the client to (re)adopt it). No idempotent skip, or a re-issue strands.
	// secret was already RegisterSecret'd in the helper on the 200 branch.
	logTelegramRegistrationDebug(logger, "Telegram registration: provisioning relay secret (overwrite) -> %s", identity.NotifySecretPath(baseDir))
	if err := identity.PersistNotifySecret(ctx, baseDir, secret, logger); err != nil {
		res.Provision = TelegramProvisionPersistFailed
		logTelegramRegistrationDebug(logger, "Telegram registration: failed to persist provisioned relay secret: %v", err)
		return res // non-fatal: do NOT confirm; server re-issues next run
	}
	if err := confirmTelegramRelaySecret(ctx, http.DefaultClient, serverAPIHost, serverID, secret, logger); err != nil {
		res.Provision = TelegramProvisionConfirmFailed
		logTelegramRegistrationDebug(logger, "Telegram registration: relay secret confirm failed (non-fatal): %v", err)
		return res
	}
	res.Provision = TelegramProvisionConfirmed
	return res
}

// confirmTelegramRelaySecret completes phase 2: POST the freshly adopted relay
// token back to /api/confirm-secret (X-Server-Auth: secret) so the server marks
// it confirmed and stops re-issuing. The client param lets the fetch path reuse
// the notifier's t.client (and its test transport); nil falls back to
// http.DefaultClient. Best-effort and NON-FATAL: failures are logged (never the
// secret) and the server re-issues next run. Transport errors are redacted.
func confirmTelegramRelaySecret(ctx context.Context, client *http.Client, serverAPIHost, serverID, secret string, logger *logging.Logger) error {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(secret) == "" || strings.TrimSpace(serverID) == "" {
		return fmt.Errorf("confirm: missing secret or serverID")
	}
	endpoint := strings.TrimRight(serverAPIHost, "/") + "/api/confirm-secret"
	body, err := json.Marshal(struct {
		ServerID string `json:"server_id"`
	}{ServerID: serverID})
	if err != nil {
		return fmt.Errorf("confirm: encode failed: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("confirm: create request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Server-Auth", secret)
	pv := setProxsaveVersionHeader(req)
	logTelegramRegistrationDebug(logger, "Telegram: confirm-secret POST %s (serverID=%q X-Proxsave-Version=%q)", endpoint, serverID, pv)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("confirm: request failed: %s", logging.RedactSecrets(err.Error(), secret))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		logTelegramRegistrationDebug(logger, "Telegram: confirm-secret accepted (HTTP 200)")
		return nil
	}
	return fmt.Errorf("confirm: server rejected (HTTP %d)", resp.StatusCode)
}
