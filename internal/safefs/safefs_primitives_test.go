package safefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

// A stalled lstat is abandoned when its timeout fires: Lstat returns the lstat
// TimeoutError for the path, which unwraps to ErrTimeout, at the timeout and not
// later, and osLstat is put back afterwards. The test runs in a synctest bubble,
// whose clock only moves when every goroutine in it is blocked, so the budget is
// exact and a slow or frozen runner cannot stretch it.
func TestLstat_StalledCallTimesOutWithinItsBudget(t *testing.T) {
	const path = "/does/not/matter"
	const timeout = 25 * time.Millisecond
	origLstat, origLimiter := osLstat, fsOpLimiter

	synctest.Test(t, func(t *testing.T) {
		// A limiter made inside the bubble, so every channel Lstat uses belongs to it.
		fsOpLimiter = newOperationLimiter(1)
		unblock := make(chan struct{})
		osLstat = func(string) (os.FileInfo, error) {
			<-unblock
			return nil, os.ErrNotExist
		}
		// Cleanups run inside the bubble, last registered first, before it waits for
		// its goroutines: the stalled worker is released, then the globals go back.
		t.Cleanup(func() {
			osLstat, fsOpLimiter = origLstat, origLimiter
		})
		t.Cleanup(func() { close(unblock) })

		type outcome struct {
			err     error
			elapsed time.Duration
		}
		done := make(chan outcome, 1)
		start := time.Now()
		go func() {
			_, err := Lstat(context.Background(), path, timeout)
			done <- outcome{err: err, elapsed: time.Since(start)}
		}()

		time.Sleep(timeout)
		synctest.Wait()
		var got outcome
		select {
		case got = <-done:
		default:
			t.Fatalf("Lstat still blocked after its %s timeout", timeout)
		}

		var te *TimeoutError
		if !errors.Is(got.err, ErrTimeout) || !errors.As(got.err, &te) {
			t.Fatalf("Lstat err = %v; want a TimeoutError", got.err)
		}
		if te.Op != "lstat" || te.Path != path || te.Timeout != timeout {
			t.Fatalf("TimeoutError = %+v; want Op lstat, Path %s, Timeout %s", *te, path, timeout)
		}
		if got.elapsed != timeout {
			t.Fatalf("Lstat returned after %s; want %s", got.elapsed, timeout)
		}
	})

	if reflect.ValueOf(osLstat).Pointer() != reflect.ValueOf(origLstat).Pointer() {
		t.Fatal("osLstat was not restored")
	}
	if fsOpLimiter != origLimiter {
		t.Fatal("fsOpLimiter was not restored")
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
