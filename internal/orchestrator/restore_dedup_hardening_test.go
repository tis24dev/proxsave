package orchestrator

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// openErrFS delegates to osFS but forces Open to fail with a non-not-exist error, to
// exercise the strict manifest Open-error guard (a transient EACCES/EIO, not ENOENT).
type openErrFS struct {
	osFS
	err error
}

func (f openErrFS) Open(string) (*os.File, error) { return nil, f.err }

// F-05-02: on the strict/staged path a duplicate whose canonical is missing from
// the archive must make materialization FAIL (refuse to apply a dangling-link tree)
// and KEEP the manifest; best-effort keeps the symlink and returns nil.
func TestMaterializeDedupStrictFailsOnMissingCanonical(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	newCase := func(t *testing.T) (archive, destRoot string) {
		root := t.TempDir()
		archive = writeTarArchiveForTest(t, root, map[string]string{"unrelated.cfg": "x"})
		destRoot = t.TempDir()
		if err := os.MkdirAll(filepath.Join(destRoot, "b"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../a/one.cfg", filepath.Join(destRoot, "b", "two.cfg")); err != nil {
			t.Fatal(err)
		}
		writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "b/two.cfg", Mode: 0o640}})
		return archive, destRoot
	}
	manifestPath := func(destRoot string) string {
		return filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
	}
	logger := logging.New(types.LogLevelError, false)

	// strict=true: error + manifest kept.
	archive, destRoot := newCase(t)
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, true, nil); err == nil {
		t.Fatal("strict materialization must fail when a canonical is missing from the archive")
	}
	if _, err := os.Stat(manifestPath(destRoot)); err != nil {
		t.Fatalf("strict failure must keep the manifest, stat err=%v", err)
	}

	// strict=false: nil, symlink kept (existing best-effort behavior).
	archive, destRoot = newCase(t)
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false, nil); err != nil {
		t.Fatalf("best-effort must tolerate a missing canonical, got %v", err)
	}
	if info, err := os.Lstat(filepath.Join(destRoot, "b", "two.cfg")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("best-effort must keep the symlink, info=%v err=%v", info, err)
	}
}

// F-05-03: an oversized manifest is refused (strict: error+kept; best-effort:
// skipped) and never fully allocated.
func TestMaterializeDedupManifestSizeCap(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })
	origCap := maxDedupManifestBytes
	maxDedupManifestBytes = 32
	t.Cleanup(func() { maxDedupManifestBytes = origCap })

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "payload"})
	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	// A manifest larger than the 32-byte test cap.
	entries := make([]backup.DedupManifestEntry, 0, 8)
	for i := 0; i < 8; i++ {
		entries = append(entries, backup.DedupManifestEntry{Path: "a/two.cfg", Mode: 0o640})
	}
	writeDedupManifestForTest(t, destRoot, entries)
	manifest := filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
	logger := logging.New(types.LogLevelError, false)

	// strict: error, manifest kept, symlink NOT materialized.
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, true, nil); err == nil {
		t.Fatal("strict must refuse an oversized manifest")
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("strict must keep the oversized manifest, err=%v", err)
	}
	if info, _ := os.Lstat(filepath.Join(destRoot, "a", "two.cfg")); info == nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("oversized manifest must not materialize anything")
	}
}

// F-05-03: a canonical larger than the cap leaves its duplicate as a link (counted
// missing), never allocating the whole entry.
func TestMaterializeDedupCanonicalSizeCap(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })
	origCap := maxDedupCanonicalBytes
	maxDedupCanonicalBytes = 8
	t.Cleanup(func() { maxDedupCanonicalBytes = origCap })

	root := t.TempDir()
	// Canonical content is 20 bytes > 8-byte test cap.
	archive := writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "0123456789abcdefghij"})
	destRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "a/two.cfg", Mode: 0o640}})
	logger := logging.New(types.LogLevelError, false)

	// best-effort: symlink kept (canonical treated as missing).
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false, nil); err != nil {
		t.Fatalf("best-effort over-cap canonical should not error: %v", err)
	}
	if info, err := os.Lstat(filepath.Join(destRoot, "a", "two.cfg")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("over-cap canonical must leave the duplicate as a link, info=%v err=%v", info, err)
	}

	// strict: same over-cap canonical -> missing>0 -> error (F-05-02 interplay).
	destRoot2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(destRoot2, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.cfg", filepath.Join(destRoot2, "a", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot2, []backup.DedupManifestEntry{{Path: "a/two.cfg", Mode: 0o640}})
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot2, logger, true, nil); err == nil {
		t.Fatal("strict over-cap canonical must fail closed")
	}
}

// F-05-01: in a selective restore, a manifest entry whose path was NOT extracted
// this run (out of the selected scope, or a pre-existing live symlink) must be left
// UNTOUCHED, not atomically replaced with archive content.
// F-05-01 (full restore): the dedup scope gate must be active on full restore too,
// not only selective. processRestoreArchiveEntries must return a NON-nil extractedSet
// even with no categories, so materializeDedupSymlinks never clobbers a pre-existing
// live symlink an out-of-scope/malicious manifest entry points at (worst case: /).
func TestProcessRestoreArchiveEntries_FullRestoreGatesDedup(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte("x")
	if err := tw.WriteHeader(&tar.Header{Name: "foo.cfg", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	opts := restoreArchiveOptions{
		destRoot:   t.TempDir(),
		logger:     logging.New(types.LogLevelError, false),
		categories: nil, // full restore
	}
	extractionLog := newRestoreExtractionLog(opts)
	defer extractionLog.close()

	_, extractedSet, err := processRestoreArchiveEntries(context.Background(), tar.NewReader(&buf), opts, extractionLog)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if extractedSet == nil {
		t.Fatal("full restore must build a non-nil extractedSet so the F-05-01 dedup scope gate is active")
	}
	if !extractedSet[dedupCleanArchivePath("foo.cfg")] {
		t.Fatalf("extracted entry not recorded in set: %v", extractedSet)
	}
}

// F-05-02 sibling: a manifest that EXISTS but cannot be opened (non not-exist error,
// e.g. EACCES/EIO) must fail closed on strict too, not just the read/parse legs. A
// genuinely-absent manifest (not-exist) still returns nil (no dedup to materialize).
func TestMaterializeDedupStrictFailsOnUnreadableManifest(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	logger := logging.New(types.LogLevelError, false)

	newCase := func(t *testing.T) (archive, destRoot string) {
		restoreFS = osFS{}
		root := t.TempDir()
		archive = writeTarArchiveForTest(t, root, map[string]string{"unrelated.cfg": "x"})
		destRoot = t.TempDir()
		writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{{Path: "b/two.cfg", Mode: 0o640}})
		return archive, destRoot
	}

	// strict=true: a non-not-exist Open error fails closed.
	archive, destRoot := newCase(t)
	restoreFS = openErrFS{err: errors.New("permission denied")}
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, true, nil); err == nil {
		t.Fatal("strict materialization must fail when the manifest exists but cannot be opened")
	}

	// strict=false: tolerate (nil).
	archive, destRoot = newCase(t)
	restoreFS = openErrFS{err: errors.New("permission denied")}
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false, nil); err != nil {
		t.Fatalf("best-effort must tolerate an unreadable manifest, got %v", err)
	}
}

// F-05-02 sibling: a corrupt (unparseable) dedup manifest must ALSO fail closed on
// the strict/staged path (refuse to apply a partial restore) and keep the manifest,
// mirroring the oversize guard; best-effort removes it and returns nil.
func TestMaterializeDedupStrictFailsOnCorruptManifest(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	newCase := func(t *testing.T) (archive, destRoot string) {
		root := t.TempDir()
		archive = writeTarArchiveForTest(t, root, map[string]string{"unrelated.cfg": "x"})
		destRoot = t.TempDir()
		mp := filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
		if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mp, []byte("{ this is not valid json"), 0o640); err != nil {
			t.Fatal(err)
		}
		return archive, destRoot
	}
	manifestPath := func(destRoot string) string {
		return filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
	}
	logger := logging.New(types.LogLevelError, false)

	// strict=true: error + manifest kept.
	archive, destRoot := newCase(t)
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, true, nil); err == nil {
		t.Fatal("strict materialization must fail on a corrupt dedup manifest")
	}
	if _, err := os.Stat(manifestPath(destRoot)); err != nil {
		t.Fatalf("strict failure must keep the manifest, stat err=%v", err)
	}

	// strict=false: nil, manifest removed (existing best-effort behavior).
	archive, destRoot = newCase(t)
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false, nil); err != nil {
		t.Fatalf("best-effort must tolerate a corrupt manifest, got %v", err)
	}
	if _, err := os.Stat(manifestPath(destRoot)); !os.IsNotExist(err) {
		t.Fatalf("best-effort must remove the corrupt manifest, stat err=%v", err)
	}
}

func TestMaterializeDedupSelectiveScopeGate(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	root := t.TempDir()
	archive := writeTarArchiveForTest(t, root, map[string]string{
		"a/one.cfg":    "payload", // canonical of the in-scope duplicate
		"x/secret.cfg": "EVIL",    // canonical the out-of-scope entry points at
	})

	destRoot := t.TempDir()
	// In-scope duplicate, extracted this run.
	if err := os.MkdirAll(filepath.Join(destRoot, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../a/one.cfg", filepath.Join(destRoot, "b", "two.cfg")); err != nil {
		t.Fatal(err)
	}
	// Out-of-scope pre-existing live symlink NOT extracted this run.
	if err := os.Symlink("x/secret.cfg", filepath.Join(destRoot, "victim.cfg")); err != nil {
		t.Fatal(err)
	}
	writeDedupManifestForTest(t, destRoot, []backup.DedupManifestEntry{
		{Path: "b/two.cfg", Mode: 0o640},
		{Path: "victim.cfg", Mode: 0o640},
	})

	extracted := map[string]bool{"b/two.cfg": true} // selective: only the in-scope name
	logger := logging.New(types.LogLevelError, false)
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false, extracted); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// In-scope duplicate rebuilt.
	if info, err := os.Lstat(filepath.Join(destRoot, "b", "two.cfg")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("in-scope duplicate must be materialized, info=%v err=%v", info, err)
	}
	// Out-of-scope symlink untouched (still a symlink, still pointing where it did).
	info, err := os.Lstat(filepath.Join(destRoot, "victim.cfg"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("out-of-scope entry must be left as a symlink, info=%v err=%v", info, err)
	}
	if tgt, _ := os.Readlink(filepath.Join(destRoot, "victim.cfg")); tgt != "x/secret.cfg" {
		t.Fatalf("out-of-scope symlink target changed to %q", tgt)
	}
}
