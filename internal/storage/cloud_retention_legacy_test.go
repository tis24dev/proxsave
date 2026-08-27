package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// legacyCloudArchive is the pre-Go archive name every test in this file shares. It
// carries no host token: "proxmox" is the product name, not a machine.
const legacyCloudArchive = "proxmox-backup-20250102-100000.tar.gz"

// cloudLegacyFixture runs one cloud retention pass over a shared remote root holding
// two archives written by ownArchiveHost plus one pre-Go archive, and returns the
// argument list of every delete the pass issued.
//
// thisHost is the machine running retention. legacySidecar is what an `rclone cat`
// of the pre-Go archive's .metadata returns; an empty string makes that read FAIL,
// which is the ordinary case on a remote whose sidecars were never uploaded or
// cannot be reached. Every other `cat` fails too, so the two modern archives are
// attributed by their filename token, which is what happens on a real remote whose
// pre-Go entries have no JSON manifest.
//
// The retention policy is simple with MaxBackups 1, so with three attributable
// archives two would be pruned and with two attributable archives one would be.
func cloudLegacyFixture(t *testing.T, ownArchiveHost, thisHost, legacySidecar string) []string {
	t.Helper()

	cfg := &config.Config{CloudEnabled: true, CloudRemote: "gdrive"}
	cs := newCloudStorageForTest(cfg)
	// Set by hand on purpose: these tests pin the deletion boundary, not the
	// constructor wiring (TestNewCloudStorageRecordsWrittenHostnames covers that),
	// and the boundary must hold whatever the constructor does.
	cs.hostname = thisHost
	cs.hostAliases = nil

	listing := "" +
		"      100 2025-01-04 10:00:00.000000000 " + ownArchiveHost + "-backup-20250104-100000.tar.zst\n" +
		"       10 2025-01-04 10:00:00.000000000 " + ownArchiveHost + "-backup-20250104-100000.tar.zst.sha256\n" +
		"      100 2025-01-03 10:00:00.000000000 " + ownArchiveHost + "-backup-20250103-100000.tar.zst\n" +
		"       10 2025-01-03 10:00:00.000000000 " + ownArchiveHost + "-backup-20250103-100000.tar.zst.sha256\n" +
		"      100 2025-01-02 10:00:00.000000000 " + legacyCloudArchive + "\n" +
		"       10 2025-01-02 10:00:00.000000000 " + legacyCloudArchive + ".sha256\n" +
		"       40 2025-01-02 10:00:00.000000000 " + legacyCloudArchive + ".metadata\n"

	var deletes []string
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "lsl" {
				return []byte(listing), nil
			}
		}
		if len(args) > 0 && args[0] == "cat" {
			target := args[len(args)-1]
			if legacySidecar != "" && strings.Contains(target, legacyCloudArchive) && strings.HasSuffix(target, ".metadata") {
				return []byte(legacySidecar), nil
			}
			return nil, fmt.Errorf("cannot read %s", target)
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "delete") {
			deletes = append(deletes, joined)
		}
		return nil, nil
	}

	if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	return deletes
}

// deletedAnyMatching reports whether any delete the pass issued mentions needle.
func deletedAnyMatching(deletes []string, needle string) bool {
	for _, d := range deletes {
		if strings.Contains(d, needle) {
			return true
		}
	}
	return false
}

// TestCloudRetentionLeavesUnattributableLegacyArchivesAlone is the shipped-default
// shape: CLOUD_REMOTE_PATH defaults to /proxsave/backup with no host component, so
// two machines pointed at the same rclone remote share one listing. The pre-Go
// archive's sidecar cannot be read, so nothing names the machine that wrote it and
// no machine may claim it. Deletion on the cloud backend is irreversible under the
// shipped RCLONE_FLAGS, which carry --drive-use-trash=false.
//
// The second assertion is what stops this passing for the wrong reason: this host's
// own surplus must still be pruned.
func TestCloudRetentionLeavesUnattributableLegacyArchivesAlone(t *testing.T) {
	deletes := cloudLegacyFixture(t, "hostA", "hostA", "")

	if deletedAnyMatching(deletes, "proxmox-backup-") {
		t.Errorf("retention deleted a pre-Go archive nothing attributes to this machine, on a remote root the shipped default shares between hosts: %q", deletes)
	}
	if !deletedAnyMatching(deletes, "hostA-backup-20250103") {
		t.Errorf("retention pruned nothing of this host's own: scoping must narrow what retention deletes, not switch it off: %q", deletes)
	}
}

// TestCloudRetentionLeavesALegacyArchiveThatNamesAnotherHostAlone is the reproduction
// of the reported harm on the backend where it is worst. hostA lists a shared remote
// root holding a pre-Go archive whose sidecar says HOSTNAME=hostB. The cloud
// attribution path could read only the JSON manifest form, so a KEY=VALUE sidecar
// left the archive unattributed and hostA deleted the archive, its .sha256 and the
// very .metadata that named hostB.
func TestCloudRetentionLeavesALegacyArchiveThatNamesAnotherHostAlone(t *testing.T) {
	deletes := cloudLegacyFixture(t, "hostA", "hostA", "COMPRESSION_TYPE=gzip\nHOSTNAME=hostB\n")

	if deletedAnyMatching(deletes, "proxmox-backup-") {
		t.Errorf("retention on hostA deleted an archive whose own sidecar names hostB, together with the sidecars that said so. That is one machine destroying another machine's backup irreversibly: %q", deletes)
	}
	if !deletedAnyMatching(deletes, "hostA-backup-20250103") {
		t.Errorf("retention pruned nothing of this host's own: %q", deletes)
	}
}

// TestCloudRetentionPrunesALegacyArchiveThatNamesThisHost is the same archive read
// from the machine that wrote it. Attribution has to name exactly one host: not
// nobody (the archive would never rotate again anywhere) and not everybody (the
// deletion channel). This is the only test that observes the cloud manifest change
// end to end, since the sidecar is in the pre-Go KEY=VALUE form the JSON-only reader
// could never parse.
func TestCloudRetentionPrunesALegacyArchiveThatNamesThisHost(t *testing.T) {
	deletes := cloudLegacyFixture(t, "hostB", "hostB", "COMPRESSION_TYPE=gzip\nHOSTNAME=hostB\n")

	if !deletedAnyMatching(deletes, legacyCloudArchive) {
		t.Errorf("retention on hostB left a pre-Go archive its own sidecar attributes to hostB. Nothing else will ever delete it, so the remote grows without bound: %q", deletes)
	}
	if !deletedAnyMatching(deletes, "hostB-backup-20250103") {
		t.Errorf("retention pruned nothing of this host's own: %q", deletes)
	}
	if deletedAnyMatching(deletes, "hostB-backup-20250104") {
		t.Errorf("retention deleted the newest archive, which the keep limit protects: %q", deletes)
	}
}
