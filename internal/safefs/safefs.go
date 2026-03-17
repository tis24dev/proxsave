package safefs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"syscall"
	"time"
)

var (
	osStat        = os.Stat
	osReadDir     = os.ReadDir
	syscallStatfs = syscall.Statfs
	fsOpLimiter   = newOperationLimiter(32)
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

func normalizeContextErr(ctx context.Context, deadlineErr error) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return deadlineErr
	}
	return err
}

// operationLimiter bounds the number of in-flight filesystem goroutines whose
// callers may already have returned due to timeout/cancellation.
type operationLimiter struct {
	slots chan struct{}
}

func newOperationLimiter(capacity int) *operationLimiter {
	if capacity < 1 {
		capacity = 1
	}
	return &operationLimiter{
		slots: make(chan struct{}, capacity),
	}
}

func (l *operationLimiter) acquire(ctx context.Context, timer <-chan time.Time) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return normalizeContextErr(ctx, ErrTimeout)
	case <-timer:
		return ErrTimeout
	}
}

func (l *operationLimiter) release() {
	select {
	case <-l.slots:
	default:
	}
}

func (l *operationLimiter) inflight() int {
	return len(l.slots)
}

func runLimited[T any](ctx context.Context, timeout time.Duration, timeoutErr *TimeoutError, run func() (T, error)) (T, error) {
	var zero T
	if err := normalizeContextErr(ctx, timeoutErr); err != nil {
		return zero, err
	}
	timeout = effectiveTimeout(ctx, timeout)
	if timeout <= 0 {
		if err := normalizeContextErr(ctx, timeoutErr); err != nil {
			return zero, err
		}
		return run()
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	limiter := fsOpLimiter
	if err := limiter.acquire(ctx, timer.C); err != nil {
		if errors.Is(err, ErrTimeout) {
			return zero, timeoutErr
		}
		return zero, err
	}

	type result struct {
		value T
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		defer limiter.release()
		value, err := run()
		ch <- result{value: value, err: err}
	}()

	select {
	case r := <-ch:
		return r.value, r.err
	case <-ctx.Done():
		return zero, normalizeContextErr(ctx, timeoutErr)
	case <-timer.C:
		return zero, timeoutErr
	}
}

func Stat(ctx context.Context, path string, timeout time.Duration) (fs.FileInfo, error) {
	stat := osStat
	return runLimited(ctx, timeout, &TimeoutError{Op: "stat", Path: path, Timeout: effectiveTimeout(ctx, timeout)}, func() (fs.FileInfo, error) {
		return stat(path)
	})
}

func ReadDir(ctx context.Context, path string, timeout time.Duration) ([]os.DirEntry, error) {
	readDir := osReadDir
	return runLimited(ctx, timeout, &TimeoutError{Op: "readdir", Path: path, Timeout: effectiveTimeout(ctx, timeout)}, func() ([]os.DirEntry, error) {
		return readDir(path)
	})
}

func Statfs(ctx context.Context, path string, timeout time.Duration) (syscall.Statfs_t, error) {
	statfs := syscallStatfs
	return runLimited(ctx, timeout, &TimeoutError{Op: "statfs", Path: path, Timeout: effectiveTimeout(ctx, timeout)}, func() (syscall.Statfs_t, error) {
		var stat syscall.Statfs_t
		err := statfs(path, &stat)
		return stat, err
	})
}

// SpaceUsageFromStatfs converts statfs counters into total, user-available, and
// actually-used byte counts. "Available" tracks Bavail (space a non-root user can
// allocate), while "used" tracks Blocks-Bfree (space already consumed).
func SpaceUsageFromStatfs(stat syscall.Statfs_t) (totalBytes, availableBytes, usedBytes int64) {
	totalBytes = statfsBlocksToBytes(stat.Blocks, stat.Bsize)
	availableBytes = statfsBlocksToBytes(stat.Bavail, stat.Bsize)

	if stat.Blocks > stat.Bfree {
		usedBytes = statfsBlocksToBytes(stat.Blocks-stat.Bfree, stat.Bsize)
	}
	if availableBytes > totalBytes {
		availableBytes = totalBytes
	}
	if usedBytes > totalBytes {
		usedBytes = totalBytes
	}

	return totalBytes, availableBytes, usedBytes
}

func statfsBlocksToBytes(blocks uint64, blockSize int64) int64 {
	if blocks == 0 || blockSize <= 0 {
		return 0
	}

	size := uint64(blockSize)
	if blocks > uint64(math.MaxInt64)/size {
		return math.MaxInt64
	}
	return int64(blocks * size)
}
