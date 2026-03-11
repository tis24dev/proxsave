package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestIsRcloneRemote(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "Valid rclone remote with colon",
			path:     "gdrive:",
			expected: true,
		},
		{
			name:     "Valid rclone remote with path",
			path:     "gdrive:backups",
			expected: true,
		},
		{
			name:     "Valid rclone remote with subdirectory",
			path:     "s3backup:servers/pve1",
			expected: true,
		},
		{
			name:     "Local absolute path (not rclone)",
			path:     "/opt/backup",
			expected: false,
		},
		{
			name:     "Empty path",
			path:     "",
			expected: false,
		},
		{
			name:     "Path without colon (not rclone)",
			path:     "backup",
			expected: false,
		},
		{
			name:     "Path with spaces",
			path:     "  gdrive:backups  ",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isRcloneRemote(tt.path)
			if result != tt.expected {
				t.Errorf("isRcloneRemote(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestBuildCloudRemotePathVariants(t *testing.T) {
	t.Run("remote name only no prefix", func(t *testing.T) {
		got := buildCloudRemotePath("gdrive", "")
		if got != "gdrive:" {
			t.Fatalf("buildCloudRemotePath() = %q; want %q", got, "gdrive:")
		}
	})

	t.Run("remote name with prefix", func(t *testing.T) {
		got := buildCloudRemotePath("gdrive", "pbs-backups/server1")
		if got != "gdrive:pbs-backups/server1" {
			t.Fatalf("buildCloudRemotePath() = %q; want %q", got, "gdrive:pbs-backups/server1")
		}
	})

	t.Run("remote with base path no extra prefix", func(t *testing.T) {
		got := buildCloudRemotePath("gdrive:pbs-backups", "")
		if got != "gdrive:pbs-backups" {
			t.Fatalf("buildCloudRemotePath() = %q; want %q", got, "gdrive:pbs-backups")
		}
	})

	t.Run("remote with base path and prefix", func(t *testing.T) {
		got := buildCloudRemotePath("gdrive:pbs-backups", "server1")
		if got != "gdrive:pbs-backups/server1" {
			t.Fatalf("buildCloudRemotePath() = %q; want %q", got, "gdrive:pbs-backups/server1")
		}
	})

	t.Run("absolute mount path with prefix", func(t *testing.T) {
		got := buildCloudRemotePath("/mnt/cloud/backups", "server1")
		want := filepath.Join("/mnt/cloud/backups", "server1")
		if got != want {
			t.Fatalf("buildCloudRemotePath() = %q; want %q", got, want)
		}
	})
}

func TestBuildDecryptPathOptions_CloudVariants(t *testing.T) {
	makeCfg := func() *config.Config {
		return &config.Config{
			BackupPath:       "/local",
			SecondaryEnabled: true,
			SecondaryPath:    "/secondary",
		}
	}

	t.Run("rclone remote with name and prefix", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = true
		cfg.CloudRemote = "gdrive"
		cfg.CloudRemotePath = "pbs-backups/server1"

		opts := buildDecryptPathOptions(cfg, nil)
		if len(opts) != 3 {
			t.Fatalf("len(options) = %d; want 3 (local + secondary + cloud)", len(opts))
		}
		// Verify local and secondary are present
		if opts[0].Path != "/local" {
			t.Fatalf("opts[0].Path = %q; want /local", opts[0].Path)
		}
		if opts[1].Path != "/secondary" {
			t.Fatalf("opts[1].Path = %q; want /secondary", opts[1].Path)
		}
		if opts[2].IsRclone != true {
			t.Fatalf("opts[2].IsRclone = %v; want true", opts[2].IsRclone)
		}
	})

	t.Run("rclone remote with base path and extra prefix", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = true
		cfg.CloudRemote = "gdrive:pbs-backups"
		cfg.CloudRemotePath = "server1"

		opts := buildDecryptPathOptions(cfg, nil)
		if len(opts) != 3 {
			t.Fatalf("len(options) = %d; want 3 (local + secondary + cloud)", len(opts))
		}
	})

	t.Run("local filesystem mount", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = true
		cfg.CloudRemote = "/mnt/cloud/backups"
		cfg.CloudRemotePath = "server1"

		opts := buildDecryptPathOptions(cfg, nil)
		if len(opts) != 3 {
			t.Fatalf("len(options) = %d; want 3 (local + secondary + cloud)", len(opts))
		}
	})

	t.Run("cloud disabled", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = false
		cfg.CloudRemote = "gdrive:pbs-backups"

		opts := buildDecryptPathOptions(cfg, nil)
		if len(opts) != 3 {
			t.Fatalf("len(options) = %d; want 3 (local + secondary + cloud)", len(opts))
		}
	})
}

func TestBuildDecryptPathOptions_FullConfigOrder(t *testing.T) {
	cfg := &config.Config{
		BackupPath:       "/local",
		SecondaryEnabled: true,
		SecondaryPath:    "/secondary",
		CloudEnabled:     true,
		CloudRemote:      "gdrive",
		CloudRemotePath:  "pbs-backups/server1",
	}

	opts := buildDecryptPathOptions(cfg, nil)
	if len(opts) != 3 {
		t.Fatalf("len(options) = %d; want 3 (local + secondary + cloud)", len(opts))
	}

	if opts[0].Label != "Local backups" || opts[0].Path != "/local" {
		t.Fatalf("opts[0] = %#v; want Label=Local backups, Path=/local", opts[0])
	}
	if opts[1].Label != "Secondary backups" || opts[1].Path != "/secondary" {
		t.Fatalf("opts[1] = %#v; want Label=Secondary backups, Path=/secondary", opts[1])
	}
	if opts[2].Label != "Cloud backups (rclone)" || opts[2].Path != "gdrive:pbs-backups/server1" || !opts[2].IsRclone {
		t.Fatalf("opts[2] = %#v; want Label=Cloud backups (rclone), Path=gdrive:pbs-backups/server1, IsRclone=true", opts[2])
	}
}

func TestDiscoverRcloneBackups_ListsAndParsesBundles(t *testing.T) {
	ctx := context.Background()
	logger := logging.New(types.LogLevelDebug, false)

	manifest, cleanup := setupFakeRcloneListAndCat(t)
	defer cleanup()

	candidates, err := discoverRcloneBackups(ctx, nil, "gdrive:pbs-backups/server1", logger, nil)
	if err != nil {
		t.Fatalf("discoverRcloneBackups() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discoverRcloneBackups() returned %d candidates, want 1", len(candidates))
	}
	cand := candidates[0]
	if cand.Manifest == nil {
		t.Fatal("candidate Manifest is nil")
	}
	if cand.Manifest.ArchivePath != manifest.ArchivePath {
		t.Fatalf("ArchivePath = %q; want %q", cand.Manifest.ArchivePath, manifest.ArchivePath)
	}
	if !cand.IsRclone {
		t.Fatalf("IsRclone = false; want true")
	}
}

func TestDiscoverRcloneBackups_IncludesRawMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	manifest := backup.Manifest{
		ArchivePath:    "/var/backups/node-backup-20251205.tar.xz",
		ProxmoxType:    "pve",
		ProxmoxVersion: "8.1",
		CreatedAt:      time.Date(2025, 12, 5, 12, 0, 0, 0, time.UTC),
		EncryptionMode: "none",
		SHA256:         checksumHexForBytes([]byte("node-backup-20251205")),
	}
	metaBytes, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	metadataPath := filepath.Join(tmpDir, "node-backup-20251205.tar.xz.metadata")
	if err := os.WriteFile(metadataPath, metaBytes, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
subcmd="$1"
case "$subcmd" in
  lsf)
    printf 'node-backup-20251205.tar.xz\n'
    printf 'node-backup-20251205.tar.xz.metadata\n'
    ;;
  cat)
    cat "$METADATA_PATH"
    ;;
  *)
    echo "unexpected subcommand: $subcmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)

	if err := os.Setenv("METADATA_PATH", metadataPath); err != nil {
		t.Fatalf("set METADATA_PATH: %v", err)
	}
	defer os.Unsetenv("METADATA_PATH")

	ctx := context.Background()
	candidates, err := discoverRcloneBackups(ctx, nil, "gdrive:pbs-backups/server1", nil, nil)
	if err != nil {
		t.Fatalf("discoverRcloneBackups() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discoverRcloneBackups() returned %d candidates; want 1", len(candidates))
	}
	cand := candidates[0]
	if cand.Source != sourceRaw {
		t.Fatalf("Source = %v; want sourceRaw", cand.Source)
	}
	if !cand.IsRclone {
		t.Fatalf("IsRclone = false; want true")
	}
	if cand.Manifest == nil {
		t.Fatal("Manifest is nil")
	}
	if cand.Manifest.ArchivePath != manifest.ArchivePath {
		t.Fatalf("ArchivePath = %q; want %q", cand.Manifest.ArchivePath, manifest.ArchivePath)
	}
	if !strings.HasSuffix(cand.RawArchivePath, "node-backup-20251205.tar.xz") {
		t.Fatalf("RawArchivePath = %q; want to end with archive name", cand.RawArchivePath)
	}
	if !strings.HasSuffix(cand.RawMetadataPath, "node-backup-20251205.tar.xz.metadata") {
		t.Fatalf("RawMetadataPath = %q; want to end with metadata name", cand.RawMetadataPath)
	}
}

func TestDiscoverRcloneBackups_MixedCandidatesSortedByCreatedAt(t *testing.T) {
	tmpDir := t.TempDir()

	// 1) Raw candidate (newest)
	rawNewestArchive := filepath.Join(tmpDir, "raw-newest.tar.xz")
	rawNewestMeta := filepath.Join(tmpDir, "raw-newest.tar.xz.metadata")
	rawNewest := backup.Manifest{
		ArchivePath:    "/var/backups/raw-newest.tar.xz",
		CreatedAt:      time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC),
		EncryptionMode: "none",
		ProxmoxType:    "pve",
		SHA256:         checksumHexForBytes([]byte("x")),
	}
	rawNewestData, _ := json.Marshal(&rawNewest)
	if err := os.WriteFile(rawNewestMeta, rawNewestData, 0o600); err != nil {
		t.Fatalf("write raw newest metadata: %v", err)
	}

	// 2) Bundle candidate (middle)
	bundlePath := filepath.Join(tmpDir, "bundle-mid.tar.xz.bundle.tar")
	bundleManifest := backup.Manifest{
		ArchivePath:    "/var/backups/bundle-mid.tar.xz",
		CreatedAt:      time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
		EncryptionMode: "age",
		ProxmoxType:    "pve",
	}
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	tw := tar.NewWriter(f)
	bData, _ := json.Marshal(&bundleManifest)
	if err := tw.WriteHeader(&tar.Header{Name: "backup/bundle-mid.metadata", Mode: 0o600, Size: int64(len(bData))}); err != nil {
		t.Fatalf("write bundle header: %v", err)
	}
	if _, err := tw.Write(bData); err != nil {
		t.Fatalf("write bundle body: %v", err)
	}
	_ = tw.Close()
	_ = f.Close()

	// 3) Raw candidate (oldest, with ArchivePath empty to exercise fallback)
	rawOldArchive := filepath.Join(tmpDir, "raw-old.tar.xz")
	rawOldMeta := filepath.Join(tmpDir, "raw-old.tar.xz.metadata")
	rawOld := backup.Manifest{
		ArchivePath:    "",
		CreatedAt:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		EncryptionMode: "none",
		ProxmoxType:    "pve",
		SHA256:         checksumHexForBytes([]byte("x")),
	}
	rawOldData, _ := json.Marshal(&rawOld)
	if err := os.WriteFile(rawOldMeta, rawOldData, 0o600); err != nil {
		t.Fatalf("write raw old metadata: %v", err)
	}

	// Fake rclone that supports lsf + cat for the above files.
	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
subcmd="$1"
target="$2"
case "$subcmd" in
  lsf)
    printf 'raw-newest.tar.xz\n'
    printf 'raw-newest.tar.xz.metadata\n'
    printf 'bundle-mid.tar.xz.bundle.tar\n'
    printf 'raw-old.tar.xz\n'
    printf 'raw-old.tar.xz.metadata\n'
    ;;
  cat)
    case "$target" in
      *bundle-mid.tar.xz.bundle.tar) cat "$BUNDLE_PATH" ;;
      *raw-newest.tar.xz.metadata) cat "$RAW_NEWEST_META" ;;
      *raw-old.tar.xz.metadata) cat "$RAW_OLD_META" ;;
      *) echo "unexpected cat target: $target" >&2; exit 1 ;;
    esac
    ;;
  *)
    echo "unexpected subcommand: $subcmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)
	_ = os.Setenv("BUNDLE_PATH", bundlePath)
	_ = os.Setenv("RAW_NEWEST_META", rawNewestMeta)
	_ = os.Setenv("RAW_OLD_META", rawOldMeta)
	defer os.Unsetenv("BUNDLE_PATH")
	defer os.Unsetenv("RAW_NEWEST_META")
	defer os.Unsetenv("RAW_OLD_META")

	// Ensure archives appear in lsf snapshot; their content is not fetched.
	_ = os.WriteFile(rawNewestArchive, []byte("x"), 0o600)
	_ = os.WriteFile(rawOldArchive, []byte("x"), 0o600)

	candidates, err := discoverRcloneBackups(context.Background(), nil, "gdrive:backups", nil, nil)
	if err != nil {
		t.Fatalf("discoverRcloneBackups error: %v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates=%d; want 3", len(candidates))
	}
	if candidates[0].Manifest.CreatedAt != rawNewest.CreatedAt {
		t.Fatalf("candidates[0].CreatedAt=%s; want %s", candidates[0].Manifest.CreatedAt, rawNewest.CreatedAt)
	}
	if candidates[1].Manifest.CreatedAt != bundleManifest.CreatedAt {
		t.Fatalf("candidates[1].CreatedAt=%s; want %s", candidates[1].Manifest.CreatedAt, bundleManifest.CreatedAt)
	}
	if candidates[2].Manifest.CreatedAt != rawOld.CreatedAt {
		t.Fatalf("candidates[2].CreatedAt=%s; want %s", candidates[2].Manifest.CreatedAt, rawOld.CreatedAt)
	}
	if candidates[2].Manifest.ArchivePath != "gdrive:backups/raw-old.tar.xz" {
		t.Fatalf("raw-old ArchivePath=%q; want fallback to remote archive", candidates[2].Manifest.ArchivePath)
	}
}

func TestDiscoverRcloneBackups_AllowsNilLogger(t *testing.T) {
	ctx := context.Background()
	manifest, cleanup := setupFakeRcloneListAndCat(t)
	defer cleanup()

	candidates, err := discoverRcloneBackups(ctx, nil, "gdrive:pbs-backups/server1", nil, nil)
	if err != nil {
		t.Fatalf("discoverRcloneBackups() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discoverRcloneBackups() returned %d candidates, want 1", len(candidates))
	}
	if candidates[0].Manifest.ArchivePath != manifest.ArchivePath {
		t.Fatalf("ArchivePath = %q; want %q", candidates[0].Manifest.ArchivePath, manifest.ArchivePath)
	}
}

func TestRemoveDecryptPathOption_RemovesMatchingOption(t *testing.T) {
	options := []decryptPathOption{
		{Label: "Local", Path: "/local", IsRclone: false},
		{Label: "Secondary", Path: "/secondary", IsRclone: false},
		{Label: "Cloud", Path: "gdrive:pbs-backups", IsRclone: true},
	}

	target := decryptPathOption{Label: "Secondary", Path: "/secondary", IsRclone: false}
	got := removeDecryptPathOption(options, target)

	if len(got) != 2 {
		t.Fatalf("len(options) = %d; want 2 after removal", len(got))
	}
	if got[0].Label != "Local" || got[1].Label != "Cloud" {
		t.Fatalf("unexpected options after removal: %+v", got)
	}

	// Removing an option that doesn't exist should be a no-op.
	unchanged := removeDecryptPathOption(got, decryptPathOption{Label: "Missing", Path: "/missing"})
	if len(unchanged) != len(got) {
		t.Fatalf("expected no change when removing non-existent option")
	}
}

func TestInspectRcloneBundleManifest_UsesRcloneCat(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "backup.bundle.tar")

	// Create a minimal bundle with a single manifest entry.
	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	tw := tar.NewWriter(f)

	manifest := backup.Manifest{
		ArchivePath:    "/var/backups/pbs-backup.tar.xz",
		ProxmoxType:    "pve",
		ProxmoxVersion: "7.4",
		CreatedAt:      time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		EncryptionMode: "age",
	}
	data, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	hdr := &tar.Header{
		Name: "backup/backup.tar.xz.metadata",
		Mode: 0o600,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close bundle: %v", err)
	}

	// Fake rclone binary that simply cats the bundle pointed to by $BUNDLE_PATH.
	scriptPath := filepath.Join(tmpDir, "rclone")
	script := "#!/bin/sh\ncat \"$BUNDLE_PATH\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)

	if err := os.Setenv("BUNDLE_PATH", bundlePath); err != nil {
		t.Fatalf("set BUNDLE_PATH: %v", err)
	}
	defer os.Unsetenv("BUNDLE_PATH")

	ctx := context.Background()
	logger := logging.New(types.LogLevelInfo, false)

	got, err := inspectRcloneBundleManifest(ctx, "gdrive:pbs-backups/backup.bundle.tar", logger)
	if err != nil {
		t.Fatalf("inspectRcloneBundleManifest() error = %v", err)
	}
	if got == nil {
		t.Fatal("inspectRcloneBundleManifest() returned nil manifest")
	}
	if got.ArchivePath != manifest.ArchivePath {
		t.Fatalf("ArchivePath = %q; want %q", got.ArchivePath, manifest.ArchivePath)
	}
	if got.ProxmoxType != manifest.ProxmoxType {
		t.Fatalf("ProxmoxType = %q; want %q", got.ProxmoxType, manifest.ProxmoxType)
	}
	if got.EncryptionMode != manifest.EncryptionMode {
		t.Fatalf("EncryptionMode = %q; want %q", got.EncryptionMode, manifest.EncryptionMode)
	}
}

// setupFakeRcloneListAndCat creates a temporary bundle and installs a fake
// rclone binary that supports `lsf` and `cat`, emulating cloud discovery.
// It returns the manifest embedded in the bundle and a cleanup function that
// restores PATH and auxiliary env vars.
func setupFakeRcloneListAndCat(t *testing.T) (backup.Manifest, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "pbs1-backup-20251205.tar.xz.bundle.tar")

	manifest := backup.Manifest{
		ArchivePath:    "/var/backups/pbs-backup.tar.xz",
		ProxmoxType:    "pve",
		ProxmoxVersion: "7.4",
		CreatedAt:      time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC),
		EncryptionMode: "age",
	}

	f, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	tw := tar.NewWriter(f)
	data, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	hdr := &tar.Header{
		Name: "backup/backup.tar.xz.metadata",
		Mode: 0o600,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close bundle: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
subcmd="$1"
case "$subcmd" in
  lsf)
    printf 'pbs1-backup-20251205.tar.xz.bundle.tar\n'
    ;;
  cat)
    cat "$BUNDLE_PATH"
    ;;
  *)
    echo "unexpected subcommand: $subcmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	if err := os.Setenv("BUNDLE_PATH", bundlePath); err != nil {
		t.Fatalf("set BUNDLE_PATH: %v", err)
	}

	cleanup := func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Unsetenv("BUNDLE_PATH")
	}

	return manifest, cleanup
}

func TestDiscoverBackupCandidates_NoLoggerSkipsRawArtifactsWithoutChecksumVerification(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "config.tar.xz")
	if err := os.WriteFile(archivePath, []byte("dummy"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	meta := backup.Manifest{
		ArchivePath:    "/etc/pve/config.tar.xz",
		ProxmoxType:    "pve",
		CreatedAt:      time.Now(),
		EncryptionMode: "none",
	}
	metaPath := archivePath + ".metadata"
	data, err := json.Marshal(&meta)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	// Intentionally skip checksum to exercise warning path with nil logger.

	candidates, err := discoverBackupCandidates(nil, tmpDir)
	if err != nil {
		t.Fatalf("discoverBackupCandidates() error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("discoverBackupCandidates() returned %d candidates; want 0", len(candidates))
	}
}

func TestDiscoverBackupCandidates_NormalizesAndStoresIntegrityExpectation(t *testing.T) {
	tmpDir := t.TempDir()
	archiveData := []byte("archive")
	archivePath := filepath.Join(tmpDir, "config.tar.xz")
	if err := os.WriteFile(archivePath, archiveData, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	checksum := checksumHexForBytes(archiveData)
	manifest := backup.Manifest{
		ArchivePath:    "/etc/pve/config.tar.xz",
		ProxmoxType:    "pve",
		CreatedAt:      time.Now().UTC(),
		EncryptionMode: "none",
		SHA256:         "  " + strings.ToUpper(checksum) + "  ",
	}
	metaPath := archivePath + ".metadata"
	data, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	checksumLine := strings.ToUpper(checksum) + "  " + filepath.Base(archivePath) + "\n"
	if err := os.WriteFile(archivePath+".sha256", []byte(checksumLine), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	candidates, err := discoverBackupCandidates(nil, tmpDir)
	if err != nil {
		t.Fatalf("discoverBackupCandidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discoverBackupCandidates() returned %d candidates; want 1", len(candidates))
	}
	cand := candidates[0]
	if cand.Manifest == nil {
		t.Fatal("candidate Manifest is nil")
	}
	if cand.Manifest.SHA256 != checksum {
		t.Fatalf("Manifest.SHA256 = %q; want %q", cand.Manifest.SHA256, checksum)
	}
	if cand.Integrity == nil {
		t.Fatal("candidate Integrity is nil")
	}
	if cand.Integrity.Checksum != checksum {
		t.Fatalf("Integrity.Checksum = %q; want %q", cand.Integrity.Checksum, checksum)
	}
	if cand.Integrity.Source != "checksum file and manifest" {
		t.Fatalf("Integrity.Source = %q; want %q", cand.Integrity.Source, "checksum file and manifest")
	}
}

func TestDiscoverBackupCandidates_RejectsMalformedOrConflictingChecksums(t *testing.T) {
	tests := []struct {
		name           string
		manifestSHA256 string
		checksumData   string
	}{
		{
			name:           "invalid manifest checksum",
			manifestSHA256: "not-a-checksum",
			checksumData:   string(checksumLineForBytes("config.tar.xz", []byte("archive"))),
		},
		{
			name:           "invalid checksum file",
			manifestSHA256: checksumHexForBytes([]byte("archive")),
			checksumData:   "not-a-checksum  config.tar.xz\n",
		},
		{
			name:           "conflicting valid checksums",
			manifestSHA256: checksumHexForBytes([]byte("archive")),
			checksumData:   string(checksumLineForBytes("config.tar.xz", []byte("different"))),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			archivePath := filepath.Join(tmpDir, "config.tar.xz")
			if err := os.WriteFile(archivePath, []byte("archive"), 0o600); err != nil {
				t.Fatalf("write archive: %v", err)
			}

			manifest := backup.Manifest{
				ArchivePath:    "/etc/pve/config.tar.xz",
				ProxmoxType:    "pve",
				CreatedAt:      time.Now().UTC(),
				EncryptionMode: "none",
				SHA256:         tt.manifestSHA256,
			}
			metaPath := archivePath + ".metadata"
			data, err := json.Marshal(&manifest)
			if err != nil {
				t.Fatalf("marshal manifest: %v", err)
			}
			if err := os.WriteFile(metaPath, data, 0o600); err != nil {
				t.Fatalf("write metadata: %v", err)
			}
			if err := os.WriteFile(archivePath+".sha256", []byte(tt.checksumData), 0o600); err != nil {
				t.Fatalf("write checksum: %v", err)
			}

			candidates, err := discoverBackupCandidates(nil, tmpDir)
			if err != nil {
				t.Fatalf("discoverBackupCandidates() error = %v", err)
			}
			if len(candidates) != 0 {
				t.Fatalf("discoverBackupCandidates() returned %d candidates; want 0", len(candidates))
			}
		})
	}
}

func TestParseLocalChecksumFile_RejectsOversizedInput(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	checksumPath := filepath.Join(t.TempDir(), "config.tar.xz.sha256")
	oversized := strings.Repeat("a", checksumFileReadLimit+1) + "\n"
	if err := os.WriteFile(checksumPath, []byte(oversized), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	_, err := parseLocalChecksumFile(checksumPath)
	if err == nil {
		t.Fatal("parseLocalChecksumFile() error = nil; want oversize error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("parseLocalChecksumFile() error = %v; want oversize error", err)
	}
}

func TestParseLocalChecksumFile_AcceptsBoundedInputWithoutTrailingNewline(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	checksumPath := filepath.Join(t.TempDir(), "config.tar.xz.sha256")
	want := checksumHexForBytes([]byte("archive"))
	if err := os.WriteFile(checksumPath, checksumLineForBytes("config.tar.xz", []byte("archive")), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	got, err := parseLocalChecksumFile(checksumPath)
	if err != nil {
		t.Fatalf("parseLocalChecksumFile() error = %v", err)
	}
	if got != want {
		t.Fatalf("parseLocalChecksumFile() = %q; want %q", got, want)
	}
}

func TestDiscoverRcloneBackups_NormalizesAndStoresIntegrityExpectation(t *testing.T) {
	tmpDir := t.TempDir()

	archiveData := []byte("archive")
	checksum := checksumHexForBytes(archiveData)
	manifest := backup.Manifest{
		ArchivePath:    "/var/backups/node-backup.tar.xz",
		ProxmoxType:    "pve",
		CreatedAt:      time.Date(2025, 12, 5, 12, 0, 0, 0, time.UTC),
		EncryptionMode: "none",
		SHA256:         "  " + strings.ToUpper(checksum) + "  ",
	}
	metaBytes, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	metadataPath := filepath.Join(tmpDir, "node-backup.tar.xz.metadata")
	if err := os.WriteFile(metadataPath, metaBytes, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	checksumPath := filepath.Join(tmpDir, "node-backup.tar.xz.sha256")
	checksumLine := strings.ToUpper(checksum) + "  node-backup.tar.xz\n"
	if err := os.WriteFile(checksumPath, []byte(checksumLine), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
subcmd="$1"
target="$2"
case "$subcmd" in
  lsf)
    printf 'node-backup.tar.xz\n'
    printf 'node-backup.tar.xz.metadata\n'
    printf 'node-backup.tar.xz.sha256\n'
    ;;
  cat)
    case "$target" in
      *node-backup.tar.xz.metadata) cat "$METADATA_PATH" ;;
      *node-backup.tar.xz.sha256) cat "$CHECKSUM_PATH" ;;
      *) echo "unexpected cat target: $target" >&2; exit 1 ;;
    esac
    ;;
  *)
    echo "unexpected subcommand: $subcmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)
	if err := os.Setenv("METADATA_PATH", metadataPath); err != nil {
		t.Fatalf("set METADATA_PATH: %v", err)
	}
	defer os.Unsetenv("METADATA_PATH")
	if err := os.Setenv("CHECKSUM_PATH", checksumPath); err != nil {
		t.Fatalf("set CHECKSUM_PATH: %v", err)
	}
	defer os.Unsetenv("CHECKSUM_PATH")

	candidates, err := discoverRcloneBackups(context.Background(), nil, "gdrive:pbs-backups/server1", nil, nil)
	if err != nil {
		t.Fatalf("discoverRcloneBackups() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discoverRcloneBackups() returned %d candidates; want 1", len(candidates))
	}
	cand := candidates[0]
	if cand.Manifest == nil {
		t.Fatal("candidate Manifest is nil")
	}
	if cand.Manifest.SHA256 != checksum {
		t.Fatalf("Manifest.SHA256 = %q; want %q", cand.Manifest.SHA256, checksum)
	}
	if cand.Integrity == nil {
		t.Fatal("candidate Integrity is nil")
	}
	if cand.Integrity.Checksum != checksum {
		t.Fatalf("Integrity.Checksum = %q; want %q", cand.Integrity.Checksum, checksum)
	}
	if cand.Integrity.Source != "checksum file and manifest" {
		t.Fatalf("Integrity.Source = %q; want %q", cand.Integrity.Source, "checksum file and manifest")
	}
}

func TestDiscoverRcloneBackups_UsesFreshTimeoutForChecksumFetch(t *testing.T) {
	tmpDir := t.TempDir()

	archiveData := []byte("archive")
	checksum := checksumHexForBytes(archiveData)
	manifest := backup.Manifest{
		ArchivePath:    "/var/backups/node-backup.tar.xz",
		ProxmoxType:    "pve",
		CreatedAt:      time.Date(2025, 12, 5, 12, 0, 0, 0, time.UTC),
		EncryptionMode: "none",
		SHA256:         checksum,
	}
	metaBytes, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	metadataPath := filepath.Join(tmpDir, "node-backup.tar.xz.metadata")
	if err := os.WriteFile(metadataPath, metaBytes, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	checksumPath := filepath.Join(tmpDir, "node-backup.tar.xz.sha256")
	checksumLine := checksum + "  node-backup.tar.xz\n"
	if err := os.WriteFile(checksumPath, []byte(checksumLine), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
subcmd="$1"
target="$2"
case "$subcmd" in
  lsf)
    printf 'node-backup.tar.xz\n'
    printf 'node-backup.tar.xz.metadata\n'
    printf 'node-backup.tar.xz.sha256\n'
    ;;
  cat)
    case "$target" in
      *node-backup.tar.xz.metadata)
        sleep 2
        cat "$METADATA_PATH"
        ;;
      *node-backup.tar.xz.sha256)
        sleep 2
        cat "$CHECKSUM_PATH"
        ;;
      *)
        echo "unexpected cat target: $target" >&2
        exit 1
        ;;
    esac
    ;;
  *)
    echo "unexpected subcommand: $subcmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)
	if err := os.Setenv("METADATA_PATH", metadataPath); err != nil {
		t.Fatalf("set METADATA_PATH: %v", err)
	}
	defer os.Unsetenv("METADATA_PATH")
	if err := os.Setenv("CHECKSUM_PATH", checksumPath); err != nil {
		t.Fatalf("set CHECKSUM_PATH: %v", err)
	}
	defer os.Unsetenv("CHECKSUM_PATH")

	cfg := &config.Config{RcloneTimeoutConnection: 3}
	candidates, err := discoverRcloneBackups(context.Background(), cfg, "gdrive:pbs-backups/server1", nil, nil)
	if err != nil {
		t.Fatalf("discoverRcloneBackups() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("discoverRcloneBackups() returned %d candidates; want 1", len(candidates))
	}
	cand := candidates[0]
	if cand.Integrity == nil {
		t.Fatal("candidate Integrity is nil")
	}
	if cand.Integrity.Checksum != checksum {
		t.Fatalf("Integrity.Checksum = %q; want %q", cand.Integrity.Checksum, checksum)
	}
}

func TestDiscoverRcloneBackups_RejectsMalformedOrConflictingChecksums(t *testing.T) {
	tests := []struct {
		name           string
		manifestSHA256 string
		checksumData   string
	}{
		{
			name:           "invalid manifest checksum",
			manifestSHA256: "not-a-checksum",
			checksumData:   string(checksumLineForBytes("node-backup.tar.xz", []byte("archive"))),
		},
		{
			name:           "invalid checksum file",
			manifestSHA256: checksumHexForBytes([]byte("archive")),
			checksumData:   "not-a-checksum  node-backup.tar.xz\n",
		},
		{
			name:           "conflicting valid checksums",
			manifestSHA256: checksumHexForBytes([]byte("archive")),
			checksumData:   string(checksumLineForBytes("node-backup.tar.xz", []byte("different"))),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			manifest := backup.Manifest{
				ArchivePath:    "/var/backups/node-backup.tar.xz",
				ProxmoxType:    "pve",
				CreatedAt:      time.Date(2025, 12, 5, 12, 0, 0, 0, time.UTC),
				EncryptionMode: "none",
				SHA256:         tt.manifestSHA256,
			}
			metaBytes, err := json.Marshal(&manifest)
			if err != nil {
				t.Fatalf("marshal manifest: %v", err)
			}
			metadataPath := filepath.Join(tmpDir, "node-backup.tar.xz.metadata")
			if err := os.WriteFile(metadataPath, metaBytes, 0o600); err != nil {
				t.Fatalf("write metadata: %v", err)
			}
			checksumPath := filepath.Join(tmpDir, "node-backup.tar.xz.sha256")
			if err := os.WriteFile(checksumPath, []byte(tt.checksumData), 0o600); err != nil {
				t.Fatalf("write checksum: %v", err)
			}

			scriptPath := filepath.Join(tmpDir, "rclone")
			script := `#!/bin/sh
subcmd="$1"
target="$2"
case "$subcmd" in
  lsf)
    printf 'node-backup.tar.xz\n'
    printf 'node-backup.tar.xz.metadata\n'
    printf 'node-backup.tar.xz.sha256\n'
    ;;
  cat)
    case "$target" in
      *node-backup.tar.xz.metadata) cat "$METADATA_PATH" ;;
      *node-backup.tar.xz.sha256) cat "$CHECKSUM_PATH" ;;
      *) echo "unexpected cat target: $target" >&2; exit 1 ;;
    esac
    ;;
  *)
    echo "unexpected subcommand: $subcmd" >&2
    exit 1
    ;;
esac
`
			if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
				t.Fatalf("write fake rclone: %v", err)
			}

			oldPath := os.Getenv("PATH")
			if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
				t.Fatalf("set PATH: %v", err)
			}
			defer os.Setenv("PATH", oldPath)
			if err := os.Setenv("METADATA_PATH", metadataPath); err != nil {
				t.Fatalf("set METADATA_PATH: %v", err)
			}
			defer os.Unsetenv("METADATA_PATH")
			if err := os.Setenv("CHECKSUM_PATH", checksumPath); err != nil {
				t.Fatalf("set CHECKSUM_PATH: %v", err)
			}
			defer os.Unsetenv("CHECKSUM_PATH")

			candidates, err := discoverRcloneBackups(context.Background(), nil, "gdrive:pbs-backups/server1", nil, nil)
			if err != nil {
				t.Fatalf("discoverRcloneBackups() error = %v", err)
			}
			if len(candidates) != 0 {
				t.Fatalf("discoverRcloneBackups() returned %d candidates; want 0", len(candidates))
			}
		})
	}
}

func TestInspectRcloneChecksumFile_RejectsOversizedInput(t *testing.T) {
	tmpDir := t.TempDir()
	checksumPath := filepath.Join(tmpDir, "node-backup.tar.xz.sha256")
	oversized := strings.Repeat("a", checksumFileReadLimit+1) + "\n"
	if err := os.WriteFile(checksumPath, []byte(oversized), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
if [ "$1" != "cat" ]; then
  echo "unexpected subcommand: $1" >&2
  exit 1
fi
cat "$CHECKSUM_PATH"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CHECKSUM_PATH", checksumPath)

	_, err := inspectRcloneChecksumFile(context.Background(), "gdrive:node-backup.tar.xz.sha256", nil)
	if err == nil {
		t.Fatal("inspectRcloneChecksumFile() error = nil; want oversize error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("inspectRcloneChecksumFile() error = %v; want oversize error", err)
	}
}

func TestInspectRcloneChecksumFile_AcceptsBoundedInputWithoutTrailingNewline(t *testing.T) {
	tmpDir := t.TempDir()
	checksumPath := filepath.Join(tmpDir, "node-backup.tar.xz.sha256")
	want := checksumHexForBytes([]byte("archive"))
	if err := os.WriteFile(checksumPath, checksumLineForBytes("node-backup.tar.xz", []byte("archive")), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
if [ "$1" != "cat" ]; then
  echo "unexpected subcommand: $1" >&2
  exit 1
fi
cat "$CHECKSUM_PATH"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CHECKSUM_PATH", checksumPath)

	got, err := inspectRcloneChecksumFile(context.Background(), "gdrive:node-backup.tar.xz.sha256", nil)
	if err != nil {
		t.Fatalf("inspectRcloneChecksumFile() error = %v", err)
	}
	if got != want {
		t.Fatalf("inspectRcloneChecksumFile() = %q; want %q", got, want)
	}
}

func TestInspectRcloneChecksumFile_SurfacesRcloneFailureAfterValidFirstLine(t *testing.T) {
	tmpDir := t.TempDir()
	checksumPath := filepath.Join(tmpDir, "node-backup.tar.xz.sha256")
	if err := os.WriteFile(checksumPath, append(checksumLineForBytes("node-backup.tar.xz", []byte("archive")), '\n'), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
if [ "$1" != "cat" ]; then
  echo "unexpected subcommand: $1" >&2
  exit 1
fi
cat "$CHECKSUM_PATH"
echo "simulated rclone failure" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CHECKSUM_PATH", checksumPath)

	_, err := inspectRcloneChecksumFile(context.Background(), "gdrive:node-backup.tar.xz.sha256", nil)
	if err == nil {
		t.Fatal("inspectRcloneChecksumFile() error = nil; want rclone failure")
	}
	if !strings.Contains(err.Error(), "rclone cat gdrive:node-backup.tar.xz.sha256 failed") {
		t.Fatalf("inspectRcloneChecksumFile() error = %v; want rclone failure", err)
	}
	if !strings.Contains(err.Error(), "simulated rclone failure") {
		t.Fatalf("inspectRcloneChecksumFile() error = %v; want stderr output", err)
	}
}
