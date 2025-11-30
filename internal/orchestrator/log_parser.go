package orchestrator

import (
	"bufio"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/notify"
)

// ParseLogCounts parses a log file and returns error/warning counts and categorized issues
// This is used both during backup completion and notification generation
func ParseLogCounts(logPath string, categoryLimit int) (categories []notify.LogCategory, errorCount, warningCount int) {
	if strings.TrimSpace(logPath) == "" {
		return nil, 0, 0
	}

	file, err := os.Open(logPath)
	if err != nil {
		return nil, 0, 0
	}
	defer file.Close()

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
		}

		label, example := splitCategoryAndExample(message)
		if label == "" {
			continue
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
				Type:    strings.ToUpper(entryType),
				Count:   1,
				Example: example,
			}
		}
	}

	if len(categoryMap) == 0 {
		return nil, errorCount, warningCount
	}

	list := make([]notify.LogCategory, 0, len(categoryMap))
	for _, cat := range categoryMap {
		list = append(list, *cat)
	}

	// Sort by type (ERROR before WARNING), then by count (descending), then by label
	sortLogCategories(list)

	if categoryLimit > 0 && len(list) > categoryLimit {
		return list[:categoryLimit], errorCount, warningCount
	}
	return list, errorCount, warningCount
}

// sortLogCategories sorts log categories by priority
func sortLogCategories(list []notify.LogCategory) {
	// Simple bubble sort - good enough for small lists (typically < 20 items)
	n := len(list)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			// Compare by type (ERROR before WARNING)
			if list[j].Type != list[j+1].Type {
				// "ERROR" < "WARNING" lexicographically, so swap if j is WARNING
				if list[j].Type > list[j+1].Type { // "WARNING" > "ERROR", swap to put ERROR first
					list[j], list[j+1] = list[j+1], list[j]
				}
				continue
			}
			// Same type, compare by count (descending)
			if list[j].Count != list[j+1].Count {
				if list[j].Count < list[j+1].Count {
					list[j], list[j+1] = list[j+1], list[j]
				}
				continue
			}
			// Same count, compare by label (ascending)
			if list[j].Label > list[j+1].Label {
				list[j], list[j+1] = list[j+1], list[j]
			}
		}
	}
}

// classifyLogLine extracts the entry type and message from a log line
// Supports both Go format ("[2025-11-14 10:30:45] WARNING message") and Bash format ("[WARNING] message")
func classifyLogLine(line string) (entryType, message string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ""
	}

	// Try to match both formats:
	// - Go format: "[2025-11-14 10:30:45] WARNING  message"
	// - Bash format: "[WARNING] message"
	for _, tag := range []struct {
		Type string
		Tags []string
	}{
		{"error", []string{"[ERROR]", "[Error]", "[error]", "ERROR", "Error"}},
		{"warning", []string{"[WARNING]", "[Warning]", "[warning]", "WARNING", "Warning"}},
	} {
		for _, marker := range tag.Tags {
			if idx := strings.Index(line, marker); idx != -1 {
				// Extract message after the marker
				afterMarker := idx + len(marker)
				if afterMarker < len(line) {
					msg := strings.TrimSpace(line[afterMarker:])
					msg = sanitizeLogMessage(msg)
					if msg != "" {
						return tag.Type, msg
					}
				}
			}
		}
	}
	return "", ""
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

	if len(msg) > 200 {
		msg = msg[:200]
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

// truncateString truncates a string to a maximum length
func truncateString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
