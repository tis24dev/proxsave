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

// seedSharedLocation lays out a directory shared by two ProxSave hosts, which is
// the documented secondary layout and the shipped cloud default
// (CLOUD_REMOTE_PATH=/proxsave/backup). Three archives belong to this machine,
// written under the FQDN its "hostname -f" returns, and two belong to a second
// machine. Every archive carries a .sha256, because an entry with no completion
// sidecar is inert for retention (partitionRetentionEligible) and the counts under
// test would then be right for the wrong reason.
func seedSharedLocation(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	seeds := []struct {
		name         string
		when         time.Time
		manifestHost string
	}{
		{name: "pve.home.arpa-backup-20250105-100000.tar.zst", when: time.Date(2025, 1, 5, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "pve.home.arpa-backup-20250104-100000.tar.zst", when: time.Date(2025, 1, 4, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "pve.home.arpa-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), manifestHost: "pve.home.arpa"},
		{name: "nas.siteb.example-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), manifestHost: "nas.siteb.example"},
		{name: "nas.siteb.example-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), manifestHost: "nas.siteb.example"},
	}

	for _, seed := range seeds {
		path := filepath.Join(dir, seed.name)
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if err := os.WriteFile(path+".sha256", []byte("h  archive\n"), 0o600); err != nil {
			t.Fatalf("seed sidecar for %s: %v", seed.name, err)
		}
		manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, seed.manifestHost, seed.when.Format(time.RFC3339))
		if err := os.WriteFile(path+".metadata", []byte(manifest), 0o600); err != nil {
			t.Fatalf("seed manifest for %s: %v", seed.name, err)
		}
		if err := os.Chtimes(path, seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}
	return dir
}

// TestRetentionPublishesTheOwnedCountWhenNothingNeedsDeleting pins the steady
// state, which is the common healthy run and the one that used to record nothing at
// all: ApplyRetention returns early once the listing is within the limit, so the
// summary stayed at its zero value and the reporter fell back to counting every
// host's archives (discussion #292).
//
// The pair of assertions is the whole point. GetStats must keep counting all five,
// because TotalSize and the free-space figures beside it describe the location, and
// the retention summary must report three, because that is what this host owns and
// what its limit is compared against. Collapsing the two into one number in either
// direction is the bug, in one direction or the other.
func TestRetentionPublishesTheOwnedCountWhenNothingNeedsDeleting(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := seedSharedLocation(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted %d archive(s) under a limit of 10; the fixture holds 3 owned and this pass must delete nothing", deleted)
	}

	summary := l.LastRetentionSummary()
	if !summary.ScopeValid {
		t.Fatal("ApplyRetention returned without publishing an ownership scope, so the reporter falls back to the unscoped listing and the summary reads every host's archives against this host's limit (discussion #292)")
	}
	if summary.Owned != 3 {
		t.Fatalf("Owned = %d, want 3: this host wrote three of the five archives in this shared directory", summary.Owned)
	}

	stats, err := l.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalBackups != 5 {
		t.Fatalf("GetStats().TotalBackups = %d, want 5: this figure sits beside TotalSize and the free-space numbers, which describe the whole location, so it must keep counting every archive present", stats.TotalBackups)
	}
}

// TestRetentionOwnedCountIsNetOfWhatThePassDeleted pins that the published number
// describes the location AFTER the pass, not before it. applyStorageStats runs
// downstream of retention in StorageAdapter.Sync, so a pre-deletion count would
// report archives that no longer exist.
func TestRetentionOwnedCountIsNetOfWhatThePassDeleted(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := seedSharedLocation(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1: three owned archives against a limit of 2. If this is 3, scoping is not being applied and the other host's archives were pruned too", deleted)
	}
	if summary := l.LastRetentionSummary(); summary.Owned != 2 {
		t.Fatalf("Owned = %d, want 2: the count must be net of this pass's deletions, because the summary is rendered after retention has run", summary.Owned)
	}
}

// TestRetentionPublishesNoScopeWhenTheHostCannotNameItself pins the one case where
// the scoped number is the more alarming lie. applyRetentionHostScope returns nil
// on an unnamed host and warns that retention will delete nothing, so publishing
// that as an owned count would print "0/7" beside a directory holding five
// archives. Leaving the scope invalid keeps the unscoped total, which is at least
// a number the operator can reconcile with what is on the disk.
func TestRetentionPublishesNoScopeWhenTheHostCannotNameItself(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "", nil }
	defer func() { retentionHostname = original }()

	dir := seedSharedLocation(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	summary := l.LastRetentionSummary()
	if summary.ScopeValid {
		t.Fatalf("an unnamed host published an ownership scope of %d; retention deleted nothing and owns nothing it can prove, so the reporter must fall back to the unscoped total rather than print 0 beside the limit", summary.Owned)
	}
}

// TestRetentionPublishesNoScopeWhenTheListingFails pins that an error path leaves
// no stale scope behind. The publication is deferred from above the first return
// precisely so the two error bails ahead of the scope call cannot leave a previous
// pass's number standing as if it were current.
func TestRetentionPublishesNoScopeWhenTheListingFails(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := seedSharedLocation(t)
	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "pve.home.arpa")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
		t.Fatalf("first ApplyRetention: %v", err)
	}
	if summary := l.LastRetentionSummary(); !summary.ScopeValid || summary.Owned != 3 {
		t.Fatalf("first pass published %+v, want a valid scope of 3", summary)
	}

	// A cancelled context bails before the listing, so the second pass learns
	// nothing about ownership and must say so rather than leave the first pass's 3
	// standing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.ApplyRetention(ctx, RetentionConfig{Policy: "simple", MaxBackups: 10}); err == nil {
		t.Fatal("ApplyRetention on a cancelled context returned no error; this test needs the bail above the scope call")
	}
	if summary := l.LastRetentionSummary(); summary.ScopeValid {
		t.Fatalf("a pass that bailed before scoping left a scope of %d standing; a stale count reported as current is the failure this publication exists to avoid", summary.Owned)
	}
}
