package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// WebhookNotifier sends notifications to configured webhook endpoints
type WebhookNotifier struct {
	config *config.WebhookConfig
	logger *logging.Logger
	client *http.Client
}

// NewWebhookNotifier creates a new webhook notifier
func NewWebhookNotifier(webhookConfig *config.WebhookConfig, logger *logging.Logger) (*WebhookNotifier, error) {
	logger.Debug("WebhookNotifier initialization starting...")
	logger.Debug("Configuration: enabled=%v, endpoints=%d, default_format=%s, timeout=%ds, max_retries=%d, retry_delay=%ds",
		webhookConfig.Enabled, len(webhookConfig.Endpoints), webhookConfig.DefaultFormat, webhookConfig.Timeout, webhookConfig.MaxRetries, webhookConfig.RetryDelay)

	// Validate configuration
	if !webhookConfig.Enabled {
		logger.Debug("Webhook notifications disabled in configuration")
		return &WebhookNotifier{
			config: webhookConfig,
			logger: logger,
			client: nil,
		}, nil
	}

	if len(webhookConfig.Endpoints) == 0 {
		return nil, fmt.Errorf("webhook notifications enabled but no endpoints configured")
	}

	// Log each endpoint configuration (with masked sensitive data)
	for i, ep := range webhookConfig.Endpoints {
		logger.Debug("Endpoint #%d configuration:", i+1)
		logger.Debug("  Name: %s", ep.Name)
		logger.Debug("  URL: %s", maskURL(ep.URL))
		logger.Debug("  Format: %s", ep.Format)
		logger.Debug("  Method: %s", ep.Method)
		logger.Debug("  Auth Type: %s", ep.Auth.Type)
		logger.Debug("  Custom Headers: %d", len(ep.Headers))
		if len(ep.Headers) > 0 {
			for k := range ep.Headers {
				logger.Debug("    Header: %s (value masked)", k)
			}
		}
	}

	// Create HTTP client with timeout
	timeout := webhookConfig.Timeout
	if timeout <= 0 {
		timeout = 30
		logger.Debug("Invalid timeout, using default: %ds", timeout)
	}

	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
	}
	logger.Debug("HTTP client created with %ds timeout", timeout)

	logger.Info("✅ WebhookNotifier initialized successfully with %d endpoint(s)", len(webhookConfig.Endpoints))

	return &WebhookNotifier{
		config: webhookConfig,
		logger: logger,
		client: client,
	}, nil
}

// Name returns the notifier name
func (w *WebhookNotifier) Name() string {
	return "Webhook"
}

// IsEnabled returns whether webhook notifications are enabled
func (w *WebhookNotifier) IsEnabled() bool {
	enabled := w.config.Enabled && len(w.config.Endpoints) > 0
	w.logger.Debug("WebhookNotifier.IsEnabled() = %v (config.Enabled=%v, endpoints=%d)",
		enabled, w.config.Enabled, len(w.config.Endpoints))
	return enabled
}

// IsCritical returns false - webhook failures should not abort backup
func (w *WebhookNotifier) IsCritical() bool {
	return false
}

// Send sends notifications to all configured webhook endpoints
func (w *WebhookNotifier) Send(ctx context.Context, data *NotificationData) (*NotificationResult, error) {
	startTime := time.Now()
	w.logger.Debug("=== WebhookNotifier.Send() called ===")
	w.logger.Debug("Processing %d webhook endpoint(s)", len(w.config.Endpoints))
	w.logger.Debug("Notification data summary: status=%s, hostname=%s, backup_size=%s, duration=%s, errors=%d, warnings=%d",
		data.Status.String(), data.Hostname, data.BackupSizeHR, FormatDuration(data.BackupDuration),
		data.ErrorCount, data.WarningCount)

	if !w.IsEnabled() {
		w.logger.Debug("Webhook notifications disabled, skipping send")
		return &NotificationResult{
			Success:      false,
			UsedFallback: false,
			Method:       "webhook",
			Error:        fmt.Errorf("webhook notifications not enabled"),
		}, nil
	}

	// Send to all endpoints
	successCount := 0
	failureCount := 0
	var lastErr error

	for i, endpoint := range w.config.Endpoints {
		w.logger.Debug("--- Processing endpoint %d/%d: '%s' ---", i+1, len(w.config.Endpoints), endpoint.Name)

		err := w.sendToEndpoint(ctx, endpoint, data)
		if err != nil {
			w.logger.Error("❌ Endpoint '%s' failed: %v", endpoint.Name, err)
			failureCount++
			lastErr = err
		} else {
			w.logger.Info("✅ Endpoint '%s' succeeded", endpoint.Name)
			successCount++
		}
	}

	totalDuration := time.Since(startTime)
	w.logger.Debug("=== WebhookNotifier.Send() complete ===")
	w.logger.Debug("Results: success=%d, failure=%d, total_duration=%dms",
		successCount, failureCount, totalDuration.Milliseconds())

	// Consider successful if at least one endpoint succeeded
	success := successCount > 0
	var resultErr error
	if !success && lastErr != nil {
		resultErr = fmt.Errorf("all %d endpoints failed: %w", len(w.config.Endpoints), lastErr)
	}

	return &NotificationResult{
		Success:      success,
		UsedFallback: false,
		Method:       "webhook",
		Error:        resultErr,
		Duration:     totalDuration,
	}, nil
}

// sendToEndpoint sends notification to a single webhook endpoint
func (w *WebhookNotifier) sendToEndpoint(ctx context.Context, endpoint config.WebhookEndpoint, data *NotificationData) error {
	w.logger.Debug("sendToEndpoint() starting for '%s'", endpoint.Name)
	w.logger.Debug("Endpoint format: %s, URL: %s", endpoint.Format, maskURL(endpoint.URL))

	// Determine format to use
	format := endpoint.Format
	if format == "" {
		format = w.config.DefaultFormat
		w.logger.Debug("Using default format: %s", format)
	}
	if format == "" {
		format = "generic"
		w.logger.Debug("No format specified, using generic")
	}

	// Build payload based on format
	w.logger.Debug("Building %s payload...", format)
	payloadStart := time.Now()

	payload, err := w.buildPayload(format, data)
	if err != nil {
		w.logger.Error("Failed to build %s payload: %v", format, err)
		return fmt.Errorf("failed to build payload: %w", err)
	}

	payloadDuration := time.Since(payloadStart)
	w.logger.Debug("Payload built successfully in %dms", payloadDuration.Milliseconds())

	// Marshal to JSON
	w.logger.Debug("Marshaling payload to JSON...")
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		w.logger.Error("Failed to marshal payload to JSON: %v", err)
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	w.logger.Debug("Payload marshaled: %d bytes", len(payloadBytes))
	if w.logger.GetLevel() <= types.LogLevelDebug {
		if len(payloadBytes) > 200 {
			w.logger.Debug("Payload preview (first 200 chars): %s...", string(payloadBytes[:200]))
		} else {
			w.logger.Debug("Payload content: %s", string(payloadBytes))
		}
	}

	// Retry loop
	maxRetries := w.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	retryDelay := w.config.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 2
	}

	w.logger.Debug("Retry configuration: max_retries=%d, retry_delay=%ds", maxRetries, retryDelay)

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			w.logger.Debug("Retry attempt %d/%d after %ds delay...", attempt, maxRetries, retryDelay)
			time.Sleep(time.Duration(retryDelay) * time.Second)
		}

		// Determine HTTP method
		method := strings.ToUpper(strings.TrimSpace(endpoint.Method))
		if method == "" {
			method = "POST"
		}

		parsedURL, parseErr := url.Parse(endpoint.URL)
		if parseErr != nil {
			lastErr = fmt.Errorf("invalid webhook URL: %w", parseErr)
			w.logger.Warning("Invalid URL for endpoint %s: %v", endpoint.Name, parseErr)
			break
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			lastErr = fmt.Errorf("invalid URL scheme %q for endpoint %s", parsedURL.Scheme, endpoint.Name)
			w.logger.Warning("Invalid URL scheme for endpoint %s: %s", endpoint.Name, parsedURL.Scheme)
			break
		}
		w.logger.Debug("Creating HTTP %s request to %s (attempt %d/%d)", method, maskURL(parsedURL.String()), attempt+1, maxRetries+1)

		var bodyReader io.Reader
		switch method {
		case "GET", "HEAD":
			w.logger.Debug("Method %s does not send a body; payload omitted", method)
		default:
			bodyReader = bytes.NewReader(payloadBytes)
		}

		// Create request
		req, err := http.NewRequestWithContext(ctx, method, parsedURL.String(), bodyReader)
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %w", err)
			w.logger.Warning("Request creation failed (attempt %d/%d): %v", attempt+1, maxRetries+1, err)
			continue
		}

		// Set default headers
		if bodyReader != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("User-Agent", fmt.Sprintf("proxsave/%s", data.ScriptVersion))
		w.logger.Debug("Default headers set (User-Agent + Content-Type when applicable)")

		// Set custom headers
		if len(endpoint.Headers) > 0 {
			blockedHeaders := map[string]struct{}{
				"host":              {},
				"content-length":    {},
				"content-type":      {},
				"transfer-encoding": {},
			}
			w.logger.Debug("Applying %d custom header(s)", len(endpoint.Headers))
			for k, v := range endpoint.Headers {
				headerName := strings.ToLower(strings.TrimSpace(k))
				if headerName == "" {
					w.logger.Warning("Skipped empty custom header name")
					continue
				}
				if _, blocked := blockedHeaders[headerName]; blocked {
					w.logger.Warning("Skipped protected custom header %s", k)
					continue
				}
				req.Header.Set(k, v)
				w.logger.Debug("  Custom header: %s = %s", k, maskHeaderValue(k, v))
			}
		}

		// Apply authentication
		if endpoint.Auth.Type != "" && endpoint.Auth.Type != "none" {
			w.logger.Debug("Applying authentication: type=%s", endpoint.Auth.Type)
			if err := w.applyAuthentication(req, endpoint.Auth, payloadBytes); err != nil {
				w.logger.Error("Authentication failed: %v", err)
				return fmt.Errorf("authentication failed: %w", err)
			}
			w.logger.Debug("Authentication applied successfully")
		} else {
			w.logger.Debug("No authentication required")
		}

		// Log final request headers (masked)
		w.logger.Debug("Final request headers:")
		for k := range req.Header {
			w.logger.Debug("  %s: %s", k, maskHeaderValue(k, req.Header.Get(k)))
		}

		// Send request
		w.logger.Debug("Sending HTTP %s request...", method)
		requestStart := time.Now()

		resp, err := w.client.Do(req)
		requestDuration := time.Since(requestStart)

		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			w.logger.Warning("⚠️ Request failed after %dms (attempt %d/%d): %v",
				requestDuration.Milliseconds(), attempt+1, maxRetries+1, err)
			continue
		}

		// Read response body
		w.logger.Debug("Reading response body...")
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			w.logger.Warning("Failed to read response body: %v", err)
			continue
		}

		w.logger.Debug("Received HTTP %d in %dms", resp.StatusCode, requestDuration.Milliseconds())
		if len(body) > 0 {
			if len(body) > 500 {
				w.logger.Debug("Response body (%d bytes, truncated): %s...", len(body), string(body[:500]))
			} else {
				w.logger.Debug("Response body (%d bytes): %s", len(body), string(body))
			}
		} else {
			w.logger.Debug("Response body: empty")
		}

		// Handle HTTP status codes
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			// Success
			w.logger.Info("✅ Webhook '%s' sent successfully: HTTP %d in %dms",
				endpoint.Name, resp.StatusCode, requestDuration.Milliseconds())
			return nil

		case resp.StatusCode == 400:
			// Bad request - don't retry
			w.logger.Error("❌ Bad request (HTTP 400): %s", string(body))
			return fmt.Errorf("bad request (HTTP 400): %s", string(body))

		case resp.StatusCode == 401:
			// Authentication failed - don't retry
			w.logger.Error("❌ Authentication failed (HTTP 401)")
			return fmt.Errorf("authentication failed (HTTP 401)")

		case resp.StatusCode == 403:
			// Forbidden - don't retry
			w.logger.Error("❌ Forbidden (HTTP 403): %s", string(body))
			return fmt.Errorf("forbidden (HTTP 403): %s", string(body))

		case resp.StatusCode == 404:
			// Not found - don't retry
			w.logger.Error("❌ Webhook endpoint not found (HTTP 404)")
			return fmt.Errorf("endpoint not found (HTTP 404)")

		case resp.StatusCode == 429:
			// Rate limit - retry with longer delay
			w.logger.Warning("⚠️ Rate limited (HTTP 429): %s", string(body))
			if attempt < maxRetries {
				w.logger.Debug("Waiting 10 seconds before retry due to rate limiting...")
				time.Sleep(10 * time.Second)
			}
			lastErr = fmt.Errorf("rate limit exceeded (HTTP 429)")
			continue

		case resp.StatusCode >= 500:
			// Server error - retry
			w.logger.Warning("⚠️ Server error (HTTP %d), will retry: %s", resp.StatusCode, string(body))
			lastErr = fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, string(body))
			continue

		default:
			// Unexpected status - retry
			w.logger.Warning("⚠️ Unexpected status (HTTP %d): %s", resp.StatusCode, string(body))
			lastErr = fmt.Errorf("unexpected status (HTTP %d): %s", resp.StatusCode, string(body))
			continue
		}
	}

	// All retries exhausted
	w.logger.Error("❌ Webhook '%s' failed after %d attempt(s): %v", endpoint.Name, maxRetries+1, lastErr)
	return fmt.Errorf("webhook failed after %d attempts: %w", maxRetries+1, lastErr)
}

// buildPayload builds the webhook payload based on format
func (w *WebhookNotifier) buildPayload(format string, data *NotificationData) (interface{}, error) {
	w.logger.Debug("buildPayload() called with format=%s", format)

	switch strings.ToLower(format) {
	case "discord":
		return buildDiscordPayload(data, w.logger)
	case "slack":
		return buildSlackPayload(data, w.logger)
	case "teams":
		return buildTeamsPayload(data, w.logger)
	case "generic":
		return buildGenericPayload(data, w.logger)
	default:
		w.logger.Warning("Unknown format '%s', using generic", format)
		return buildGenericPayload(data, w.logger)
	}
}

// applyAuthentication applies authentication to the HTTP request
func (w *WebhookNotifier) applyAuthentication(req *http.Request, auth config.WebhookAuth, payload []byte) error {
	w.logger.Debug("applyAuthentication() called with type=%s", auth.Type)

	switch strings.ToLower(auth.Type) {
	case "bearer":
		if auth.Token == "" {
			return fmt.Errorf("bearer token is empty")
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", auth.Token))
		w.logger.Debug("Bearer token applied (length: %d characters)", len(auth.Token))
		return nil

	case "basic":
		if auth.User == "" || auth.Pass == "" {
			return fmt.Errorf("basic auth user or password is empty")
		}
		credentials := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", auth.User, auth.Pass)))
		req.Header.Set("Authorization", fmt.Sprintf("Basic %s", credentials))
		w.logger.Debug("Basic auth applied for user: %s", auth.User)
		return nil

	case "hmac", "hmac-sha256":
		if auth.Secret == "" {
			return fmt.Errorf("HMAC secret is empty")
		}
		w.logger.Debug("Calculating HMAC-SHA256 signature with %d-byte secret", len(auth.Secret))
		signature := generateHMACSignature(payload, auth.Secret)
		req.Header.Set("X-Signature", signature)
		req.Header.Set("X-Signature-Algorithm", "hmac-sha256")
		w.logger.Debug("HMAC signature calculated: %s", signature)
		return nil

	case "none", "":
		w.logger.Debug("No authentication required")
		return nil

	default:
		return fmt.Errorf("unknown auth type: %s", auth.Type)
	}
}

// maskURL masks sensitive parts of URL for logging
func maskURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "***INVALID_URL***"
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "***INVALID_URL***"
	}

	var b strings.Builder
	b.Grow(len(parsed.Scheme) + len(parsed.Host) + 16)
	b.WriteString(parsed.Scheme)
	b.WriteString("://")
	b.WriteString(parsed.Host)

	if parsed.Path != "" {
		b.WriteString("/***MASKED***")
	}

	if parsed.RawQuery != "" {
		b.WriteString("?***MASKED***")
	}

	if parsed.Fragment != "" {
		b.WriteString("#***MASKED***")
	}

	return b.String()
}

// maskHeaderValue masks sensitive header values for logging
func maskHeaderValue(key, value string) string {
	key = strings.ToLower(key)
	if strings.Contains(key, "auth") || strings.Contains(key, "token") || strings.Contains(key, "key") || strings.Contains(key, "secret") {
		if len(value) > 10 {
			return value[:4] + "***MASKED***"
		}
		return "***MASKED***"
	}
	return value
}
