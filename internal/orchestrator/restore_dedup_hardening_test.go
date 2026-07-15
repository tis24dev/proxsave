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
