package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestLocalApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN is the reported bug
// end to end on the backend every user has. The writer stamps what "hostname -f"
// returns (pve.home.arpa) into the archive name while retention reads the kernel
// short name (pve), so before the fix scoping left nothing owned and retention
// deleted nothing at all, run after run, while the directory grew without bound.
//
// It is built through NewLocalStorage rather than by setting the fields, and it
// asserts on the filesystem rather than on the struct, so it observes the whole
// chain: the run's own name reaches the field, AND ApplyRetention actually reads
// that field. TestNewLocalStorageRecordsWrittenHostnames only proves the first
// half; replacing l.hostAliases with nil at the applyRetentionHostScope call
// reinstates the bug and leaves that pin green, but turns this one red.
//
// Local is the ".metadata SIDECAR" fixture of the three: it is the only one whose
// attribution runs through LocalStorage.loadMetadata. Each of the five archives
// pins a different mechanism, and each dies under a different mutation:
//
//	1 ours by manifest, newest      -> kept (the keep limit is 1)
//	2 foreign NAME, own MANIFEST    -> deleted; only a manifest read can claim it
//	3 own NAME, foreign MANIFEST    -> spared; the data-loss DIRECTION of manifest
//	                                   precedence, and red under a first-label fold
//	4 own name, NO manifest         -> deleted by the filename TOKEN fallback
//	5 foreign name, no manifest     -> spared; the cross-host boundary
func TestLocalApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	// manifestHost "" seeds no .metadata, so attribution falls through to the
	// filename token. Every archive still carries a .sha256, because
	// backupHasCompletionSidecar accepts .manifest.json, .sha256 or a .bundle.tar
	// suffix but NOT .metadata, and an unverified entry is inert for retention:
	// without it the test would pass for the wrong reason.
	seeds := []struct {
		name         string
		when         time.Time
		manifestHost string
	}{
		{name: "pve.home.arpa-backup-20250105-100000.tar.zst", when: time.Date(2025, 1, 5, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "pve.siteb.example-backup-20250104-100000.tar.zst", when: time.Date(2025, 1, 4, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "pve.home.arpa-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), manifestHost: "pve.siteb.example"},
		{name: "pve.home.arpa-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)},
		{name: "pve.siteb.example-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
	}

	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		path := filepath.Join(dir, seed.name)
		paths[i] = path
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if err := os.WriteFile(path+".sha256", []byte("h  archive\n"), 0o600); err != nil {
			t.Fatalf("seed sidecar for %s: %v", seed.name, err)
		}
		if seed.manifestHost != "" {
			// created_at is set to the same instant as Chtimes: loadMetadata takes
			// the timestamp from the manifest and only falls back to ModTime when it
			// is zero, so letting the two disagree would make the ordering depend on
			// which source won.
			manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, seed.manifestHost, seed.when.Format(time.RFC3339))
			if err := os.WriteFile(path+".metadata", []byte(manifest), 0o600); err != nil {
				t.Fatalf("seed manifest for %s: %v", seed.name, err)
			}
		}
		if err := os.Chtimes(path, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}

	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("retention deleted the archive it was told to keep: %v", err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Errorf("retention spared %s: its manifest names this machine, so it is this machine's own work whatever its filename says, and it must rotate (stat err=%v)", filepath.Base(paths[1]), err)
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
		t.Errorf("deleted = %d, want 2 (the archive attributed by its manifest and the one attributed by its filename token); the count feeds the run summary and the retention report", deleted)
	}
}
