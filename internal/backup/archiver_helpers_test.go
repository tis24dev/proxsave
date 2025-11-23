package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestArchiverConfigValidateLevelsAndCompression(t *testing.T) {
	tests := []struct {
		name    string
		cfg     ArchiverConfig
		wantErr bool
	}{
		{
			name:    "invalid compression",
			cfg:     ArchiverConfig{Compression: "weird"},
			wantErr: true,
		},
		{
			name:    "gzip level out of range low",
			cfg:     ArchiverConfig{Compression: types.CompressionGzip, CompressionLevel: 0},
			wantErr: true,
		},
		{
			name: "gzip level ok",
			cfg:  ArchiverConfig{Compression: types.CompressionGzip, CompressionLevel: 5},
		},
		{
			name:    "xz level high",
			cfg:     ArchiverConfig{Compression: types.CompressionXZ, CompressionLevel: 99},
			wantErr: true,
		},
		{
			name: "zstd level ok",
			cfg:  ArchiverConfig{Compression: types.CompressionZstd, CompressionLevel: 10},
		},
		{
			name:    "threads negative",
			cfg:     ArchiverConfig{Compression: types.CompressionNone, CompressionThreads: -1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestNormalizeHelpers(t *testing.T) {
	levelCases := []struct {
		comp     types.CompressionType
		level    int
		expected int
	}{
		{types.CompressionGzip, 0, 6},
		{types.CompressionPigz, 20, 6},
		{types.CompressionXZ, -1, 6},
		{types.CompressionBzip2, 10, 6},
		{types.CompressionLZMA, -5, 6},
		{types.CompressionZstd, 30, 6},
		{types.CompressionNone, 5, 0},
		{"unknown", 2, 6},
	}

	for _, tc := range levelCases {
		if got := normalizeLevelForCompression(tc.comp, tc.level); got != tc.expected {
			t.Fatalf("normalizeLevelForCompression(%s,%d)=%d want %d", tc.comp, tc.level, got, tc.expected)
		}
	}

	if mode := normalizeCompressionMode("FAST"); mode != "fast" {
		t.Fatalf("normalizeCompressionMode FAST = %s, want fast", mode)
	}
	if mode := normalizeCompressionMode("unknown"); mode != "standard" {
		t.Fatalf("normalizeCompressionMode default = %s, want standard", mode)
	}

	if !requiresExtremeMode("ultra") || !requiresExtremeMode("maximum") {
		t.Fatalf("requiresExtremeMode should be true for ultra/maximum")
	}
	if requiresExtremeMode("standard") {
		t.Fatalf("requiresExtremeMode should be false for standard")
	}
}

func TestBuildPigzArgs(t *testing.T) {
	args := buildPigzArgs(7, 0, "standard")
	want := []string{"-7", "-c"}
	if strings.Join(args, ",") != strings.Join(want, ",") {
		t.Fatalf("buildPigzArgs standard = %#v, want %#v", args, want)
	}

	args = buildPigzArgs(5, 3, "ultra")
	want = []string{"-p3", "-5", "--best", "-c"}
	if strings.Join(args, ",") != strings.Join(want, ",") {
		t.Fatalf("buildPigzArgs ultra = %#v, want %#v", args, want)
	}
}

func TestWrapEncryptionWriterWithoutRecipients(t *testing.T) {
	logger := newTestLogger()
	cfg := &ArchiverConfig{
		Compression:    types.CompressionNone,
		EncryptArchive: true,
	}
	archiver := NewArchiver(logger, cfg)
	archiver.encryptArchive = true
	archiver.ageRecipients = nil

	_, _, err := archiver.wrapEncryptionWriter(os.Stdout)
	if err == nil {
		t.Fatalf("expected error when encryption enabled but no recipients")
	}
}

func TestVerifyArchiveSkipsEncryptedDetails(t *testing.T) {
	logger := newTestLogger()
	cfg := &ArchiverConfig{
		Compression:    types.CompressionNone,
		EncryptArchive: true,
	}
	archiver := NewArchiver(logger, cfg)
	archiver.encryptArchive = true

	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "test.tar.age")
	if err := os.WriteFile(archivePath, []byte("data"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	if err := archiver.VerifyArchive(context.Background(), archivePath); err != nil {
		t.Fatalf("VerifyArchive should skip detailed checks for encrypted archives: %v", err)
	}
}

// newTestLogger returns a silent logger for helper tests.
func newTestLogger() *logging.Logger {
	return logging.New(types.LogLevelError, false)
}
