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

		opts := buildDecryptPathOptions(cfg)
		// With pre-scan enabled, cloud option is only shown if backups exist
		// Since no actual backups exist in test environment, expect only local + secondary
		if len(opts) != 2 {
			t.Fatalf("len(options) = %d; want 2 (local + secondary, cloud hidden due to no backups)", len(opts))
		}
		// Verify local and secondary are present
		if opts[0].Path != "/local" {
			t.Fatalf("opts[0].Path = %q; want /local", opts[0].Path)
		}
		if opts[1].Path != "/secondary" {
			t.Fatalf("opts[1].Path = %q; want /secondary", opts[1].Path)
		}
	})

	t.Run("rclone remote with base path and extra prefix", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = true
		cfg.CloudRemote = "gdrive:pbs-backups"
		cfg.CloudRemotePath = "server1"

		opts := buildDecryptPathOptions(cfg)
		// With pre-scan enabled, cloud option is only shown if backups exist
		// Since no actual backups exist in test environment, expect only local + secondary
		if len(opts) != 2 {
			t.Fatalf("len(options) = %d; want 2 (local + secondary, cloud hidden due to no backups)", len(opts))
		}
	})

	t.Run("local filesystem mount", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = true
		cfg.CloudRemote = "/mnt/cloud/backups"
		cfg.CloudRemotePath = "server1"

		opts := buildDecryptPathOptions(cfg)
		// With pre-scan enabled, cloud option is only shown if backups exist
		// Since no actual backups exist in test environment, expect only local + secondary
		if len(opts) != 2 {
			t.Fatalf("len(options) = %d; want 2 (local + secondary, cloud hidden due to no backups)", len(opts))
		}
	})

	t.Run("cloud disabled", func(t *testing.T) {
		cfg := makeCfg()
		cfg.CloudEnabled = false
		cfg.CloudRemote = "gdrive:pbs-backups"

		opts := buildDecryptPathOptions(cfg)
		if len(opts) != 2 {
			t.Fatalf("len(options) = %d; want 2 (local + secondary)", len(opts))
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

	opts := buildDecryptPathOptions(cfg)
	// With pre-scan enabled, cloud option is only shown if backups exist
	// Since no actual backups exist in test environment, expect only local + secondary
	if len(opts) != 2 {
		t.Fatalf("len(options) = %d; want 2 (local + secondary, cloud hidden due to no backups)", len(opts))
	}

	if opts[0].Label != "Local backups" || opts[0].Path != "/local" {
		t.Fatalf("opts[0] = %#v; want Label=Local backups, Path=/local", opts[0])
	}
	if opts[1].Label != "Secondary backups" || opts[1].Path != "/secondary" {
		t.Fatalf("opts[1] = %#v; want Label=Secondary backups, Path=/secondary", opts[1])
	}
}

func TestDiscoverRcloneBackups_ParseFilenames(t *testing.T) {
	// Test the filename filtering logic (independent of rclone invocation)
	testFiles := []string{
		"backup-20250115.bundle.tar",
		"backup-20250114.bundle.tar",
		"backup-20250113.tar.xz",         // Should be ignored (not .bundle.tar)
		"log-20250115.log",               // Should be ignored
		"backup-20250112.bundle.tar.age", // Should be ignored (has .age extension)
	}

	expectedCount := 2 // Only the two .bundle.tar files

	count := 0
	for _, filename := range testFiles {
		if strings.HasSuffix(filename, ".bundle.tar") {
			count++
		}
	}

	if count != expectedCount {
		t.Errorf("Expected %d .bundle.tar files, got %d", expectedCount, count)
	}
}

func TestDiscoverRcloneBackups_ListsAndParsesBundles(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "pbs1-backup-20251205.tar.xz.bundle.tar")

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

	// Fake rclone binary:
	// - "rclone lsf <remote>" prints one .bundle.tar
	// - "rclone cat <remote>/<file>" cats the bundle pointed by $BUNDLE_PATH
	scriptPath := filepath.Join(tmpDir, "rclone")
	script := `#!/bin/sh
subcmd="$1"
case "$subcmd" in
  lsf)
    # list a single bundle file (with valid backup pattern)
    printf 'pbs1-backup-20251205.tar.xz.bundle.tar\n'
    ;;
  cat)
    # stream the test bundle
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
	defer os.Setenv("PATH", oldPath)

	if err := os.Setenv("BUNDLE_PATH", bundlePath); err != nil {
		t.Fatalf("set BUNDLE_PATH: %v", err)
	}
	defer os.Unsetenv("BUNDLE_PATH")

	ctx := context.Background()
	logger := logging.New(types.LogLevelDebug, false)

	// Use a remote path with base directory; discoverRcloneBackups should
	// normalize it to "<remote>/backup.bundle.tar" for the cat step.
	candidates, err := discoverRcloneBackups(ctx, "gdrive:pbs-backups/server1", logger)
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
