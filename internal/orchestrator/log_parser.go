package orchestrator

import (
	"bufio"
	"os"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// notifyErrorToken is the level token the notify-scoped logger writes for a
// notification/communication failure. Single-sourced from logging.NotifyErrorLabel
// (the emitter) so the parser can never drift from it; a change to the value there
// propagates here at compile time. A NOTIFY-ERR line displays as an error but is
// counted separately (warning-weight) for the run status.
const notifyErrorToken = logging.NotifyErrorLabel

// ParseLogCounts parses a log file and returns error/warning/notify counts and
// categorized issues. This is used both during backup completion and notification
// generation. notifyCount tallies NOTIFY-ERR lines (notification/communication
// failures) which display as errors but are warning-weight for the run status.
func ParseLogCounts(logPath string, categoryLimit int) (categories []notify.LogCategory, errorCount, warningCount, notifyCount int) {
	if strings.TrimSpace(logPath) == "" {
		return nil, 0, 0, 0
	}

	file, err := safefs.OpenFileUnderRoot(logPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, 0, 0, 0
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	categoryMap := make(map[string]*notify.LogCategory)
	errorCount = 0
	warningCount = 0

	for scanner.Scan() {
		entryType, message := classifyLogLine(scanner.Text())
		if entryType == "" || message == "" {
			continue
		}

		switch entryType {
		case "error":
			errorCount++
		case "warning":
			warningCount++
		case "notify_error":
			// Notification/communication failure: warning-weight for the run
			// status, but shown as an error in the category list.
			notifyCount++
		}

		label, example := splitCategoryAndExample(message)
		if label == "" {
			continue
		}

		// A notify-error displays as ERROR even though it is counted separately.
		categoryType := strings.ToUpper(entryType)
		if entryType == "notify_error" {
			categoryType = "ERROR"
		}

		key := entryType + "::" + label
		if cat, ok := categoryMap[key]; ok {
			cat.Count++
			if cat.Example == "" && example != "" {
				cat.Example = example
			}
		} else {
			categoryMap[key] = &notify.LogCategory{
				Label:   label,
				Type:    categoryType,
				Count:   1,
				Example: example,
			}
		}
	}

	if len(categoryMap) == 0 {
		return nil, errorCount, warningCount, notifyCount
	}

	list := make([]notify.LogCategory, 0, len(categoryMap))
	for _, cat := range categoryMap {
		list = append(list, *cat)
	}

	// Sort by type (ERROR before WARNING), symbol, count (descending), then label
	sortLogCategories(list)

	if categoryLimit > 0 && len(list) > categoryLimit {
		return list[:categoryLimit], errorCount, warningCount, notifyCount
	}
	return list, errorCount, warningCount, notifyCount
}

// sortLogCategories sorts log categories by priority: ERROR before WARNING; within a
// type the labels that open with ✗ (theme.SymbolError), then those that open with ⚠
// (theme.SymbolWarning, the ⚠️ form included), then all the others; then by count,
// descending; then by label, ascending and CASE-INSENSITIVELY. A raw byte compare put
// every lowercase-initial label below every uppercase one (39 production warnings begin
// lowercase against 645 uppercase), and without the symbol rank a "✗ ... backup not
// saved" sorted after every plain label, at the bottom of a long list.
func sortLogCategories(list []notify.LogCategory) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Type != b.Type {
			// "ERROR" < "WARNING" lexicographically.
			return a.Type < b.Type
		}
		if ra, rb := logCategorySymbolRank(a.Label), logCategorySymbolRank(b.Label); ra != rb {
			return ra < rb
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if la, lb := strings.ToLower(a.Label), strings.ToLower(b.Label); la != lb {
			return la < lb
		}
		return a.Label < b.Label
	})
}

// logCategorySymbolRank is 0 for a label that opens with the error symbol, 1 for one
// that opens with the warning symbol (with or without its variation selector), 2 for
// any other.
func logCategorySymbolRank(label string) int {
	label = strings.TrimSpace(label)
	switch {
	case strings.HasPrefix(label, theme.SymbolError):
		return 0
	case strings.HasPrefix(label, theme.SymbolWarning):
		return 1
	default:
		return 2
	}
}

// classifyLogLine extracts the entry type and message from a log line
// Supports both Go format ("[2025-11-14 10:30:45] WARNING message") and Bash format ("[WARNING] message")
func classifyLogLine(line string) (entryType, message string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}

	// Only count issues from non-debug lines.
	//
	// Supported formats:
	// - Go file logger: "[2025-11-14 10:30:45] WARNING  message"
	// - Legacy/Bash:    "[WARNING] message"
	// - Mixed:          "[2025-11-14 10:30:45] [WARNING] message"

	// 1) Pure bracketed legacy format (no timestamp).
	if t, msg := classifyBracketedIssueLine(line); t != "" {
		return t, msg
	}

	// 2) Go logger format with timestamp prefix.
	if strings.HasPrefix(line, "[") {
		if closeIdx := strings.Index(line, "]"); closeIdx != -1 {
			rest := strings.TrimSpace(line[closeIdx+1:])
			if rest == "" {
				return "", ""
			}

			// Timestamp + "[WARNING]/[ERROR]" legacy style.
			if t, msg := classifyBracketedIssueLine(rest); t != "" {
				return t, msg
			}

			levelToken, msg := splitFirstToken(rest)
			if levelToken == "" {
				return "", ""
			}

			switch strings.ToUpper(levelToken) {
			case "DEBUG":
				return "", ""
			case "WARNING":
				msg = sanitizeLogMessage(msg)
				if msg == "" {
					return "", ""
				}
				return "warning", msg
			case "ERROR", "CRITICAL":
				msg = sanitizeLogMessage(msg)
				if msg == "" {
					return "", ""
				}
				return "error", msg
			case notifyErrorToken:
				// Notification/communication failure: displays as an error but is
				// counted separately (warning-weight) for the run status.
				msg = sanitizeLogMessage(msg)
				if msg == "" {
					return "", ""
				}
				return "notify_error", msg
			default:
				return "", ""
			}
		}
	}

	return "", ""
}

func classifyBracketedIssueLine(line string) (entryType, message string) {
	if strings.HasPrefix(line, "[ERROR]") || strings.HasPrefix(line, "[Error]") || strings.HasPrefix(line, "[error]") {
		msg := strings.TrimSpace(line[strings.Index(line, "]")+1:])
		msg = sanitizeLogMessage(msg)
		if msg == "" {
			return "", ""
		}
		return "error", msg
	}
	if strings.HasPrefix(line, "[WARNING]") || strings.HasPrefix(line, "[Warning]") || strings.HasPrefix(line, "[warning]") {
		msg := strings.TrimSpace(line[strings.Index(line, "]")+1:])
		msg = sanitizeLogMessage(msg)
		if msg == "" {
			return "", ""
		}
		return "warning", msg
	}
	return "", ""
}

func splitFirstToken(value string) (token, rest string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	for i := 0; i < len(value); i++ {
		if value[i] == ' ' || value[i] == '\t' || value[i] == '\n' || value[i] == '\r' {
			return value[:i], strings.TrimSpace(value[i:])
		}
	}
	return value, ""
}

// sanitizeLogMessage cleans up log messages
func sanitizeLogMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	switch {
	case strings.HasPrefix(msg, "[Warning]"):
		msg = strings.TrimSpace(msg[len("[Warning]"):])
	case strings.HasPrefix(msg, "[warning]"):
		msg = strings.TrimSpace(msg[len("[warning]"):])
	case strings.HasPrefix(msg, "[Error]"):
		msg = strings.TrimSpace(msg[len("[Error]"):])
	case strings.HasPrefix(msg, "[error]"):
		msg = strings.TrimSpace(msg[len("[error]"):])
	}

	if strings.HasPrefix(msg, "#") {
		i := 1
		for i < len(msg) && msg[i] >= '0' && msg[i] <= '9' {
			i++
		}
		msg = strings.TrimSpace(msg[i:])
	}

	if len(msg) > 300 {
		msg = msg[:297] + "..."
	}
	return msg
}

// splitCategoryAndExample splits a message into category and example
func splitCategoryAndExample(msg string) (label, example string) {
	parts := strings.SplitN(msg, " - ", 2)
	label = strings.TrimSpace(parts[0])
	if label == "" {
		label = msg
	}
	label = truncateString(label, 120)

	if len(parts) == 2 {
		example = strings.TrimSpace(parts[1])
	} else {
		example = msg
	}
	example = truncateString(example, 120)
	return label, example
}

// truncateString truncates a string to a maximum length, adding "..." if truncated
func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	if max > 3 {
		return value[:max-3] + "..."
	}
	return value[:max]
}
