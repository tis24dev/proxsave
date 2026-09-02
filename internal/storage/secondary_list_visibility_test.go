package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// newSecondaryWithCapturedLog builds a secondary backend over dir whose logger
// writes to the returned buffer, so a test can assert on the lines the operator
// would actually see rather than on internal state.
func newSecondaryWithCapturedLog(t *testing.T, dir string) (*SecondaryStorage, *bytes.Buffer) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	s, err := NewSecondaryStorage(&config.Config{SecondaryPath: dir}, logger, "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	return s, buf
}

func warningLines(out string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "WARNING") {
			got = append(got, line)
		}
	}
	return got
}

func countWarningLines(out string) int { return len(warningLines(out)) }

// warningText is what the operator reads at DEBUG_LEVEL=warning: the WARNING lines
// alone, with the INFO block and the Debug detail excluded. Asserting on the whole
// buffer would pass on names that only the block below carries.
func warningText(out string) string { return strings.Join(warningLines(out), "\n") }

// itemLines are the indented entries of a header-plus-list block, the shape
// cron_indirect_refs.go:2139-2143 established: header and items at INFO, one
// WARNING carrying the verdict.
func itemLines(out string) []string {
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "INFO") && strings.Contains(line, "  - ") {
			got = append(got, line)
		}
	}
	return got
}

func seedArchives(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("host-backup-202603%02d-000000.tar.zst", i+1))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
}

// An operator who hits Ctrl+C, or a bound that expires, abandons the listing.
// Neither is a property of any single archive, so the run must say so once, by
// returning, and not once per archive still to be stat'ed.
func TestAnAbandonedListingReturnsTheErrorInsteadOfOneWarningPerArchive(t *testing.T) {
	dir := t.TempDir()
	seedArchives(t, dir, 6)

	restore := safefs.SetOsStatForTest(func(p string) (os.FileInfo, error) {
		time.Sleep(20 * time.Millisecond)
		return os.Lstat(p)
	})
	defer restore()

	s, buf := newSecondaryWithCapturedLog(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	got, err := s.List(ctx)
	if err == nil {
		t.Fatalf("List returned %d backups and a nil error after the context was cancelled", len(got))
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("List error = %v; want it to unwrap to context.Canceled", err)
	}
	if n := countWarningLines(buf.String()); n != 0 {
		t.Fatalf("an abandoned listing wrote %d WARNING lines, want 0:\n%s", n, buf.String())
	}
}

// One unreadable mount is one fault. The run must report it once with a count,
// not once per archive: List runs at least three times per backend per run, so a
// per-entry line multiplies by the number of archives and again by the callers.
func TestUnreadableArchivesAreReportedOnceNotOncePerArchive(t *testing.T) {
	dir := t.TempDir()
	seedArchives(t, dir, 1)
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, fmt.Sprintf("host-backup-202604%02d-000000.tar.zst", i+1))
		if err := os.Symlink(p, p); err != nil { // self-link: ELOOP on stat
			t.Fatalf("symlink %s: %v", p, err)
		}
	}

	s, buf := newSecondaryWithCapturedLog(t, dir)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d backups; want the 1 readable archive", len(got))
	}
	out := buf.String()
	if n := countWarningLines(out); n != 1 {
		t.Fatalf("5 unreadable archives wrote %d WARNING lines, want exactly 1:\n%s", n, out)
	}
	// The single WARNING has to stand on its own, because DEBUG_LEVEL=warning hides
	// the block below it.
	if warned := warningText(out); !strings.Contains(warned, "5 archive(s)") {
		t.Fatalf("the warning does not carry the count of unreadable archives:\n%s", out)
	}
	// One readable line per archive, each with its own cause, not one inline blob.
	items := itemLines(out)
	if len(items) != 5 {
		t.Fatalf("the block lists %d archives, want one line each for 5:\n%s", len(items), out)
	}
	for i, line := range items {
		name := fmt.Sprintf("host-backup-202604%02d-000000.tar.zst", i+1)
		if !strings.Contains(line, name) {
			t.Fatalf("item %d does not name %s:\n%s", i, name, out)
		}
		if !strings.Contains(line, "too many levels of symbolic links") {
			t.Fatalf("item %d does not carry its own cause:\n%s", i, out)
		}
	}
}

// When the archives failed for different reasons, each line carries its own: no
// archive borrows another's cause.
func TestArchivesThatFailedForDifferentReasonsEachCarryTheirOwn(t *testing.T) {
	dir := t.TempDir()
	seedArchives(t, dir, 1)
	loop := filepath.Join(dir, "host-backup-20260501-000000.tar.zst")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	denied := filepath.Join(dir, "host-backup-20260502-000000.tar.zst")
	if err := os.WriteFile(denied, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	restore := safefs.SetOsStatForTest(func(p string) (os.FileInfo, error) {
		if strings.HasSuffix(p, "20260502-000000.tar.zst") {
			return nil, &fs.PathError{Op: "stat", Path: p, Err: syscall.EIO}
		}
		return os.Stat(p)
	})
	defer restore()

	s, buf := newSecondaryWithCapturedLog(t, dir)
	if _, err := s.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	out := buf.String()
	if n := countWarningLines(out); n != 1 {
		t.Fatalf("wrote %d WARNING lines, want exactly 1:\n%s", n, out)
	}
	items := itemLines(out)
	if len(items) != 2 {
		t.Fatalf("the block lists %d archives, want 2:\n%s", len(items), out)
	}
	want := []struct{ name, cause string }{
		{"host-backup-20260501-000000.tar.zst", "too many levels of symbolic links"},
		{"host-backup-20260502-000000.tar.zst", "input/output error"},
	}
	for i, w := range want {
		if !strings.Contains(items[i], w.name) || !strings.Contains(items[i], w.cause) {
			t.Fatalf("item %d should read %q with cause %q:\n%s", i, w.name, w.cause, out)
		}
	}
}

// The dominant fault: the glob saw the archives, then the mount went away, so
// every stat answers ENOENT and the location answers ENOENT too. Before this,
// the run returned an empty list, a nil error, and not one line.
func TestAVanishedLocationIsNamedInsteadOfReturningNothingInSilence(t *testing.T) {
	dir := t.TempDir()
	seedArchives(t, dir, 4)

	restore := safefs.SetOsStatForTest(func(p string) (os.FileInfo, error) {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: syscall.ENOENT}
	})
	defer restore()

	s, buf := newSecondaryWithCapturedLog(t, dir)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List returned %d backups; want 0", len(got))
	}
	out := buf.String()
	if n := countWarningLines(out); n != 1 {
		t.Fatalf("a vanished location wrote %d WARNING lines, want exactly 1:\n%s", n, out)
	}
	warned := warningText(out)
	if !strings.Contains(warned, dir) {
		t.Fatalf("the warning does not name the location %s:\n%s", dir, out)
	}
	if !strings.Contains(warned, "4 archive(s)") {
		t.Fatalf("the warning does not carry the count of archives left unlisted:\n%s", out)
	}
}

// A backup deleted by hand or by another run between the glob and the stat is
// not a fault and must stay off the console.
func TestASingleArchiveThatVanishedBetweenGlobAndStatStaysSilent(t *testing.T) {
	dir := t.TempDir()
	seedArchives(t, dir, 1)
	link := filepath.Join(dir, "host-backup-20260601-000000.tar.zst")
	if err := os.Symlink(filepath.Join(dir, "already-deleted"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	s, buf := newSecondaryWithCapturedLog(t, dir)
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d backups; want the 1 that is really there", len(got))
	}
	if n := countWarningLines(buf.String()); n != 0 {
		t.Fatalf("a single vanished archive wrote %d WARNING lines, want 0:\n%s", n, buf.String())
	}
}

// The per-archive loop already returns on an abandoned stat (the test above), but
// the location probe after it did not: a cancellation landing between the last
// archive stat and the probe made the probe's CancelError read as "location
// stopped answering", and List returned an EMPTY list with a NIL error. Retention
// and stats then ran on that empty answer after the operator had already aborted.
func TestAnAbandonedLocationProbeReturnsTheErrorNotAnEmptyList(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("host-backup-202605%02d-000000.tar.zst", i+1))
		if err := os.Symlink(p, p); err != nil { // self-link: ELOOP, so zero archives are readable
			t.Fatalf("symlink %s: %v", p, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The seam cancels the context the moment the PROBE's stat runs (the probe is
	// the only stat List aims at the base directory), which is exactly the window
	// the per-archive arm cannot cover.
	restore := safefs.SetOsStatForTest(func(p string) (os.FileInfo, error) {
		if p == dir {
			cancel()
			time.Sleep(50 * time.Millisecond)
		}
		return os.Stat(p)
	})
	defer restore()

	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	// FS_IO_TIMEOUT on, as shipped (default 30): with the 0 opt-out safefs runs
	// unbounded and the probe's stat is never raced against the cancellation.
	s, err := NewSecondaryStorage(&config.Config{SecondaryPath: dir, FsIoTimeoutSeconds: 30}, logger, "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	got, err := s.List(ctx)
	if err == nil {
		t.Fatalf("List returned %d backups and a nil error after the probe was abandoned", len(got))
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("List error = %v; want it to unwrap to context.Canceled", err)
	}
	if n := countWarningLines(buf.String()); n != 0 {
		t.Fatalf("an abandoned probe wrote %d WARNING lines, want 0:\n%s", n, buf.String())
	}
}
