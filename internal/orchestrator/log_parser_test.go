package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/notify"
)

func TestParseLogCounts(t *testing.T) {
	// Create a temporary log file with known content
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	content := `[2025-11-10 14:30:00] INFO Starting backup
[2025-11-10 14:30:01] [WARNING] Failed to backup file /etc/test.conf - File not found
[2025-11-10 14:30:02] [ERROR] Database connection failed - Timeout after 30s
[2025-11-10 14:30:03] [WARNING] Disk space low - Only 5GB remaining
[2025-11-10 14:30:04] [ERROR] Permission denied - Cannot access /root/secrets
[2025-11-10 14:30:05] [WARNING] Failed to backup file /var/log/test.log - File not found
[2025-11-10 14:30:06] INFO Backup completed
`

	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test log file: %v", err)
	}

	// Parse the log file
	categories, errorCount, warningCount := ParseLogCounts(logFile, 10)

	// Verify counts
	if errorCount != 2 {
		t.Errorf("Expected 2 errors, got %d", errorCount)
	}
	if warningCount != 3 {
		t.Errorf("Expected 3 warnings, got %d", warningCount)
	}

	// Verify categories were extracted
	if len(categories) == 0 {
		t.Error("Expected categories to be extracted, got none")
	}

	// Verify category structure
	foundError := false
	foundWarning := false
	for _, cat := range categories {
		if cat.Type == "ERROR" {
			foundError = true
			if cat.Count == 0 {
				t.Errorf("ERROR category should have count > 0, got %d", cat.Count)
			}
		}
		if cat.Type == "WARNING" {
			foundWarning = true
			if cat.Count == 0 {
				t.Errorf("WARNING category should have count > 0, got %d", cat.Count)
			}
		}
	}

	if !foundError {
		t.Error("Expected to find ERROR categories")
	}
	if !foundWarning {
		t.Error("Expected to find WARNING categories")
	}
}

func TestParseLogCountsEmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "empty.log")

	if err := os.WriteFile(logFile, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create empty log file: %v", err)
	}

	categories, errorCount, warningCount := ParseLogCounts(logFile, 10)

	if errorCount != 0 {
		t.Errorf("Expected 0 errors in empty file, got %d", errorCount)
	}
	if warningCount != 0 {
		t.Errorf("Expected 0 warnings in empty file, got %d", warningCount)
	}
	if len(categories) != 0 {
		t.Errorf("Expected no categories in empty file, got %d", len(categories))
	}
}

func TestParseLogCountsNonExistentFile(t *testing.T) {
	categories, errorCount, warningCount := ParseLogCounts("/nonexistent/file.log", 10)

	if errorCount != 0 {
		t.Errorf("Expected 0 errors for nonexistent file, got %d", errorCount)
	}
	if warningCount != 0 {
		t.Errorf("Expected 0 warnings for nonexistent file, got %d", warningCount)
	}
	if len(categories) != 0 {
		t.Errorf("Expected no categories for nonexistent file, got %d", len(categories))
	}
}

func TestParseLogCountsCategoryLimit(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	// Create log with many different error types
	content := `[ERROR] Error type 1
[ERROR] Error type 2
[ERROR] Error type 3
[ERROR] Error type 4
[ERROR] Error type 5
[WARNING] Warning type 1
[WARNING] Warning type 2
[WARNING] Warning type 3
`

	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test log file: %v", err)
	}

	// Parse with limit of 3
	categories, errorCount, warningCount := ParseLogCounts(logFile, 3)

	// Counts should be correct regardless of limit
	if errorCount != 5 {
		t.Errorf("Expected 5 errors, got %d", errorCount)
	}
	if warningCount != 3 {
		t.Errorf("Expected 3 warnings, got %d", warningCount)
	}

	// Categories should be limited to 3
	if len(categories) != 3 {
		t.Errorf("Expected 3 categories (limited), got %d", len(categories))
	}
}

func TestClassifyLogLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantType    string
		wantMessage string
	}{
		{
			name:        "ERROR tag",
			line:        "[2025-11-10 14:30:00] [ERROR] Database connection failed",
			wantType:    "error",
			wantMessage: "Database connection failed",
		},
		{
			name:        "WARNING tag",
			line:        "[2025-11-10 14:30:00] [WARNING] Disk space low",
			wantType:    "warning",
			wantMessage: "Disk space low",
		},
		{
			name:        "INFO tag (should be ignored)",
			line:        "[2025-11-10 14:30:00] [INFO] Backup started",
			wantType:    "",
			wantMessage: "",
		},
		{
			name:        "No tag",
			line:        "Just a regular log line",
			wantType:    "",
			wantMessage: "",
		},
		{
			name:        "Empty line",
			line:        "",
			wantType:    "",
			wantMessage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotMessage := classifyLogLine(tt.line)
			if gotType != tt.wantType {
				t.Errorf("classifyLogLine() type = %v, want %v", gotType, tt.wantType)
			}
			if tt.wantMessage != "" && gotMessage != tt.wantMessage {
				t.Errorf("classifyLogLine() message = %v, want %v", gotMessage, tt.wantMessage)
			}
		})
	}
}

func TestSplitCategoryAndExample(t *testing.T) {
	tests := []struct {
		name         string
		message      string
		wantLabel    string
		wantExample  string
		checkExample bool
	}{
		{
			name:         "With separator",
			message:      "Failed to backup file - /etc/test.conf not found",
			wantLabel:    "Failed to backup file",
			wantExample:  "/etc/test.conf not found",
			checkExample: true,
		},
		{
			name:        "Without separator",
			message:     "Database connection timeout",
			wantLabel:   "Database connection timeout",
			wantExample: "Database connection timeout",
		},
		{
			name:      "Empty message",
			message:   "",
			wantLabel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLabel, gotExample := splitCategoryAndExample(tt.message)
			if gotLabel != tt.wantLabel {
				t.Errorf("splitCategoryAndExample() label = %v, want %v", gotLabel, tt.wantLabel)
			}
			if tt.checkExample && gotExample != tt.wantExample {
				t.Errorf("splitCategoryAndExample() example = %v, want %v", gotExample, tt.wantExample)
			}
		})
	}
}

func TestSortLogCategories(t *testing.T) {
	categories := []notify.LogCategory{
		{Label: "Warning B", Type: "WARNING", Count: 5},
		{Label: "Error A", Type: "ERROR", Count: 3},
		{Label: "Warning A", Type: "WARNING", Count: 10},
		{Label: "Error B", Type: "ERROR", Count: 7},
	}

	sortLogCategories(categories)

	// Verify ERROR comes before WARNING
	if categories[0].Type != "ERROR" {
		t.Errorf("Expected first category to be ERROR, got %s", categories[0].Type)
	}

	// Verify sorting within same type (by count descending)
	if categories[0].Count < categories[1].Count {
		t.Error("ERROR categories should be sorted by count descending")
	}
}

func TestSanitizeLogMessage(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"[Warning] something bad", "something bad"},
		{"[error] details", "details"},
		{"#123 some code", "some code"},
		{strings.Repeat("a", 300), strings.Repeat("a", 200)},
	}
	for _, c := range cases {
		if got := sanitizeLogMessage(c.in); got != c.want {
			t.Fatalf("sanitizeLogMessage(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestTruncateString(t *testing.T) {
	if got := truncateString("short", 10); got != "short" {
		t.Fatalf("truncateString short = %q; want short", got)
	}
	if got := truncateString("longstring", 4); got != "long" {
		t.Fatalf("truncateString long = %q; want long", got)
	}
	if got := truncateString("exact", 5); got != "exact" {
		t.Fatalf("truncateString exact = %q; want exact", got)
	}
}
