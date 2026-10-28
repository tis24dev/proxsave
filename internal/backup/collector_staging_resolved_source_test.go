package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A CUSTOM_BACKUP_PATHS entry whose symlinks resolve into the staging tree
// (/alias/run/etc with /alias -> the staging tree's parent) has nothing in common with
// tempDir as text, and below the staging root the inode check does not match either:
// the archive's own files were copied into the archive again. The source is compared
// with its symlinks resolved; entries outside the staging tree are still collected.
func TestCustomPathResolvingIntoTheStagingTreeIsSkipped(t *testing.T) {
	base := t.TempDir()
	staging := filepath.Join(base, "tmp", "proxsave", "run")
	if err := os.MkdirAll(filepath.Join(staging, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "etc", "staged.conf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "tmp", "proxsave", "kept.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(base, "tmp", "proxsave"), alias); err != nil {
		t.Fatal(err)
	}
	dirEntry := filepath.Join(alias, "run", "etc")
	fileEntry := filepath.Join(alias, "run", "etc", "staged.conf")
	keptEntry := filepath.Join(alias, "kept.txt")

	cfg := GetDefaultCollectorConfig()
	cfg.CustomBackupPaths = []string{dirEntry, fileEntry, keptEntry}
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, staging, types.ProxmoxVE, false, CollectorDeps{
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})
	if err := c.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths: %v", err)
	}
	for _, entry := range []string{dirEntry, fileEntry} {
		copied := filepath.Join(staging, strings.TrimPrefix(entry, "/"))
		if _, err := os.Lstat(copied); err == nil {
			t.Errorf("%s resolves into the staging tree and was copied into it at %s", entry, copied)
		}
	}
	kept := filepath.Join(staging, strings.TrimPrefix(keptEntry, "/"))
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the entry outside the staging tree was not collected: %v", err)
	}
}
