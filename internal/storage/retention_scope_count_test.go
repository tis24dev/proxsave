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

// seedNamed lays out one archive with an optional manifest naming its writer.
func seedNamed(t *testing.T, dir, name string, when time.Time, manifestHost string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("archive"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := os.WriteFile(p+".sha256", []byte("h  archive\n"), 0o600); err != nil {
		t.Fatalf("seed sidecar for %s: %v", name, err)
	}
	if manifestHost != "" {
		manifest := fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, manifestHost, when.Format(time.RFC3339))
		if err := os.WriteFile(p+".metadata", []byte(manifest), 0o600); err != nil {
			t.Fatalf("seed manifest for %s: %v", name, err)
		}
	}
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
}

// TestReportedCountCoversArchivesNoHostManages is the regression pin for the whole
// point of the second return of applyRetentionHostScope.
//
// A count of archives retention will actually prune is a FALSE ALL-CLEAR on the two
// populations that grow without bound, and both are documented failure modes:
// docs/TROUBLESHOOTING.md cause 2 (this machine's own work written under a name it
// stopped resolving) and cause 3 (pre-Go archives nothing can attribute). Neither
// will ever be pruned by anyone, so if this number leaves them out, the one figure
// an operator watches says "within the limit" while the directory fills.
//
// Archives carrying a DIFFERENT machine's name are excluded, and that exclusion is
// the fix this file exists for. The two rules pull in opposite directions on
// purpose, and the four rows below are the four ways they interact.
func TestReportedCountCoversArchivesNoHostManages(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2025, 1, n, 10, 0, 0, 0, time.UTC) }

	cases := []struct {
		name     string
		resolves string
		written  string
		seeds    []struct {
			file     string
			manifest string
		}
		wantReported int
		wantOnDisk   int
		why          string
	}{
		{
			name: "pre-Go archives nobody can attribute still count",
			// The whole directory predates the Go rewrite. Nothing names a writer, so
			// no host claims these and no host ever deletes them.
			resolves: "pve", written: "pve",
			seeds: []struct{ file, manifest string }{
				{"proxmox-backup-20250101-100000.tar.zst", ""},
				{"proxmox-backup-20250102-100000.tar.zst", ""},
				{"proxmox-backup-20250103-100000.tar.zst", ""},
			},
			wantReported: 3, wantOnDisk: 3,
			why: "reporting 0 here tells an operator holding three restorable archives that the location is empty",
		},
		{
			name: "the ordinary upgrade: legacy backlog beside new archives",
			// The common shape after upgrading from the Bash version, and NOT a shared
			// location: one machine, its own private directory.
			resolves: "pve", written: "pve",
			seeds: []struct{ file, manifest string }{
				{"proxmox-backup-20250101-100000.tar.zst", ""},
				{"proxmox-backup-20250102-100000.tar.zst", ""},
				{"pve-backup-20250103-100000.tar.zst", "pve"},
			},
			wantReported: 3, wantOnDisk: 3,
			why: "reporting 1 of 3 says 'within the limit' while the two legacy archives grow for ever",
		},
		{
			name: "this host's own work under a spelling it no longer resolves",
			// docs/TROUBLESHOOTING.md cause 2, which is discussion #292 itself. The
			// writer stamped the FQDN; this run resolves only the short name, so the
			// archives file as contended and retention leaves them alone.
			resolves: "pve", written: "pve",
			seeds: []struct{ file, manifest string }{
				{"pve.home.arpa-backup-20250101-100000.tar.zst", "pve.home.arpa"},
				{"pve.home.arpa-backup-20250102-100000.tar.zst", "pve.home.arpa"},
			},
			wantReported: 2, wantOnDisk: 2,
			why: "these are this machine's own archives and they have stopped rotating; hiding them removes the only signal that says so",
		},
		{
			name: "a second machine's archives are not this host's to report",
			// The case the scoping was introduced for. These belong to a named other
			// host, which prunes them and reports them itself.
			resolves: "pve", written: "pve",
			seeds: []struct{ file, manifest string }{
				{"pve-backup-20250103-100000.tar.zst", "pve"},
				{"nas.siteb.example-backup-20250101-100000.tar.zst", "nas.siteb.example"},
				{"nas.siteb.example-backup-20250102-100000.tar.zst", "nas.siteb.example"},
			},
			wantReported: 1, wantOnDisk: 3,
			why: "counting the other machine's two archives against this host's limit is the 40/7 the fix removed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := retentionHostname
			retentionHostname = func() (string, error) { return tc.resolves, nil }
			defer func() { retentionHostname = original }()

			dir := t.TempDir()
			for i, s := range tc.seeds {
				seedNamed(t, dir, s.file, day(i+1), s.manifest)
			}

			l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), tc.written)
			if err != nil {
				t.Fatalf("NewLocalStorage: %v", err)
			}
			// A limit above the fixture size, so nothing is deleted and the number
			// under test is the one the notification renders on a steady run.
			if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
				t.Fatalf("ApplyRetention: %v", err)
			}

			summary := l.LastRetentionSummary()
			if !summary.ScopeValid {
				t.Fatal("no ownership scope published, so the reporter falls back to the unscoped listing")
			}
			if summary.Owned != tc.wantReported {
				t.Fatalf("reported %d, want %d: %s", summary.Owned, tc.wantReported, tc.why)
			}

			stats, err := l.GetStats(context.Background())
			if err != nil {
				t.Fatalf("GetStats: %v", err)
			}
			if stats.TotalBackups != tc.wantOnDisk {
				t.Fatalf("GetStats().TotalBackups = %d, want %d: that figure sits beside the free-space numbers and must keep describing the whole location", stats.TotalBackups, tc.wantOnDisk)
			}
		})
	}
}
