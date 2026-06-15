package backup

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// tarEntryNames reads an uncompressed tar and returns the set of member names
// (with the leading "./" stripped) for assertions.
func tarEntryNames(t *testing.T, archivePath string) map[string]bool {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()

	names := map[string]bool{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		names[strings.TrimPrefix(hdr.Name, "./")] = true
	}
	return names
}

// newTestSocket creates a unix socket inside dir. A socket is a file type
// tar.FileInfoHeader cannot represent ("sockets not supported"), which is how the
// tests deterministically force a per-file archiving failure even when running as
// root (permission-based failures do not block root). Returns the socket path.
func newTestSocket(t *testing.T, dir string) string {
	t.Helper()
	sockPath := filepath.Join(dir, "x.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Skipf("cannot create unix socket for test (path length / platform?): %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return sockPath
}

// TestCreateArchive_UnarchivableEntryFailsClosed covers H04 fix (a): a source
// entry that cannot be added to the tar makes CreateArchive fail with
// ErrArchiveIncomplete instead of returning a valid-looking but incomplete
// archive as success. The walk still continues so every other file is captured.
func TestCreateArchive_UnarchivableEntryFailsClosed(t *testing.T) {
	dir, err := os.MkdirTemp("", "arx")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	src := filepath.Join(dir, "s")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write keep: %v", err)
	}
	newTestSocket(t, src)

	archiver := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionNone})
	out := filepath.Join(dir, "out.tar")

	err = archiver.CreateArchive(context.Background(), src, out)
	if err == nil {
		t.Fatal("expected CreateArchive to fail for an incomplete archive, got nil")
	}
	if !errors.Is(err, ErrArchiveIncomplete) {
		t.Fatalf("expected ErrArchiveIncomplete, got %v", err)
	}

	// The failure is recorded but the walk is not aborted: the good file is still
	// in the archive, only the unrepresentable socket is missing.
	names := tarEntryNames(t, out)
	if !names["keep.txt"] {
		t.Errorf("expected keep.txt to be archived despite the skipped socket, got %v", names)
	}
	if names["x.sock"] {
		t.Errorf("the socket must not appear in the archive")
	}
}

// TestCreateArchive_CompleteArchiveSucceeds is the negative control for fix (a):
// a fully archivable tree yields no error, marks the instance as the producer,
// and counts every written entry.
func TestCreateArchive_CompleteArchiveSucceeds(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("bb"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	archiver := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionNone})
	out := filepath.Join(tempDir, "out.tar")
	if err := archiver.CreateArchive(context.Background(), src, out); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}
	if len(archiver.skipped) != 0 {
		t.Errorf("expected no skipped entries, got %v", archiver.skipped)
	}
	if !archiver.contentVerify {
		t.Error("expected contentVerify to be set after a successful create")
	}
	// sub dir + a.txt + sub/b.txt = 3 tar entries.
	if archiver.entriesWritten != 3 {
		t.Errorf("expected 3 entries written, got %d", archiver.entriesWritten)
	}
}

// TestVerifyArchive_EntryCountMismatchFailsClosed covers H04 fix (b): if the
// finished archive lists fewer entries than were written (entries lost to on-disk
// corruption/truncation that `tar -t` integrity alone does not catch),
// VerifyArchive fails with ErrArchiveEntryCountMismatch.
func TestVerifyArchive_EntryCountMismatchFailsClosed(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	archiver := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionNone})
	out := filepath.Join(tempDir, "out.tar")
	ctx := context.Background()
	if err := archiver.CreateArchive(ctx, src, out); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	// Simulate an entry that vanished after being written: the archive on disk now
	// lists one fewer entry than the archiver believes it wrote.
	archiver.entriesWritten++

	err := archiver.VerifyArchive(ctx, out)
	if !errors.Is(err, ErrArchiveEntryCountMismatch) {
		t.Fatalf("expected ErrArchiveEntryCountMismatch, got %v", err)
	}
}

// TestVerifyArchive_ToleratesListingLongerThanWritten guards the "<" (not "!=")
// reconciliation rule: a listing with MORE lines than entries written is never
// data loss (it happens when a tar flavour like busybox splits a member name on
// an embedded newline), so VerifyArchive must accept it. Only a SHORTER listing
// (a lost entry) is a failure.
func TestVerifyArchive_ToleratesListingLongerThanWritten(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	archiver := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionNone})
	out := filepath.Join(tempDir, "out.tar")
	ctx := context.Background()
	if err := archiver.CreateArchive(ctx, src, out); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	// Pretend we wrote one fewer entry than the listing reports (listed > written),
	// mimicking a tar flavour that splits a name across lines.
	archiver.entriesWritten--

	if err := archiver.VerifyArchive(ctx, out); err != nil {
		t.Fatalf("a listing longer than written must be tolerated, got %v", err)
	}
}

// TestVerifyArchive_SkipsReconciliationForForeignArchive guards the contentVerify
// gate: an archiver that did not create the archive must NOT reconcile entry
// counts (its entriesWritten is 0), preserving legacy verify-only behaviour and
// avoiding a false mismatch.
func TestVerifyArchive_SkipsReconciliationForForeignArchive(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	ctx := context.Background()
	creator := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionNone})
	out := filepath.Join(tempDir, "out.tar")
	if err := creator.CreateArchive(ctx, src, out); err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}

	// A different instance verifies the archive; it never ran CreateArchive, so it
	// must skip reconciliation rather than compare against its zero count.
	verifier := NewArchiver(logging.New(types.LogLevelError, false), &ArchiverConfig{Compression: types.CompressionNone})
	if err := verifier.VerifyArchive(ctx, out); err != nil {
		t.Fatalf("foreign-archive verification must skip entry reconciliation, got %v", err)
	}
}
