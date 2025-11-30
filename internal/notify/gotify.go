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

	"github.com/tis24dev/proxsave/internal/logging"
)

// GotifyConfig holds configuration for Gotify notifications.
type GotifyConfig struct {
	Enabled         bool
	ServerURL       string
	Token           string
	PrioritySuccess int
	PriorityWarning int
	PriorityFailure int
}

// GotifyNotifier implements the Notifier interface for Gotify.
type GotifyNotifier struct {
	config GotifyConfig
	logger *logging.Logger
	client *http.Client
}

// gotifyMessage represents the JSON payload accepted by Gotify.
type gotifyMessage struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

// NewGotifyNotifier creates a new Gotify notifier.
func NewGotifyNotifier(cfg GotifyConfig, logger *logging.Logger) (*GotifyNotifier, error) {
	trimmedURL := strings.TrimSpace(cfg.ServerURL)
	if cfg.Enabled {
		if trimmedURL == "" {
			return nil, fmt.Errorf("GOTIFY_SERVER_URL is required when GOTIFY_ENABLED=true")
		}
		if strings.TrimSpace(cfg.Token) == "" {
			return nil, fmt.Errorf("GOTIFY_TOKEN is required when GOTIFY_ENABLED=true")
		}
	}

	cfg.ServerURL = strings.TrimRight(trimmedURL, "/")
	if cfg.PrioritySuccess <= 0 {
		cfg.PrioritySuccess = 2
	}
	if cfg.PriorityWarning <= 0 {
		cfg.PriorityWarning = 5
	}
	if cfg.PriorityFailure <= 0 {
		cfg.PriorityFailure = 8
	}

	return &GotifyNotifier{
		config: cfg,
		logger: logger,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Name returns the notifier name.
func (g *GotifyNotifier) Name() string {
	return "Gotify"
}

// IsEnabled returns whether the notifier is enabled.
func (g *GotifyNotifier) IsEnabled() bool {
	return g != nil && g.config.Enabled
}

// IsCritical returns whether failures should abort the backup (never for notifications).
func (g *GotifyNotifier) IsCritical() bool {
	return false
}

// Send sends a notification to Gotify.
func (g *GotifyNotifier) Send(ctx context.Context, data *NotificationData) (*NotificationResult, error) {
	start := time.Now()
	result := &NotificationResult{
		Method:   "gotify",
		Metadata: make(map[string]interface{}),
	}

	if !g.IsEnabled() {
		g.logger.Debug("Gotify notifications disabled - skipping")
		result.Success = false
		result.Duration = time.Since(start)
		return result, nil
	}

	endpoint, err := g.buildEndpoint()
	if err != nil {
		g.logger.Warning("WARNING: Invalid Gotify configuration: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(start)
		return result, nil
	}

	payload := gotifyMessage{
		Title:    BuildEmailSubject(data),
		Message:  BuildEmailPlainText(data),
		Priority: g.mapPriority(data.Status),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed to marshal Gotify payload: %w", err)
		g.logger.Warning("WARNING: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(start)
		return result, nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		err = fmt.Errorf("failed to create Gotify request: %w", err)
		g.logger.Warning("WARNING: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(start)
		return result, nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		err = fmt.Errorf("gotify request failed: %w", err)
		g.logger.Warning("WARNING: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(start)
		return result, nil
	}
	defer resp.Body.Close()

	result.Metadata["status_code"] = resp.StatusCode
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("gotify returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		g.logger.Warning("WARNING: %v", err)
		result.Success = false
		result.Error = err
		result.Duration = time.Since(start)
		return result, nil
	}

	g.logger.Debug("Gotify confirmed notification delivery (status=%d)", resp.StatusCode)
	result.Success = true
	result.Duration = time.Since(start)
	return result, nil
}

func (g *GotifyNotifier) buildEndpoint() (string, error) {
	baseURL := g.config.ServerURL
	if baseURL == "" {
		return "", fmt.Errorf("server URL is empty")
	}

	parsed, err := url.Parse(baseURL + "/message")
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("token", g.config.Token)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (g *GotifyNotifier) mapPriority(status NotificationStatus) int {
	switch status {
	case StatusFailure:
		return g.config.PriorityFailure
	case StatusWarning:
		return g.config.PriorityWarning
	default:
		return g.config.PrioritySuccess
	}
}
