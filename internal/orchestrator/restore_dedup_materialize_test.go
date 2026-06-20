package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestIsDedupManifestEntry(t *testing.T) {
	cases := map[string]bool{
		"./" + backup.DedupManifestRelPath:                  true,
		backup.DedupManifestRelPath:                         true,
		"/" + backup.DedupManifestRelPath:                   true,
		"etc/pve/user.cfg":                                  false,
		"var/lib/proxsave-info/commands/pve/pve_users.json": false,
	}
	for in, want := range cases {
		if got := isDedupManifestEntry(in); got != want {
			t.Errorf("isDedupManifestEntry(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSkipRestoreArchiveEntryAlwaysExtractsDedupManifest(t *testing.T) {
	hdr := &tar.Header{Name: "./" + backup.DedupManifestRelPath}
	// Selective restore with a category the manifest path does not match: the dedup
	// manifest is force-extracted regardless so materialization can run (issue #70).
	opts := restoreArchiveOptions{categories: []Category{{ID: "pve_access_control"}}}
	var stats restoreExtractionStats
	if skipRestoreArchiveEntry(hdr, opts, true, &restoreExtractionLog{}, &stats) {
		t.Fatal("dedup manifest must never be skipped, even in selective restore")
	}
}

func TestMaterializeDedupSymlinksFullRestore(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "payload"})

	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destRoot, "a", "one.cfg"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "a/two.cfg", Mode: 0o640}})

	materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false))

	two := filepath.Join(destRoot, "a", "two.cfg")
	info, err := os.Lstat(two)
	if err != nil {
		t.Fatalf("lstat materialized file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected a/two.cfg to be a regular file after materialization, got symlink")
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected mode 0640, got %o", info.Mode().Perm())
	}
	if data, err := os.ReadFile(two); err != nil || string(data) != "payload" {
		t.Fatalf("materialized content mismatch: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))); !os.IsNotExist(err) {
		t.Fatalf("dedup manifest should be removed after materialization, stat err=%v", err)
	}
}

// TestMaterializeDedupCrossCategoryRebuildsFromArchive is the #70 regression guard:
// a selective restore that selects the symlinked DUPLICATE but not its canonical
// TARGET's category must rebuild the duplicate from the archive content, NOT delete
// the user-selected file.
func TestMaterializeDedupCrossCategoryRebuildsFromArchive(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	root := t.TempDir()
	// The archive holds the canonical a/one.cfg (category A, NOT selected).
	archive := writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "payload"})

	destRoot := t.TempDir()
	// Simulate selective extraction of ONLY category B: the duplicate symlink exists,
	// but its canonical a/one.cfg was not extracted.
	if err := os.MkdirAll(filepath.Join(destRoot, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a/one.cfg", filepath.Join(destRoot, "b", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "b/two.cfg", Mode: 0o640}})

	materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false))

	two := filepath.Join(destRoot, "b", "two.cfg")
	info, err := os.Lstat(two)
	if err != nil {
		t.Fatalf("selected duplicate must NOT be deleted on cross-category selective restore: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected b/two.cfg rebuilt as a regular file from the archive, got symlink")
	}
	if data, err := os.ReadFile(two); err != nil || string(data) != "payload" {
		t.Fatalf("rebuilt content mismatch: %q err=%v", data, err)
	}
}

// TestMaterializeDedupReturnsErrorWhenIncomplete is the #8 guard: when the archive
// scan cannot complete (here a canceled context with a duplicate still to rebuild),
// materializeDedupSymlinks must RETURN an error so a staged restore that cannot
// tolerate a partial result fails closed instead of applying it, and the manifest is
// kept for a retry.
func TestMaterializeDedupReturnsErrorWhenIncomplete(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "payload"})

	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a/one.cfg", filepath.Join(destRoot, "b", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "b/two.cfg", Mode: 0o640}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the archive scan: materialization cannot complete

	if err := materializeDedupSymlinks(ctx, archive, destRoot, logging.New(types.LogLevelError, false)); err == nil {
		t.Fatal("materializeDedupSymlinks must return an error when materialization is incomplete (canceled scan)")
	}
	// The manifest must be kept (not removed) so a re-run can finish.
	if _, statErr := os.Stat(filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))); statErr != nil {
		t.Fatalf("dedup manifest should be kept for retry on incomplete materialization, stat err=%v", statErr)
	}
}

// TestMaterializeDedupMissingCanonicalKeepsSymlink: if the canonical is genuinely
// absent from the archive (corrupt backup), the symlink is kept (no deletion of the
// user-selected file).
func TestMaterializeDedupMissingCanonicalKeepsSymlink(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{"unrelated.cfg": "x"})

	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a/one.cfg", filepath.Join(destRoot, "b", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "b/two.cfg", Mode: 0o640}})

	materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false))

	info, err := os.Lstat(filepath.Join(destRoot, "b", "two.cfg"))
	if err != nil {
		t.Fatalf("symlink must be kept when the canonical is missing from the archive, not deleted: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("expected the symlink to remain when the canonical is unavailable, got a regular file")
	}
}

// TestMaterializeDedupUsesArchiveNotStaleDisk guards the HIGH fast-path defect: even
// when a (stale) file exists on disk at the canonical path, the duplicate must be
// rebuilt from the ARCHIVE bytes, never from the on-disk/live content.
func TestMaterializeDedupUsesArchiveNotStaleDisk(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "FRESH-from-archive"})

	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A STALE canonical exists on disk (e.g. the live pre-restore file, or a failed
	// extraction left the old bytes).
	if err := os.WriteFile(filepath.Join(destRoot, "a", "one.cfg"), []byte("STALE-on-disk"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "a/two.cfg", Mode: 0o640}})

	materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false))

	data, err := os.ReadFile(filepath.Join(destRoot, "a", "two.cfg"))
	if err != nil {
		t.Fatalf("read materialized: %v", err)
	}
	if string(data) != "FRESH-from-archive" {
		t.Fatalf("duplicate must be rebuilt from the archive, not from stale disk content: got %q", data)
	}
}

func writeDedupManifestForTest(t *testing.T, destRoot string, entries []backup.DedupManifestEntry) {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeTarArchiveForTest writes a plain (uncompressed) .tar with the given regular
// files (archive-relative paths -> content) and returns its path.
func writeTarArchiveForTest(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	p := filepath.Join(dir, "backup.tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tw := tar.NewWriter(f)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     "./" + name,
			Mode:     0o640,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
