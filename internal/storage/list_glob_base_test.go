package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// BACKUP_PATH and SECONDARY_PATH are taken literally when the backups are listed: a
// '[' in the folder name was read as a pattern, so the listing came back empty (a
// balanced class) or failed (an unbalanced one) on every run, and retention never
// deleted anything.
func TestListTakesTheBackupFolderLiterally(t *testing.T) {
	for _, name := range []string{"backup[pve1]", "backup[x", "back*up?"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(dir, "node-backup-20261027-010000.tar.zst")
		if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive+".sha256", []byte("x  node-backup-20261027-010000.tar.zst\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		logger := logging.New(types.LogLevelError, false)
		ls, err := NewLocalStorage(&config.Config{BackupPath: dir}, logger, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := ls.List(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("local %q: listed %d, err %v; want the one archive", name, len(got), err)
		}
		ss, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dir}, logger, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err = ss.List(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("secondary %q: listed %d, err %v; want the one archive", name, len(got), err)
		}
	}
}
