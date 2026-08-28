package storage

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestSecondaryApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN is the reported
// bug end to end on the secondary backend, which is one of the two places it was
// actually seen: the documented secondary layout is a NAS mount several hosts
// write into, so host scoping is load bearing here and a scope that recognises
// nothing prunes nothing.
//
// It is built through NewSecondaryStorage and asserts on the filesystem, so it
// observes the whole chain rather than the constructor alone: replacing
// s.hostAliases with nil at the applyRetentionHostScope call reinstates the bug
// and leaves TestNewSecondaryStorageRecordsWrittenHostnames green, but turns this
// one red.
//
// Secondary is the BUNDLE fixture of the three, and the only end to end coverage
// anywhere of manifestHostnameFromLocalArchive: its bundle branch is reachable
// only from secondary retention, and discussion #292's own archive was a bundle
// (pve.home.arpa-backup-20260825-192531.tar.xz.bundle.tar). Deleting the
// s.resolveRetentionOwners call turns this test red twice over, on archive 2 and
// on archive 3. Each of the five archives pins a different mechanism:
//
//	1 own name, newest              -> kept (the keep limit is 1)
//	2 foreign NAME, own IN-BUNDLE   -> deleted; only a bundle manifest read claims it
//	  manifest
//	3 own NAME, foreign .metadata   -> spared; the data-loss DIRECTION of manifest
//	                                   precedence, and red under a first-label fold
//	4 own name, no manifest         -> deleted by the filename TOKEN fallback
//	5 foreign name, no manifest     -> spared; the cross-host boundary
func TestSecondaryApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	// A .sha256 makes List mark an archive Verified: an unverified entry is inert
	// for retention and would make this test pass for the wrong reason. The bundle
	// needs none, because backupHasCompletionSidecar verifies any .bundle.tar by
	// its suffix (a bundle is only produced after verify plus sidecars).
	seeds := []struct {
		name string
		when time.Time
		// bundleHost, when set, makes this archive a .bundle.tar carrying its
		// manifest INSIDE. sidecarHost, when set, writes a .metadata beside it.
		// Neither set means no manifest at all, so attribution falls through to the
		// filename token.
		bundleHost  string
		sidecarHost string
	}{
		{name: "pve.home.arpa-backup-20250105-100000.tar.zst", when: time.Date(2025, 1, 5, 10, 0, 0, 0, time.UTC)},
		{name: "pve.siteb.example-backup-20250104-100000.tar.xz.bundle.tar", when: time.Date(2025, 1, 4, 10, 0, 0, 0, time.UTC), bundleHost: "pve.home.arpa"},
		{name: "pve.home.arpa-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), sidecarHost: "pve.siteb.example"},
		{name: "pve.home.arpa-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)},
		{name: "pve.siteb.example-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
	}

	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		path := filepath.Join(dir, seed.name)
		paths[i] = path
		if seed.bundleHost != "" {
			writeRetentionTestBundle(t, path, fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, seed.bundleHost, seed.when.Format(time.RFC3339)))
		} else {
			if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
				t.Fatalf("seed %s: %v", seed.name, err)
			}
			if err := os.WriteFile(path+".sha256", []byte("h  archive\n"), 0o600); err != nil {
				t.Fatalf("seed sidecar for %s: %v", seed.name, err)
			}
		}
		if seed.sidecarHost != "" {
			manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, seed.sidecarHost, seed.when.Format(time.RFC3339))
			if err := os.WriteFile(path+".metadata", []byte(manifest), 0o600); err != nil {
				t.Fatalf("seed manifest for %s: %v", seed.name, err)
			}
		}
		// Secondary's List never reads the manifest's created_at: it stats. So the
		// modification time is the only thing that orders this listing.
		if err := os.Chtimes(path, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}

	s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}

	deleted, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("retention deleted the archive it was told to keep: %v", err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Errorf("retention spared the bundle %s: the manifest inside it names this machine, so it is this machine's own work whatever its filename says, and it must rotate (stat err=%v)", filepath.Base(paths[1]), err)
	}
	if _, err := os.Stat(paths[2]); err != nil {
		t.Errorf("retention deleted %s: its filename carries this host's own spelling but its manifest names another machine, and the manifest is what decides. This is the direction in which manifest precedence destroys another host's backups: %v", filepath.Base(paths[2]), err)
	}
	if _, err := os.Stat(paths[3]); !os.IsNotExist(err) {
		t.Errorf("retention pruned nothing written under this run's own name: %s still present (stat err=%v)", filepath.Base(paths[3]), err)
	}
	if _, err := os.Stat(paths[4]); err != nil {
		t.Errorf("retention deleted another machine's backup: %v", err)
	}
	// Asserted last, so a real regression reports WHICH archive survived before it
	// reports a number.
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2 (the bundle attributed by the manifest inside it and the archive attributed by its filename token); the count feeds the run summary and the retention report", deleted)
	}
}

// writeRetentionTestBundle writes the minimal bundle manifestFromBundle can read: a
// tar holding one entry named <archive>.metadata. The entry name is load bearing,
// because manifestFromBundle computes the name it expects from the bundle's own
// path and skips every other entry. Every step fails the test loudly, so a refused
// open reads as a refused open rather than as a missing manifest.
func writeRetentionTestBundle(t *testing.T, bundlePath, manifestJSON string) {
	t.Helper()

	file, err := os.Create(bundlePath)
	if err != nil {
		t.Fatalf("create bundle %s: %v", filepath.Base(bundlePath), err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Fatalf("close bundle %s: %v", filepath.Base(bundlePath), err)
		}
	}()

	writer := tar.NewWriter(file)
	name := strings.TrimSuffix(filepath.Base(bundlePath), bundleSuffix) + ".metadata"
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(manifestJSON))}
	if err := writer.WriteHeader(hdr); err != nil {
		t.Fatalf("write bundle header %s: %v", name, err)
	}
	if _, err := writer.Write([]byte(manifestJSON)); err != nil {
		t.Fatalf("write bundle manifest %s: %v", name, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close bundle writer %s: %v", filepath.Base(bundlePath), err)
	}
}
