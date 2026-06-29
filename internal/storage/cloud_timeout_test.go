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
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
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

// Item 3b: Store's pre-upload local source stat is bounded by FS_IO_TIMEOUT, so a
// dead/stale BACKUP_PATH mount cannot wedge Store before uploadCtx/rclone apply.
// The source file EXISTS, so a reverted raw os.Stat would SUCCEED (the dead-mount
// stub only intercepts safefs's stat) and let the upload run; the per-chunk fix
// instead times the stat out and skips the upload. Reverting Store's os.Stat ->
// safefs.Stat swap turns this red (it would proceed to rclone).
func TestStoreLocalSourceStatBoundedAgainstDeadMount(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "node-backup.tar.zst")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote", FsIoTimeoutSeconds: 1, RcloneTimeoutOperation: 30}
	cs := newCloudStorageForTest(cfg)
	var uploaded bool
	cs.execCommand = func(context.Context, string, ...string) ([]byte, error) {
		uploaded = true
		return nil, nil
	}

	// Simulate a dead/stale local mount: the source stat never returns. Installed
	// last so test setup is unaffected; the abandoned worker is released on cleanup.
	park := make(chan struct{})
	t.Cleanup(func() { close(park) })
	restore := safefs.SetOsStatForTest(func(string) (os.FileInfo, error) {
		<-park
		return nil, errors.New("released")
	})
	t.Cleanup(restore)

	done := make(chan error, 1)
	go func() { done <- cs.Store(context.Background(), src, &types.BackupMetadata{}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Store must fail when the local source stat times out, not succeed")
		}
		if uploaded {
			t.Fatal("no rclone upload may run after a source-stat timeout (a raw os.Stat would have let it through)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Store hung on a dead-mount source stat: the local source stat is not bounded")
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

// Item 5 (mutation-prover for the per-chunk cloud-verify fix): a HEALTHY local
// archive whose read is slow-but-progressing (aggregate hash time exceeds
// FS_IO_TIMEOUT, yet every individual chunk lands well under it) must be hashed
// to completion and verified by CONTENT, not silently downgraded to size-only.
//
// This pins verifyRemoteChecksum to the per-chunk-bounded backup.GenerateChecksumBounded.
// The superseded whole-file safefs.Run(..., FS_IO_TIMEOUT) wrapper would abandon
// this read at the 1s whole-file deadline and degrade to size-only, so the wrong
// remote hash below would be wrongly accepted as (true, nil). With the per-chunk
// budget the read finishes and the mismatch is caught: (false, "checksum mismatch").
// Reverting cloud.go to the whole-file wrapper turns this test red.
func TestVerifyRemoteChecksumHealthySlowReadIsContentVerified(t *testing.T) {
	tmp := t.TempDir()
	fifo := filepath.Join(tmp, "log.txt")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	// A slow-but-alive producer: 15 chunks, 100ms apart => ~1.5s aggregate (well
	// over the 1s FS_IO_TIMEOUT) while no single read waits anywhere near 1s. The
	// total payload (<1 KiB) stays inside the pipe buffer, so the writer is paced
	// solely by its own sleeps, never by the reader.
	const chunks = 15
	chunk := []byte("proxsave-healthy-slow-chunk\n")
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0) // rendezvous: unblocks once the reader opens the FIFO
		if err != nil {
			return
		}
		defer func() { _ = w.Close() }()
		for i := 0; i < chunks; i++ {
			time.Sleep(100 * time.Millisecond)
			if _, werr := w.Write(chunk); werr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		select {
		case <-writerDone:
		case <-time.After(5 * time.Second):
			t.Error("slow FIFO writer did not finish")
		}
	})

	cfg := &config.Config{CloudRemote: "remote", FsIoTimeoutSeconds: 1, RcloneTimeoutOperation: 30}
	cs := newCloudStorageForTest(cfg)
	// A valid-but-wrong remote SHA256: the streamed content hashes to something
	// else, so a COMPLETED local hash must report a mismatch rather than a
	// size-only "OK". (A whole-file deadline would never reach this comparison.)
	q := &commandQueue{t: t, queue: []queuedResponse{
		{name: "rclone", out: strings.Repeat("0", 64) + "  log.txt"},
	}}
	cs.execCommand = q.exec

	start := time.Now()
	ok, err := cs.verifyRemoteChecksum(context.Background(), fifo, "remote:logs/log.txt", "log.txt")
	if d := time.Since(start); d < time.Second {
		t.Fatalf("hash finished in %s; the slow producer must push it past the 1s FS_IO_TIMEOUT to exercise the per-chunk budget", d)
	}
	if ok || err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want content-verified mismatch (false, \"checksum mismatch\"), got (%v, %v)", ok, err)
	}
}
