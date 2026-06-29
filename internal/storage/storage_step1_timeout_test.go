package storage

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

func expiredStorageCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	t.Cleanup(cancel)
	return ctx
}

// LocalStorage.DetectFilesystem bounds its MkdirAll; on timeout it returns a
// CRITICAL StorageError wrapping safefs.ErrTimeout (primary is critical).
func TestLocalDetectFilesystemTimeoutIsCritical(t *testing.T) {
	cfg := &config.Config{BackupPath: t.TempDir(), FsIoTimeoutSeconds: 30}
	l, err := NewLocalStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	_, err = l.DetectFilesystem(expiredStorageCtx(t))
	if !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("want safefs.ErrTimeout, got %v", err)
	}
	var se *StorageError
	if !errors.As(err, &se) || !se.IsCritical {
		t.Fatalf("want a critical StorageError, got %v", err)
	}
}

// SecondaryStorage.DetectFilesystem bounds its MkdirAll; on timeout it returns a
// NON-critical StorageError (secondary is best-effort).
func TestSecondaryDetectFilesystemTimeoutNonCritical(t *testing.T) {
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir(), FsIoTimeoutSeconds: 30}
	s, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	_, err = s.DetectFilesystem(expiredStorageCtx(t))
	if !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("want safefs.ErrTimeout, got %v", err)
	}
	var se *StorageError
	if !errors.As(err, &se) || se.IsCritical {
		t.Fatalf("want a non-critical StorageError, got %v", err)
	}
}

// copyFile bounds its leaf ops: a source on a wedged mount (FIFO with no writer)
// makes the bounded Open time out instead of hanging.
func TestSecondaryCopyFileLeafOpenTimeout(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "src.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Cleanup(func() {
		if w, e := os.OpenFile(fifo, os.O_WRONLY, 0); e == nil {
			_ = w.Close()
		}
	})

	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: dir, FsIoTimeoutSeconds: 1}
	s, err := NewSecondaryStorage(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.copyFile(context.Background(), fifo, filepath.Join(dir, "dst")) }()
	select {
	case err := <-done:
		if !errors.Is(err, safefs.ErrTimeout) {
			t.Fatalf("want safefs.ErrTimeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copyFile hung on a wedged source mount")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "dst")); !os.IsNotExist(statErr) {
		t.Fatalf("dst must not exist after a timed-out copy; stat err = %v", statErr)
	}
}

// A healthy copy still works with the bounded leaf ops in place.
func TestSecondaryCopyFileHealthy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("payload"), 0o640); err != nil {
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
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "payload" {
		t.Fatalf("dst content = %q, err = %v; want \"payload\"", data, err)
	}
}
