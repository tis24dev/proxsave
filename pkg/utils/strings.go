package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// FormatBytes converts bytes to a human-readable format (KB, MB, GB, etc.).
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ParseBool converts a string to a boolean (supports multiple formats).
func ParseBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes" || s == "on" || s == "enabled"
}

// TrimQuotes removes surrounding quotes from a string.
func TrimQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// SplitKeyValue splits a "key=value" string into key and value.
// Supports inline comments too: KEY="value" # comment
func SplitKeyValue(line string) (string, string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	valuePart := strings.TrimSpace(parts[1])

	// Remove inline comments (but respect quotes)
	// If the value is quoted, take everything inside quotes
	// Otherwise, stop at the first #
	value := valuePart
	if strings.HasPrefix(valuePart, "\"") || strings.HasPrefix(valuePart, "'") {
		// Find the closing quote
		quote := valuePart[0]
		endIdx := strings.IndexByte(valuePart[1:], quote)
		if endIdx >= 0 {
			value = valuePart[:endIdx+2] // Include both quotes
		}
	} else {
		// Not quoted, remove everything after #
		if idx := strings.Index(valuePart, "#"); idx >= 0 {
			value = strings.TrimSpace(valuePart[:idx])
		}
	}

	value = TrimQuotes(strings.TrimSpace(value))
	return key, value, true
}

// IsComment checks whether a line is a comment (starts with #).
func IsComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "#") || trimmed == ""
}

// GenerateRandomString generates a random string of the specified length
func GenerateRandomString(length int) string {
	bytes := make([]byte, (length+1)/2)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to a simple timestamp-based string if crypto/rand fails
		return fmt.Sprintf("%d", length)
	}
	return hex.EncodeToString(bytes)[:length]
}
