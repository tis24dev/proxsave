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

// FindInlineCommentIndex returns the index of a # that starts an inline comment.
// A # inside quotes or escaped with a backslash is ignored.
func FindInlineCommentIndex(line string) int {
	inQuote := false
	var quoteChar byte
	escaped := false

	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if inQuote {
			if ch == quoteChar {
				inQuote = false
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = true
			quoteChar = ch
			continue
		}
		if ch == '#' {
			return i
		}
	}
	return -1
}

// FindClosingQuoteIndex returns the index of the closing quote in s,
// honoring backslash escapes. Assumes s[0] is the opening quote.
func FindClosingQuoteIndex(s string, quote byte) int {
	escaped := false
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == quote {
			return i
		}
	}
	return -1
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

	if strings.HasPrefix(valuePart, "\"") || strings.HasPrefix(valuePart, "'") {
		quote := valuePart[0]
		if endIdx := FindClosingQuoteIndex(valuePart, quote); endIdx >= 0 {
			valuePart = valuePart[:endIdx+1]
		}
	} else if idx := FindInlineCommentIndex(valuePart); idx >= 0 {
		valuePart = strings.TrimSpace(valuePart[:idx])
	}

	value := TrimQuotes(strings.TrimSpace(valuePart))
	return key, value, true
}

// SetEnvValue sets or updates a KEY=VALUE line in a template, preserving indentation and comments.
func SetEnvValue(template, key, value string) string {
	lines := strings.Split(template, "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if IsComment(trimmed) {
			continue
		}

		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) >= 1 {
			parsedKey := strings.TrimSpace(parts[0])
			exportPrefix := ""
			if fields := strings.Fields(parsedKey); len(fields) >= 2 && fields[0] == "export" {
				parsedKey = fields[1]
				exportPrefix = "export "
			}

			if parsedKey != key {
				continue
			}
			// Found match.
			leadingLen := len(line) - len(strings.TrimLeft(line, " \t"))
			leading := ""
			if leadingLen > 0 {
				leading = line[:leadingLen]
			}

			comment := ""
			commentSpacing := ""

			commentIdx := FindInlineCommentIndex(line)
			if commentIdx >= 0 {
				comment = line[commentIdx:]

				beforeComment := line[:commentIdx]
				trimmedBefore := strings.TrimRight(beforeComment, " \t")
				if len(trimmedBefore) < len(beforeComment) {
					commentSpacing = beforeComment[len(trimmedBefore):]
				}
			}

			newLine := leading + exportPrefix + key + "=" + value
			if comment != "" {
				spacing := commentSpacing
				if spacing == "" {
					spacing = " "
				}
				newLine += spacing + comment
			}
			lines[i] = newLine
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	return strings.Join(lines, "\n")
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
