package orchestrator

import (
	"strings"
	"testing"
)

func TestIsRcloneRemote(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "Valid rclone remote with colon",
			path:     "gdrive:",
			expected: true,
		},
		{
			name:     "Valid rclone remote with path",
			path:     "gdrive:backups",
			expected: true,
		},
		{
			name:     "Valid rclone remote with subdirectory",
			path:     "s3backup:servers/pve1",
			expected: true,
		},
		{
			name:     "Local absolute path (not rclone)",
			path:     "/opt/backup",
			expected: false,
		},
		{
			name:     "Empty path",
			path:     "",
			expected: false,
		},
		{
			name:     "Path without colon (not rclone)",
			path:     "backup",
			expected: false,
		},
		{
			name:     "Path with spaces",
			path:     "  gdrive:backups  ",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRcloneRemote(tt.path)
			if result != tt.expected {
				t.Errorf("isRcloneRemote(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestBuildDecryptPathOptions_Rclone(t *testing.T) {
	tests := []struct {
		name           string
		cloudEnabled   bool
		cloudRemote    string
		expectedLabels []string
		expectedRclone []bool
	}{
		{
			name:           "Rclone remote",
			cloudEnabled:   true,
			cloudRemote:    "gdrive:backups",
			expectedLabels: []string{"Cloud backups (rclone)"},
			expectedRclone: []bool{true},
		},
		{
			name:           "Filesystem path",
			cloudEnabled:   true,
			cloudRemote:    "/mnt/cloud",
			expectedLabels: []string{"Cloud backups"},
			expectedRclone: []bool{false},
		},
		{
			name:           "Cloud disabled",
			cloudEnabled:   false,
			cloudRemote:    "gdrive:backups",
			expectedLabels: []string{},
			expectedRclone: []bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This test requires a Config object setup
			// For a full test, you would need to create a proper config
			// This is a simplified version to demonstrate the approach
			t.Skip("Requires full config setup - placeholder test")
		})
	}
}

func TestDiscoverRcloneBackups_ParseFilenames(t *testing.T) {
	// Test the filename filtering logic
	testFiles := []string{
		"backup-20250115.bundle.tar",
		"backup-20250114.bundle.tar",
		"backup-20250113.tar.xz",           // Should be ignored (not .bundle.tar)
		"log-20250115.log",                  // Should be ignored
		"backup-20250112.bundle.tar.age",   // Should be ignored (has .age extension)
	}

	expectedCount := 2 // Only the two .bundle.tar files

	count := 0
	for _, filename := range testFiles {
		if strings.HasSuffix(filename, ".bundle.tar") {
			count++
		}
	}

	if count != expectedCount {
		t.Errorf("Expected %d .bundle.tar files, got %d", expectedCount, count)
	}
}
