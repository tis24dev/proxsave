package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

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
	if err := applyOwnershipWalkEntry(context.Background(), "/some/unreadable/subdir", nil, errors.New("permission denied"), 0, 0, 0, permWalkTestLogger()); err != nil {
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
	if err := applyOwnershipWalkEntry(context.Background(), file, entries[0], nil, os.Getuid(), os.Getgid(), 0, permWalkTestLogger()); err != nil {
		t.Fatalf("a normal entry must return nil, got %v", err)
	}
}
