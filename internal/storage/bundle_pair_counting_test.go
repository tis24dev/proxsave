package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// Characterization of PARKED bug 6 (diagnostics/fable-check.md, maintainer call
// 2026-09-02): when the SAME archive exists in both forms - standalone and
// .bundle.tar - the two gates behave very differently, and only one of them is
// a design.
//
// The both-forms state is not a normal run's product: bundling removes the raw
// files after building the bundle, and only a removeAssociatedFiles failure
// right after bundling (backup_run_phases.go, Warning "Failed to remove raw
// files after bundling") leaves the pair behind. Nobody has ever reported it in
// the wild, which is why the OFF-side behavior is parked, not fixed.
//
// These tests exist so the state stays MEASURED: if either side changes, it
// must change consciously, and if the pair ever shows up in a real report the
// fix flips the OFF-side assertions.

func seedBundlePair(t *testing.T) (dir, standalone, bundle string) {
	t.Helper()
	dir = t.TempDir()
	standalone = filepath.Join(dir, "host-backup-20260101-120000.tar.zst")
	bundle = standalone + ".bundle.tar"
	for _, p := range []string{standalone, bundle} {
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return dir, standalone, bundle
}

// The default everyone runs: BUNDLE_ASSOCIATED_FILES=true. The pair collapses
// to ONE logical backup (the standalone is skipped because its bundle exists),
// which is exactly why no user has ever seen a double count.
func TestBundlePairCountsOnceWithBundlingOn(t *testing.T) {
	dir, _, bundle := seedBundlePair(t)
	ls, err := NewLocalStorage(&config.Config{BackupPath: dir, BundleAssociatedFiles: true, MaxLocalBackups: 7}, newTestLogger(), "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := ls.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].BackupFile != bundle {
		t.Fatalf("with bundling ON the pair must collapse to the bundle alone, got %d entries: %+v", len(list), list)
	}
}

// PARKED bug 6, pinned as it stands today: with bundling OFF nothing collapses
// the pair, so List reports TWO backups where one exists on disk - while Delete
// of EITHER entry removes both forms (buildBackupCandidatePaths always includes
// the bundle). Simple retention counting entries therefore keeps fewer real
// backups than MAX_LOCAL_BACKUPS promises whenever pairs are present. The
// secondary backend carries the same two gates (secondary.go List/Delete).
func TestBundlePairDoubleCountWithBundlingOffIsTheParkedState(t *testing.T) {
	dir, standalone, bundle := seedBundlePair(t)
	ls, err := NewLocalStorage(&config.Config{BackupPath: dir, BundleAssociatedFiles: false, MaxLocalBackups: 7}, newTestLogger(), "")
	if err != nil {
		t.Fatal(err)
	}
	list, err := ls.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("the parked state changed: List = %d entries, the pin expects today's double count of 2 - if this is a deliberate fix of bug 6, flip this test", len(list))
	}
	if err := ls.Delete(context.Background(), standalone); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, p := range []string{standalone, bundle} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("deleting one entry is expected to remove BOTH forms (that linkage is why the double count understates retention), %s survived", p)
		}
	}
}
