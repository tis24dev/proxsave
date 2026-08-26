package storage

import (
	"context"
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
// The foreign archive is the other half of the guarantee: widening ownership to
// this machine's own spellings must not widen it to a second machine that happens
// to share the short label.
func TestLocalApplyRetentionPrunesArchivesWrittenUnderTheRunFQDN(t *testing.T) {
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

	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
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
