package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The archive is the first file of a backup set; it must be born root-only, never
// opened to a group by the archiver (group read is SET_BACKUP_PERMISSIONS's call).
func TestCreateArchiveWritesRootOnlyArchive(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, compression := range []types.CompressionType{types.CompressionNone, types.CompressionGzip} {
		t.Run(string(compression), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "pve1-backup-20261002-101010.tar.partial")
			a := NewArchiver(logger, &ArchiverConfig{Compression: compression, CompressionLevel: 1})
			if err := a.CreateArchive(context.Background(), src, out); err != nil {
				t.Fatalf("CreateArchive: %v", err)
			}
			info, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != ArtifactFilePerm {
				t.Fatalf("archive mode %o, want %o", got, ArtifactFilePerm)
			}
		})
	}
}

// The collector's staging content files keep the mode they had (and are recorded
// with inside the archive): only the archive itself changed. The expectation is a
// sibling created with the same 0640 under the same umask.
func TestCreateBackupOutputFileKeepsContentFileMode(t *testing.T) {
	dir := t.TempDir()
	f, err := createBackupOutputFile(filepath.Join(dir, "storage_backup_summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	ref, err := os.OpenFile(filepath.Join(dir, "reference.txt"), os.O_CREATE|os.O_WRONLY, os.FileMode(defaultOptimizedFilePerm))
	if err != nil {
		t.Fatal(err)
	}
	_ = ref.Close()
	got, err := os.Stat(filepath.Join(dir, "storage_backup_summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(filepath.Join(dir, "reference.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode().Perm() != want.Mode().Perm() {
		t.Fatalf("content file mode %o, want %o", got.Mode().Perm(), want.Mode().Perm())
	}
}
