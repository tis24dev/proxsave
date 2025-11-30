package orchestrator

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

func TestCreateBundle_CreatesValidTarArchive(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")

	// Create test files with specific content
	testData := map[string]string{
		"":                    "archive-content",
		".sha256":             "checksum1",
		".metadata":           "metadata-json",
		".metadata.sha256":    "checksum2",
	}

	for suffix, content := range testData {
		if err := os.WriteFile(archive+suffix, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}

	o := &Orchestrator{
		logger: logger,
		fs:     osFS{},
	}

	bundlePath, err := o.createBundle(context.Background(), archive)
	if err != nil {
		t.Fatalf("createBundle: %v", err)
	}

	expectedPath := archive + ".bundle.tar"
	if bundlePath != expectedPath {
		t.Fatalf("bundle path = %s, want %s", bundlePath, expectedPath)
	}

	// Verify bundle file exists
	bundleInfo, err := os.Stat(bundlePath)
	if err != nil {
		t.Fatalf("expected bundle file, got %v", err)
	}
	if bundleInfo.Size() == 0 {
		t.Fatalf("bundle file is empty")
	}

	// Verify tar contents
	bundleFile, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer bundleFile.Close()

	tr := tar.NewReader(bundleFile)
	foundFiles := make(map[string]bool)
	expectedFiles := []string{
		"backup.tar",
		"backup.tar.sha256",
		"backup.tar.metadata",
		"backup.tar.metadata.sha256",
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar header: %v", err)
		}

		foundFiles[header.Name] = true

		// Verify file content matches
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read file %s from tar: %v", header.Name, err)
		}

		expectedContent := testData[""]
		if header.Name == "backup.tar.sha256" {
			expectedContent = testData[".sha256"]
		} else if header.Name == "backup.tar.metadata" {
			expectedContent = testData[".metadata"]
		} else if header.Name == "backup.tar.metadata.sha256" {
			expectedContent = testData[".metadata.sha256"]
		}

		if string(content) != expectedContent {
			t.Errorf("file %s content = %q, want %q", header.Name, content, expectedContent)
		}
	}

	// Verify all expected files are present
	for _, expected := range expectedFiles {
		if !foundFiles[expected] {
			t.Errorf("expected file %s not found in tar", expected)
		}
	}
}

func TestRemoveAssociatedFiles_RemovesAll(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	tempDir := t.TempDir()
	archive := filepath.Join(tempDir, "backup.tar")
	files := []string{
		archive,
		archive + ".sha256",
		archive + ".metadata",
		archive + ".metadata.sha256",
		archive + ".manifest.json",
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("x"), 0o640); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	o := &Orchestrator{
		logger: logger,
		fs:     osFS{},
	}
	if err := o.removeAssociatedFiles(archive); err != nil {
		t.Fatalf("removeAssociatedFiles: %v", err)
	}
	for _, f := range files {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, got %v", f, err)
		}
	}
}
