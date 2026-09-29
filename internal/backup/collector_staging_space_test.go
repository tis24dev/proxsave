package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// failingWriter accepts limit bytes, then fails every write with err: a destination
// that fills up (or breaks) in the middle of a file.
type failingWriter struct {
	f     *os.File
	limit int
	err   error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.limit <= 0 {
		return 0, w.err
	}
	n := len(p)
	if n > w.limit {
		n = w.limit
	}
	written, err := w.f.Write(p[:n])
	w.limit -= written
	if err != nil {
		return written, err
	}
	if written < len(p) {
		return written, w.err
	}
	return written, nil
}

func (w *failingWriter) Close() error { return w.f.Close() }

// failWritesTo makes the copy of every file named name fail after 3 bytes with err.
func failWritesTo(t *testing.T, name string, err error) {
	t.Helper()
	orig := osOpenFile
	t.Cleanup(func() { osOpenFile = orig })
	osOpenFile = func(path string, flag int, perm os.FileMode) (io.WriteCloser, error) {
		f, openErr := os.OpenFile(path, flag, perm)
		if openErr != nil || filepath.Base(path) != name {
			return f, openErr
		}
		return &failingWriter{f: f, limit: 3, err: err}, nil
	}
}

func newStagingTestCollector(t *testing.T) (*Collector, *bytes.Buffer, string) {
	t.Helper()
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	tempDir := filepath.Join(t.TempDir(), "proxsave-run")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewCollector(logger, GetDefaultCollectorConfig(), tempDir, types.ProxmoxVE, false), &buf, tempDir
}

func writeStagingSource(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("payload-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

// A copy that fails on the working directory side leaves no file behind: it used to stay
// in the staging tree truncated and go into the archive as if it were the real one. The
// warning says the write failed, not that the source was unreadable.
func TestCopyWriteFailureLeavesNoPartialFile(t *testing.T) {
	c, log, tempDir := newStagingTestCollector(t)
	src := writeStagingSource(t)
	failWritesTo(t, "b.txt", syscall.EIO)

	dest := filepath.Join(tempDir, "etc", "mydir")
	if err := c.safeCopyDir(context.Background(), src, dest, "mydir"); err != nil {
		t.Fatalf("an I/O error on one file must not stop the directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial b.txt left in the working directory: %v", err)
	}
	for _, name := range []string{"a.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("%s not collected: %v", name, err)
		}
	}
	if !strings.Contains(log.String(), "Skipping file "+filepath.Join(src, "b.txt")+" in mydir: could not write it to the working directory:") {
		t.Fatalf("write failure not reported as such:\n%s", log.String())
	}
	if strings.Contains(log.String(), "unreadable file "+filepath.Join(src, "b.txt")) {
		t.Fatalf("write failure reported as an unreadable source:\n%s", log.String())
	}
	if c.StagingNoSpaceErr() != nil {
		t.Fatalf("an I/O error is not a full working directory")
	}
}

// A full working directory stops the copy at the file that hit it, drops that file and
// says where the space ran out, once: on a 300M tmpfs the run used to go on and archive
// that file truncated and the 17 after it empty, SSH host keys among them.
func TestFullWorkingDirectoryStopsTheCopy(t *testing.T) {
	c, log, tempDir := newStagingTestCollector(t)
	src := writeStagingSource(t)
	failWritesTo(t, "b.txt", syscall.ENOSPC)

	dest := filepath.Join(tempDir, "etc", "mydir")
	err := c.safeCopyDir(context.Background(), src, dest, "mydir")
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("safeCopyDir = %v, want the ENOSPC that stopped it", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "b.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("truncated b.txt left in the working directory: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "c.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the copy went on past the full working directory: %v", statErr)
	}
	line := "Working directory - no space left on " + filepath.Dir(tempDir) + " ("
	if got := strings.Count(log.String(), line); got != 1 {
		t.Fatalf("want the full working directory line once, got %d:\n%s", got, log.String())
	}
	if !strings.Contains(log.String(), "): collection stopped, no archive written") {
		t.Fatalf("full working directory line misses its tail:\n%s", log.String())
	}
	if !errors.Is(c.StagingNoSpaceErr(), syscall.ENOSPC) {
		t.Fatalf("StagingNoSpaceErr = %v", c.StagingNoSpaceErr())
	}
}

// A brick that hit a full working directory and carried on still ends the recipe: the
// bricks after it would only write empty or truncated files.
func TestRecipeStopsAfterFullWorkingDirectory(t *testing.T) {
	c, _, _ := newStagingTestCollector(t)
	var ran []string
	r := recipe{Name: "test", Bricks: []collectionBrick{
		{ID: "first", Run: func(context.Context, *collectionState) error {
			ran = append(ran, "first")
			c.NoteStagingWriteError(&os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC})
			return nil
		}},
		{ID: "second", Run: func(context.Context, *collectionState) error {
			ran = append(ran, "second")
			return nil
		}},
	}}
	err := runRecipe(context.Background(), r, newCollectionState(c))
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("runRecipe = %v, want the ENOSPC", err)
	}
	if len(ran) != 1 {
		t.Fatalf("bricks ran after the full working directory: %v", ran)
	}
}

// The system recipe turns its own errors into a warning; a full working directory must
// still fail the collection, as it did not in the measured run.
func TestCollectAllFailsOnFullWorkingDirectory(t *testing.T) {
	c, _, _ := newStagingTestCollector(t)
	c.proxType = types.ProxmoxUnknown
	orig := osOpenFile
	t.Cleanup(func() { osOpenFile = orig })
	osOpenFile = func(string, int, os.FileMode) (io.WriteCloser, error) {
		return nil, &os.PathError{Op: "open", Path: "x", Err: syscall.ENOSPC}
	}
	if err := c.CollectAll(context.Background()); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("CollectAll = %v, want the ENOSPC", err)
	}
}
