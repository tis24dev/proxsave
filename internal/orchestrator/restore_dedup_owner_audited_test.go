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

// F07-04: a deduplicated file rebuilt by writeMaterializedFile must chown the fresh
// inode back to the source owner recorded in the manifest, mirroring the normal
// extracted-file path (restore_archive_entries.go). An old manifest with no uid/gid
// must NOT be force-chowned to 0:0 (backward compatible, best-effort).
//
// The manifest is written as raw JSON so the test exercises the on-disk contract
// (old manifests are simply JSON without uid/gid) and asserts behavior, not struct
// shape: before the fix no chown happens and the deduplicated file keeps the restore
// process owner.
func TestMaterializeDedupPreservesOwner(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	var chownCalled bool
	var gotUID, gotGID int
	origChown := atomicFileChown
	t.Cleanup(func() { atomicFileChown = origChown })
	atomicFileChown = func(f *os.File, uid, gid int) error {
		chownCalled = true
		gotUID, gotGID = uid, gid
		return nil
	}

	setup := func(t *testing.T, manifestJSON string) (archive, destRoot string) {
		t.Helper()
		root := t.TempDir()
		archive = writeTarArchiveForTest(t, root, map[string]string{"a/one.cfg": "payload"})

		destRoot = t.TempDir()
		if err := os.MkdirAll(filepath.Join(destRoot, "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destRoot, "a", "one.cfg"), []byte("payload"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("one.cfg", filepath.Join(destRoot, "a", "two.cfg")); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(destRoot, filepath.FromSlash(backup.DedupManifestRelPath))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(manifestJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		return archive, destRoot
	}

	t.Run("owner recorded chowns to source owner", func(t *testing.T) {
		chownCalled, gotUID, gotGID = false, -1, -1
		// mode 416 == 0o640
		archive, destRoot := setup(t, `[{"path":"a/two.cfg","mode":416,"uid":1000,"gid":1000}]`)
		if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false), false, nil); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		if !chownCalled {
			t.Fatal("atomicFileChown was not called; deduplicated file keeps the restore process owner instead of the source owner")
		}
		if gotUID != 1000 || gotGID != 1000 {
			t.Fatalf("chown args = (%d,%d); want source (1000,1000)", gotUID, gotGID)
		}
	})

	t.Run("old manifest without owner skips chown", func(t *testing.T) {
		chownCalled, gotUID, gotGID = false, -1, -1
		archive, destRoot := setup(t, `[{"path":"a/two.cfg","mode":416}]`)
		if err := materializeDedupSymlinks(context.Background(), archive, destRoot, logging.New(types.LogLevelError, false), false, nil); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		if chownCalled {
			t.Fatalf("atomicFileChown must NOT be called for a nil-owner (old) manifest entry; was called with (%d,%d)", gotUID, gotGID)
		}
	})
}
