package notify

import (
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
// public status AND, on a 200, the one-time notify_secret parsed from the body.
// The secret is registered with the logger BEFORE the body-preview line below, so
// the masker scrubs it from every subsequent log line for ALL callers.
func checkTelegramRegistrationWithSecret(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) (TelegramRegistrationStatus, string) {
	logTelegramRegistrationDebug(logger, "Telegram registration: start (serverAPIHost=%q serverID=%q len=%d)", serverAPIHost, serverID, len(serverID))

	if serverID == "" {
		logTelegramRegistrationDebug(logger, "Telegram registration: missing serverID (empty string)")
		return TelegramRegistrationStatus{
			Code:    0,
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
	logTelegramRegistrationDebug(logger, "Telegram registration: X-Proxsave-Version=%q", pv)
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
// server. Signature and behavior are unchanged; it does not provision the relay
// secret (see CheckTelegramRegistrationAndProvision).
func CheckTelegramRegistration(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) TelegramRegistrationStatus {
	status, _ := checkTelegramRegistrationWithSecret(ctx, serverAPIHost, serverID, logger)
	return status
}

// CheckTelegramRegistrationAndProvision performs the same handshake and, on a 200
// that carries a one-time notify_secret, persists it into the immutable identity
// store so the subsequent Send in this same run relays through the server instead
// of leaking the bot token. The returned status is identical to
// CheckTelegramRegistration's; provisioning is best-effort and idempotent, and a
// persist failure is logged and NEVER changes the returned status.
func CheckTelegramRegistrationAndProvision(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) TelegramRegistrationStatus {
	status, secret := checkTelegramRegistrationWithSecret(ctx, serverAPIHost, serverID, logger)
	if status.Code != 200 || secret == "" || strings.TrimSpace(baseDir) == "" {
		logTelegramRegistrationDebug(logger, "Telegram registration: skip provisioning (statusCode=%d secretPresent=%v baseDir=%q)", status.Code, secret != "", baseDir)
		return status
	}
	// Idempotent: never clobber a secret already on disk.
	if existing, _ := identity.LoadNotifySecret(baseDir, logger); existing != "" {
		logTelegramRegistrationDebug(logger, "Telegram registration: relay secret already persisted (len=%d), skipping provisioning", len(existing))
		return status
	}
	logTelegramRegistrationDebug(logger, "Telegram registration: provisioning relay secret -> %s", identity.NotifySecretPath(baseDir))
	if err := identity.PersistNotifySecret(ctx, baseDir, secret, logger); err != nil {
		logTelegramRegistrationDebug(logger, "Telegram registration: failed to persist provisioned relay secret: %v", err)
		return status // non-fatal: status unchanged
	}
	logTelegramRegistrationDebug(logger, "Telegram registration: provisioned and persisted per-server relay secret")
	return status
}
