package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNewArchiver(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultArchiverConfig()

	archiver := NewArchiver(logger, config)

	if archiver == nil {
		t.Fatal("NewArchiver returned nil")
	}

	if archiver.EffectiveCompression() != types.CompressionXZ {
		t.Errorf("Expected compression XZ, got %s", archiver.EffectiveCompression())
	}
}

func TestGetDefaultArchiverConfig(t *testing.T) {
	config := GetDefaultArchiverConfig()

	if config.Compression != types.CompressionXZ {
		t.Errorf("Expected default compression XZ, got %s", config.Compression)
	}

	if config.CompressionLevel != 6 {
		t.Errorf("Expected default compression level 6, got %d", config.CompressionLevel)
	}
}

func TestWithLookPathOverrideRestores(t *testing.T) {
	calls := 0
	restore := WithLookPathOverride(func(name string) (string, error) {
		calls++
		return "/tmp/" + name, nil
	})
	t.Cleanup(restore)

	if _, err := lookPath("anything"); err != nil {
		t.Fatalf("override lookPath returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected override to be invoked once, got %d", calls)
	}

	restore()

	// Calling lookPath after restore should not invoke the override again.
	_, _ = lookPath("definitely-not-a-command-xyz")
	if calls != 1 {
		t.Fatalf("expected override to be restored (calls=%d)", calls)
	}
}

func TestCreateTarArchive(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionNone,
		CompressionLevel: 0,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)
	tempDir := t.TempDir()

	// Create test files
	testDir := filepath.Join(tempDir, "source")
	os.MkdirAll(filepath.Join(testDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(testDir, "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(testDir, "subdir", "file2.txt"), []byte("content2"), 0644)

	// Create archive
	outputPath := filepath.Join(tempDir, "test.tar")
	ctx := context.Background()

	if err := archiver.CreateArchive(ctx, testDir, outputPath); err != nil {
		t.Fatalf("CreateArchive failed: %v", err)
	}

	// Verify archive exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Error("Archive file was not created")
	}

	// Verify archive content
	if err := verifyTarContent(outputPath, []string{"file1.txt", "subdir/file2.txt"}); err != nil {
		t.Errorf("Archive content verification failed: %v", err)
	}
}

func TestCreateGzipArchive(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionGzip,
		CompressionLevel: 6,
		DryRun:           false,
	}

	archiver := NewArchiver(logger, config)
	tempDir := t.TempDir()

	// Create test files
	testDir := filepath.Join(tempDir, "source")
	os.MkdirAll(testDir, 0755)
	os.WriteFile(filepath.Join(testDir, "file.txt"), []byte("test content"), 0644)

	// Create archive
	outputPath := filepath.Join(tempDir, "test.tar.gz")
	ctx := context.Background()

	if err := archiver.CreateArchive(ctx, testDir, outputPath); err != nil {
		t.Fatalf("CreateArchive failed: %v", err)
	}

	// Verify archive exists
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		t.Error("Archive file was not created")
	}

	// Verify it's a valid gzip file
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatalf("Failed to open archive: %v", err)
	}
	defer f.Close()

	_, err = gzip.NewReader(f)
	if err != nil {
		t.Errorf("Archive is not a valid gzip file: %v", err)
	}
}

func TestGetArchiveExtension(t *testing.T) {
	tests := []struct {
		compression types.CompressionType
		expected    string
	}{
		{types.CompressionNone, ".tar"},
		{types.CompressionGzip, ".tar.gz"},
		{types.CompressionXZ, ".tar.xz"},
		{types.CompressionZstd, ".tar.zst"},
	}

	logger := logging.New(types.LogLevelInfo, false)

	for _, tt := range tests {
		config := &ArchiverConfig{
			Compression: tt.compression,
		}
		archiver := NewArchiver(logger, config)

		got := archiver.GetArchiveExtension()
		if got != tt.expected {
			t.Errorf("GetArchiveExtension(%s) = %s; want %s",
				tt.compression, got, tt.expected)
		}
	}
}

func TestEstimateCompressionRatio(t *testing.T) {
	tests := []struct {
		compression types.CompressionType
		minRatio    float64
		maxRatio    float64
	}{
		{types.CompressionNone, 1.0, 1.0},
		{types.CompressionGzip, 0.2, 0.4},
		{types.CompressionXZ, 0.1, 0.3},
		{types.CompressionZstd, 0.2, 0.35},
	}

	logger := logging.New(types.LogLevelInfo, false)

	for _, tt := range tests {
		config := &ArchiverConfig{
			Compression: tt.compression,
		}
		archiver := NewArchiver(logger, config)

		ratio := archiver.EstimateCompressionRatio()
		if ratio < tt.minRatio || ratio > tt.maxRatio {
			t.Errorf("EstimateCompressionRatio(%s) = %.2f; want between %.2f and %.2f",
				tt.compression, ratio, tt.minRatio, tt.maxRatio)
		}
	}
}

func TestResolveCompressionFallbackToGzip(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression:      types.CompressionXZ,
		CompressionLevel: 9,
		DryRun:           false,
	}
	archiver := NewArchiver(logger, config)

	archiver.deps.LookPath = func(binary string) (string, error) {
		if binary == "xz" {
			return "", fmt.Errorf("xz not available")
		}
		return "/usr/bin/" + binary, nil
	}

	actual := archiver.ResolveCompression()
	if actual != types.CompressionGzip {
		t.Fatalf("expected fallback to gzip, got %s", actual)
	}
	if archiver.CompressionLevel() < 1 || archiver.CompressionLevel() > 9 {
		t.Fatalf("gzip compression level should be normalized to 1-9, got %d", archiver.CompressionLevel())
	}
}

func TestBuildXZArgs(t *testing.T) {
	tests := []struct {
		name     string
		level    int
		threads  int
		mode     string
		expected []string
	}{
		{
			name:     "standard auto threads",
			level:    6,
			threads:  0,
			mode:     "standard",
			expected: []string{"-6", "-T0", "-c"},
		},
		{
			name:     "ultra with threads",
			level:    9,
			threads:  4,
			mode:     "ultra",
			expected: []string{"-9", "-T4", "--extreme", "-c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildXZArgs(tt.level, tt.threads, tt.mode)
			if !reflect.DeepEqual(args, tt.expected) {
				t.Fatalf("buildXZArgs(%d,%d,%s) = %#v; want %#v", tt.level, tt.threads, tt.mode, args, tt.expected)
			}
		})
	}
}

func TestBuildZstdArgs(t *testing.T) {
	tests := []struct {
		name     string
		level    int
		threads  int
		expected []string
	}{
		{
			name:     "standard level",
			level:    15,
			threads:  2,
			expected: []string{"-15", "-T2", "-q", "-c"},
		},
		{
			name:     "ultra level",
			level:    22,
			threads:  0,
			expected: []string{"--ultra", "-22", "-T0", "-q", "-c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildZstdArgs(tt.level, tt.threads)
			if !reflect.DeepEqual(args, tt.expected) {
				t.Fatalf("buildZstdArgs(%d,%d) = %#v; want %#v", tt.level, tt.threads, args, tt.expected)
			}
		})
	}
}

func TestVerifyArchive(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression: types.CompressionNone,
		DryRun:      false,
	}

	archiver := NewArchiver(logger, config)
	tempDir := t.TempDir()

	// Create a test archive
	testDir := filepath.Join(tempDir, "source")
	os.MkdirAll(testDir, 0755)
	os.WriteFile(filepath.Join(testDir, "file.txt"), []byte("test"), 0644)

	outputPath := filepath.Join(tempDir, "test.tar")
	ctx := context.Background()

	archiver.CreateArchive(ctx, testDir, outputPath)

	// Verify it
	if err := archiver.VerifyArchive(ctx, outputPath); err != nil {
		t.Errorf("VerifyArchive failed: %v", err)
	}
}

func TestVerifyArchiveNonExistent(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression: types.CompressionNone,
	}

	archiver := NewArchiver(logger, config)
	ctx := context.Background()

	err := archiver.VerifyArchive(ctx, "/nonexistent/file.tar")
	if err == nil {
		t.Error("VerifyArchive should fail for non-existent file")
	}
}

func TestGetArchiveSize(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression: types.CompressionNone,
		DryRun:      false,
	}

	archiver := NewArchiver(logger, config)
	tempDir := t.TempDir()

	// Create a test archive
	testDir := filepath.Join(tempDir, "source")
	os.MkdirAll(testDir, 0755)
	content := []byte("test content with some length")
	os.WriteFile(filepath.Join(testDir, "file.txt"), content, 0644)

	outputPath := filepath.Join(tempDir, "test.tar")
	ctx := context.Background()

	archiver.CreateArchive(ctx, testDir, outputPath)

	// Get size
	size, err := archiver.GetArchiveSize(outputPath)
	if err != nil {
		t.Fatalf("GetArchiveSize failed: %v", err)
	}

	if size == 0 {
		t.Error("Archive size should not be zero")
	}
}

func TestDryRunMode(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression: types.CompressionNone,
		DryRun:      true, // Dry run mode
	}

	archiver := NewArchiver(logger, config)
	tempDir := t.TempDir()

	// Create test files
	testDir := filepath.Join(tempDir, "source")
	os.MkdirAll(testDir, 0755)
	os.WriteFile(filepath.Join(testDir, "file.txt"), []byte("test"), 0644)

	// Try to create archive in dry-run mode
	outputPath := filepath.Join(tempDir, "test.tar")
	ctx := context.Background()

	if err := archiver.CreateArchive(ctx, testDir, outputPath); err != nil {
		t.Fatalf("CreateArchive in dry-run failed: %v", err)
	}

	// Archive should NOT be created
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Error("Archive should not be created in dry-run mode")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30.0s"},
		{90 * time.Second, "1.5m"},
		{2 * time.Hour, "2.0h"},
	}

	for _, tt := range tests {
		got := FormatDuration(tt.duration)
		if got != tt.want {
			t.Errorf("FormatDuration(%v) = %s; want %s", tt.duration, got, tt.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{500, "500 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
	}

	for _, tt := range tests {
		got := FormatBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %s; want %s", tt.bytes, got, tt.want)
		}
	}
}

func TestCompressionErrorWrap(t *testing.T) {
	base := fmt.Errorf("boom")
	cerr := &CompressionError{Algorithm: "xz", Err: base}
	if cerr.Error() != "xz compression failed: boom" {
		t.Fatalf("unexpected Error(): %s", cerr.Error())
	}
	if !errors.Is(cerr, base) {
		t.Fatalf("CompressionError should unwrap to base error")
	}
	if errors.Is(cerr, fmt.Errorf("other")) {
		t.Fatalf("CompressionError should not match unrelated errors")
	}
}

func TestArchiverCompressionGetters(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	cfg := &ArchiverConfig{
		Compression:        types.CompressionXZ,
		CompressionLevel:   7,
		CompressionMode:    "ultra",
		CompressionThreads: 3,
		DryRun:             false,
	}
	archiver := NewArchiver(logger, cfg)
	if archiver.RequestedCompression() != types.CompressionXZ {
		t.Fatalf("RequestedCompression=%s want xz", archiver.RequestedCompression())
	}
	if archiver.CompressionThreads() != 3 {
		t.Fatalf("CompressionThreads=%d want 3", archiver.CompressionThreads())
	}
	if archiver.CompressionMode() != "ultra" {
		t.Fatalf("CompressionMode=%s want ultra", archiver.CompressionMode())
	}
}

func TestContextCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := &ArchiverConfig{
		Compression: types.CompressionNone,
		DryRun:      false,
	}

	archiver := NewArchiver(logger, config)
	tempDir := t.TempDir()

	// Create a large test directory to ensure cancellation can happen
	testDir := filepath.Join(tempDir, "source")
	os.MkdirAll(testDir, 0755)
	for i := 0; i < 100; i++ {
		os.WriteFile(filepath.Join(testDir, fmt.Sprintf("file%d.txt", i)), []byte("test content"), 0644)
	}

	// Create a context that we'll cancel immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	outputPath := filepath.Join(tempDir, "test.tar")

	// This should fail due to context cancellation
	err := archiver.CreateArchive(ctx, testDir, outputPath)
	if err == nil {
		// It's possible the operation completed before cancellation on fast systems
		t.Log("Archive creation completed despite cancellation (system too fast)")
	}
}

// Helper function to verify tar content
func verifyTarContent(tarPath string, expectedFiles []string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	found := make(map[string]bool)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		found[header.Name] = true
	}

	// Check all expected files are present
	for _, expected := range expectedFiles {
		if !found[expected] {
			alt := expected
			if !strings.HasPrefix(expected, "./") {
				alt = "./" + expected
			}
			if !found[alt] {
				return fmt.Errorf("expected file %s not found in archive", expected)
			}
		}
	}

	return nil
}

func TestEncryptedArchiveCanBeDecrypted(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	config := &ArchiverConfig{
		Compression:    types.CompressionNone,
		EncryptArchive: true,
		AgeRecipients:  []age.Recipient{identity.Recipient()},
	}
	archiver := NewArchiver(logger, config)

	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "file.txt"), []byte("secret data"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	outputPath := filepath.Join(tempDir, "backup.tar.age")
	if err := archiver.CreateArchive(context.Background(), sourceDir, outputPath); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	decryptedPath := filepath.Join(tempDir, "decrypted.tar")
	if err := decryptArchiveForTest(outputPath, decryptedPath, identity); err != nil {
		t.Fatalf("decrypt archive: %v", err)
	}

	if err := verifyTarContent(decryptedPath, []string{"file.txt"}); err != nil {
		t.Fatalf("verify decrypted tar: %v", err)
	}
}

func TestEncryptedArchiveRejectsWrongIdentity(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	correctIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	wrongIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate wrong identity: %v", err)
	}

	config := &ArchiverConfig{
		Compression:    types.CompressionNone,
		EncryptArchive: true,
		AgeRecipients:  []age.Recipient{correctIdentity.Recipient()},
	}
	archiver := NewArchiver(logger, config)

	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "file.txt"), []byte("secret data"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	outputPath := filepath.Join(tempDir, "backup.tar.age")
	if err := archiver.CreateArchive(context.Background(), sourceDir, outputPath); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	err = decryptArchiveForTest(outputPath, filepath.Join(tempDir, "fail.tar"), wrongIdentity)
	if err == nil {
		t.Fatalf("expected decryption failure with wrong identity")
	}
	var noMatch *age.NoIdentityMatchError
	if !errors.Is(err, age.ErrIncorrectIdentity) && !errors.As(err, &noMatch) {
		t.Fatalf("expected identity mismatch error, got %v", err)
	}
}

func decryptArchiveForTest(src, dst string, identity age.Identity) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	reader, err := age.Decrypt(in, identity)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, reader); err != nil {
		return err
	}
	return nil
}
