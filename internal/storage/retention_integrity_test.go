package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// A newest-MTIME but UNVERIFIED entry (no manifest/checksum) must never occupy a
// retention keep-slot nor be deleted: the older VERIFIED backup survives.
func TestSimpleRetentionKeepsVerifiedOverUnverified(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: false}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	valid := filepath.Join(dir, "valid-backup.tar.zst")
	partial := filepath.Join(dir, "partial-backup.tar.zst")
	for _, p := range []string{valid, partial} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}

	now := time.Now()
	backups := []*types.BackupMetadata{
		{BackupFile: partial, Timestamp: now, Verified: false},                 // newest, unverified
		{BackupFile: valid, Timestamp: now.Add(-24 * time.Hour), Verified: true}, // older, verified
	}

	// keep 1: naive retention would keep the newest (partial) and delete the valid one.
	deleted, err := local.applySimpleRetention(context.Background(), backups, 1)
	if err != nil {
		t.Fatalf("applySimpleRetention: %v", err)
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatalf("valid verified backup must survive: %v", err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("unverified entry must NOT be deleted (fail-safe): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted=%d; want 0 (unverified inert, single verified within limit)", deleted)
	}
}

// List must mark a backup Verified iff a completion sidecar (.manifest.json or
// .sha256) exists, so the retention gate has a truthful signal to act on.
func TestListMarksVerifiedFromSidecar(t *testing.T) {
	dir := t.TempDir()
	// Canonical pipeline names (<host>-backup-<id>.tar.*) so List's glob enumerates
	// them; a bare "<x>-backup.tar.zst" would not match "*-backup-*.tar*".
	verified := filepath.Join(dir, "node-backup-verified.tar.zst")
	bare := filepath.Join(dir, "node-backup-bare.tar.zst")
	for _, p := range []string{verified, bare} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	if err := os.WriteFile(verified+".sha256", []byte("h  x\n"), 0o600); err != nil {
		t.Fatalf("seed sha256: %v", err)
	}

	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: false}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	got, err := local.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]bool{}
	for _, b := range got {
		seen[filepath.Base(b.BackupFile)] = b.Verified
	}
	if !seen["node-backup-verified.tar.zst"] {
		t.Fatalf("backup with .sha256 must be Verified=true")
	}
	if seen["node-backup-bare.tar.zst"] {
		t.Fatalf("bare .tar with no sidecar must be Verified=false")
	}
}

// A verified entry whose Timestamp could not be determined (zero) must be
// excluded from deletion (fail-safe), never deleted as the "oldest".
func TestSimpleRetentionExcludesUndatableFromDelete(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: false}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	newest := filepath.Join(dir, "newest-backup.tar.zst")
	undatable := filepath.Join(dir, "undatable-backup.tar.zst")
	for _, p := range []string{newest, undatable} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}

	backups := []*types.BackupMetadata{
		{BackupFile: newest, Timestamp: time.Now(), Verified: true},
		{BackupFile: undatable, Timestamp: time.Time{}, Verified: true}, // zero timestamp
	}
	// keep 1: naive retention deletes the "oldest" = the zero-timestamp entry.
	if _, err := local.applySimpleRetention(context.Background(), backups, 1); err != nil {
		t.Fatalf("applySimpleRetention: %v", err)
	}
	if _, err := os.Stat(undatable); err != nil {
		t.Fatalf("undatable entry must NOT be deleted (fail-safe): %v", err)
	}
}
