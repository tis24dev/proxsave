package filelock

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	crashHelperEnv       = "PROXSAVE_FILELOCK_CRASH_HELPER"
	crashHelperLockEnv   = "PROXSAVE_FILELOCK_CRASH_LOCK"
	crashHelperReadyEnv  = "PROXSAVE_FILELOCK_CRASH_READY"
	crashHelperPollDelay = 10 * time.Millisecond
)

var crashHelperRelease func() error

func TestTryAcquireReportsHeldThenReacquiresAfterRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource.lock")

	releaseFirst, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}

	if releaseUnexpected, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = releaseUnexpected()
		}
		_ = releaseFirst()
		t.Fatalf("second TryAcquire error = %v, want ErrHeld", err)
	}

	if err := releaseFirst(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}

	releaseNext, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := releaseNext(); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}

func TestAcquireWaitsUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resource.lock")
	releaseFirst, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}

	type acquireResult struct {
		release func() error
		err     error
	}
	result := make(chan acquireResult, 1)
	go func() {
		release, err := Acquire(path)
		result <- acquireResult{release: release, err: err}
	}()

	select {
	case got := <-result:
		if got.err == nil {
			_ = got.release()
		}
		_ = releaseFirst()
		t.Fatalf("Acquire returned before the held lock was released: %v", got.err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := releaseFirst(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("blocked Acquire after release: %v", got.err)
		}
		if err := got.release(); err != nil {
			t.Fatalf("release blocked acquisition: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire remained blocked after the first lock was released")
	}
}

func TestAcquireRequiresExistingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing")
	path := filepath.Join(parent, "resource.lock")

	if release, err := Acquire(path); err == nil {
		_ = release()
		t.Fatal("Acquire succeeded with a missing parent directory")
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("parent directory state after Acquire = %v, want not exist", err)
	}
}

func TestLockIsReleasedWhenOwnerProcessDies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resource.lock")
	readyPath := filepath.Join(dir, "ready")

	var stderr bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecoveryHelper$")
	cmd.Env = append(os.Environ(),
		crashHelperEnv+"=1",
		crashHelperLockEnv+"="+path,
		crashHelperReadyEnv+"="+readyPath,
	)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock owner helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat helper ready marker: %v", err)
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatalf("lock owner helper did not become ready: %s", stderr.String())
		}
		time.Sleep(crashHelperPollDelay)
	}

	if releaseUnexpected, err := TryAcquire(path); !errors.Is(err, ErrHeld) {
		if err == nil {
			_ = releaseUnexpected()
		}
		t.Fatalf("TryAcquire while helper is alive error = %v, want ErrHeld", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill lock owner helper: %v", err)
	}
	state, err := cmd.Process.Wait()
	if err != nil {
		t.Fatalf("wait for killed lock owner helper: %v", err)
	}
	if state.Success() {
		t.Fatal("killed lock owner helper exited successfully")
	}

	release, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after helper death: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release lock acquired after helper death: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persistent lock sidecar after helper death: %v", err)
	}
}

func TestCrashRecoveryHelper(t *testing.T) {
	if os.Getenv(crashHelperEnv) != "1" {
		t.Skip("helper process only")
	}

	var err error
	crashHelperRelease, err = Acquire(os.Getenv(crashHelperLockEnv))
	if err != nil {
		t.Fatalf("Acquire in helper: %v", err)
	}
	if err := os.WriteFile(os.Getenv(crashHelperReadyEnv), []byte("ready"), 0o600); err != nil {
		t.Fatalf("write helper ready marker: %v", err)
	}

	time.Sleep(24 * time.Hour)
}
