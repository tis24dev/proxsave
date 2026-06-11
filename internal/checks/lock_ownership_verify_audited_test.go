package checks

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Regression for the ownsLockFile fail-closed fix (code review 2026-06-09):
// ownsLockFile previously returned true on any read failure or missing pid, so
// ReleaseLock could delete a lock it could no longer prove it owned. It now returns
// true only when the lock is already gone or its pid/host match this process, and
// false otherwise (fail closed), leaving the file for the stale-age reaper.

func ownsLockTestChecker(t *testing.T) *Checker {
	t.Helper()
	l := logging.New(types.LogLevelDebug, false)
	l.SetOutput(io.Discard)
	return NewChecker(l, &CheckerConfig{})
}

func TestOwnsLockFile_FailsClosedWhenOwnershipUnprovable(t *testing.T) {
	c := ownsLockTestChecker(t)
	dir := t.TempDir()
	hostname, _ := os.Hostname()

	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"missing file (already gone)", filepath.Join(dir, "absent.lock"), true},
		{"our pid and host", write("ours.lock", fmt.Sprintf("pid=%d\nhost=%s\n", os.Getpid(), hostname)), true},
		{"foreign pid", write("foreign.lock", "pid=999999\nhost=otherhost\n"), false},
		{"no parseable pid", write("nopid.lock", "garbage without a pid\n"), false},
		{"empty file", write("empty.lock", ""), false},
	}
	for _, tc := range cases {
		if got := c.ownsLockFile(tc.path); got != tc.want {
			t.Errorf("%s: ownsLockFile = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// End-to-end: after acquiring, if the lock's pid becomes unverifiable (truncated /
// tampered), ReleaseLock must NOT remove it (previously it would have).
func TestReleaseLock_LeavesLockWithUnverifiablePidInPlace(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".backup.lock")
	cfg := &CheckerConfig{
		BackupPath:   dir,
		LogPath:      dir,
		LockDirPath:  dir,
		LockFilePath: lockPath,
		MaxLockAge:   time.Hour,
	}
	c := NewChecker(logger, cfg)

	if res := c.CheckLockFile(); !res.Passed {
		t.Fatalf("CheckLockFile should acquire the lock: %s", res.Message)
	}

	// Corrupt the lock so its pid can no longer be parsed.
	if err := os.WriteFile(lockPath, []byte("corrupted, no pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock returned error: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("a lock whose ownership cannot be confirmed must be left in place, got: %v", err)
	}
}
