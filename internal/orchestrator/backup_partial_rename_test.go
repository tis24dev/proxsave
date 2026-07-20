package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/types"
)

// backupTestArchiver builds a real *backup.Archiver with an uncompressed config
// so tests exercise the concrete create/verify path without external tools.
func backupTestArchiver(t *testing.T) *backup.Archiver {
	t.Helper()
	return backup.NewArchiver(newTestLogger(), &backup.ArchiverConfig{
		Compression: types.CompressionNone,
	})
}

// promoteBackupArchive must move the verified partial onto the final path and
// leave no .partial behind; discardPartialArchive must remove a partial on the
// error path so a truncated archive never lands on the final path.
func TestPromoteAndDiscardPartialArchive(t *testing.T) {
	dir := t.TempDir()
	final := filepath.Join(dir, "host-backup-20260717.tar.zst")
	partial := final + ".partial"
	fs := osFS{}

	// Promote: partial with content -> final has content, partial gone.
	if err := os.WriteFile(partial, []byte("archive-bytes"), 0o640); err != nil {
		t.Fatalf("seed partial: %v", err)
	}
	if err := promoteBackupArchive(fs, partial, final); err != nil {
		t.Fatalf("promoteBackupArchive: %v", err)
	}
	if b, err := os.ReadFile(final); err != nil || string(b) != "archive-bytes" {
		t.Fatalf("final content=%q err=%v; want archive-bytes", b, err)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("partial must be gone after promote, stat err=%v", err)
	}

	// Discard: error path leaves neither partial nor final.
	final2 := filepath.Join(dir, "host-backup-err.tar.zst")
	partial2 := final2 + ".partial"
	if err := os.WriteFile(partial2, []byte("truncated"), 0o640); err != nil {
		t.Fatalf("seed partial2: %v", err)
	}
	discardPartialArchive(fs, partial2)
	if _, err := os.Stat(partial2); !os.IsNotExist(err) {
		t.Fatalf("partial2 must be discarded, stat err=%v", err)
	}
	if _, err := os.Stat(final2); !os.IsNotExist(err) {
		t.Fatalf("final2 must never exist on the error path, stat err=%v", err)
	}
	// discard of an absent partial is a no-op (best-effort).
	discardPartialArchive(fs, filepath.Join(dir, "absent.tar.partial"))
}

// createBackupArchiveFile must target the .partial path, never the final path,
// so an interrupted creation cannot leave a truncated archive on the final path.
func TestCreateBackupArchiveFileTargetsPartial(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	final := filepath.Join(dir, "host-backup-20260717.tar")
	partial := final + ".partial"

	// Minimal uncompressed archiver (mirror archiver_helpers_test.go pattern).
	archiver := backupTestArchiver(t)
	if err := createBackupArchiveFile(t.Context(), archiver, source, partial); err != nil {
		t.Fatalf("createBackupArchiveFile: %v", err)
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatalf("archive must be created at the partial path: %v", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("archive must NOT be created at the final path; stat err=%v", err)
	}
}
