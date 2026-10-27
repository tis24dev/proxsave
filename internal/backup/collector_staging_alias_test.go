package backup

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A CUSTOM_BACKUP_PATHS entry that reaches the staging tree through a symlink in the
// middle of its path (/scratch/proxsave with /scratch -> /tmp) has nothing in common
// with tempDir as text: the walk copied the staging tree into itself, level after
// level, until the path length limit. The staging directory is now recognised by its
// inode; the rest of the entry is still collected.
func TestCustomPathThroughAnIntermediateSymlinkSkipsTheStagingTree(t *testing.T) {
	for _, viaLink := range []bool{false, true} {
		base := t.TempDir()
		staging := filepath.Join(base, "tmp", "proxsave", "run")
		if err := os.MkdirAll(staging, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, "tmp", "proxsave", "kept.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		entry := filepath.Join(base, "tmp", "proxsave")
		if viaLink {
			if err := os.Symlink(filepath.Join(base, "tmp"), filepath.Join(base, "scratch")); err != nil {
				t.Fatal(err)
			}
			entry = filepath.Join(base, "scratch", "proxsave")
		}
		cfg := GetDefaultCollectorConfig()
		cfg.CustomBackupPaths = []string{entry}
		c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, staging, types.ProxmoxVE, false, CollectorDeps{
			DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
		})
		if err := c.collectCustomPaths(context.Background()); err != nil {
			t.Fatalf("viaLink=%v: collectCustomPaths: %v", viaLink, err)
		}
		nested := 0
		_ = filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && p != staging && filepath.Base(p) == "run" {
				nested++
			}
			return nil
		})
		if nested != 0 {
			t.Fatalf("viaLink=%v: the staging tree was copied into itself (%d nested copies)", viaLink, nested)
		}
		kept := filepath.Join(staging, strings.TrimPrefix(entry, "/"), "kept.txt")
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("viaLink=%v: the entry's own file was not collected: %v", viaLink, err)
		}
	}
}
