package safefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"
)

var (
	osStat        = os.Stat
	osReadDir     = os.ReadDir
	syscallStatfs = syscall.Statfs
)

// ErrTimeout is a sentinel error used to classify filesystem operations that did not
// complete within the configured timeout.
var ErrTimeout = errors.New("filesystem operation timed out")

// TimeoutError is returned when a filesystem operation exceeds its allowed duration.
// Note that this does not cancel the underlying kernel call; it only stops waiting.
type TimeoutError struct {
	Op      string
	Path    string
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	if e == nil {
		return "filesystem operation timed out"
	}
	if e.Timeout > 0 {
		return fmt.Sprintf("%s %s: timeout after %s", e.Op, e.Path, e.Timeout)
	}
	return fmt.Sprintf("%s %s: timeout", e.Op, e.Path)
}

func (e *TimeoutError) Unwrap() error { return ErrTimeout }

func effectiveTimeout(ctx context.Context, timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 0
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0
		}
		if remaining < timeout {
			return remaining
		}
	}
	return timeout
}

func Stat(ctx context.Context, path string, timeout time.Duration) (fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout = effectiveTimeout(ctx, timeout)
	if timeout <= 0 {
		return osStat(path)
	}

	type result struct {
		info fs.FileInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		info, err := osStat(path)
		ch <- result{info: info, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.info, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, &TimeoutError{Op: "stat", Path: path, Timeout: timeout}
	}
}

func ReadDir(ctx context.Context, path string, timeout time.Duration) ([]os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout = effectiveTimeout(ctx, timeout)
	if timeout <= 0 {
		return osReadDir(path)
	}

	type result struct {
		entries []os.DirEntry
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		entries, err := osReadDir(path)
		ch <- result{entries: entries, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.entries, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, &TimeoutError{Op: "readdir", Path: path, Timeout: timeout}
	}
}

func Statfs(ctx context.Context, path string, timeout time.Duration) (syscall.Statfs_t, error) {
	if err := ctx.Err(); err != nil {
		return syscall.Statfs_t{}, err
	}
	timeout = effectiveTimeout(ctx, timeout)
	if timeout <= 0 {
		var stat syscall.Statfs_t
		return stat, syscallStatfs(path, &stat)
	}

	type result struct {
		stat syscall.Statfs_t
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var stat syscall.Statfs_t
		err := syscallStatfs(path, &stat)
		ch <- result{stat: stat, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.stat, r.err
	case <-ctx.Done():
		return syscall.Statfs_t{}, ctx.Err()
	case <-timer.C:
		return syscall.Statfs_t{}, &TimeoutError{Op: "statfs", Path: path, Timeout: timeout}
	}
}
