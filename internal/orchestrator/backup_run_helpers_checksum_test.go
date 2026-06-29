package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/safefs"
)

// generateArchiveChecksum must thread FS_IO_TIMEOUT (o.fsIoTimeout()) into the
// bounded hash: hashing an archive on a dead mount (a FIFO with no writer) must
// produce a prompt timeout, not hang. A regression that drops o.fsIoTimeout()
// back to a 0/unbounded value would hang here and trip the guard.
func TestGenerateArchiveChecksum_ThreadsFsIoTimeout(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "archive.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Cleanup(func() {
		// O_NONBLOCK so this never blocks: with the abandoned reader present the
		// open succeeds and the close releases it; with no reader (a regression
		// where the code under test never opened the FIFO) it returns ENXIO
		// immediately -> e != nil -> skip, so the cleanup cannot wedge CI.
		if w, e := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); e == nil {
			_ = w.Close() // release the abandoned blocked open
		}
	})

	o := &Orchestrator{logger: newTestLogger(), cfg: &config.Config{FsIoTimeoutSeconds: 1}}

	done := make(chan error, 1)
	go func() {
		_, err := o.generateArchiveChecksum(context.Background(), fifo)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, safefs.ErrTimeout) {
			t.Fatalf("err = %v; want a threaded FS_IO_TIMEOUT (safefs.ErrTimeout)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("generateArchiveChecksum hung: FS_IO_TIMEOUT was not threaded into the bounded hash")
	}
}

// With FS_IO_TIMEOUT unset (0 = opt-out), a healthy archive still hashes fine.
func TestGenerateArchiveChecksum_HealthyUnbounded(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.tar")
	if err := os.WriteFile(archive, []byte("archive-bytes"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	o := &Orchestrator{logger: newTestLogger(), cfg: &config.Config{}} // FsIoTimeoutSeconds=0
	sum, err := o.generateArchiveChecksum(context.Background(), archive)
	if err != nil || sum == "" {
		t.Fatalf("generateArchiveChecksum = (%q, %v); want a checksum", sum, err)
	}
}
