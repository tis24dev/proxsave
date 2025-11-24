package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestGenerateAndVerifyChecksum(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	ctx := context.Background()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.txt")
	content := []byte("checksum-test-content")

	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	checksum, err := GenerateChecksum(ctx, logger, filePath)
	if err != nil {
		t.Fatalf("GenerateChecksum failed: %v", err)
	}
	if checksum == "" {
		t.Fatal("checksum should not be empty")
	}

	ok, err := VerifyChecksum(ctx, logger, filePath, checksum)
	if err != nil {
		t.Fatalf("VerifyChecksum failed: %v", err)
	}
	if !ok {
		t.Fatal("expected checksum verification to succeed")
	}

	// Modify file and ensure verification fails
	if err := os.WriteFile(filePath, []byte("modified"), 0644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	ok, err = VerifyChecksum(ctx, logger, filePath, checksum)
	if err != nil {
		t.Fatalf("VerifyChecksum after modification failed: %v", err)
	}
	if ok {
		t.Fatal("expected checksum verification to fail after modification")
	}
}

func TestCreateAndLoadManifest(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	ctx := context.Background()

	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.json")

	manifest := &Manifest{
		ArchivePath:      "/opt/proxmox-backup/backup/test.tar.xz",
		ArchiveSize:      1024,
		SHA256:           "abc123",
		CreatedAt:        time.Now().UTC().Truncate(time.Second),
		CompressionType:  "xz",
		CompressionLevel: 6,
		CompressionMode:  "ultra",
		ProxmoxType:      "pbs",
		ProxmoxTargets:   []string{"pbs"},
		ProxmoxVersion:   "7.4-3",
		Hostname:         "test-host",
		ScriptVersion:    "0.2.0",
		EncryptionMode:   "age",
		ClusterMode:      "cluster",
	}

	if err := CreateManifest(ctx, logger, manifest, manifestPath); err != nil {
		t.Fatalf("CreateManifest failed: %v", err)
	}

	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if loaded.ArchivePath != manifest.ArchivePath {
		t.Errorf("ArchivePath mismatch: got %s, want %s", loaded.ArchivePath, manifest.ArchivePath)
	}
	if loaded.SHA256 != manifest.SHA256 {
		t.Errorf("SHA256 mismatch: got %s, want %s", loaded.SHA256, manifest.SHA256)
	}
	if loaded.CompressionType != manifest.CompressionType {
		t.Errorf("CompressionType mismatch: got %s, want %s", loaded.CompressionType, manifest.CompressionType)
	}
	if loaded.CompressionLevel != manifest.CompressionLevel {
		t.Errorf("CompressionLevel mismatch: got %d, want %d", loaded.CompressionLevel, manifest.CompressionLevel)
	}
	if loaded.CompressionMode != manifest.CompressionMode {
		t.Errorf("CompressionMode mismatch: got %s, want %s", loaded.CompressionMode, manifest.CompressionMode)
	}
	if loaded.Hostname != manifest.Hostname {
		t.Errorf("Hostname mismatch: got %s, want %s", loaded.Hostname, manifest.Hostname)
	}
	if loaded.ScriptVersion != manifest.ScriptVersion {
		t.Errorf("ScriptVersion mismatch: got %s, want %s", loaded.ScriptVersion, manifest.ScriptVersion)
	}
	if loaded.EncryptionMode != manifest.EncryptionMode {
		t.Errorf("EncryptionMode mismatch: got %s, want %s", loaded.EncryptionMode, manifest.EncryptionMode)
	}
	if loaded.ClusterMode != manifest.ClusterMode {
		t.Errorf("ClusterMode mismatch: got %s, want %s", loaded.ClusterMode, manifest.ClusterMode)
	}
	if len(loaded.ProxmoxTargets) != len(manifest.ProxmoxTargets) {
		t.Errorf("ProxmoxTargets length mismatch: got %d, want %d", len(loaded.ProxmoxTargets), len(manifest.ProxmoxTargets))
	} else {
		for i := range loaded.ProxmoxTargets {
			if loaded.ProxmoxTargets[i] != manifest.ProxmoxTargets[i] {
				t.Errorf("ProxmoxTargets mismatch at %d: got %s, want %s", i, loaded.ProxmoxTargets[i], manifest.ProxmoxTargets[i])
			}
		}
	}
	if loaded.ProxmoxVersion != manifest.ProxmoxVersion {
		t.Errorf("ProxmoxVersion mismatch: got %s, want %s", loaded.ProxmoxVersion, manifest.ProxmoxVersion)
	}
}

func TestLoadManifestLegacy(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "backup.tar.xz")
	if err := os.WriteFile(archivePath, []byte("legacy data"), 0644); err != nil {
		t.Fatalf("failed to write archive: %v", err)
	}

	metadata := `COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6
PROXMOX_TYPE=pve
HOSTNAME=legacy-host
SCRIPT_VERSION=legacy-1.0
ENCRYPTION_MODE=age
`
	metadataPath := archivePath + ".metadata"
	if err := os.WriteFile(metadataPath, []byte(metadata), 0644); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}
	shaPath := archivePath + ".sha256"
	if err := os.WriteFile(shaPath, []byte("deadbeef "+filepath.Base(archivePath)), 0644); err != nil {
		t.Fatalf("failed to write sha file: %v", err)
	}

	manifest, err := LoadManifest(metadataPath)
	if err != nil {
		t.Fatalf("LoadManifest legacy failed: %v", err)
	}
	if manifest.ArchivePath != archivePath {
		t.Fatalf("ArchivePath = %s; want %s", manifest.ArchivePath, archivePath)
	}
	if manifest.CompressionType != "xz" || manifest.CompressionLevel != 6 {
		t.Fatalf("compression fields not preserved: %+v", manifest)
	}
	if manifest.Hostname != "legacy-host" || manifest.ScriptVersion != "legacy-1.0" {
		t.Fatalf("legacy metadata not parsed correctly: %+v", manifest)
	}
	if manifest.SHA256 != "deadbeef" {
		t.Fatalf("expected SHA256 from sidecar, got %q", manifest.SHA256)
	}
	if manifest.EncryptionMode != "age" {
		t.Fatalf("expected ENCRYPTION_MODE=age, got %q", manifest.EncryptionMode)
	}
	if manifest.ArchiveSize == 0 {
		t.Fatalf("ArchiveSize should be filled from archive stat")
	}
	if manifest.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt should be derived from archive mod time")
	}
}

func TestLoadManifestLegacyDetectsAgeByExtension(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "backup.tar.age")
	if err := os.WriteFile(archivePath, []byte("encrypted data"), 0644); err != nil {
		t.Fatalf("failed to write archive: %v", err)
	}
	metadataPath := archivePath + ".metadata"
	if err := os.WriteFile(metadataPath, []byte("COMPRESSION_TYPE=xz\n"), 0644); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	manifest, err := LoadManifest(metadataPath)
	if err != nil {
		t.Fatalf("LoadManifest legacy age fallback failed: %v", err)
	}
	if manifest.EncryptionMode != "age" {
		t.Fatalf("expected encryption mode derived from .age extension, got %q", manifest.EncryptionMode)
	}
}

func TestLoadManifestInvalidFile(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "invalid.json")
	if err := os.WriteFile(manifestPath, []byte("{invalid"), 0644); err != nil {
		t.Fatalf("failed to write invalid manifest: %v", err)
	}
	if _, err := LoadManifest(manifestPath); err == nil {
		t.Fatal("expected LoadManifest to fail for invalid JSON")
	}
}
