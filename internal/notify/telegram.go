package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/logging"
)

// TelegramMode represents the Telegram bot configuration mode
type TelegramMode string

const (
	TelegramModePersonal    TelegramMode = "personal"
	TelegramModeCentralized TelegramMode = "centralized"
)

// TelegramConfig holds Telegram notification configuration
type TelegramConfig struct {
	Enabled       bool
	Mode          TelegramMode
	BotToken      string
	ChatID        string
	ServerAPIHost string // For centralized mode
	ServerID      string // Server identifier for centralized mode
}

// TelegramNotifier implements the Notifier interface for Telegram
type TelegramNotifier struct {
	config TelegramConfig
	logger *logging.Logger
	client *http.Client
}

// Telegram API response for centralized mode
type telegramCentralizedResponse struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
	Status   int    `json:"status"`
	Message  string `json:"message,omitempty"`
}

// Token and ChatID validation regex patterns
var (
	tokenRegex  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{35,}$`)
	chatIDRegex = regexp.MustCompile(`^-?[0-9]+$`)
)

// NewTelegramNotifier creates a new Telegram notifier
func NewTelegramNotifier(config TelegramConfig, logger *logging.Logger) (*TelegramNotifier, error) {
	if !config.Enabled {
		return &TelegramNotifier{
			config: config,
			logger: logger,
			client: &http.Client{Timeout: 30 * time.Second},
		}, nil
	}

	// Validate mode
	if config.Mode != TelegramModePersonal && config.Mode != TelegramModeCentralized {
		return nil, fmt.Errorf("invalid Telegram mode: %s (must be 'personal' or 'centralized')", config.Mode)
	}

	// Personal mode: validate token and chat ID
	if config.Mode == TelegramModePersonal {
		if config.BotToken == "" {
			return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required for personal mode")
		}
		if config.ChatID == "" {
			return nil, fmt.Errorf("TELEGRAM_CHAT_ID is required for personal mode")
		}

		if !tokenRegex.MatchString(config.BotToken) {
			return nil, fmt.Errorf("invalid TELEGRAM_BOT_TOKEN format (expected: digits:alphanumeric_35+)")
		}
		if !chatIDRegex.MatchString(config.ChatID) {
			return nil, fmt.Errorf("invalid TELEGRAM_CHAT_ID format (expected: numeric)")
		}
	}

	// Centralized mode: validate server API host and server ID
	if config.Mode == TelegramModeCentralized {
		if config.ServerAPIHost == "" {
			return nil, fmt.Errorf("TELEGRAM_SERVER_API_HOST is required for centralized mode")
		}
		if config.ServerID == "" {
			return nil, fmt.Errorf("SERVER_ID is required for centralized mode")
		}
	}

	return &TelegramNotifier{
		config: config,
		logger: logger,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name returns the notifier name
func (t *TelegramNotifier) Name() string {
	return "Telegram"
}

// IsEnabled returns whether Telegram notifications are enabled
func (t *TelegramNotifier) IsEnabled() bool {
	return t.config.Enabled
}

// IsCritical returns whether Telegram failures should abort backup (always false)
func (t *TelegramNotifier) IsCritical() bool {
	return false // Notification failures never abort backup
}

// Send sends a Telegram notification
func (t *TelegramNotifier) Send(ctx context.Context, data *NotificationData) (*NotificationResult, error) {
	startTime := time.Now()
	result := &NotificationResult{
		Method:   "telegram",
		Metadata: make(map[string]interface{}),
	}

	if !t.config.Enabled {
		t.logger.Debug("Telegram notifications disabled")
		result.Success = false
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Get bot token and chat ID (fetch if centralized mode)
	botToken := t.config.BotToken
	chatID := t.config.ChatID

	if t.config.Mode == TelegramModeCentralized {
		t.logger.Debug("Fetching Telegram credentials from central server...")
		var err error
		botToken, chatID, err = t.fetchCentralizedCredentials(ctx)
		if err != nil {
			t.logger.Warning("WARNING: Failed to fetch Telegram credentials: %v", err)
			result.Success = false
			result.Error = err
			result.Duration = time.Since(startTime)
			return result, nil // Non-critical error, don't abort backup
		}
	}

	// Validate credentials
	if botToken == "" || chatID == "" {
		err := fmt.Errorf("missing bot token or chat ID")
		t.logger.Warning("WARNING: Telegram notification skipped: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(startTime)
		return result, nil
	}

	// Build message
	message := t.buildMessage(data)

	// Send to Telegram API
	err := t.sendToTelegram(ctx, botToken, chatID, message)
	if err != nil {
		t.logger.Warning("WARNING: Failed to send Telegram notification: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(startTime)
		return result, nil // Non-critical error
	}

	t.logger.Debug("Telegram API confirmed message delivery")
	result.Success = true
	result.Duration = time.Since(startTime)
	return result, nil
}

// fetchCentralizedCredentials fetches bot credentials from central server
func (t *TelegramNotifier) fetchCentralizedCredentials(ctx context.Context) (string, string, error) {
	// Build API URL
	apiURL := fmt.Sprintf("%s/api/get-chat-id?server_id=%s",
		strings.TrimRight(t.config.ServerAPIHost, "/"),
		url.QueryEscape(t.config.ServerID))

	// Create request with 5-second timeout
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", apiURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}

	// Make request
	resp, err := t.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}

	// Handle HTTP status codes
	switch resp.StatusCode {
	case 200:
		// Success - parse response
		var response telegramCentralizedResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return "", "", fmt.Errorf("failed to parse response: %w", err)
		}

		// Validate credentials
		if !tokenRegex.MatchString(response.BotToken) {
			return "", "", fmt.Errorf("invalid bot token format from server")
		}
		if !chatIDRegex.MatchString(response.ChatID) {
			return "", "", fmt.Errorf("invalid chat ID format from server")
		}

		t.logger.Debug("Telegram credentials fetched successfully")
		return response.BotToken, response.ChatID, nil

	case 403:
		return "", "", fmt.Errorf("first communication - bot not started (HTTP 403)")
	case 409:
		return "", "", fmt.Errorf("missing registration - register with bot (HTTP 409)")
	case 422:
		return "", "", fmt.Errorf("invalid SERVER_ID (HTTP 422)")
	default:
		return "", "", fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}

// buildMessage builds the Telegram message text
func (t *TelegramNotifier) buildMessage(data *NotificationData) string {
	var msg strings.Builder

	// Header with status and hostname
	statusEmoji := GetStatusEmoji(data.Status)
	msg.WriteString(fmt.Sprintf("%s Backup %s - %s\n\n",
		statusEmoji,
		data.ProxmoxType.String(),
		data.Hostname))

	// Storage status
	localEmoji := GetStorageEmoji(data.LocalStatus)
	msg.WriteString(fmt.Sprintf("%s Local      (%s backups)\n", localEmoji, data.LocalStatusSummary))

	if data.SecondaryEnabled {
		secondaryEmoji := GetStorageEmoji(data.SecondaryStatus)
		msg.WriteString(fmt.Sprintf("%s Secondary  (%s backups)\n", secondaryEmoji, data.SecondaryStatusSummary))
	} else {
		msg.WriteString("➖ Secondary  (disabled)\n")
	}

	if data.CloudEnabled {
		cloudEmoji := GetStorageEmoji(data.CloudStatus)
		msg.WriteString(fmt.Sprintf("%s Cloud      (%s backups)\n", cloudEmoji, data.CloudStatusSummary))
	} else {
		msg.WriteString("➖ Cloud      (disabled)\n")
	}

	// Email status
	emailEmoji := GetStorageEmoji(data.EmailStatus)
	msg.WriteString(fmt.Sprintf("%s Email\n\n", emailEmoji))

	// File counts
	msg.WriteString(fmt.Sprintf("📁 Included files: %d\n", data.FilesIncluded))
	if data.FilesMissing > 0 {
		msg.WriteString(fmt.Sprintf("⚠️ Missing files: %d\n", data.FilesMissing))
	}
	msg.WriteString("\n")

	// Disk space
	msg.WriteString("💾 Available space:\n")
	msg.WriteString(fmt.Sprintf("🔹 Local: %s\n", data.LocalFree))
	if data.SecondaryEnabled && data.SecondaryFree != "" {
		msg.WriteString(fmt.Sprintf("🔹 Secondary: %s\n", data.SecondaryFree))
	}
	msg.WriteString("\n")

	// Backup metadata
	msg.WriteString(fmt.Sprintf("📅 Backup date: %s\n", data.BackupDate.Format("2006-01-02 15:04")))
	msg.WriteString(fmt.Sprintf("⏱️ Duration: %s\n\n", FormatDuration(data.BackupDuration)))

	// Exit code
	msg.WriteString(fmt.Sprintf("🔢 Exit code: %d", data.ExitCode))

	return msg.String()
}

// sendToTelegram sends a message to Telegram Bot API
func (t *TelegramNotifier) sendToTelegram(ctx context.Context, botToken, chatID, message string) error {
	// Build Telegram API URL
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	// Prepare form data
	formData := url.Values{}
	formData.Set("chat_id", chatID)
	formData.Set("text", message)

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Send request
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("api request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram api returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
