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

	got, _ := cs.remoteManifestOwner(context.Background(), "server9-backup-20250102-100000.tar.zst")
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

	if got, _ := cs.remoteManifestOwner(context.Background(), "a-backup-20250102-100000.tar.zst"); got != "from-json" {
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

	if got, _ := cs.remoteManifestOwner(context.Background(), "a-backup-20250102-100000.tar.zst"); got != "" {
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
	if !backupBelongsToHost(list[0], hostOnly(cs.hostname)) {
		t.Fatal("an archive the manifest attributes to this host must be in retention scope")
	}
}

// TestApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN is the reported bug end to
// end. Every archive here was written by this machine under the name "hostname -f"
// returns, while retention reads the kernel short name, so before the fix scoping
// left nothing owned and ApplyRetention returned 0 without issuing a single delete.
// The foreign archive in the listing is there to prove the fix widens ownership to
// this machine's own names only. It is built through NewCloudStorage rather than by
// setting the fields, so it also proves the run's own name reaches the field
// retention reads.
func TestApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs, err := NewCloudStorage(cfg, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}

	// The second archive was RENAMED on the remote: its filename token names another
	// machine while its manifest names this one. Only a real resolveRetentionOwners
	// call can put it in scope, so it is what makes that call load bearing here.
	const renamed = "pbs.siteb.example-backup-20250102-120000"
	listing := "" +
		"      100 2025-01-03 10:00:00.000000000 pve.home.arpa-backup-20250103-100000.tar.zst\n" +
		"       10 2025-01-03 10:00:00.000000000 pve.home.arpa-backup-20250103-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 12:00:00.000000000 " + renamed + ".tar.zst\n" +
		"       10 2025-01-02 12:00:00.000000000 " + renamed + ".tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 pve.home.arpa-backup-20250102-100000.tar.zst\n" +
		"       10 2025-01-02 10:00:00.000000000 pve.home.arpa-backup-20250102-100000.tar.zst.sha256\n" +
		"      100 2025-01-01 10:00:00.000000000 pbs.home.arpa-backup-20250101-100000.tar.zst\n" +
		"       10 2025-01-01 10:00:00.000000000 pbs.home.arpa-backup-20250101-100000.tar.zst.sha256\n"

	var calls []commandCall
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, commandCall{name: name, args: append([]string(nil), args...)})
		for _, a := range args {
			if a == "lsl" {
				return []byte(listing), nil
			}
		}
		// Only the renamed archive's manifest reads back. Every other `cat` returns
		// nothing usable, so those three degrade to their filename token, which
		// carries the same FQDN the manifest would; the renamed one can be
		// attributed by its manifest and by nothing else.
		if args[0] == "cat" && strings.Contains(args[len(args)-1], renamed) {
			return []byte(`{"hostname":"pve.home.arpa"}`), nil
		}
		return nil, nil
	}

	deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	deletedOwn := false
	deletedRenamed := false
	for _, call := range calls {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "delete") {
			continue
		}
		if strings.Contains(joined, "pbs.home.arpa") {
			t.Errorf("retention deleted another host's backup: %+v", calls)
		}
		if strings.Contains(joined, "pve.home.arpa-backup-20250102") {
			deletedOwn = true
		}
		if strings.Contains(joined, renamed) {
			deletedRenamed = true
		}
	}
	if !deletedOwn {
		t.Errorf("retention pruned nothing written under this run's own name: %+v", calls)
	}
	if !deletedRenamed {
		t.Errorf("retention spared %s: its manifest names this machine, so it is this machine's own work whatever its filename says. Only a real resolveRetentionOwners call can see that, so this is what fails when the call is dropped: %+v", renamed, calls)
	}
	// Asserted last, so a real regression reports WHICH archive survived before it
	// reports a number.
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2 (the renamed archive attributed by its manifest and the older archive attributed by its filename token); the count feeds the run summary and the retention report", deleted)
	}
}

// TestApplyRetentionLeavesSameShortNameForeignFQDNAlone is the data-loss boundary end
// to end, and the single most important test in this change. This host is a stock
// node called "pve" whose "hostname -f" fails, so it has no aliases, and the shared
// remote root holds only a second machine's archives, spelled "pve.siteb.example".
// The two share a short label and are different machines. Retention must delete
// nothing at all. A fold to the first label turns this test red by destroying the
// other machine's older archive.
func TestApplyRetentionLeavesSameShortNameForeignFQDNAlone(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)
	// Set by hand on purpose: this test pins the deletion boundary, not the
	// constructor wiring (that is TestNewCloudStorageRecordsWrittenHostnames), and it
	// must stay true whatever the constructor does.
	cs.hostname = "pve"
	cs.hostAliases = nil

	listing := "" +
		"      100 2025-01-03 10:00:00.000000000 pve.siteb.example-backup-20250103-100000.tar.zst\n" +
		"       10 2025-01-03 10:00:00.000000000 pve.siteb.example-backup-20250103-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 pve.siteb.example-backup-20250102-100000.tar.zst\n" +
		"       10 2025-01-02 10:00:00.000000000 pve.siteb.example-backup-20250102-100000.tar.zst.sha256\n"

	var calls []commandCall
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, commandCall{name: name, args: append([]string(nil), args...)})
		for _, a := range args {
			if a == "lsl" {
				return []byte(listing), nil
			}
		}
		return nil, nil
	}

	deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0: none of these archives belong to this machine", deleted)
	}
	for _, call := range calls {
		if strings.Contains(strings.Join(call.args, " "), "delete") {
			t.Fatalf("retention deleted another machine's backup: %+v", calls)
		}
	}
}
