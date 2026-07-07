package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func writeLock(t *testing.T, dir string, content string) string {
	t.Helper()
	p := filepath.Join(dir, BackupLockFileName)
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	return p
}

func TestDefaultBackupLockPath(t *testing.T) {
	got := DefaultBackupLockPath("/opt/proxsave")
	want := filepath.Join("/opt/proxsave", "lock", ".backup.lock")
	if got != want {
		t.Fatalf("DefaultBackupLockPath = %q, want %q", got, want)
	}
}

func TestBackupInProgressNoLock(t *testing.T) {
	if BackupInProgress(filepath.Join(t.TempDir(), ".backup.lock"), time.Hour) {
		t.Fatal("missing lock must report not in progress")
	}
}

func TestBackupInProgressLivePidOnHost(t *testing.T) {
	orig := killFunc
	t.Cleanup(func() { killFunc = orig })
	killFunc = func(pid int, sig syscall.Signal) error { return nil } // pid alive

	host, _ := os.Hostname()
	p := writeLock(t, t.TempDir(), fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\n", host, time.Now().Format(time.RFC3339)))
	if !BackupInProgress(p, time.Hour) {
		t.Fatal("a live pid on this host must read as in progress")
	}
}

func TestBackupInProgressDeadPidOnHost(t *testing.T) {
	orig := killFunc
	t.Cleanup(func() { killFunc = orig })
	killFunc = func(pid int, sig syscall.Signal) error { return syscall.ESRCH } // pid gone

	host, _ := os.Hostname()
	p := writeLock(t, t.TempDir(), fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\n", host, time.Now().Format(time.RFC3339)))
	if BackupInProgress(p, time.Hour) {
		t.Fatal("a dead pid (ESRCH) means a stale lock, not in progress")
	}
}

func TestBackupInProgressForeignHostAgeFallback(t *testing.T) {
	// A lock owned by a different host cannot be liveness-checked, so it falls back to
	// age: a fresh one reads as in progress, an old one as stale.
	dir := t.TempDir()
	p := writeLock(t, dir, "pid=4242\nhost=some-other-host\ntime=now\n")
	if !BackupInProgress(p, time.Hour) {
		t.Fatal("a fresh foreign-host lock must read as in progress (age fallback)")
	}

	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if BackupInProgress(p, time.Hour) {
		t.Fatal("an aged-out foreign-host lock must read as stale (not in progress)")
	}
}
