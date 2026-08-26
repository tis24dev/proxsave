package storage

import (
	"context"
	"os"
	"path/filepath"
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
// The foreign archive pins the other half: recognising this machine's own
// spellings must not extend to a second machine sharing the short label.
func TestSecondaryApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	newest := filepath.Join(dir, "pve.home.arpa-backup-20250103-100000.tar.zst")
	oldest := filepath.Join(dir, "pve.home.arpa-backup-20250102-100000.tar.zst")
	foreign := filepath.Join(dir, "pve.siteb.example-backup-20250101-100000.tar.zst")

	// Each archive carries a .sha256 so List marks it Verified: an unverified entry
	// is inert for retention and would make this test pass for the wrong reason.
	base := time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC)
	for i, path := range []string{newest, oldest, foreign} {
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", filepath.Base(path), err)
		}
		if err := os.WriteFile(path+".sha256", []byte("h  archive\n"), 0o600); err != nil {
			t.Fatalf("seed sidecar for %s: %v", filepath.Base(path), err)
		}
		when := base.Add(-time.Duration(i) * 24 * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("chtimes %s: %v", filepath.Base(path), err)
		}
	}

	s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}

	if _, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Errorf("retention pruned nothing written under this run's own name: %s still present (stat err=%v)", filepath.Base(oldest), err)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Errorf("retention deleted the archive it was told to keep: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("retention deleted another machine's backup: %v", err)
	}
}
