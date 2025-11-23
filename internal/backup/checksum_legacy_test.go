package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLegacyManifestWithShaAndFallbackEncryption(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "backup.tar.xz")
	if err := os.WriteFile(archive, []byte("payload"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	metadataPath := archive + ".metadata"
	legacyContent := strings.Join([]string{
		"COMPRESSION_TYPE=xz",
		"COMPRESSION_LEVEL=6",
		"PROXMOX_TYPE=pbs",
		"HOSTNAME=pbs-node",
		"SCRIPT_VERSION=1.0.0",
		// ENCRYPTION_MODE intentionally omitted to trigger fallback.
	}, "\n")
	if err := os.WriteFile(metadataPath, []byte(legacyContent), 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	shaLine := "deadbeef  " + filepath.Base(archive) + "\n"
	if err := os.WriteFile(archive+".sha256", []byte(shaLine), 0o640); err != nil {
		t.Fatalf("write sha256: %v", err)
	}

	m, err := LoadManifest(metadataPath)
	if err != nil {
		t.Fatalf("LoadManifest legacy: %v", err)
	}

	if m.ArchivePath != archive {
		t.Fatalf("ArchivePath = %s, want %s", m.ArchivePath, archive)
	}
	if m.CompressionType != "xz" || m.CompressionLevel != 6 {
		t.Fatalf("unexpected compression fields: type=%s level=%d", m.CompressionType, m.CompressionLevel)
	}
	if m.ProxmoxType != "pbs" || m.Hostname != "pbs-node" || m.ScriptVersion != "1.0.0" {
		t.Fatalf("unexpected metadata fields: %+v", m)
	}
	if m.EncryptionMode != "plain" {
		t.Fatalf("expected fallback encryption mode plain, got %s", m.EncryptionMode)
	}
	if m.SHA256 != "deadbeef" {
		t.Fatalf("expected sha256 deadbeef, got %s", m.SHA256)
	}
	if time.Since(m.CreatedAt) > time.Minute {
		t.Fatalf("unexpected CreatedAt too old: %v", m.CreatedAt)
	}
}

func TestLoadLegacyManifestAgeFallbackFromExtension(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "backup.tar.xz.age")
	if err := os.WriteFile(archive, []byte("data"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	metadataPath := archive + ".metadata"
	if err := os.WriteFile(metadataPath, []byte("COMPRESSION_TYPE=xz\n"), 0o640); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	m, err := LoadManifest(metadataPath)
	if err != nil {
		t.Fatalf("LoadManifest age fallback: %v", err)
	}
	if m.EncryptionMode != "age" {
		t.Fatalf("expected EncryptionMode=age from extension, got %s", m.EncryptionMode)
	}
}

func TestLoadManifestInvalidJSONError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "manifest.json")
	if err := os.WriteFile(path, []byte("{"), 0o640); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatalf("expected error for invalid JSON manifest")
	}
}
