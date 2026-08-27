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

// seedBundledServerIDFixture writes the same fixture seedServerIDFixture writes, in
// the shape the SHIPPED default produces. With BUNDLE_ASSOCIATED_FILES=true the
// backup run removes the raw archive and both sidecars after bundling, so every
// archive on disk is a ".bundle.tar" whose manifest lives INSIDE the tar and there is
// no .metadata beside it to read. That is the ordinary layout of a local backup
// directory, and it is served by a different reader from the sidecar one.
//
// No .sha256 is written and none is needed: backupHasCompletionSidecar marks any
// bundle Verified by its suffix, because a bundle is only ever produced after the
// verify step and its sidecars.
func seedBundledServerIDFixture(t *testing.T, dir string, seeds []serverIDSeed) []string {
	t.Helper()
	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		path := filepath.Join(dir, seed.name+bundleSuffix)
		paths[i] = path
		// created_at matches the mtime set below for the same reason the sidecar
		// fixture matches them: loadMetadataFromBundle takes the timestamp from the
		// manifest and falls back to ModTime only when it is zero, so letting the two
		// disagree would make the ordering depend on which source won.
		manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q`, seed.manifestHost, seed.when.Format(time.RFC3339))
		if seed.manifestID != "" {
			manifest += fmt.Sprintf(`,"server_id":%q`, seed.manifestID)
		}
		manifest += "}"
		writeRetentionTestBundle(t, path, manifest)
		if err := os.Chtimes(path, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}
	return paths
}

// TestLocalRetentionAdoptsBundledArchivesWrittenUnderALostFQDN is discussion #292 on
// the layout the shipped configuration actually produces. It is the twin of
// TestLocalRetentionAdoptsArchivesWrittenUnderALostFQDN, and the ONE difference is
// where the manifest lives: beside the archive there, inside the bundle tar here.
//
// That one difference is a whole separate reader. LocalStorage.loadMetadata branches
// on the ".bundle.tar" suffix long before it looks at any manifest, and the bundle
// branch maps its own types.BackupMetadata in loadMetadataFromBundle. So the sidecar
// test says nothing at all about this path, and with BUNDLE_ASSOCIATED_FILES shipping
// as true this is the path most installations are on: the reporter's own archive in
// discussion #292 was a bundle.
//
// The fixture is run twice, with and without an identity recorded inside the bundles,
// and the two outcomes are compared. The second run is the control that makes the
// first one evidence about the identity: the archives name a spelling this host does
// not answer to, so nothing but the identity inside the bundle can put them back into
// rotation, and without it retention must behave exactly as it did before the field
// existed.
func TestLocalRetentionAdoptsBundledArchivesWrittenUnderALostFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	type outcome struct {
		deleted   int
		survivors []string
		log       string
		warnings  int64
	}

	run := func(t *testing.T, manifestID string) outcome {
		t.Helper()
		dir := t.TempDir()
		paths := seedBundledServerIDFixture(t, dir, lostFQDNSeeds(manifestID))

		logger, buf := newRecordingRetentionLogger()
		// The written hostname is deliberately empty: this machine can no longer
		// resolve the name the bundles were written under, which is the whole point
		// of the fixture. BundleAssociatedFiles records which installation shape this
		// fixture stands for and nothing more: the read path never consults it.
		// loadMetadata branches on the ".bundle.tar" suffix of the file it found, so
		// removing the field here changes no behaviour and no assertion.
		l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID, BundleAssociatedFiles: true}, logger, "")
		if err != nil {
			t.Fatalf("NewLocalStorage: %v", err)
		}

		deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
		if err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}

		var survivors []string
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				survivors = append(survivors, filepath.Base(path))
			}
		}
		return outcome{deleted: deleted, survivors: survivors, log: buf.String(), warnings: logger.WarningCount()}
	}

	with := run(t, ourServerID)
	if want := []string{"pve.home.arpa-backup-20250103-100000.tar.zst" + bundleSuffix}; strings.Join(with.survivors, ",") != strings.Join(want, ",") {
		t.Errorf("bundled archives left: %v, want %v. They carry this host's own server identity inside the bundle, they name a spelling this host lost, and this host answers to their short label and to no other spelling of it, so they are this machine's own work and must rotate again", with.survivors, want)
	}
	if with.deleted != 2 {
		t.Errorf("deleted = %d, want 2; the count feeds the run summary and the retention report", with.deleted)
	}
	if !strings.Contains(with.log, "back into rotation") {
		t.Errorf("nothing said the bundles had been brought back. The recovery is invisible to the operator otherwise. Log: %s", with.log)
	}
	if with.warnings != 0 {
		t.Errorf("%d WARNING line(s) for a location this host now fully manages; every one of them promotes an otherwise clean run to exit 1. Log: %s", with.warnings, with.log)
	}

	without := run(t, "")
	if len(without.survivors) != 3 || without.deleted != 0 {
		t.Errorf("without an identity inside the bundles, retention deleted %d and left %v. Every bundle written before this change records no identity, and from here they are indistinguishable from a second machine's work, so they must be classified exactly as they were before the field existed", without.deleted, without.survivors)
	}
	if !strings.Contains(without.log, "different spelling") {
		t.Errorf("the pre-existing spelling-mismatch warning stopped firing for a population nothing has claimed. The operator's only signal that rotation has stopped is that line. Log: %s", without.log)
	}
}
