package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemDetectorTestOwnershipSupportRejectsNonDirectory(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())

	tmp := t.TempDir()
	file := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if detector.testOwnershipSupport(context.Background(), file) {
		t.Fatalf("expected ownership support test to fail for non-directory path")
	}
}

func TestFilesystemDetectorTestOwnershipSupportSucceedsInTempDir(t *testing.T) {
	detector := NewFilesystemDetector(newTestLogger())
	if ok := detector.testOwnershipSupport(context.Background(), t.TempDir()); !ok {
		t.Fatalf("expected ownership support test to succeed in temp dir")
	}
}
