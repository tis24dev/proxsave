package backup

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNormalizeChecksum(t *testing.T) {
	valid := strings.Repeat("A", 64)
	got, err := NormalizeChecksum("  " + valid + "  ")
	if err != nil {
		t.Fatalf("NormalizeChecksum(valid) returned error: %v", err)
	}
	if want := strings.Repeat("a", 64); got != want {
		t.Fatalf("NormalizeChecksum(valid) = %q; want %q", got, want)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "checksum is empty"},
		{name: "wrong length", input: "abc", want: "checksum must be 64 hex characters"},
		{name: "invalid hex", input: strings.Repeat("z", 64), want: "checksum is not valid hex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NormalizeChecksum(tt.input); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NormalizeChecksum(%q) error = %v; want substring %q", tt.input, err, tt.want)
			}
		})
	}
}

func TestParseChecksumData(t *testing.T) {
	valid := strings.Repeat("b", 64)
	got, err := ParseChecksumData([]byte(valid + "  backup.tar.zst\n"))
	if err != nil {
		t.Fatalf("ParseChecksumData(valid) returned error: %v", err)
	}
	if got != valid {
		t.Fatalf("ParseChecksumData(valid) = %q; want %q", got, valid)
	}

	if _, err := ParseChecksumData(nil); err == nil || !strings.Contains(err.Error(), "checksum file is empty") {
		t.Fatalf("ParseChecksumData(empty) error = %v; want empty checksum error", err)
	}
}

func TestGenerateChecksumErrorPaths(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	t.Run("missing file", func(t *testing.T) {
		if _, err := GenerateChecksum(context.Background(), logger, filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "failed to open file") {
			t.Fatalf("GenerateChecksum(missing) error = %v; want open failure", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "data.txt")
		if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := GenerateChecksum(ctx, logger, path); err == nil || err != context.Canceled {
			t.Fatalf("GenerateChecksum(cancelled) error = %v; want %v", err, context.Canceled)
		}
	})

	t.Run("read failure on directory", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := GenerateChecksum(context.Background(), logger, dir); err == nil || !strings.Contains(err.Error(), "failed to read file") {
			t.Fatalf("GenerateChecksum(directory) error = %v; want read failure", err)
		}
	})
}

func TestCreateManifestAndVerifyChecksumErrorPaths(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	tmp := t.TempDir()

	t.Run("create manifest parent failure", func(t *testing.T) {
		blockingParent := filepath.Join(tmp, "blocked")
		if err := os.WriteFile(blockingParent, []byte("file"), 0o644); err != nil {
			t.Fatalf("write blocking parent: %v", err)
		}

		err := CreateManifest(context.Background(), logger, &Manifest{}, filepath.Join(blockingParent, "manifest.json"))
		if err == nil || !strings.Contains(err.Error(), "failed to create output directory") {
			t.Fatalf("CreateManifest() error = %v; want output directory failure", err)
		}
	})

	t.Run("create manifest write failure", func(t *testing.T) {
		outputPath := filepath.Join(tmp, "manifest-as-dir.json")
		if err := os.MkdirAll(outputPath, 0o755); err != nil {
			t.Fatalf("mkdir output path: %v", err)
		}

		err := CreateManifest(context.Background(), logger, &Manifest{}, outputPath)
		if err == nil || !strings.Contains(err.Error(), "failed to write manifest file") {
			t.Fatalf("CreateManifest(write failure) error = %v; want write failure", err)
		}
	})

	t.Run("verify checksum invalid expected", func(t *testing.T) {
		path := filepath.Join(tmp, "archive.tar")
		if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
			t.Fatalf("write archive: %v", err)
		}

		ok, err := VerifyChecksum(context.Background(), logger, path, "bad")
		if err == nil || ok {
			t.Fatalf("VerifyChecksum(invalid expected) = (%v, %v); want error", ok, err)
		}
		if !strings.Contains(err.Error(), "invalid expected checksum") {
			t.Fatalf("VerifyChecksum(invalid expected) error = %v; want invalid expected checksum", err)
		}
	})

	t.Run("verify checksum file open failure", func(t *testing.T) {
		ok, err := VerifyChecksum(context.Background(), logger, filepath.Join(tmp, "missing.tar"), strings.Repeat("c", 64))
		if err == nil || ok {
			t.Fatalf("VerifyChecksum(missing file) = (%v, %v); want error", ok, err)
		}
		if !strings.Contains(err.Error(), "failed to generate checksum") {
			t.Fatalf("VerifyChecksum(missing file) error = %v; want checksum generation failure", err)
		}
	})
}

func TestParseLegacyMetadata_AllFieldsAndTargetMerge(t *testing.T) {
	legacy := &Manifest{
		ProxmoxTargets: []string{"pve"},
	}
	scanner := bufio.NewScanner(strings.NewReader(strings.Join([]string{
		"",
		"# comment",
		"INVALID_LINE",
		"COMPRESSION_TYPE=zst",
		"COMPRESSION_LEVEL=not-a-number",
		"COMPRESSION_LEVEL=9",
		"PROXMOX_TYPE=dual",
		"BACKUP_TARGETS=pve,pbs",
		"PROXMOX_TARGETS=pbs,dual",
		"PROXMOX_VERSION=8.2/3.4",
		"PVE_VERSION=8.2-1",
		"PBS_VERSION=3.4-2",
		"HOSTNAME=dual-node",
		"SCRIPT_VERSION=2.0.0",
		"ENCRYPTION_MODE=age",
	}, "\n")))

	parseLegacyMetadata(scanner, legacy)

	if legacy.CompressionType != "zst" {
		t.Fatalf("CompressionType = %q; want zst", legacy.CompressionType)
	}
	if legacy.CompressionLevel != 9 {
		t.Fatalf("CompressionLevel = %d; want 9", legacy.CompressionLevel)
	}
	if legacy.ProxmoxType != "dual" {
		t.Fatalf("ProxmoxType = %q; want dual", legacy.ProxmoxType)
	}
	if want := []string{"pve", "pbs", "dual"}; !reflect.DeepEqual(legacy.ProxmoxTargets, want) {
		t.Fatalf("ProxmoxTargets = %v; want %v", legacy.ProxmoxTargets, want)
	}
	if legacy.ProxmoxVersion != "8.2/3.4" || legacy.PVEVersion != "8.2-1" || legacy.PBSVersion != "3.4-2" {
		t.Fatalf("unexpected version fields: %+v", legacy)
	}
	if legacy.Hostname != "dual-node" || legacy.ScriptVersion != "2.0.0" || legacy.EncryptionMode != "age" {
		t.Fatalf("unexpected identity/encryption fields: %+v", legacy)
	}
}

func TestLoadLegacyManifestErrorPaths(t *testing.T) {
	if _, err := loadLegacyManifest("/tmp/manifest.json", []byte("COMPRESSION_TYPE=xz")); err == nil || !strings.Contains(err.Error(), "not a legacy metadata file") {
		t.Fatalf("loadLegacyManifest(non-metadata) error = %v; want non-legacy error", err)
	}

	tmp := t.TempDir()
	metadataPath := filepath.Join(tmp, "missing.tar.zst.metadata")
	if _, err := loadLegacyManifest(metadataPath, []byte("COMPRESSION_TYPE=xz")); err == nil || !strings.Contains(err.Error(), "cannot stat archive") {
		t.Fatalf("loadLegacyManifest(missing archive) error = %v; want stat archive failure", err)
	}
}

func TestLoadManifestReadFailure(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "missing-manifest.json")); err == nil || !strings.Contains(err.Error(), "failed to read manifest file") {
		t.Fatalf("LoadManifest(missing) error = %v; want read failure", err)
	}
}
