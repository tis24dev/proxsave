package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// secondaryServerIDSeed describes one archive of the secondary fixture. The secondary
// backend lists with stat() only, so nothing here is read until retention asks: the
// manifest is opened by resolveRetentionOwners and by nothing else.
type secondaryServerIDSeed struct {
	name string
	when time.Time
	// bundled makes this archive a ".bundle.tar" carrying its manifest INSIDE the
	// tar, which is what BUNDLE_ASSOCIATED_FILES=true produces. Otherwise the
	// manifest is a ".metadata" beside the archive. Both shapes end up in a real
	// secondary directory, because the setting can be turned on or off between runs,
	// and manifestOwnerFromLocalArchive serves them on two different branches.
	bundled      bool
	manifestHost string
	manifestID   string
}

// seedSecondaryServerIDFixture writes the fixture and returns the archive paths in
// the order given. A .sha256 accompanies every non-bundled archive because
// backupHasCompletionSidecar does not accept a .metadata on its own, and an
// unverified entry is inert for retention: without it these assertions would pass for
// the wrong reason. A bundle needs none, being verified by its suffix.
func seedSecondaryServerIDFixture(t *testing.T, dir string, seeds []secondaryServerIDSeed) []string {
	t.Helper()
	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q`, seed.manifestHost, seed.when.Format(time.RFC3339))
		if seed.manifestID != "" {
			manifest += fmt.Sprintf(`,"server_id":%q`, seed.manifestID)
		}
		manifest += "}"

		path := filepath.Join(dir, seed.name)
		if seed.bundled {
			path += bundleSuffix
			writeRetentionTestBundle(t, path, manifest)
		} else {
			if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
				t.Fatalf("seed %s: %v", seed.name, err)
			}
			if err := os.WriteFile(path+".sha256", []byte("h  archive\n"), 0o600); err != nil {
				t.Fatalf("seed checksum for %s: %v", seed.name, err)
			}
			if err := os.WriteFile(path+".metadata", []byte(manifest), 0o600); err != nil {
				t.Fatalf("seed manifest for %s: %v", seed.name, err)
			}
		}
		paths[i] = path
		// Secondary's List never reads the manifest's created_at: it stats. So the
		// modification time is the only thing that orders this listing.
		if err := os.Chtimes(path, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}
	return paths
}

// TestSecondaryRetentionAdoptsArchivesWrittenUnderALostFQDN is discussion #292 on the
// backend where a shared location is the DOCUMENTED layout: the secondary path is a
// NAS mount several hosts write into, so an ownership signal that silently stops
// working here is the one most likely to be spent on somebody else's archives, and
// the one most likely to strand this host's own.
//
// The secondary backend reaches the identity by a route of its own. Its List stats
// and nothing more, deliberately, so both facts about a writer arrive later through
// resolveRetentionOwners and manifestOwnerFromLocalArchive. Neither of the local
// backend's two readers is involved, so neither of the local tests says anything
// about this path.
//
// The fixture mixes the two manifest shapes on purpose, because
// manifestOwnerFromLocalArchive returns the identity on two separate branches and a
// single-shape fixture would pin only one of them:
//
//	1 own lost FQDN, sidecar manifest, newest -> kept (the keep limit is 1)
//	2 own lost FQDN, manifest INSIDE a bundle -> adopted and deleted
//	3 own lost FQDN, sidecar manifest         -> adopted and deleted
//	4 another host, another identity          -> spared; the cross-host boundary,
//	                                             which adoption must never widen
//
// The whole fixture is then run again with no identity recorded anywhere, as the
// control that makes the first run evidence about the identity rather than about
// something that widened the hostname rule: this host does not answer to the name
// these archives carry, so without the identity nothing may be deleted at all.
func TestSecondaryRetentionAdoptsArchivesWrittenUnderALostFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	type outcome struct {
		deleted   int
		survivors []string
	}

	run := func(t *testing.T, manifestID string) outcome {
		t.Helper()
		dir := t.TempDir()
		paths := seedSecondaryServerIDFixture(t, dir, []secondaryServerIDSeed{
			{name: "pve.home.arpa-backup-20250104-100000.tar.zst", when: time.Date(2025, 1, 4, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa", manifestID: manifestID},
			{name: "pve.home.arpa-backup-20250103-100000.tar.xz", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), bundled: true, manifestHost: "pve.home.arpa", manifestID: manifestID},
			{name: "pve.home.arpa-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa", manifestID: manifestID},
			// Never this host's, under any rule: another short label entirely, and
			// another machine's identity beside it.
			{name: "pbs.home.arpa-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), manifestHost: "pbs.home.arpa", manifestID: anotherServerID},
		})

		// The written hostname is empty because this machine no longer resolves the
		// name its own archives were written under, which is the whole fixture.
		// BundleAssociatedFiles records the installation shape and nothing more:
		// manifestOwnerFromLocalArchive branches on the ".bundle.tar" suffix of each
		// file it was handed, never on this flag.
		cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: dir, ServerID: ourServerID, BundleAssociatedFiles: true}
		s, err := NewSecondaryStorage(cfg, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}

		deleted, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
		if err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}

		var survivors []string
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				survivors = append(survivors, filepath.Base(path))
			}
		}
		return outcome{deleted: deleted, survivors: survivors}
	}

	with := run(t, ourServerID)
	want := []string{
		"pve.home.arpa-backup-20250104-100000.tar.zst",
		"pbs.home.arpa-backup-20250101-100000.tar.zst",
	}
	if strings.Join(with.survivors, ",") != strings.Join(want, ",") {
		t.Errorf("secondary retention left %v, want %v.\nThe two surplus archives carry this host's own server identity, one in a sidecar and one inside a bundle, and name a spelling this host lost, so they are this machine's own work and must rotate again. The archive named for another host must be spared whatever it carries: on the shared NAS this location is documented to be, deleting it cannot be undone", with.survivors, want)
	}
	if with.deleted != 2 {
		t.Errorf("deleted = %d, want 2; the count feeds the run summary and the retention report", with.deleted)
	}

	without := run(t, "")
	if without.deleted != 0 || len(without.survivors) != 4 {
		t.Errorf("without an identity in the manifests, retention deleted %d and left %v, want 0 deleted and all four left. Every archive written before this change records no identity, and from a host that cannot resolve the name they carry they are indistinguishable from a second machine's work", without.deleted, without.survivors)
	}
}
