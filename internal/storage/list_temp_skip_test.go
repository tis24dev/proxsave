package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// backupPaths maps backup metadata to their base file names for failure messages.
func backupPaths(bs []*types.BackupMetadata) []string {
	names := make([]string, 0, len(bs))
	for _, b := range bs {
		names = append(names, filepath.Base(b.BackupFile))
	}
	return names
}

// List must ignore in-flight temp (.tmp-...) and partial (<name>.partial) files
// so they are never counted as backups.
func TestListSkipsTempAndPartial(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "host-backup-20260717.tar.zst")
	partial := filepath.Join(dir, "host-backup-20260717.tar.zst.partial")
	temp := filepath.Join(dir, ".tmp-host-backup-20260717.tar.zst-123")
	for _, p := range []string{real, partial, temp} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", p, err)
		}
	}
	// Completion sidecar so the real backup lists as verified.
	if err := os.WriteFile(real+".sha256", []byte("h  x\n"), 0o600); err != nil {
		t.Fatalf("seed sha256: %v", err)
	}

	cfg := &config.Config{BackupPath: dir, BundleAssociatedFiles: false}
	local, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	got, err := local.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || filepath.Base(got[0].BackupFile) != "host-backup-20260717.tar.zst" {
		t.Fatalf("List returned %d entries %v; want only the real backup", len(got), backupPaths(got))
	}
}
