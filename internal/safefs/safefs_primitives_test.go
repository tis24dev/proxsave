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

func TestOpen_ReturnsTimeoutError(t *testing.T) {
	prev := osOpen
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "open completion", unblock, finished, func() { osOpen = prev })
	osOpen = func(string) (*os.File, error) {
		<-unblock
		close(finished)
		return nil, os.ErrNotExist
	}
	if _, err := Open(context.Background(), "/does/not/matter", 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Open err = %v; want timeout", err)
	}
}

func TestRemove_ReturnsTimeoutError(t *testing.T) {
	prev := osRemove
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "remove completion", unblock, finished, func() { osRemove = prev })
	osRemove = func(string) error {
		<-unblock
		close(finished)
		return nil
	}
	if err := Remove(context.Background(), "/does/not/matter", 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Remove err = %v; want timeout", err)
	}
}

func TestRename_ReturnsTimeoutError(t *testing.T) {
	prev := osRename
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "rename completion", unblock, finished, func() { osRename = prev })
	osRename = func(string, string) error {
		<-unblock
		close(finished)
		return nil
	}
	if err := Rename(context.Background(), "/a", "/b", 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Rename err = %v; want timeout", err)
	}
}

func TestCreateTemp_ReturnsTimeoutError(t *testing.T) {
	prev := osCreateTemp
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "createtemp completion", unblock, finished, func() { osCreateTemp = prev })
	osCreateTemp = func(string, string) (*os.File, error) {
		<-unblock
		close(finished)
		return nil, os.ErrNotExist
	}
	if _, err := CreateTemp(context.Background(), "/dir", ".tmp-", 25*time.Millisecond); err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("CreateTemp err = %v; want timeout", err)
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

func TestLchmod(t *testing.T) {
	dir := t.TempDir()

	// Regular file: Lchmod sets the mode.
	reg := filepath.Join(dir, "file")
	if err := os.WriteFile(reg, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := Lchmod(context.Background(), reg, 0o640, time.Second); err != nil {
		t.Fatalf("Lchmod regular file: %v", err)
	}
	fi, err := os.Lstat(reg)
	if err != nil {
		t.Fatalf("lstat file: %v", err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("regular file mode = %o; want 0640", fi.Mode().Perm())
	}

	// Directory: Lchmod sets the mode.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	if err := Lchmod(context.Background(), sub, 0o750, time.Second); err != nil {
		t.Fatalf("Lchmod dir: %v", err)
	}
	fi, err = os.Lstat(sub)
	if err != nil {
		t.Fatalf("lstat dir: %v", err)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Fatalf("dir mode = %o; want 0750", fi.Mode().Perm())
	}

	// Symlink to a sentinel: Lchmod must refuse (not follow) and leave the
	// sentinel's mode untouched.
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("s"), 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	if err := Lchmod(context.Background(), link, 0o777, time.Second); err == nil {
		t.Fatal("Lchmod on a symlink must return an error, not follow it")
	}
	fi, err = os.Lstat(sentinel)
	if err != nil {
		t.Fatalf("lstat sentinel: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("sentinel mode = %o; Lchmod followed the symlink", fi.Mode().Perm())
	}
}
