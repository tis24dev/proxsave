package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// --- Task 2: metadata parse + reuse helper ---

func TestParseLockFileMetadataStartTime(t *testing.T) {
	meta := parseLockFileMetadata([]byte("pid=42\nhost=h\ntime=t\nstarttime=987654\n"))
	if !meta.StartTimeKnown || meta.StartTime != 987654 {
		t.Fatalf("StartTime=%d known=%v, want 987654/true", meta.StartTime, meta.StartTimeKnown)
	}
	old := parseLockFileMetadata([]byte("pid=42\nhost=h\ntime=t\n"))
	if old.StartTimeKnown {
		t.Fatal("old-format lock must have StartTimeKnown=false")
	}
}

func TestLockOwnerReused(t *testing.T) {
	orig := procStartTimeFunc
	t.Cleanup(func() { procStartTimeFunc = orig })

	if lockOwnerReused(lockFileMetadata{PID: 42, StartTimeKnown: false}) {
		t.Fatal("old-format lock (no start-time) must not be judged reused")
	}
	procStartTimeFunc = func(int) (uint64, bool) { return 0, false }
	if lockOwnerReused(lockFileMetadata{PID: 42, StartTime: 5, StartTimeKnown: true}) {
		t.Fatal("unreadable current start-time must not be judged reused")
	}
	procStartTimeFunc = func(int) (uint64, bool) { return 5, true }
	if lockOwnerReused(lockFileMetadata{PID: 42, StartTime: 5, StartTimeKnown: true}) {
		t.Fatal("matching start-time must not be judged reused")
	}
	procStartTimeFunc = func(int) (uint64, bool) { return 9, true }
	if !lockOwnerReused(lockFileMetadata{PID: 42, StartTime: 5, StartTimeKnown: true}) {
		t.Fatal("mismatched start-time must be judged reused")
	}
}

// --- Task 3: CheckLockFile pid-alive identity ---

func TestCheckLockFile_ReusedPidRemovesStaleLock(t *testing.T) {
	origKill, origProc := killFunc, procStartTimeFunc
	t.Cleanup(func() { killFunc = origKill; procStartTimeFunc = origProc })
	killFunc = func(int, syscall.Signal) error { return nil }         // pid alive
	procStartTimeFunc = func(int) (uint64, bool) { return 222, true } // != recorded 111 -> reused

	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")
	host, _ := os.Hostname()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\nstarttime=111\n", host, time.Now().Format(time.RFC3339))), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := &CheckerConfig{BackupPath: tmpDir, LogPath: tmpDir, LockDirPath: tmpDir, LockFilePath: lockPath, MaxLockAge: time.Hour}
	checker := NewChecker(logger, cfg)
	t.Cleanup(func() { _ = checker.ReleaseLock() })
	if result := checker.CheckLockFile(); !result.Passed {
		t.Fatalf("a reused-pid lock must be treated as stale and re-acquired, got: %s", result.Message)
	}
}

func TestCheckLockFile_SameOwnerStaysInProgress(t *testing.T) {
	origKill, origProc := killFunc, procStartTimeFunc
	t.Cleanup(func() { killFunc = origKill; procStartTimeFunc = origProc })
	killFunc = func(int, syscall.Signal) error { return nil }         // alive
	procStartTimeFunc = func(int) (uint64, bool) { return 111, true } // == recorded

	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")
	host, _ := os.Hostname()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\nstarttime=111\n", host, time.Now().Format(time.RFC3339))), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := &CheckerConfig{BackupPath: tmpDir, LogPath: tmpDir, LockDirPath: tmpDir, LockFilePath: lockPath, MaxLockAge: time.Hour}
	result := NewChecker(logger, cfg).CheckLockFile()
	if result.Passed {
		t.Fatal("a live lock with matching start-time must read as in progress")
	}
	if result.Code != CheckCodeBackupInProgress {
		t.Fatalf("expected Code=%q, got %q", CheckCodeBackupInProgress, result.Code)
	}
}

func TestCheckLockFile_OldFormatLockStaysInProgress(t *testing.T) {
	origKill := killFunc
	t.Cleanup(func() { killFunc = origKill })
	killFunc = func(int, syscall.Signal) error { return nil } // alive; no starttime in lock

	logger := logging.New(types.LogLevelInfo, false)
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, ".backup.lock")
	host, _ := os.Hostname()
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\n", host, time.Now().Format(time.RFC3339))), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg := &CheckerConfig{BackupPath: tmpDir, LogPath: tmpDir, LockDirPath: tmpDir, LockFilePath: lockPath, MaxLockAge: time.Hour}
	result := NewChecker(logger, cfg).CheckLockFile()
	if result.Passed {
		t.Fatal("an old-format live lock must stay in progress (current behavior preserved)")
	}
}

// --- Task 4: BackupInProgress identity + 260-15 ---

func TestBackupInProgressReusedPid(t *testing.T) {
	origKill, origProc := killFunc, procStartTimeFunc
	t.Cleanup(func() { killFunc = origKill; procStartTimeFunc = origProc })
	killFunc = func(int, syscall.Signal) error { return nil }
	procStartTimeFunc = func(int) (uint64, bool) { return 222, true } // != recorded 111

	host, _ := os.Hostname()
	p := writeLock(t, t.TempDir(), fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\nstarttime=111\n", host, time.Now().Format(time.RFC3339)))
	if BackupInProgress(p, time.Hour) {
		t.Fatal("a reused pid means a stale lock -> not in progress")
	}
}

func TestBackupInProgressSameOwner(t *testing.T) {
	origKill, origProc := killFunc, procStartTimeFunc
	t.Cleanup(func() { killFunc = origKill; procStartTimeFunc = origProc })
	killFunc = func(int, syscall.Signal) error { return nil }
	procStartTimeFunc = func(int) (uint64, bool) { return 111, true } // == recorded

	host, _ := os.Hostname()
	p := writeLock(t, t.TempDir(), fmt.Sprintf("pid=4242\nhost=%s\ntime=%s\nstarttime=111\n", host, time.Now().Format(time.RFC3339)))
	if !BackupInProgress(p, time.Hour) {
		t.Fatal("matching start-time -> a backup is in progress")
	}
}

func TestBackupInProgressStatErrorFailsClosed(t *testing.T) {
	orig := osStat
	t.Cleanup(func() { osStat = orig })
	osStat = func(string) (os.FileInfo, error) { return nil, syscall.EACCES }
	if !BackupInProgress("/whatever/.backup.lock", time.Hour) {
		t.Fatal("a non-IsNotExist stat error must fail closed (assume in progress)")
	}
}
