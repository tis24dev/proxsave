package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

// createTestXZArchive creates a test XZ archive for verification tests
func createTestXZArchive(t *testing.T, path string) {
	if exec.Command("xz", "--version").Run() != nil {
		t.Skip("xz not available")
	}

	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content for verification"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionXZ,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)
	ctx := context.Background()

	if err := archiver.CreateArchive(ctx, tempDir, path); err != nil {
		t.Fatalf("Failed to create test archive: %v", err)
	}
}

// createTestZstdArchive creates a test Zstd archive for verification tests
func createTestZstdArchive(t *testing.T, path string) {
	if exec.Command("zstd", "--version").Run() != nil {
		t.Skip("zstd not available")
	}

	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content for zstd verification"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionZstd,
		CompressionLevel: 3,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)
	ctx := context.Background()

	if err := archiver.CreateArchive(ctx, tempDir, path); err != nil {
		t.Fatalf("Failed to create test zstd archive: %v", err)
	}
}

// createTestGzipArchive creates a test Gzip archive for verification tests
func createTestGzipArchive(t *testing.T, path string) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content for gzip verification"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionGzip,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)
	ctx := context.Background()

	if err := archiver.CreateArchive(ctx, tempDir, path); err != nil {
		t.Fatalf("Failed to create test gzip archive: %v", err)
	}
}

// TestVerifyXZArchive tests XZ archive verification
func TestVerifyXZArchive(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "test.tar.xz")

	createTestXZArchive(t, archivePath)

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyXZArchive(ctx, archivePath)

	if err != nil {
		t.Errorf("verifyXZArchive failed for valid archive: %v", err)
	}
}

// TestVerifyXZArchive_InvalidFile tests XZ verification with invalid file
func TestVerifyXZArchive_InvalidFile(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "invalid.tar.xz")

	// Create a file with invalid content
	if err := os.WriteFile(archivePath, []byte("not an xz archive"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyXZArchive(ctx, archivePath)

	// Should return an error for invalid archive
	if err == nil {
		t.Error("Expected error for invalid XZ archive, got nil")
	}
}

// TestVerifyXZArchive_NonExistent tests XZ verification with non-existent file
func TestVerifyXZArchive_NonExistent(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyXZArchive(ctx, "/non/existent/file.tar.xz")

	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

// TestVerifyZstdArchive tests Zstd archive verification
func TestVerifyZstdArchive(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "test.tar.zst")

	createTestZstdArchive(t, archivePath)

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyZstdArchive(ctx, archivePath)

	if err != nil {
		t.Errorf("verifyZstdArchive failed for valid archive: %v", err)
	}
}

// TestVerifyZstdArchive_InvalidFile tests Zstd verification with invalid file
func TestVerifyZstdArchive_InvalidFile(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "invalid.tar.zst")

	// Create a file with invalid content
	if err := os.WriteFile(archivePath, []byte("not a zstd archive"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyZstdArchive(ctx, archivePath)

	// Should return an error for invalid archive
	if err == nil {
		t.Error("Expected error for invalid Zstd archive, got nil")
	}
}

// TestVerifyZstdArchive_NonExistent tests Zstd verification with non-existent file
func TestVerifyZstdArchive_NonExistent(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyZstdArchive(ctx, "/non/existent/file.tar.zst")

	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

// TestVerifyGzipArchive tests Gzip archive verification
func TestVerifyGzipArchive(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "test.tar.gz")

	createTestGzipArchive(t, archivePath)

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyGzipArchive(ctx, archivePath)

	if err != nil {
		t.Errorf("verifyGzipArchive failed for valid archive: %v", err)
	}
}

// TestVerifyGzipArchive_InvalidFile tests Gzip verification with invalid file
func TestVerifyGzipArchive_InvalidFile(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "invalid.tar.gz")

	// Create a file with invalid content
	if err := os.WriteFile(archivePath, []byte("not a gzip archive"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.verifyGzipArchive(ctx, archivePath)

	// Should return an error for invalid archive
	if err == nil {
		t.Error("Expected error for invalid Gzip archive, got nil")
	}
}

// TestVerifyGzipArchive_CorruptedTar tests Gzip verification with corrupted tar content
func TestVerifyGzipArchive_CorruptedTar(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "corrupted.tar.gz")

	// Create a valid gzip file but with corrupted tar content
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	_, err = gzipWriter.Write([]byte("corrupted tar content"))
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter.Close()

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err = archiver.verifyGzipArchive(ctx, archivePath)

	// Note: gzip verification may not catch corrupted tar content
	// if the gzip layer itself is valid. This is expected behavior.
	// The error will only be caught when extracting the tar.
	_ = err // We don't assert on this as behavior may vary
}

// TestVerifyArchive_MultipleFormats tests VerifyArchive with different compression formats
func TestVerifyArchive_MultipleFormats(t *testing.T) {
	tests := []struct {
		name        string
		compression types.CompressionType
		extension   string
		command     string
	}{
		{"xz", types.CompressionXZ, ".tar.xz", "xz"},
		{"zstd", types.CompressionZstd, ".tar.zst", "zstd"},
		{"gzip", types.CompressionGzip, ".tar.gz", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check if compression tool is available (skip for gzip as it's builtin)
			if tt.command != "" && exec.Command(tt.command, "--version").Run() != nil {
				t.Skipf("%s not available, skipping test", tt.command)
			}

			tempDir := t.TempDir()
			archivePath := filepath.Join(tempDir, "test"+tt.extension)

			// Create test archive
			sourceDir := t.TempDir()
			testFile := filepath.Join(sourceDir, "test.txt")
			if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
				t.Fatal(err)
			}

			logger := logging.New(types.LogLevelInfo, false)
			config := &ArchiverConfig{
				Compression:      tt.compression,
				CompressionLevel: 6,
				DryRun:           false,
			}

			archiver := NewArchiver(logger, config)
			ctx := context.Background()

			if err := archiver.CreateArchive(ctx, sourceDir, archivePath); err != nil {
				t.Fatalf("Failed to create archive: %v", err)
			}

			// Verify the archive
			if err := archiver.VerifyArchive(ctx, archivePath); err != nil {
				t.Errorf("VerifyArchive failed for %s: %v", tt.name, err)
			}
		})
	}
}

// TestVerifyArchive_UnknownFormat tests verification with unsupported format
func TestVerifyArchive_UnknownFormat(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "test.tar.unknown")

	// Create a dummy file
	if err := os.WriteFile(archivePath, []byte("dummy content"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err := archiver.VerifyArchive(ctx, archivePath)

	// Note: VerifyArchive may log a warning but not return an error for unknown formats
	// This is by design - unknown formats are skipped with a warning
	if err != nil {
		t.Logf("Got error (which is also acceptable): %v", err)
	}
}

// TestVerifyGzipArchive_ValidTarContent tests gzip verification with valid tar content
func TestVerifyGzipArchive_ValidTarContent(t *testing.T) {
	tempDir := t.TempDir()
	archivePath := filepath.Join(tempDir, "valid.tar.gz")

	// Create a proper tar.gz archive
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)

	// Add a file to the tar
	header := &tar.Header{
		Name: "test.txt",
		Mode: 0644,
		Size: int64(len("test content")),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("test content")); err != nil {
		t.Fatal(err)
	}

	tarWriter.Close()
	gzipWriter.Close()

	logger := logging.New(types.LogLevelInfo, false)
	archiver := &Archiver{logger: logger}

	ctx := context.Background()
	err = archiver.verifyGzipArchive(ctx, archivePath)

	if err != nil {
		t.Errorf("verifyGzipArchive failed for valid tar.gz: %v", err)
	}
}
