package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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

// CheckTelegramRegistration checks the registration status on the centralized server.
func CheckTelegramRegistration(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) TelegramRegistrationStatus {
	logTelegramRegistrationDebug(logger, "Telegram registration: start (serverAPIHost=%q serverID=%q len=%d)", serverAPIHost, serverID, len(serverID))

	if serverID == "" {
		logTelegramRegistrationDebug(logger, "Telegram registration: missing serverID (empty string)")
		return TelegramRegistrationStatus{
			Code:    0,
			Message: "SERVER_ID not available",
			Error:   fmt.Errorf("server ID missing"),
		}
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
		return TelegramRegistrationStatus{Message: "Failed to create HTTP request", Error: err}
	}
	logTelegramRegistrationDebug(logger, "Telegram registration: performing HTTP request (method=%s host=%s path=%s)", req.Method, req.URL.Host, req.URL.Path)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logTelegramRegistrationDebug(logger, "Telegram registration: request failed: %v", err)
		return TelegramRegistrationStatus{Message: "Connection failed", Error: err}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	logTelegramRegistrationDebug(logger, "Telegram registration: response status=%d bodyBytes=%d bodyPreview=%q", resp.StatusCode, len(body), truncateTelegramRegistrationBody(body, 200))

	switch resp.StatusCode {
	case 200:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 200 (active)")
		return TelegramRegistrationStatus{Code: 200, Message: "200 - Registration active"}
	case 403:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 403 (bot not started / first contact)")
		return TelegramRegistrationStatus{Code: 403, Message: "403 - Start the bot and send the Server ID", Error: fmt.Errorf("%s", body)}
	case 409:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 409 (missing registration)")
		return TelegramRegistrationStatus{Code: 409, Message: "409 - Registration missing on the bot", Error: fmt.Errorf("%s", body)}
	case 422:
		logTelegramRegistrationDebug(logger, "Telegram registration: status 422 (invalid server ID)")
		return TelegramRegistrationStatus{Code: 422, Message: "422 - Invalid Server ID", Error: fmt.Errorf("%s", body)}
	default:
		logTelegramRegistrationDebug(logger, "Telegram registration: unexpected status %d", resp.StatusCode)
		return TelegramRegistrationStatus{
			Code:    resp.StatusCode,
			Message: fmt.Sprintf("%d - Unexpected response: %s", resp.StatusCode, string(body)),
			Error:   fmt.Errorf("unexpected status %d", resp.StatusCode),
		}
	}
}
