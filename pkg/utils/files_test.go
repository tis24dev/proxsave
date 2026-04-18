package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test file exists
	if !FileExists(testFile) {
		t.Error("FileExists should return true for existing file")
	}

	// Test file doesn't exist
	if FileExists(filepath.Join(tmpDir, "nonexistent.txt")) {
		t.Error("FileExists should return false for nonexistent file")
	}

	// Test directory (should return false for directories)
	if FileExists(tmpDir) {
		t.Error("FileExists should return false for directories")
	}
}

func TestDirExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a subdirectory
	testDir := filepath.Join(tmpDir, "testdir")
	if err := os.Mkdir(testDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Test directory exists
	if !DirExists(testDir) {
		t.Error("DirExists should return true for existing directory")
	}

	// Test directory doesn't exist
	if DirExists(filepath.Join(tmpDir, "nonexistent")) {
		t.Error("DirExists should return false for nonexistent directory")
	}

	// Create a file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test file (should return false for files)
	if DirExists(testFile) {
		t.Error("DirExists should return false for files")
	}
}
