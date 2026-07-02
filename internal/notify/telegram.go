package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/version"
)

// errRelayAuthRejected signals the relay rejected our per-server secret (HTTP
// 401/403): the secret is stale or rotated and should be dropped + reprovisioned
// rather than treated as a permanent delivery failure.
var errRelayAuthRejected = errors.New("relay auth rejected")

// proxsaveVersionHeader carries the running ProxSave version so the central
// server can gate version-specific behavior (e.g. the one-time secret handoff).
const proxsaveVersionHeader = "X-Proxsave-Version"

// proxsaveProvisionHeader marks a real provisioning call (issue/re-issue the relay
// secret). Sent ONLY by the two provisioning paths, never the bare status probe.
const proxsaveProvisionHeader = "X-Proxsave-Provision"

// proxsaveNotifyIDHeader carries a client-generated id that makes the enqueue
// idempotent and is the handle the client polls for the real delivery outcome.
const proxsaveNotifyIDHeader = "X-Notify-Id"

// newNotifyID returns a random 32-hex-char id (<=128, matching the server's cap).
// Generated ONCE per Send and reused across the relay POST + status poll (and any
// relay retry) so the server-side idempotency dedupe never double-queues.
func newNotifyID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand effectively never fails; on the impossible error fall back to
		// a time-derived id so the poll still has a usable handle.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// setProxsaveVersionHeader stamps an outbound central-server request with the
// normalized ProxSave version (e.g. "0.28.0").
func setProxsaveVersionHeader(req *http.Request) string {
	v := version.String()
	req.Header.Set(proxsaveVersionHeader, v)
	return v
}

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
	NotifySecret  string // Per-server relay secret; when set, use the server-side relay
	BaseDir       string // Identity store root for lazy-load / persist of the relay secret

	// Delivery confirmation (two-response CLI): after the relay accepts (202), poll
	// GET /api/notify/status until Telegram confirms delivery or the budget elapses.
	ConfirmDelivery bool
	ConfirmTimeout  time.Duration
	ConfirmInterval time.Duration
}

// TelegramNotifier implements the Notifier interface for Telegram
type TelegramNotifier struct {
	config TelegramConfig
	logger *logging.Logger
	client *http.Client
}

// Telegram API response for centralized mode
type telegramCentralizedResponse struct {
	BotToken     string `json:"bot_token"`
	ChatID       string `json:"chat_id"`
	Status       int    `json:"status"`
	Message      string `json:"message,omitempty"`
	NotifySecret string `json:"notify_secret"` // TOFU one-time provisioning
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
	// One idempotent id per notification, reused across the relay POST and the
	// delivery-status poll (and any relay retry after a stale-secret reprovision) so
	// the server-side idempotency dedupe never double-queues this notification.
	notifyID := newNotifyID()

	if !t.config.Enabled {
		t.logger.Debug("Telegram notifications disabled")
		result.Success = false
		result.Duration = time.Since(startTime)
		return result, nil
	}

	if t.config.Mode == TelegramModeCentralized {
		t.logger.Debug("Telegram: send start (mode=centralized serverID=%q secretPreloaded=%v baseDir=%q)", t.config.ServerID, t.config.NotifySecret != "", t.config.BaseDir)
	}

	// Lazily adopt a previously provisioned per-server secret from the
	// immutable identity file so the relay is used without fetching the token.
	if t.config.Mode == TelegramModeCentralized && t.config.NotifySecret == "" && t.config.BaseDir != "" {
		if sec, err := identity.LoadNotifySecret(t.config.BaseDir, t.logger); err != nil {
			t.logger.Debug("Telegram: could not read persisted relay secret: %v", err)
		} else if sec != "" {
			t.config.NotifySecret = sec
			t.logger.RegisterSecret(sec)
			t.logger.Debug("Telegram: loaded persisted per-server relay secret (len=%d)", len(sec))
		} else {
			t.logger.Debug("Telegram: no persisted relay secret on disk; will attempt provisioning during fetch")
		}
	}

	// Secure relay path: when a per-server secret is provisioned, the bot
	// token never leaves the host. The server looks up the chat and sends
	// the message itself. Falls back to the legacy path when no secret set.
	if t.config.Mode == TelegramModeCentralized && t.config.NotifySecret != "" {
		t.logger.Debug("Telegram: using server-side relay path (bot token stays on host)")
		message := t.buildMessage(data)
		status, err := t.sendViaRelay(ctx, message, notifyID)
		if err == nil {
			t.recordRelayAcceptedAndPoll(ctx, result, notifyID, status, time.Since(startTime))
			result.Success = true
			result.Duration = time.Since(startTime)
			return result, nil
		}
		if !errors.Is(err, errRelayAuthRejected) {
			t.logger.Warning("WARNING: Failed to send Telegram notification via relay: %v", err)
			result.Metadata["relay_accepted"] = false
			result.Success = false
			result.Error = err
			result.Duration = time.Since(startTime)
			return result, nil // Non-critical error, don't abort backup
		}
		// The relay rejected our per-server secret (stale/rotated). Drop it and fall
		// through to the fetch path below, which reprovisions a fresh secret and
		// relays this run (or falls back to the legacy bot-token path). Bounded: the
		// fetch path retries the relay at most once and never loops back here.
		t.logger.Warning("Telegram: relay auth rejected; dropping stale relay secret and reprovisioning")
		t.config.NotifySecret = ""
	}

	// Get bot token and chat ID (fetch if centralized mode)
	botToken := t.config.BotToken
	chatID := t.config.ChatID

	if t.config.Mode == TelegramModeCentralized {
		t.logger.Debug("Fetching Telegram credentials from central server...")
		var err error
		botToken, chatID, err = t.fetchCentralizedCredentials(ctx) // may TOFU-provision+persist+set NotifySecret
		if err != nil {
			t.logger.Warning("WARNING: Failed to fetch Telegram credentials: %v", err)
			result.Success = false
			result.Error = err
			result.Duration = time.Since(startTime)
			return result, nil // Non-critical error, don't abort backup
		}

		// First-run provisioning: if the fetch just delivered a fresh secret,
		// deliver THIS run via the relay so the new secret is used and the bot
		// token is not.
		if t.config.NotifySecret != "" {
			t.logger.Debug("Telegram: fetch provisioned a fresh relay secret; relaying this run")
			message := t.buildMessage(data)
			status, err := t.sendViaRelay(ctx, message, notifyID)
			if err != nil {
				t.logger.Warning("WARNING: Failed to send Telegram notification via relay: %v", err)
				result.Metadata["relay_accepted"] = false
				result.Success = false
				result.Error = err
				result.Duration = time.Since(startTime)
				return result, nil // Non-critical error, don't abort backup
			}
			t.recordRelayAcceptedAndPoll(ctx, result, notifyID, status, time.Since(startTime))
			result.Success = true
			result.Duration = time.Since(startTime)
			return result, nil
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
	t.logger.Debug("Telegram: legacy direct send via bot token (token leaves host; mode=%s)", t.config.Mode)
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
	pv := setProxsaveVersionHeader(req)
	// Only ask the server to (re)issue a relay secret when we can actually persist
	// it (BaseDir set). Otherwise the 200 body's secret is discarded below and every
	// run would churn a fresh unconfirmed token server-side.
	provisionIntent := t.config.BaseDir != ""
	if provisionIntent {
		req.Header.Set(proxsaveProvisionHeader, "1")
	}
	t.logger.Debug("Telegram: get-chat-id GET (serverID=%q X-Proxsave-Version=%q provisionIntent=%v)", t.config.ServerID, pv, provisionIntent)

	// Make request
	resp, err := t.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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
		t.logger.Debug("Telegram: get-chat-id 200 (notifySecretPresent=%v botTokenPresent=%v chatIDPresent=%v)", response.NotifySecret != "", response.BotToken != "", response.ChatID != "")

		// Adopt-on-token-present: whenever a 200 carries a notify_secret, the
		// server wants the client to (re)adopt it, so we OVERWRITE any existing
		// persisted secret (no idempotent skip, or a re-issued token would be
		// stranded) and then CONFIRM it. When the body carries no token we keep
		// whatever we already have. The secret is never logged.
		if provisioned := strings.TrimSpace(response.NotifySecret); provisioned != "" && t.config.BaseDir != "" {
			t.logger.RegisterSecret(provisioned) // mask before any I/O can echo it
			if err := identity.PersistNotifySecret(ctx, t.config.BaseDir, provisioned, t.logger); err != nil {
				t.logger.Warning("WARNING: failed to persist provisioned relay secret: %v", err)
			} else {
				t.config.NotifySecret = provisioned // adopt in-memory for this run
				t.logger.Debug("Telegram: adopted+persisted per-server relay secret (overwrite)")
				if err := confirmTelegramRelaySecret(ctx, t.client, t.config.ServerAPIHost, t.config.ServerID, provisioned, t.logger); err != nil {
					t.logger.Debug("Telegram: relay secret confirm failed (non-fatal): %v", err)
				}
			}
		}

		// With a relay secret in hand, the bot token is irrelevant for this run.
		if t.config.NotifySecret != "" {
			return response.BotToken, response.ChatID, nil
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

// sendViaRelay sends a message through the authenticated server-side relay so
// the bot token never leaves this host. The per-server secret is sent only in
// the X-Server-Auth header; any returned error string is scrubbed of it.
func (t *TelegramNotifier) sendViaRelay(ctx context.Context, message, notifyID string) (int, error) {
	endpoint := strings.TrimRight(t.config.ServerAPIHost, "/") + "/api/notify"

	payload := struct {
		ServerID string `json:"server_id"`
		Message  string `json:"message"`
	}{
		ServerID: t.config.ServerID,
		Message:  message,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("failed to encode relay request: %w", err)
	}

	// 5-second timeout, mirroring fetchCentralizedCredentials.
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to create relay request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Server-Auth", t.config.NotifySecret)
	if notifyID != "" {
		req.Header.Set(proxsaveNotifyIDHeader, notifyID)
	}
	pv := setProxsaveVersionHeader(req)
	t.logger.Debug("Telegram: relay POST %s (serverID=%q msgLen=%d notifyID=%q X-Proxsave-Version=%q)", endpoint, t.config.ServerID, len(message), notifyID, pv)

	resp, err := t.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("relay request failed: %s", logging.RedactSecrets(err.Error(), t.config.NotifySecret))
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case 200, 202:
		// 200 = legacy synchronous relay (outbox kill-switch off) = already
		// delivered. 202 = accepted onto the durable outbox; the real delivery
		// outcome is learned via the status poll.
		t.logger.Debug("Telegram: relay accepted (HTTP %d notifyID=%q)", resp.StatusCode, notifyID)
		return resp.StatusCode, nil
	case 401, 403:
		return resp.StatusCode, fmt.Errorf("%w (HTTP %d)", errRelayAuthRejected, resp.StatusCode)
	case 404:
		return resp.StatusCode, fmt.Errorf("server unknown (HTTP 404)")
	case 409:
		return resp.StatusCode, fmt.Errorf("registration missing (HTTP 409)")
	case 413:
		return resp.StatusCode, fmt.Errorf("message too long (HTTP 413)")
	default:
		respBody, _ := io.ReadAll(resp.Body)
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return resp.StatusCode, fmt.Errorf("relay returned status %d: %s",
			resp.StatusCode, logging.RedactSecrets(snippet, t.config.NotifySecret))
	}
}

// recordRelayAcceptedAndPoll records that the ProxSave server accepted the
// notification (first response) and, when confirmation is enabled, polls for the
// real Telegram delivery outcome (second response). Everything lands in
// result.Metadata (relay_accepted, notify_id, telegram_state, telegram_message_id,
// telegram_reason); it NEVER changes result.Success -- server acceptance is the
// success signal, the delivery outcome is a separate best-effort line.
func (t *TelegramNotifier) recordRelayAcceptedAndPoll(ctx context.Context, result *NotificationResult, notifyID string, httpStatus int, acceptDuration time.Duration) {
	result.Metadata["relay_accepted"] = true
	result.Metadata["notify_id"] = notifyID
	// Time to server ACCEPTANCE only (before any delivery poll), so the adapter's
	// first line reports true relay latency, not latency + the poll budget.
	result.Metadata["relay_accept_duration"] = acceptDuration

	// HTTP 200 = legacy synchronous relay (outbox kill-switch off): the server
	// already delivered inside the request, so there is nothing to poll.
	if httpStatus == 200 {
		result.Metadata["telegram_state"] = "delivered"
		return
	}
	if !t.config.ConfirmDelivery {
		// Confirmation disabled by config: this is NOT a pending delivery, so use a
		// distinct state and let the adapter stay quiet (no "delivery in progress"
		// warning on every backup).
		result.Metadata["telegram_state"] = "unconfirmed"
		return
	}

	ds := pollTelegramDeliveryStatus(ctx, t.client, t.config.ServerAPIHost,
		t.config.ServerID, t.config.NotifySecret, notifyID, t.pollCfg(), t.logger)
	result.Metadata["telegram_state"] = ds.State
	if ds.State == "delivered" && ds.MessageID != 0 {
		result.Metadata["telegram_message_id"] = ds.MessageID
	}
	if ds.State == "failed" {
		result.Metadata["telegram_reason"] = ds.Reason
	}
}

// pollCfg builds the delivery-status poll config from the notifier config, applying
// sane floors so a mis-set interval/timeout can never spin.
func (t *TelegramNotifier) pollCfg() deliveryPollConfig {
	interval := t.config.ConfirmInterval
	if interval <= 0 {
		interval = time.Second
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second // ceiling: a mis-set interval must not starve the poll
	}
	timeout := t.config.ConfirmTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second // ceiling: never hang end-of-backup for minutes
	}
	return deliveryPollConfig{
		Timeout:      timeout,
		Interval:     interval,
		InitialDelay: interval,
	}
}

// buildMessage builds the Telegram message text
func (t *TelegramNotifier) buildMessage(data *NotificationData) string {
	var msg strings.Builder

	// Tool name and version header
	version := strings.TrimSpace(data.ScriptVersion)
	if version != "" {
		fmt.Fprintf(&msg, "ProxSave - v%s\n\n", version)
	} else {
		msg.WriteString("ProxSave\n\n")
	}

	// Header with status and hostname
	statusEmoji := GetStatusEmoji(data.Status)
	fmt.Fprintf(&msg, "%s Backup %s - %s\n\n",
		statusEmoji,
		data.ProxmoxType.String(),
		data.Hostname)

	// Storage status
	localEmoji := GetStorageEmoji(data.LocalStatus)
	fmt.Fprintf(&msg, "%s Local      (%s backups)\n", localEmoji, data.LocalStatusSummary)

	if data.SecondaryEnabled {
		secondaryEmoji := GetStorageEmoji(data.SecondaryStatus)
		fmt.Fprintf(&msg, "%s Secondary  (%s backups)\n", secondaryEmoji, data.SecondaryStatusSummary)
	} else {
		msg.WriteString("➖ Secondary  (disabled)\n")
	}

	if data.CloudEnabled {
		cloudEmoji := GetStorageEmoji(data.CloudStatus)
		fmt.Fprintf(&msg, "%s Cloud      (%s backups)\n", cloudEmoji, data.CloudStatusSummary)
	} else {
		msg.WriteString("➖ Cloud      (disabled)\n")
	}

	// Email status
	emailEmoji := GetStorageEmoji(data.EmailStatus)
	fmt.Fprintf(&msg, "%s Email\n\n", emailEmoji)

	// File counts
	fmt.Fprintf(&msg, "📁 Included files: %d\n", data.FilesIncluded)
	if data.FilesMissing > 0 {
		fmt.Fprintf(&msg, "⚠️ Missing files: %d\n", data.FilesMissing)
	}
	msg.WriteString("\n")

	// Disk space
	msg.WriteString("💾 Available space:\n")
	fmt.Fprintf(&msg, "🔹 Local: %s\n", data.LocalFree)
	if data.SecondaryEnabled && data.SecondaryFree != "" {
		fmt.Fprintf(&msg, "🔹 Secondary: %s\n", data.SecondaryFree)
	}
	msg.WriteString("\n")

	// Backup metadata
	fmt.Fprintf(&msg, "📅 Backup date: %s\n", data.BackupDate.Format("2006-01-02 15:04"))
	fmt.Fprintf(&msg, "⏱️ Duration: %s\n\n", FormatDuration(data.BackupDuration))

	// Exit code
	fmt.Fprintf(&msg, "🔢 Exit code: %d", data.ExitCode)

	// Optional version update information
	if data.NewVersionAvailable && strings.TrimSpace(data.LatestVersion) != "" {
		msg.WriteString("\n\n⬆️ Update available\n")

		current := strings.TrimSpace(data.CurrentVersion)
		if current != "" {
			fmt.Fprintf(&msg, "New version: %s (current: %s)\n", data.LatestVersion, current)
		} else {
			fmt.Fprintf(&msg, "New version: %s\n", data.LatestVersion)
		}
		msg.WriteString("Run 'proxsave --upgrade'\n")
	}

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
		// The bot token is embedded in the URL path, which a *url.Error carries
		// verbatim; redact it before the error is wrapped/logged/propagated.
		return fmt.Errorf("api request failed: %s", logging.RedactSecrets(err.Error(), botToken))
	}
	defer func() { _ = resp.Body.Close() }()

	// Check response
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram api returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
