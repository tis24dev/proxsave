package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestCloudRetentionAdoptsArchivesWrittenUnderALostFQDN is discussion #292 on the
// cloud backend, which is the location most likely to be shared: the shipped
// CLOUD_REMOTE_PATH default is a root with no host component, so several machines
// write into one prefix and ownership is the only thing keeping one of them from
// pruning another's archives.
//
// The cloud backend reaches the identity by a fourth route, distinct from all three
// local ones. Its List() parses an `rclone lsl` listing and deliberately attributes
// nothing, because attribution costs one `rclone cat` per archive and List also backs
// the run counter and the stats screen. Both facts about a writer therefore arrive in
// resolveRetentionOwners, off the bytes remoteManifestOwner fetches from the remote.
//
// TWO LIMITS ON WHAT THIS TEST COVERS, both real and both deliberate:
//
//   - The fixture is the BUNDLING-OFF shape, and it has to be. With the shipped
//     BUNDLE_ASSOCIATED_FILES=true default, CloudStorage.Store skips the sidecar
//     upload entirely (see the `if !c.config.BundleAssociatedFiles` guard there), so
//     the remote holds the bundle alone, remoteManifestOwner finds nothing to cat,
//     and cloud retention attributes by the filename token with no identity in play
//     at all. This plumb's reach in the field is narrower than the other three: it
//     covers installations that turned bundling off.
//   - The `cat` here answers for ".metadata" only. A real bundling-off upload puts
//     BOTH ".manifest.json" and ".metadata" on the remote (see the associatedFiles
//     slice in CloudStorage.Store); this fixture serves one of them deliberately, so
//     that the assertion is about the plumb and not about which suffix won. Nothing
//     here observes the suffix loop's ORDER or its refusal to merge two payloads,
//     and nothing needs to: reversing remoteManifestSuffixes leaves this test green
//     but turns TestRemoteManifestHostnameReadsTheManifest and
//     TestRemoteManifestHostnameFallsBackToManifestJSON red, which is where that
//     property is pinned and where it belongs.
//
// The fixture is run twice, with and without an identity in the manifest, and the
// control run is what makes the first one evidence about the identity: these archives
// name a spelling this host does not answer to, so nothing else can put them back
// into rotation.
func TestCloudRetentionAdoptsArchivesWrittenUnderALostFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	// Three archives this machine wrote under the name "hostname -f" returned, and one
	// belonging to another machine that must be spared whatever it carries.
	const newest = "pve.home.arpa-backup-20250103-100000.tar.zst"
	const surplus = "pve.home.arpa-backup-20250102-100000.tar.zst"
	const oldest = "pve.home.arpa-backup-20250101-100000.tar.zst"
	const foreign = "pbs.home.arpa-backup-20241231-100000.tar.zst"
	listing := "" +
		"      100 2025-01-03 10:00:00.000000000 " + newest + "\n" +
		"       10 2025-01-03 10:00:00.000000000 " + newest + ".sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 " + surplus + "\n" +
		"       10 2025-01-02 10:00:00.000000000 " + surplus + ".sha256\n" +
		"      100 2025-01-01 10:00:00.000000000 " + oldest + "\n" +
		"       10 2025-01-01 10:00:00.000000000 " + oldest + ".sha256\n" +
		"      100 2024-12-31 10:00:00.000000000 " + foreign + "\n" +
		"       10 2024-12-31 10:00:00.000000000 " + foreign + ".sha256\n"

	run := func(t *testing.T, manifestID string) (deleted int, deletedFiles []string) {
		t.Helper()

		// resolveRetentionOwners fans its `cat` calls out over CLOUD_PARALLEL_MAX_JOBS
		// goroutines. This config leaves that at one, but the fake must not be the
		// reason the test is deterministic.
		var mu sync.Mutex

		manifestFor := func(remotePath string) string {
			host := "pve.home.arpa"
			id := manifestID
			if strings.Contains(remotePath, "pbs.home.arpa") {
				host = "pbs.home.arpa"
				id = anotherServerID
			}
			if id == "" {
				return fmt.Sprintf(`{"hostname":%q}`, host)
			}
			return fmt.Sprintf(`{"hostname":%q,"server_id":%q}`, host, id)
		}

		// BundleAssociatedFiles stays false: see the note above, a bundling-on remote
		// carries no sidecar for remoteManifestOwner to read.
		cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive", ServerID: ourServerID}
		// The written hostname is empty because this machine no longer resolves the
		// name its own archives were written under, which is the whole fixture. Built
		// through the constructor so cfg.ServerID really has to reach the field
		// retention reads.
		cs, err := NewCloudStorage(cfg, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}

		cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			for _, a := range args {
				if a == "lsl" {
					return []byte(listing), nil
				}
			}
			remotePath := args[len(args)-1]
			switch args[0] {
			case "cat":
				// In THIS fixture only the ".metadata" sidecar exists. A real
				// bundling-off remote holds ".manifest.json" too; serving one keeps
				// the assertion about the plumb. Anything else reads back as an
				// error, exactly as rclone reports an absent object.
				if !strings.HasSuffix(remotePath, ".metadata") {
					return nil, fmt.Errorf("object not found: %s", remotePath)
				}
				return []byte(manifestFor(remotePath)), nil
			case "delete", "deletefile":
				mu.Lock()
				deletedFiles = append(deletedFiles, remotePath)
				mu.Unlock()
			}
			return nil, nil
		}

		deleted, err = cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
		if err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}
		return deleted, deletedFiles
	}

	withDeleted, withFiles := run(t, ourServerID)

	// Membership, not substring. Every archive is deleted together with its sidecars,
	// so the list holds "<name>.sha256" and "<name>.metadata" beside "<name>", and a
	// substring test is satisfied by a sidecar deletion alone: the ARCHIVE could be
	// spared and the assertion would still pass. deletedArchive compares whole paths.
	deletedArchive := func(name string) bool {
		for _, got := range withFiles {
			if got == name || strings.HasSuffix(got, "/"+name) || strings.HasSuffix(got, ":"+name) {
				return true
			}
		}
		return false
	}
	for _, name := range []string{surplus, oldest} {
		if !deletedArchive(name) {
			t.Errorf("cloud retention spared %s. Its manifest on the remote carries this host's own server identity and names a spelling this host lost, so it is this machine's own work under a name it no longer resolves and it must rotate again. Deleted: %v", name, withFiles)
		}
	}
	if deletedArchive(foreign) {
		t.Errorf("cloud retention deleted %s, which belongs to another machine and records another machine's identity. The shipped remote path is a shared root, and rclone deletes there are not recoverable (RCLONE_FLAGS ships --drive-use-trash=false). Deleted: %v", foreign, withFiles)
	}
	if deletedArchive(newest) {
		t.Errorf("cloud retention deleted the archive it was told to keep. Deleted: %v", withFiles)
	}
	if withDeleted != 2 {
		t.Errorf("deleted = %d, want 2; the count feeds the run summary and the retention report", withDeleted)
	}

	withoutDeleted, withoutFiles := run(t, "")
	if withoutDeleted != 0 || len(withoutFiles) != 0 {
		t.Errorf("without a server identity in the remote manifests, cloud retention deleted %d archive(s): %v. Every archive uploaded before this change records none, and from a host that cannot resolve the name they carry they are indistinguishable from a second machine's work on a shared remote", withoutDeleted, withoutFiles)
	}
}
