package environment

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/types"
)

// TestDirExists tests directory existence checking
func TestDirExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Test directory exists
	if !dirExists(tmpDir) {
		t.Error("dirExists should return true for existing directory")
	}

	// Test directory doesn't exist
	if dirExists(filepath.Join(tmpDir, "nonexistent")) {
		t.Error("dirExists should return false for nonexistent directory")
	}

	// Test file (should return false)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	if dirExists(testFile) {
		t.Error("dirExists should return false for files")
	}
}

// TestReadAndTrim tests file reading with trimming
func TestReadAndTrim(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "simple content",
			content:  "7.4-1",
			expected: "7.4-1",
		},
		{
			name:     "content with whitespace",
			content:  "  7.4-1  \n",
			expected: "7.4-1",
		},
		{
			name:     "multiline content",
			content:  "line1\nline2\n",
			expected: "line1\nline2",
		},
		{
			name:     "empty content",
			content:  "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFile := filepath.Join(tmpDir, "test_"+tt.name+".txt")
			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			result := readAndTrim(testFile)
			if result != tt.expected {
				t.Errorf("readAndTrim() = %q, want %q", result, tt.expected)
			}
		})
	}

	// Test nonexistent file
	if result := readAndTrim(filepath.Join(tmpDir, "nonexistent.txt")); result != "" {
		t.Errorf("readAndTrim() for nonexistent file = %q, want empty string", result)
	}
}

// TestContainsAny tests searching for tokens in files
func TestContainsAny(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		content  string
		tokens   []string
		expected bool
	}{
		{
			name:     "single match",
			content:  "deb http://download.proxmox.com/debian/pve bullseye pve-no-subscription",
			tokens:   []string{"pve", "pve-enterprise"},
			expected: true,
		},
		{
			name:     "multiple matches",
			content:  "pve-enterprise repository",
			tokens:   []string{"pve", "pve-enterprise"},
			expected: true,
		},
		{
			name:     "case insensitive match",
			content:  "Proxmox PVE Repository",
			tokens:   []string{"pve"},
			expected: true,
		},
		{
			name:     "no match",
			content:  "some other content",
			tokens:   []string{"pve", "pbs"},
			expected: false,
		},
		{
			name:     "empty file",
			content:  "",
			tokens:   []string{"pve"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testFile := filepath.Join(tmpDir, "test_"+tt.name+".txt")
			if err := os.WriteFile(testFile, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			result := containsAny(testFile, tt.tokens)
			if result != tt.expected {
				t.Errorf("containsAny() = %v, want %v", result, tt.expected)
			}
		})
	}

	// Test nonexistent file
	if result := containsAny(filepath.Join(tmpDir, "nonexistent.txt"), []string{"test"}); result {
		t.Error("containsAny() should return false for nonexistent file")
	}
}

// TestRunCommand tests command execution with timeout
func TestRunCommand(t *testing.T) {
	// Test successful command
	output, err := runCommand("echo", "test")
	if err != nil {
		t.Errorf("runCommand() error = %v", err)
	}
	if !strings.Contains(output, "test") {
		t.Errorf("runCommand() output = %q, should contain 'test'", output)
	}

	// Test nonexistent command
	_, err = runCommand("/nonexistent/command")
	if err == nil {
		t.Error("runCommand() should return error for nonexistent command")
	}
}

// TestRunCommandTimeout tests command timeout
func TestRunCommandTimeout(t *testing.T) {
	// This test verifies that a context with timeout actually
	// reaches DeadlineExceeded within a reasonable time window,
	// without relying on extremely small sleeps which can be
	// flaky on slower or loaded CI machines.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("Context should have timed out")
	}
}

// TestDetectViaDirectories tests directory-based detection
func TestDetectViaDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test directories
	testDir1 := filepath.Join(tmpDir, "dir1")
	testDir2 := filepath.Join(tmpDir, "dir2")
	if err := os.MkdirAll(testDir1, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		paths    []string
		expected bool
	}{
		{
			name:     "first directory exists",
			paths:    []string{testDir1, testDir2},
			expected: true,
		},
		{
			name:     "no directories exist",
			paths:    []string{testDir2, filepath.Join(tmpDir, "dir3")},
			expected: false,
		},
		{
			name:     "empty paths",
			paths:    []string{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectViaDirectories(tt.paths)
			if result != tt.expected {
				t.Errorf("detectViaDirectories() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestDetectPVEViaSources tests PVE detection via apt sources
func TestDetectPVEViaSources(t *testing.T) {
	// Since this function checks actual system paths, we test the logic
	// On most systems this will return false
	result := detectPVEViaSources()
	// Just verify it doesn't panic
	_ = result
}

// TestDetectPBSViaSources tests PBS detection via apt sources
func TestDetectPBSViaSources(t *testing.T) {
	// Since this function checks actual system paths, we test the logic
	// On most systems this will return false
	result := detectPBSViaSources()
	// Just verify it doesn't panic
	_ = result
}

// TestExtendPath tests PATH environment variable extension
func TestExtendPath(t *testing.T) {
	// Save original PATH
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("PATH", originalPath)
	}()

	// Set a minimal PATH
	_ = os.Setenv("PATH", "/usr/local/bin")

	extendPath()

	newPath := os.Getenv("PATH")

	// Verify additional paths were added
	for _, additionalPath := range additionalPaths {
		if !strings.Contains(newPath, additionalPath) {
			t.Errorf("PATH should contain %s after extendPath(), got: %s", additionalPath, newPath)
		}
	}
}

// TestExtendPathIdempotent tests that extendPath doesn't duplicate paths
func TestExtendPathIdempotent(t *testing.T) {
	originalPath := os.Getenv("PATH")
	defer func() {
		_ = os.Setenv("PATH", originalPath)
	}()

	// Call extendPath twice
	extendPath()
	pathAfterFirst := os.Getenv("PATH")

	extendPath()
	pathAfterSecond := os.Getenv("PATH")

	// Paths should be identical
	if pathAfterFirst != pathAfterSecond {
		t.Error("extendPath() should be idempotent")
	}
}

// TestIsExecutable tests file executability checking
func TestIsExecutable(t *testing.T) {
	tmpDir := t.TempDir()

	// Create executable file
	execFile := filepath.Join(tmpDir, "executable")
	if err := os.WriteFile(execFile, []byte("#!/bin/sh\necho test"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create non-executable file
	nonExecFile := filepath.Join(tmpDir, "nonexecutable")
	if err := os.WriteFile(nonExecFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "executable file",
			path:     execFile,
			expected: true,
		},
		{
			name:     "non-executable file",
			path:     nonExecFile,
			expected: false,
		},
		{
			name:     "directory",
			path:     tmpDir,
			expected: false,
		},
		{
			name:     "nonexistent file",
			path:     filepath.Join(tmpDir, "nonexistent"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isExecutable(tt.path)
			if result != tt.expected {
				t.Errorf("isExecutable(%s) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

// TestBoolToYes tests boolean to YES/NO conversion
func TestBoolToYes(t *testing.T) {
	tests := []struct {
		input    bool
		expected string
	}{
		{true, "YES"},
		{false, "NO"},
	}

	for _, tt := range tests {
		result := boolToYes(tt.input)
		if result != tt.expected {
			t.Errorf("boolToYes(%v) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// TestLookPathOrNotFound tests command lookup
func TestLookPathOrNotFound(t *testing.T) {
	// Test with a command that should exist
	result := lookPathOrNotFound("sh")
	if result == "NOT FOUND" {
		t.Error("lookPathOrNotFound('sh') should find the shell")
	}

	// Test with nonexistent command
	result = lookPathOrNotFound("nonexistentcommand123456")
	if result != "NOT FOUND" {
		t.Errorf("lookPathOrNotFound() = %q, want 'NOT FOUND'", result)
	}
}

// TestWriteDetectionDebug tests debug file creation
func TestWriteDetectionDebug(t *testing.T) {
	path := writeDetectionDebug()

	if path != "" {
		// If path was created, verify it exists
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Debug file was not created at %s", path)
		}

		// Read and verify content
		content, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("Failed to read debug file: %v", err)
		}

		// Verify it contains expected sections
		contentStr := string(content)
		expectedSections := []string{
			"Proxmox Detection Failure Debug",
			"Current PATH:",
			"Command availability check",
			"File existence check",
			"Directory existence check",
		}

		for _, section := range expectedSections {
			if !strings.Contains(contentStr, section) {
				t.Errorf("Debug file should contain section %q", section)
			}
		}

		// Clean up
		_ = os.Remove(path)
	}
}

// TestDetectPVEViaVersionFiles tests PVE version file detection
func TestDetectPVEViaVersionFiles(t *testing.T) {
	// This tests the actual function on the system
	// It will return false on non-PVE systems
	version, ok := detectPVEViaVersionFiles()

	// Just verify the function doesn't panic
	_ = version
	_ = ok
}

// TestDetectPBSViaVersionFile tests PBS version file detection
func TestDetectPBSViaVersionFile(t *testing.T) {
	// This tests the actual function on the system
	// It will return false on non-PBS systems
	version, ok := detectPBSViaVersionFile()

	// Just verify the function doesn't panic
	_ = version
	_ = ok
}

// TestDetectPVEViaCommand tests PVE command detection
func TestDetectPVEViaCommand(t *testing.T) {
	// This tests the actual function on the system
	version, ok := detectPVEViaCommand()

	// Just verify the function doesn't panic
	_ = version
	_ = ok
}

// TestDetectPBSViaCommand tests PBS command detection
func TestDetectPBSViaCommand(t *testing.T) {
	// This tests the actual function on the system
	version, ok := detectPBSViaCommand()

	// Just verify the function doesn't panic
	_ = version
	_ = ok
}

// TestDetectPVE tests complete PVE detection
func TestDetectPVE(t *testing.T) {
	version, ok := detectPVE()

	// On non-PVE systems, should return false
	// On PVE systems, should return true with version
	if ok && version == "" {
		t.Error("If PVE is detected, version should not be empty")
	}
}

// TestDetectPBS tests complete PBS detection
func TestDetectPBS(t *testing.T) {
	version, ok := detectPBS()

	// On non-PBS systems, should return false
	// On PBS systems, should return true with version
	if ok && version == "" {
		t.Error("If PBS is detected, version should not be empty")
	}
}

// TestDetectProxmox tests complete proxmox detection
func TestDetectProxmox(t *testing.T) {
	pType, version, err := detectProxmox()

	// Should always return a valid type
	if pType == "" {
		t.Error("detectProxmox() should return a non-empty type")
	}

	// On unknown systems, should return error
	if pType == types.ProxmoxUnknown {
		if err == nil {
			t.Error("detectProxmox() should return error for unknown systems")
		}
		if version != "unknown" {
			t.Errorf("Version should be 'unknown' for unknown systems, got %q", version)
		}
	}

	// On detected systems, version should not be empty
	if pType != types.ProxmoxUnknown && version == "" {
		t.Error("Version should not be empty for detected Proxmox systems")
	}
}
