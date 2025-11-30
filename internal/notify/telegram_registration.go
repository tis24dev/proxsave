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

// TelegramRegistrationStatus rappresenta l'esito dell'handshake con il server Telegram centralizzato.
type TelegramRegistrationStatus struct {
	Code    int
	Message string
	Error   error
}

// CheckTelegramRegistration verifica lo stato della registrazione sul server centralizzato.
func CheckTelegramRegistration(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) TelegramRegistrationStatus {
	if serverID == "" {
		return TelegramRegistrationStatus{
			Code:    0,
			Message: "SERVER_ID non disponibile",
			Error:   fmt.Errorf("server ID missing"),
		}
	}

	apiURL := fmt.Sprintf("%s/api/get-chat-id?server_id=%s",
		strings.TrimRight(serverAPIHost, "/"),
		url.QueryEscape(serverID))

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", apiURL, nil)
	if err != nil {
		return TelegramRegistrationStatus{Message: "Failed to create HTTP request", Error: err}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return TelegramRegistrationStatus{Message: "Connection failed", Error: err}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 200:
		return TelegramRegistrationStatus{Code: 200, Message: "200 - Registration active"}
	case 403:
		return TelegramRegistrationStatus{Code: 403, Message: "403 - Start the bot and send the Server ID", Error: fmt.Errorf("%s", body)}
	case 409:
		return TelegramRegistrationStatus{Code: 409, Message: "409 - Registration missing on the bot", Error: fmt.Errorf("%s", body)}
	case 422:
		return TelegramRegistrationStatus{Code: 422, Message: "422 - Invalid Server ID", Error: fmt.Errorf("%s", body)}
	default:
		return TelegramRegistrationStatus{
			Code:    resp.StatusCode,
			Message: fmt.Sprintf("%d - Unexpected response: %s", resp.StatusCode, string(body)),
			Error:   fmt.Errorf("unexpected status %d", resp.StatusCode),
		}
	}
}
