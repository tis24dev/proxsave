package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// CloudRelayConfig holds configuration for email cloud relay
type CloudRelayConfig struct {
	WorkerURL   string
	WorkerToken string
	HMACSecret  string
	Timeout     int // seconds
	MaxRetries  int
	RetryDelay  int // seconds
}

// Default cloud relay configuration (hardcoded for compatibility with Bash script)
var DefaultCloudRelayConfig = CloudRelayConfig{
	WorkerURL:   "https://relay-tis24.weathered-hill-5216.workers.dev/send",
	WorkerToken: "v1_public_20251024",
	HMACSecret:  "4cc8946c15338082674d7213aee19069571e1afe60ad21b44be4d68260486fb2", // From wrangler.jsonc
	Timeout:     30,
	MaxRetries:  2,
	RetryDelay:  2,
}

// EmailRelayPayload represents the JSON payload sent to the cloud worker
type EmailRelayPayload struct {
	To        string                 `json:"to"`
	Subject   string                 `json:"subject"`
	Report    map[string]interface{} `json:"report"`
	Timestamp int64                  `json:"t"`
	ServerMAC string                 `json:"server_mac"`

	// Metadata not serialized but needed for headers
	ScriptVersion string `json:"-"`
	ServerID      string `json:"-"`
}

// EmailRelayResponse represents the response from the cloud worker
type EmailRelayResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// sendViaCloudRelay sends an email via Cloudflare Worker relay
func sendViaCloudRelay(
	ctx context.Context,
	config CloudRelayConfig,
	payload EmailRelayPayload,
	logger *logging.Logger,
) error {
	// Use default if not configured
	if config.WorkerURL == "" {
		config = DefaultCloudRelayConfig
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: time.Duration(config.Timeout) * time.Second,
	}

	// Marshal payload to JSON
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Generate HMAC signature
	signature := generateHMACSignature(payloadBytes, config.HMACSecret)

	// Retry loop
	var lastErr error
	skipDefaultDelay := false
	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			if skipDefaultDelay {
				logger.Debug("Retry attempt %d/%d resuming after rate-limit cooldown (no extra delay)", attempt, config.MaxRetries)
				skipDefaultDelay = false
			} else {
				logger.Debug("Retry attempt %d/%d after %ds delay...", attempt, config.MaxRetries, config.RetryDelay)
				time.Sleep(time.Duration(config.RetryDelay) * time.Second)
			}
		}

		// Create request
		req, err := http.NewRequestWithContext(ctx, "POST", config.WorkerURL, bytes.NewReader(payloadBytes))
		if err != nil {
			lastErr = fmt.Errorf("failed to create request: %w", err)
			continue
		}

		// Set headers
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.WorkerToken))
		req.Header.Set("X-Signature", signature)
		req.Header.Set("X-Script-Version", payload.ScriptVersion)
		req.Header.Set("X-Server-MAC", payload.ServerMAC)
		req.Header.Set("User-Agent", fmt.Sprintf("proxsave/%s", payload.ScriptVersion))

		// Send request
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			logger.Warning("Cloud relay request failed (attempt %d/%d): %v", attempt+1, config.MaxRetries+1, err)
			continue
		}

		// Read response body
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			continue
		}

		// Handle HTTP status codes
		switch resp.StatusCode {
		case 200:
			// Success
			logger.Debug("Cloud relay: email sent successfully")
			return nil

		case 400:
			// Bad request - don't retry
			var apiResp EmailRelayResponse
			_ = json.Unmarshal(body, &apiResp)
			return fmt.Errorf("bad request (HTTP 400): %s", apiResp.Error)

		case 401:
			// Authentication failed - don't retry
			return fmt.Errorf("authentication failed (HTTP 401): invalid token")

		case 403:
			// Forbidden - HMAC validation failed - don't retry
			return fmt.Errorf("forbidden (HTTP 403): HMAC signature validation failed")

		case 429:
			// Rate limit exceeded - show detailed message
			var apiResp EmailRelayResponse
			_ = json.Unmarshal(body, &apiResp)
			detail := strings.TrimSpace(apiResp.Message)
			if detail == "" {
				detail = strings.TrimSpace(apiResp.Error)
			}
			if detail == "" {
				detail = "no additional details provided"
			}

			logger.Debug("Cloud relay: rate limit exceeded (HTTP 429): %s", detail)

			// Permanent quota violations should fall back immediately
			if isQuotaLimit(detail) {
				return fmt.Errorf("rate limit exceeded: %s", detail)
			}

			// If last attempt, return error with guidance
			if attempt == config.MaxRetries {
				return fmt.Errorf("rate limit exceeded: %s", detail)
			}

			// Otherwise, retry with longer delay
			logger.Debug("Waiting 5 seconds before retry due to rate limiting...")
			time.Sleep(5 * time.Second)
			skipDefaultDelay = true
			lastErr = fmt.Errorf("rate limit exceeded")
			continue

		case 500, 502, 503, 504:
			// Server errors - retry
			logger.Warning("Cloud relay: server error (HTTP %d), will retry", resp.StatusCode)
			lastErr = fmt.Errorf("server error (HTTP %d): %s", resp.StatusCode, string(body))
			continue

		default:
			// Unexpected status - retry
			logger.Warning("Cloud relay: unexpected status (HTTP %d): %s", resp.StatusCode, string(body))
			lastErr = fmt.Errorf("unexpected status (HTTP %d): %s", resp.StatusCode, string(body))
			continue
		}
	}

	// All retries exhausted
	return fmt.Errorf("cloud relay failed after %d attempts: %w", config.MaxRetries+1, lastErr)
}

// generateHMACSignature generates an HMAC-SHA256 signature for the payload
func generateHMACSignature(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// isQuotaLimit returns true if the rate-limit detail clearly indicates a quota cap
// (e.g., daily per-server quota) that won't succeed with retries.
func isQuotaLimit(detail string) bool {
	lower := strings.ToLower(detail)
	keywords := []string{
		"quota",
		"per server",
		"per account",
		"daily",
		"write me on github",
	}
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// buildReportData builds the structured report data for cloud relay
// This must match the Bash script's collect_email_report_data() output exactly
// to ensure HMAC signature validation passes
func buildReportData(data *NotificationData) map[string]interface{} {
	// Build nested structure matching Bash format exactly
	return map[string]interface{}{
		// Top-level fields
		"status":         data.Status.String(),
		"status_message": data.StatusMessage,
		"status_color":   getStatusColor(data.Status),
		"proxmox_type":   data.ProxmoxType.String(),
		"hostname":       data.Hostname,
		"server_id":      data.ServerID,
		"server_mac":     data.ServerMAC,
		"backup_date":    data.BackupDate.Format("2006-01-02 15:04:05"),
		"script_version": data.ScriptVersion,

		// Nested emojis object
		"emojis": map[string]interface{}{
			"primary":   GetStorageEmoji(data.LocalStatus),
			"secondary": GetStorageEmoji(data.SecondaryStatus),
			"cloud":     GetStorageEmoji(data.CloudStatus),
			"email":     GetStorageEmoji(data.EmailStatus),
		},

		// Nested backup object with sub-objects
		"backup": map[string]interface{}{
			"primary": map[string]interface{}{
				"status": data.LocalStatusSummary,
				"emoji":  GetStorageEmoji(data.LocalStatus),
				"count":  data.LocalCount,
			},
			"secondary": map[string]interface{}{
				"status": data.SecondaryStatusSummary,
				"emoji":  GetStorageEmoji(data.SecondaryStatus),
				"count":  data.SecondaryCount,
			},
			"cloud": map[string]interface{}{
				"status": data.CloudStatusSummary,
				"emoji":  GetStorageEmoji(data.CloudStatus),
				"count":  data.CloudCount,
			},
		},

		// Nested storage object with local and secondary sub-objects
		"storage": buildStorageData(data),

		// Nested metrics object
		"metrics": map[string]interface{}{
			"backup_file_name":  data.BackupFileName,
			"files_included":    data.FilesIncluded,
			"file_missing":      data.FilesMissing, // Fixed: was "files_missing"
			"backup_duration":   FormatDuration(data.BackupDuration),
			"backup_size":       data.BackupSizeHR,
			"compression_type":  data.CompressionType,
			"compression_level": fmt.Sprintf("%d", data.CompressionLevel),
			"compression_mode":  data.CompressionMode,
			"compression_ratio": fmt.Sprintf("%.2f", data.CompressionRatio),
		},

		// Nested log_summary object with categories array
		"log_summary": map[string]interface{}{
			"errors":         data.ErrorCount,
			"warnings":       data.WarningCount,
			"total":          data.TotalIssues,
			"log_file":       data.LogFilePath,
			"categories":     data.LogCategories,
			"color":          getLogSummaryColor(data.ErrorCount, data.WarningCount),
			"has_categories": len(data.LogCategories) > 0,
			"has_entries":    data.ErrorCount > 0 || data.WarningCount > 0,
		},

		// Nested paths object
		"paths": map[string]interface{}{
			"local":         data.LocalPath,
			"secondary":     data.SecondaryPath,
			"cloud":         data.CloudPath,
			"cloud_display": formatCloudPathDisplay(data.CloudPath),
			"has_secondary": data.SecondaryEnabled,
			"has_cloud":     data.CloudEnabled,
		},

		// Exit code at top level
		"exit_code": data.ExitCode,
	}
}

// buildStorageData builds the storage section matching Bash format
func buildStorageData(data *NotificationData) map[string]interface{} {
	storage := map[string]interface{}{
		"local": map[string]interface{}{
			"space":       data.LocalFree, // Total space shown as free space
			"used":        data.LocalUsed,
			"free":        data.LocalFree,
			"percent":     data.LocalPercent,
			"percent_num": data.LocalUsagePercent,
		},
	}

	// Only add secondary if enabled
	if data.SecondaryEnabled {
		storage["secondary"] = map[string]interface{}{
			"space":       data.SecondaryFree, // Total space shown as free space
			"used":        data.SecondaryUsed,
			"free":        data.SecondaryFree,
			"percent":     data.SecondaryPercent,
			"percent_num": data.SecondaryUsagePercent,
		}
	}

	return storage
}

// getLogSummaryColor returns the hex color code for log summary border based on error/warning counts
func getLogSummaryColor(errorCount, warningCount int) string {
	if errorCount > 0 {
		return "#dc3545" // Red for errors
	}
	if warningCount > 0 {
		return "#ffc107" // Yellow for warnings
	}
	return "#28a745" // Green for no issues
}

// formatCloudPathDisplay formats cloud path for display
func formatCloudPathDisplay(cloudPath string) string {
	if cloudPath == "" {
		return "Not configured"
	}
	// If it's a PBS repository format (user@host:datastore), extract meaningful parts
	// Otherwise just return as-is
	return cloudPath
}
