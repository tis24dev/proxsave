package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestValidateRcloneArgsAllowsCat pins "cat" in the subcommand allowlist. It is not
// cosmetic: without it c.exec rejects the call before running anything, so retention's
// manifest attribution silently returned "" for every archive and fell back to the
// filename token - the feature looked implemented and did nothing.
func TestValidateRcloneArgsAllowsCat(t *testing.T) {
	if err := validateRcloneArgs([]string{"cat", "remote:backup.tar.zst.metadata"}); err != nil {
		t.Fatalf("cat must be allowed: %v", err)
	}
	// The allowlist must stay an allowlist.
	if err := validateRcloneArgs([]string{"purge", "remote:"}); err == nil {
		t.Fatal("purge must still be rejected")
	}
}

// TestRemoteManifestHostnameReadsTheManifest is the discriminating test: the archive's
// FILENAME says one host and its MANIFEST says another. Only a real manifest read can
// return the manifest's value, so this fails if the cat call is rejected, unbuilt, or
// pointed at the wrong path.
func TestRemoteManifestHostnameReadsTheManifest(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)

	var catPaths []string
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "rclone" || len(args) == 0 || args[0] != "cat" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		catPaths = append(catPaths, args[len(args)-1])
		return []byte(`{"hostname":"from-manifest","created_at":"2025-01-02T10:00:00Z"}`), nil
	}

	got := cs.remoteManifestHostname(context.Background(), "server9-backup-20250102-100000.tar.zst")
	if got != "from-manifest" {
		t.Fatalf("hostname = %q, want %q (the manifest must win over the filename token)", got, "from-manifest")
	}
	if len(catPaths) != 1 || !strings.HasSuffix(catPaths[0], "server9-backup-20250102-100000.tar.zst.metadata") {
		t.Fatalf("expected one cat of the .metadata sidecar, got %v", catPaths)
	}
}

// A missing .metadata must fall through to .manifest.json before giving up.
func TestRemoteManifestHostnameFallsBackToManifestJSON(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)

	var tried []string
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		path := args[len(args)-1]
		tried = append(tried, path)
		if strings.HasSuffix(path, ".metadata") {
			return nil, context.DeadlineExceeded
		}
		return []byte(`{"hostname":"from-json"}`), nil
	}

	if got := cs.remoteManifestHostname(context.Background(), "a-backup-20250102-100000.tar.zst"); got != "from-json" {
		t.Fatalf("hostname = %q, want from-json", got)
	}
	if len(tried) != 2 {
		t.Fatalf("expected .metadata then .manifest.json, got %v", tried)
	}
}

// Unreadable or absent manifests must yield "", which backupOwnerHost then degrades to
// the filename token rather than guessing.
func TestRemoteManifestHostnameUnreadableYieldsEmpty(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("not json at all"), nil
	}

	if got := cs.remoteManifestHostname(context.Background(), "a-backup-20250102-100000.tar.zst"); got != "" {
		t.Fatalf("hostname = %q, want empty", got)
	}
}

// TestResolveRetentionOwnersPrefersManifestOverFilename pins the consequence at the
// retention level: an archive whose filename names another host is still ours when its
// manifest says so, and is therefore prunable.
func TestResolveRetentionOwnersPrefersManifestOverFilename(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)
	cs.hostname = "server1"
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte(`{"hostname":"server1"}`), nil
	}

	list := []*types.BackupMetadata{{BackupFile: "server9-backup-20250102-100000.tar.zst"}}
	cs.resolveRetentionOwners(context.Background(), list)

	if list[0].Hostname != "server1" {
		t.Fatalf("Hostname = %q, want server1 from the manifest", list[0].Hostname)
	}
	if !backupBelongsToHost(list[0], cs.hostname) {
		t.Fatal("an archive the manifest attributes to this host must be in retention scope")
	}
}
