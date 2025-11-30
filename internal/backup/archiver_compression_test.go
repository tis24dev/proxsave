package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestCreatePigzArchive tests pigz compression
func TestCreatePigzArchive(t *testing.T) {
	// Create temporary directory with test files
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.gz")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionPigz,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)

	// Test with pigz unavailable - should work or fail gracefully
	ctx := context.Background()
	err := archiver.createPigzArchive(ctx, tempDir, outputPath)

	// If pigz is not available, this is expected to fail
	if err != nil {
		if exec.Command("pigz", "--version").Run() != nil {
			t.Skip("pigz not available, skipping test")
		}
		t.Errorf("createPigzArchive failed: %v", err)
	}

	// If pigz was available, verify the output file exists
	if err == nil {
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("Output archive was not created")
		}
	}
}

// TestCreateBzip2Archive tests bzip2 compression
func TestCreateBzip2Archive(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.bz2")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionBzip2,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)

	ctx := context.Background()
	err := archiver.createBzip2Archive(ctx, tempDir, outputPath)

	if err != nil {
		if exec.Command("bzip2", "--version").Run() != nil {
			t.Skip("bzip2 not available, skipping test")
		}
		t.Errorf("createBzip2Archive failed: %v", err)
	}

	if err == nil {
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("Output archive was not created")
		}
	}
}

// TestCreateLzmaArchive tests lzma compression
func TestCreateLzmaArchive(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.lzma")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionLZMA,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)

	ctx := context.Background()
	err := archiver.createLzmaArchive(ctx, tempDir, outputPath)

	if err != nil {
		if exec.Command("lzma", "--version").Run() != nil {
			t.Skip("lzma not available, skipping test")
		}
		t.Errorf("createLzmaArchive failed: %v", err)
	}

	if err == nil {
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("Output archive was not created")
		}
	}
}

// TestCreateXZArchive tests xz compression
func TestCreateXZArchive(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content for xz compression"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.xz")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:        types.CompressionXZ,
		CompressionLevel:   6,
		CompressionThreads: 2,
		CompressionMode:    "normal",
		DryRun:             false,
	}

	archiver := NewArchiver(logger, config)

	ctx := context.Background()
	err := archiver.createXZArchive(ctx, tempDir, outputPath)

	if err != nil {
		if exec.Command("xz", "--version").Run() != nil {
			t.Skip("xz not available, skipping test")
		}
		t.Errorf("createXZArchive failed: %v", err)
	}

	if err == nil {
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("Output archive was not created")
		}
	}
}

// TestCreateXZArchive_ExtremeMode tests xz compression with extreme mode
func TestCreateXZArchive_ExtremeMode(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.xz")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:        types.CompressionXZ,
		CompressionLevel:   9,
		CompressionThreads: 2,
		CompressionMode:    "extreme",
		DryRun:             false,
	}

	archiver := NewArchiver(logger, config)

	ctx := context.Background()
	err := archiver.createXZArchive(ctx, tempDir, outputPath)

	if err != nil {
		if exec.Command("xz", "--version").Run() != nil {
			t.Skip("xz not available, skipping test")
		}
		t.Errorf("createXZArchive with extreme mode failed: %v", err)
	}
}

// TestCreateZstdArchive tests zstd compression
func TestCreateZstdArchive(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content for zstd compression"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.zst")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:        types.CompressionZstd,
		CompressionLevel:   3,
		CompressionThreads: 2,
		DryRun:             false,
	}

	archiver := NewArchiver(logger, config)

	ctx := context.Background()
	err := archiver.createZstdArchive(ctx, tempDir, outputPath)

	if err != nil {
		if exec.Command("zstd", "--version").Run() != nil {
			t.Skip("zstd not available, skipping test")
		}
		t.Errorf("createZstdArchive failed: %v", err)
	}

	if err == nil {
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			t.Error("Output archive was not created")
		}
	}
}

// TestCreateZstdArchive_HighCompression tests zstd with high compression level
func TestCreateZstdArchive_HighCompression(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.zst")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:        types.CompressionZstd,
		CompressionLevel:   19,
		CompressionThreads: 4,
		DryRun:             false,
	}

	archiver := NewArchiver(logger, config)

	ctx := context.Background()
	err := archiver.createZstdArchive(ctx, tempDir, outputPath)

	if err != nil {
		if exec.Command("zstd", "--version").Run() != nil {
			t.Skip("zstd not available, skipping test")
		}
		t.Errorf("createZstdArchive with high compression failed: %v", err)
	}
}

// TestCompressionWithCancellation tests that compression respects context cancellation
func TestCompressionWithCancellation(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(tempDir, "output.tar.xz")

	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionXZ,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := archiver.createXZArchive(ctx, tempDir, outputPath)

	// Should get a context cancellation error
	if err == nil {
		t.Error("Expected error from cancelled context, got nil")
	}
}

// TestCreateArchiveWithDifferentCompressionTypes tests CreateArchive with various compression types
func TestCreateArchiveWithDifferentCompressionTypes(t *testing.T) {
	tests := []struct {
		name        string
		compression types.CompressionType
		extension   string
		command     string
	}{
		{"xz", types.CompressionXZ, ".tar.xz", "xz"},
		{"zstd", types.CompressionZstd, ".tar.zst", "zstd"},
		{"pigz", types.CompressionPigz, ".tar.gz", "pigz"},
		{"bzip2", types.CompressionBzip2, ".tar.bz2", "bzip2"},
		{"lzma", types.CompressionLZMA, ".tar.lzma", "lzma"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Check if compression tool is available
			if exec.Command(tt.command, "--version").Run() != nil {
				t.Skipf("%s not available, skipping test", tt.command)
			}

			tempDir := t.TempDir()
			testFile := filepath.Join(tempDir, "test.txt")
			if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
				t.Fatal(err)
			}

			outputPath := filepath.Join(tempDir, "output"+tt.extension)

			logger := logging.New(types.LogLevelInfo, false)
			config := &ArchiverConfig{
				Compression:        tt.compression,
				CompressionLevel:   6,
				CompressionThreads: 2,
				DryRun:             false,
			}

			archiver := NewArchiver(logger, config)

			ctx := context.Background()
			err := archiver.CreateArchive(ctx, tempDir, outputPath)

			if err != nil {
				t.Errorf("CreateArchive with %s failed: %v", tt.name, err)
			}

			if _, err := os.Stat(outputPath); os.IsNotExist(err) {
				t.Errorf("Output archive was not created for %s", tt.name)
			}
		})
	}
}
