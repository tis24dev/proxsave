package storage

import (
	"bytes"
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

// A secondary mount that delivers one chunk then wedges mid-stream must make the
// bounded copy time out (per-chunk stall budget) instead of hanging, and must
// leave no destination behind.
func TestSecondaryCopyFileMidStreamWedge(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "src.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer func() { _ = w.Close() }()
		_, _ = w.Write([]byte("first chunk delivered, then the mount wedges"))
		<-releaseWriter // keep the write end open (no EOF) so the next Read blocks
	}()
	t.Cleanup(func() {
		close(releaseWriter)
		<-writerDone
	})

	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: dir, FsIoTimeoutSeconds: 1}
	s, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}

	dst := filepath.Join(dir, "dst")
	done := make(chan error, 1)
	go func() { done <- s.copyFile(context.Background(), fifo, dst) }()
	select {
	case err := <-done:
		if !errors.Is(err, safefs.ErrTimeout) {
			t.Fatalf("want safefs.ErrTimeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copyFile hung on a mid-stream wedge")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("dst must not exist after a stalled copy; stat err = %v", statErr)
	}
}

// A healthy multi-chunk copy must still succeed byte-for-byte with the bounded
// streaming loop in place (no false stall across many chunks).
func TestSecondaryCopyFileHealthyLarge(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	data := make([]byte, 5*1024*1024+777) // > 5 chunks of 1 MiB
	for i := range data {
		data[i] = byte((i*31 + 7) % 251)
	}
	if err := os.WriteFile(src, data, 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}

	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: dir, FsIoTimeoutSeconds: 30}
	s, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	dst := filepath.Join(dir, "dst")
	if err := s.copyFile(context.Background(), src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("dst content mismatch (got %d bytes, want %d)", len(got), len(data))
	}
}
