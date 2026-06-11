package checks

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// These tests guard the backup-lock mutual exclusion against the regression
// confirmed by the 2026-06-09 pre-test audit (deferred-release-deletes-foreign-lock
// / release-runs-on-unacquired-lock-path): a checker that never acquired the lock
// must never delete the holder's lock file. They are written AFTER fixing
// ReleaseLock to be ownership-aware, hence the _audited suffix.

func newLockChecker(t *testing.T, lockPath string) (*Checker, *CheckerConfig) {
	t.Helper()
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)
	cfg := &CheckerConfig{
		BackupPath:   filepath.Dir(lockPath),
		LogPath:      filepath.Dir(lockPath),
		LockDirPath:  filepath.Dir(lockPath),
		LockFilePath: lockPath,
		MaxLockAge:   time.Hour,
		DryRun:       false,
	}
	return NewChecker(logger, cfg), cfg
}

// TestReleaseLock_NonOwnerDoesNotDeleteForeignLock is the core regression: a
// second backup that loses the lock check must not delete the winner's lock when
// its own deferred ReleaseLock fires.
func TestReleaseLock_NonOwnerDoesNotDeleteForeignLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".backup.lock")

	// Backup A acquires the lock.
	checkerA, _ := newLockChecker(t, lockPath)
	if res := checkerA.CheckLockFile(); !res.Passed {
		t.Fatalf("backup A should acquire the lock, got: %s", res.Message)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should exist after A acquired it: %v", err)
	}

	// Backup B (separate checker, same path) loses: A's pid is alive (this very
	// process), so CheckLockFile reports "another backup in progress" and does
	// NOT create a lock for B.
	checkerB, _ := newLockChecker(t, lockPath)
	if res := checkerB.CheckLockFile(); res.Passed {
		t.Fatalf("backup B must NOT acquire the lock while A holds it")
	}

	// B's deferred release must be a no-op: A's lock must survive.
	if err := checkerB.ReleaseLock(); err != nil {
		t.Fatalf("B.ReleaseLock returned error: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("A's lock file was deleted by non-owner B's ReleaseLock (mutual exclusion broken): %v", err)
	}

	// A still owns it and can release it cleanly.
	if err := checkerA.ReleaseLock(); err != nil {
		t.Fatalf("A.ReleaseLock returned error: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("A's lock file should be removed after the owner releases it")
	}
}

// TestReleaseLock_NoOpWhenNeverAcquired covers the early-error path: a checker
// whose deferred ReleaseLock fires before it ever acquired the lock (e.g. a
// storage-init failure) must not delete a pre-existing foreign lock.
func TestReleaseLock_NoOpWhenNeverAcquired(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".backup.lock")

	// A foreign lock file already on disk (held by some other backup).
	if err := os.WriteFile(lockPath, []byte("pid=999999\nhost=otherhost\ntime=2026-06-09T00:00:00Z\n"), 0o640); err != nil {
		t.Fatalf("seeding foreign lock: %v", err)
	}

	// This checker never called CheckLockFile, so it never acquired anything.
	checker, _ := newLockChecker(t, lockPath)
	if err := checker.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock returned error: %v", err)
	}

	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("foreign lock was deleted by a checker that never acquired it: %v", err)
	}
}

// TestReleaseLock_OwnerReleaseIsIdempotent ensures the owner can release, and a
// second release after the file is gone does not error or touch a lock another
// process may have created in the meantime.
func TestReleaseLock_OwnerReleaseIsIdempotent(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".backup.lock")

	checker, _ := newLockChecker(t, lockPath)
	if res := checker.CheckLockFile(); !res.Passed {
		t.Fatalf("checker should acquire the lock, got: %s", res.Message)
	}
	if err := checker.ReleaseLock(); err != nil {
		t.Fatalf("first ReleaseLock failed: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock should be gone after owner release")
	}

	// A different process now holds the lock.
	if err := os.WriteFile(lockPath, []byte("pid=999999\nhost=otherhost\ntime=2026-06-09T00:00:00Z\n"), 0o640); err != nil {
		t.Fatalf("seeding new foreign lock: %v", err)
	}
	// A stray second release from the original owner must not delete it.
	if err := checker.ReleaseLock(); err != nil {
		t.Fatalf("second ReleaseLock failed: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("second release wrongly deleted another process's lock: %v", err)
	}
}

// TestReleaseLock_SkipsRemovalWhenLockReplacedByAnotherProcess exercises the
// defense-in-depth content check: even when this checker believes it holds the
// lock (lockAcquired==true), it must not remove a lock whose on-disk pid/host
// now belong to a different process (our lock was reaped as stale and recreated).
func TestReleaseLock_SkipsRemovalWhenLockReplacedByAnotherProcess(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".backup.lock")

	checker, _ := newLockChecker(t, lockPath)
	if res := checker.CheckLockFile(); !res.Passed {
		t.Fatalf("checker should acquire the lock, got: %s", res.Message)
	}

	// Simulate our lock being reaped and re-created by another process.
	if err := os.WriteFile(lockPath, []byte("pid=999999\nhost=otherhost\ntime=2026-06-09T00:00:00Z\n"), 0o640); err != nil {
		t.Fatalf("replacing lock content: %v", err)
	}

	if err := checker.ReleaseLock(); err != nil {
		t.Fatalf("ReleaseLock returned error: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("release deleted a lock now owned by another process: %v", err)
	}
}
