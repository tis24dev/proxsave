package safefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLstat_ReturnsTimeoutError(t *testing.T) {
	prev := osLstat
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "lstat completion", unblock, finished, func() {
		osLstat = prev
	})

	osLstat = func(string) (os.FileInfo, error) {
		<-unblock
		close(finished)
		return nil, os.ErrNotExist
	}

	start := time.Now()
	_, err := Lstat(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Lstat err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("Lstat took too long: %s", time.Since(start))
	}
}

func TestMkdirAll_ReturnsTimeoutError(t *testing.T) {
	prev := osMkdirAll
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "mkdirall completion", unblock, finished, func() {
		osMkdirAll = prev
	})

	osMkdirAll = func(string, os.FileMode) error {
		<-unblock
		close(finished)
		return nil
	}

	if err := MkdirAll(context.Background(), "/does/not/matter", 0o755, 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("MkdirAll err = %v; want timeout", err)
	}
}

func TestChmod_ReturnsTimeoutError(t *testing.T) {
	prev := osChmod
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "chmod completion", unblock, finished, func() {
		osChmod = prev
	})

	osChmod = func(string, os.FileMode) error {
		<-unblock
		close(finished)
		return nil
	}

	if err := Chmod(context.Background(), "/does/not/matter", 0o600, 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Chmod err = %v; want timeout", err)
	}
}

func TestLchown_ReturnsTimeoutError(t *testing.T) {
	prev := osLchown
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "lchown completion", unblock, finished, func() {
		osLchown = prev
	})

	osLchown = func(string, int, int) error {
		<-unblock
		close(finished)
		return nil
	}

	if err := Lchown(context.Background(), "/does/not/matter", 0, 0, 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Lchown err = %v; want timeout", err)
	}
}

func TestRun_ReturnsTimeoutError(t *testing.T) {
	unblock := make(chan struct{})
	finished := make(chan struct{})
	t.Cleanup(func() {
		close(unblock)
		waitForSignal(t, finished, "run completion")
	})

	start := time.Now()
	_, err := Run(context.Background(), "probe", "/does/not/matter", 25*time.Millisecond, func() (bool, error) {
		<-unblock
		close(finished)
		return true, nil
	})
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Run err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("Run took too long: %s", time.Since(start))
	}
}

// TestPrimitives_SucceedOnHealthyFS verifies the new wrappers behave like their
// raw os counterparts on a responsive filesystem (parity guard for the
// syscall.Chmod->safefs.Chmod / syscall.Lchown->safefs.Lchown swaps).
func TestPrimitives_SucceedOnHealthyFS(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "a", "b")
	ctx := context.Background()

	if err := MkdirAll(ctx, dir, 0o755, time.Second); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := Lstat(ctx, dir, time.Second)
	if err != nil || !info.IsDir() {
		t.Fatalf("Lstat dir = (%v, %v); want a directory", info, err)
	}

	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Chmod(ctx, file, 0o600, time.Second); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	fi, err := Lstat(ctx, file, time.Second)
	if err != nil {
		t.Fatalf("Lstat file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("Chmod perm = %o; want 600", fi.Mode().Perm())
	}
	if err := Lchown(ctx, file, os.Getuid(), os.Getgid(), time.Second); err != nil {
		t.Fatalf("Lchown: %v", err)
	}
}
