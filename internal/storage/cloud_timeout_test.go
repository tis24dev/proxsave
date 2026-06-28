package storage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// Item 1: UploadToRemotePath must bound a deadline-less ctx so a stalled rclone
// cannot hang shutdown.
func TestUploadToRemotePathBoundsDeadlessCtx(t *testing.T) {
	tmp := t.TempDir()
	local := filepath.Join(tmp, "log.txt")
	writeTestFile(t, local, "x")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote", RcloneRetries: 1, RcloneTimeoutOperation: 1}
	cs := newCloudStorageForTest(cfg)
	cs.waitForRetry = func(context.Context, time.Duration) error { return nil }
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-ctx.Done() // a wedged copy that respects ctx; the WithTimeout must fire
		return nil, ctx.Err()
	}

	start := time.Now()
	err := cs.UploadToRemotePath(context.Background(), local, "remote:logs/log.txt", false)
	if err == nil {
		t.Fatal("expected a deadline error, got nil")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("UploadToRemotePath was not bounded: %s", d)
	}
}

// Item 2: defaultExecCommand must set WaitDelay and return exec.ErrWaitDelay as an
// ERROR (not swallow it to nil like osCommandRunner).
func TestDefaultExecCommandWaitDelayReturnsError(t *testing.T) {
	old := cloudExecWaitDelay
	cloudExecWaitDelay = 100 * time.Millisecond
	t.Cleanup(func() { cloudExecWaitDelay = old })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	// sh exits immediately; the backgrounded sleep inherits stdout and holds the
	// pipe open, forcing CombinedOutput's Wait past WaitDelay -> ErrWaitDelay.
	_, err := defaultExecCommand(ctx, "sh", "-c", "sleep 30 & exit 0")
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("want exec.ErrWaitDelay, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("WaitDelay did not bound the wait: %s", d)
	}
}

func TestDefaultExecCommandHappyPath(t *testing.T) {
	out, err := defaultExecCommand(context.Background(), "echo", "ok")
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("echo: out=%q err=%v", out, err)
	}
}

// Item 3: VerifyUpload's local stat is bounded; a done ctx returns before any
// rclone exec.
func TestVerifyUploadLocalStatBounded(t *testing.T) {
	tmp := t.TempDir()
	local := filepath.Join(tmp, "log.txt")
	writeTestFile(t, local, "x")
	cfg := &config.Config{CloudRemote: "remote", FsIoTimeoutSeconds: 1}
	cs := newCloudStorageForTest(cfg)
	called := false
	cs.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done -> safefs.Stat returns immediately

	ok, err := cs.VerifyUpload(ctx, local, "remote:logs/log.txt")
	if ok || err == nil {
		t.Fatalf("want (false, err), got (%v, %v)", ok, err)
	}
	if called {
		t.Fatal("no rclone exec should run when the local stat is bounded out")
	}
}

// Item 4: a timed-out local hash (dead/stale mount) degrades to the size-only
// verdict (true, nil), it must not hang or hard-fail a good upload.
func TestVerifyRemoteChecksumLocalHashTimeoutDegrades(t *testing.T) {
	tmp := t.TempDir()
	fifo := filepath.Join(tmp, "log.txt")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Cleanup(func() {
		// Unblock the abandoned GenerateChecksum os.Open(fifo) worker goroutine.
		if w, e := os.OpenFile(fifo, os.O_WRONLY, 0); e == nil {
			_ = w.Close()
		}
	})

	cfg := &config.Config{CloudRemote: "remote", FsIoTimeoutSeconds: 1, RcloneTimeoutOperation: 30}
	cs := newCloudStorageForTest(cfg)
	q := &commandQueue{t: t, queue: []queuedResponse{
		{name: "rclone", out: "0000000000000000000000000000000000000000000000000000000000000000  log.txt"},
	}}
	cs.execCommand = q.exec

	start := time.Now()
	ok, err := cs.verifyRemoteChecksum(context.Background(), fifo, "remote:logs/log.txt", "log.txt")
	if !ok || err != nil {
		t.Fatalf("want degrade to size-only (true, nil), got (%v, %v)", ok, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("local hash was not bounded: %s", d)
	}
}
