package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

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
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, true); err == nil {
		t.Fatal("strict materialization must fail when a canonical is missing from the archive")
	}
	if _, err := os.Stat(manifestPath(destRoot)); err != nil {
		t.Fatalf("strict failure must keep the manifest, stat err=%v", err)
	}

	// strict=false: nil, symlink kept (existing best-effort behavior).
	archive, destRoot = newCase(t)
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false); err != nil {
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
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, true); err == nil {
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
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logger, false); err != nil {
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
	if err := materializeDedupSymlinks(context.Background(), archive, destRoot2, logger, true); err == nil {
		t.Fatal("strict over-cap canonical must fail closed")
	}
}
