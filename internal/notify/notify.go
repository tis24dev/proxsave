// Package notify provides notification services for backup operations.
// It supports multiple notification channels (Telegram, Email) with
// configurable delivery methods and comprehensive error handling.
package notify

import (
	"context"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

// NotificationStatus represents the overall status of a backup operation
type NotificationStatus int

const (
	StatusSuccess NotificationStatus = iota
	StatusWarning
	StatusFailure
)

// String returns the string representation of NotificationStatus
func (s NotificationStatus) String() string {
	switch s {
	case StatusSuccess:
		return "success"
	case StatusWarning:
		return "warning"
	case StatusFailure:
		return "failure"
	default:
		return "unknown"
	}
}

// StatusFromExitCode maps a process exit code to a notification status.
// This allows logs and notifications (email subject emoji, Telegram, etc.)
// to stay in sync with the actual exit code emitted by the process.
func StatusFromExitCode(exitCode int) NotificationStatus {
	switch exitCode {
	case types.ExitSuccess.Int():
		return StatusSuccess
	case types.ExitGenericError.Int():
		return StatusWarning
	default:
		return StatusFailure
	}
}

// NotificationData contains all information to be sent in notifications
type NotificationData struct {
	// Overall status
	Status        NotificationStatus
	StatusMessage string
	ExitCode      int

	// System information
	Hostname    string
	ProxmoxType types.ProxmoxType // PVE or PBS
	ServerID    string            // Server identifier
	ServerMAC   string            // MAC address for rate limiting

	// Backup metadata
	BackupDate     time.Time
	BackupDuration time.Duration
	BackupFile     string // Full path to backup file
	BackupFileName string // Just the filename (for email display)
	BackupSize     int64  // bytes
	BackupSizeHR   string // human-readable

	// Compression info
	CompressionType  string
	CompressionLevel int
	CompressionMode  string
	CompressionRatio float64

	// File counts
	FilesIncluded int
	FilesMissing  int

	// Storage status
	LocalStatus        string
	LocalStatusSummary string
	LocalCount         int
	LocalFree          string
	LocalUsed          string
	LocalPercent       string
	LocalSpaceBytes    uint64
	LocalUsagePercent  float64

	// Local retention info
	LocalRetentionPolicy   string // "simple" or "gfs"
	LocalRetentionLimit    int    // MAX_LOCAL_BACKUPS (simple mode)
	LocalGFSDaily          int    // GFS limits
	LocalGFSWeekly         int
	LocalGFSMonthly        int
	LocalGFSYearly         int
	LocalGFSCurrentDaily   int // GFS current counts
	LocalGFSCurrentWeekly  int
	LocalGFSCurrentMonthly int
	LocalGFSCurrentYearly  int
	LocalBackups           int // Total current backups

	SecondaryEnabled       bool
	SecondaryStatus        string
	SecondaryStatusSummary string
	SecondaryCount         int
	SecondaryFree          string
	SecondaryUsed          string
	SecondaryPercent       string
	SecondarySpaceBytes    uint64
	SecondaryUsagePercent  float64

	// Secondary retention info
	SecondaryRetentionPolicy   string
	SecondaryRetentionLimit    int
	SecondaryGFSDaily          int
	SecondaryGFSWeekly         int
	SecondaryGFSMonthly        int
	SecondaryGFSYearly         int
	SecondaryGFSCurrentDaily   int
	SecondaryGFSCurrentWeekly  int
	SecondaryGFSCurrentMonthly int
	SecondaryGFSCurrentYearly  int
	SecondaryBackups           int

	CloudEnabled       bool
	CloudStatus        string
	CloudStatusSummary string
	CloudCount         int

	// Cloud retention info
	CloudRetentionPolicy   string
	CloudRetentionLimit    int
	CloudGFSDaily          int
	CloudGFSWeekly         int
	CloudGFSMonthly        int
	CloudGFSYearly         int
	CloudGFSCurrentDaily   int
	CloudGFSCurrentWeekly  int
	CloudGFSCurrentMonthly int
	CloudGFSCurrentYearly  int
	CloudBackups           int

	// Email notification status (for Telegram messages)
	EmailStatus    string
	TelegramStatus string

	// Paths
	LocalPath     string
	SecondaryPath string
	CloudPath     string

	// Error/Warning summary
	ErrorCount    int
	WarningCount  int
	LogFilePath   string
	TotalIssues   int
	LogCategories []LogCategory

	// Script metadata
	ScriptVersion string
}

// LogCategory represents a normalized log issue classification.
type LogCategory struct {
	Label   string `json:"label"`
	Type    string `json:"type"` // ERROR/WARNING
	Count   int    `json:"count"`
	Example string `json:"example,omitempty"`
}

// StorageStatus represents the status of a storage location
type StorageStatus struct {
	Enabled        bool
	Status         string // "ok", "warning", "error", "disabled"
	BackupCount    int
	FreeSpace      string // human-readable
	FreeSpaceBytes uint64
	UsagePercent   float64
}

// NotificationResult represents the result of a notification attempt
type NotificationResult struct {
	Success      bool
	UsedFallback bool   // True if fallback method was used after primary failed
	Method       string // "telegram", "email-relay", "email-sendmail"
	Error        error  // Original error (even if fallback succeeded)
	Duration     time.Duration
	Metadata     map[string]interface{} // Additional info (HTTP status, etc.)
}

// Notifier is the interface that must be implemented by all notification providers
type Notifier interface {
	// Name returns the notifier name (e.g., "Telegram", "Email")
	Name() string

	// IsEnabled returns whether this notifier is enabled
	IsEnabled() bool

	// Send sends a notification with the provided data
	// Returns error only for critical failures; non-critical errors are logged
	Send(ctx context.Context, data *NotificationData) (*NotificationResult, error)

	// IsCritical returns whether failures from this notifier should abort the backup
	// In practice, all notifiers should return false (notifications never abort backup)
	IsCritical() bool
}

// GetStatusEmoji returns the emoji for a given status
func GetStatusEmoji(status NotificationStatus) string {
	switch status {
	case StatusSuccess:
		return "✅"
	case StatusWarning:
		return "⚠️"
	case StatusFailure:
		return "❌"
	default:
		return "❓"
	}
}

// GetStorageEmoji returns the emoji for a storage status string
func GetStorageEmoji(status string) string {
	switch status {
	case "ok", "success":
		return "✅"
	case "warning":
		return "⚠️"
	case "error", "failed":
		return "❌"
	case "disabled", "skipped":
		return "➖"
	default:
		return "❓"
	}
}

// FormatDuration formats a duration in human-readable format (e.g., "2h 15m 30s")
func FormatDuration(d time.Duration) string {
	if d < time.Second {
		return "< 1s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return formatWithUnits(hours, minutes, seconds, "h", "m", "s")
	} else if minutes > 0 {
		return formatWithUnits(minutes, seconds, 0, "m", "s", "")
	}
	return formatWithUnits(seconds, 0, 0, "s", "", "")
}

func formatWithUnits(v1, v2, v3 int, u1, u2, u3 string) string {
	result := ""
	if v1 > 0 {
		result += formatUnit(v1, u1)
	}
	if v2 > 0 {
		if result != "" {
			result += " "
		}
		result += formatUnit(v2, u2)
	}
	if v3 > 0 && u3 != "" {
		if result != "" {
			result += " "
		}
		result += formatUnit(v3, u3)
	}
	return result
}

func formatUnit(value int, unit string) string {
	if unit == "" {
		return ""
	}
	return formatInt(value) + unit
}

func formatInt(n int) string {
	// Simple int to string conversion
	if n == 0 {
		return "0"
	}

	// Build number string
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
