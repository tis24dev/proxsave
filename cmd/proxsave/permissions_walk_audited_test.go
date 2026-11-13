package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Regression for chown-walk-aborts-partial (2026-06-09 audit): the WalkDir callback
// in applyDirOwnershipRecursive returned the traversal error on the first unreadable
// entry, which aborts filepath.WalkDir, so every not-yet-visited entry kept its old
// ownership/permissions while applyBackupPermissions still returned nil. The per-entry
// logic now lives in applyOwnershipWalkEntry, which skips a traversal error instead of
// aborting. Written after that change.

func permWalkTestLogger() *logging.Logger {
	l := logging.New(types.LogLevelDebug, false)
	l.SetOutput(io.Discard)
	return l
}

func TestApplyOwnershipWalkEntry_TraversalErrorIsSkippedNotAborted(t *testing.T) {
	// A non-nil walkErr (with a nil DirEntry, as WalkDir passes on read failures)
	// must return nil so the walk continues to the rest of the tree.
	if err := applyOwnershipWalkEntry(context.Background(), "/some/unreadable/subdir", nil, errors.New("permission denied"), 0, 0, 0, permWalkTestLogger(), new(int)); err != nil {
		t.Fatalf("a traversal error must be skipped (return nil) so the walk continues, got %v", err)
	}
}

func TestApplyOwnershipWalkEntry_NormalEntryReturnsNil(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("read dir: %v", err)
	}

	// chown to our own uid/gid is a harmless no-op; the entry must process cleanly.
	failed := 0
	if err := applyOwnershipWalkEntry(context.Background(), file, entries[0], nil, os.Getuid(), os.Getgid(), 0, permWalkTestLogger(), &failed); err != nil {
		t.Fatalf("a normal entry must return nil, got %v", err)
	}
	if failed != 0 {
		t.Fatalf("a clean entry was counted as failed (%d)", failed)
	}
}

// An entry whose chown fails no longer vanishes into a DEBUG line: the walk goes on over
// every entry and the pass reports how many could not be changed, so the install footer
// is not "normalized correctly" over them.
func TestApplyBackupPermissions_FailedEntriesAreReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown to uid 0")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := applyDirOwnershipRecursive(context.Background(), dir, 0, 0, 0, false, permWalkTestLogger())
	if err == nil || err.Error() != "2 entries could not be changed" {
		t.Fatalf("err = %v, want both entries (the directory and its file) counted", err)
	}

	var out bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&out)
	cfg := &config.Config{BackupUser: "root", BackupGroup: "root", BackupPath: dir}
	outcome := applyBackupPermissionsOutcome(context.Background(), cfg, logger, false)
	want := "Failed to apply permissions on " + dir + ": 2 entries could not be changed"
	if !strings.Contains(out.String(), "WARNING  "+want+"\n") {
		t.Fatalf("missing WARNING %q in:\n%s", want, out.String())
	}
	if len(outcome.skipped) != 1 || outcome.skipped[0] != dir+" could not be changed" {
		t.Fatalf("skipped = %q", outcome.skipped)
	}
}
