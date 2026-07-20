package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// With bundling disabled, deleting a backup must still remove an associated
// orphan .bundle.tar so old bundles do not accumulate.
func TestDeleteRemovesOrphanBundleWhenBundlingDisabled(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "host-backup-20260717.tar.zst")
	bundle := base + ".bundle.tar"
	for _, p := range []string{base, bundle} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}

	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: false}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if _, err := local.deleteBackupInternal(context.Background(), base); err != nil {
		t.Fatalf("deleteBackupInternal: %v", err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("orphan bundle must be deleted, stat err=%v", err)
	}
}
