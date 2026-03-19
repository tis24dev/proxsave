package safefs

import (
	"context"
	"errors"
	"math"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type stagedDeadlineContext struct {
	deadline time.Time
	done     <-chan struct{}
	errCalls int
}

func (c *stagedDeadlineContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}

func (c *stagedDeadlineContext) Done() <-chan struct{} {
	return c.done
}

func (c *stagedDeadlineContext) Err() error {
	c.errCalls++
	if c.errCalls == 1 {
		return nil
	}
	return context.DeadlineExceeded
}

func (c *stagedDeadlineContext) Value(any) any {
	return nil
}

type fixedErrContext struct {
	done <-chan struct{}
	err  error
}

func (c *fixedErrContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *fixedErrContext) Done() <-chan struct{} {
	return c.done
}

func (c *fixedErrContext) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *fixedErrContext) Value(any) any {
	return nil
}

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for %s", name)
	}
}

func registerBlockedOpCleanup(t *testing.T, name string, unblock chan struct{}, finished <-chan struct{}, restore func()) {
	t.Helper()

	t.Cleanup(restore)
	t.Cleanup(func() {
		close(unblock)
		waitForSignal(t, finished, name)
	})
}

func TestStat_ReturnsTimeoutError(t *testing.T) {
	prev := osStat
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "stat completion", unblock, finished, func() {
		osStat = prev
	})

	osStat = func(string) (os.FileInfo, error) {
		<-unblock
		close(finished)
		return nil, os.ErrNotExist
	}

	start := time.Now()
	_, err := Stat(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Stat err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("Stat took too long: %s", time.Since(start))
	}
}

func TestReadDir_ReturnsTimeoutError(t *testing.T) {
	prev := osReadDir
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "readdir completion", unblock, finished, func() {
		osReadDir = prev
	})

	osReadDir = func(string) ([]os.DirEntry, error) {
		<-unblock
		close(finished)
		return nil, nil
	}

	start := time.Now()
	_, err := ReadDir(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("ReadDir err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("ReadDir took too long: %s", time.Since(start))
	}
}

func TestStatfs_ReturnsTimeoutError(t *testing.T) {
	prev := syscallStatfs
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "statfs completion", unblock, finished, func() {
		syscallStatfs = prev
	})

	syscallStatfs = func(string, *syscall.Statfs_t) error {
		<-unblock
		close(finished)
		return nil
	}

	start := time.Now()
	_, err := Statfs(context.Background(), "/does/not/matter", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("Statfs err = %v; want timeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("Statfs took too long: %s", time.Since(start))
	}
}

func TestSpaceUsageFromStatfsUsesBfreeForUsedSpace(t *testing.T) {
	stat := syscall.Statfs_t{
		Blocks: 100,
		Bfree:  20,
		Bavail: 15,
		Bsize:  4096,
	}

	total, available, used := SpaceUsageFromStatfs(stat)

	if total != 100*4096 {
		t.Fatalf("total = %d; want %d", total, 100*4096)
	}
	if available != 15*4096 {
		t.Fatalf("available = %d; want %d", available, 15*4096)
	}
	if used != 80*4096 {
		t.Fatalf("used = %d; want %d", used, 80*4096)
	}
	if used == total-available {
		t.Fatalf("used should not be derived from Bavail when reserved blocks exist")
	}
}

func TestSpaceUsageFromStatfsClampsInconsistentCounters(t *testing.T) {
	stat := syscall.Statfs_t{
		Blocks: 100,
		Bfree:  150,
		Bavail: 125,
		Bsize:  1024,
	}

	total, available, used := SpaceUsageFromStatfs(stat)

	if total != 100*1024 {
		t.Fatalf("total = %d; want %d", total, 100*1024)
	}
	if available != total {
		t.Fatalf("available = %d; want clamp to total %d", available, total)
	}
	if used != 0 {
		t.Fatalf("used = %d; want 0", used)
	}
}

func TestSpaceUsageFromStatfsClampsNegativeByteCounts(t *testing.T) {
	stat := syscall.Statfs_t{
		Blocks: 10,
		Bfree:  2,
		Bavail: 1,
		Bsize:  -4096,
	}

	total, available, used := SpaceUsageFromStatfs(stat)

	if total != 0 || available != 0 || used != 0 {
		t.Fatalf("negative byte counts should clamp to zero, got total=%d available=%d used=%d", total, available, used)
	}
}

func TestSpaceUsageFromStatfsSaturatesOverflowingProducts(t *testing.T) {
	stat := syscall.Statfs_t{
		Blocks: 1<<63 - 1,
		Bfree:  0,
		Bavail: 1<<63 - 1,
		Bsize:  4096,
	}

	total, available, used := SpaceUsageFromStatfs(stat)

	if total != math.MaxInt64 {
		t.Fatalf("total = %d; want %d", total, math.MaxInt64)
	}
	if available != math.MaxInt64 {
		t.Fatalf("available = %d; want %d", available, math.MaxInt64)
	}
	if used != math.MaxInt64 {
		t.Fatalf("used = %d; want %d", used, math.MaxInt64)
	}
}

func TestStat_PropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Stat(ctx, "/does/not/matter", 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat err = %v; want context.Canceled", err)
	}
}

func TestRunLimited_ReturnsTimeoutErrorWhenDeadlineExpiresBeforeNoTimeoutPath(t *testing.T) {
	done := make(chan struct{})
	close(done)
	ctx := &stagedDeadlineContext{
		deadline: time.Now().Add(-time.Millisecond),
		done:     done,
	}

	called := false
	_, err := runLimited(ctx, 50*time.Millisecond, &TimeoutError{Op: "stat", Path: "/does/not/matter"}, func() (int, error) {
		called = true
		return 1, nil
	})

	if called {
		t.Fatal("run called; want timeout before execution")
	}
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("runLimited err = %v; want timeout", err)
	}
}

func TestRunLimited_NormalizesExpiredDeadlineAtEntry(t *testing.T) {
	done := make(chan struct{})
	close(done)
	ctx := &fixedErrContext{
		done: done,
		err:  context.DeadlineExceeded,
	}

	called := false
	_, err := runLimited(ctx, 50*time.Millisecond, &TimeoutError{Op: "stat", Path: "/does/not/matter"}, func() (int, error) {
		called = true
		return 1, nil
	})

	if called {
		t.Fatal("run called; want timeout before execution")
	}
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("runLimited err = %v; want timeout", err)
	}
}

func TestRunLimited_NormalizesDeadlineFromDoneBranch(t *testing.T) {
	done := make(chan struct{})
	ctx := &fixedErrContext{
		done: done,
		err:  context.DeadlineExceeded,
	}

	unblock := make(chan struct{})
	finished := make(chan struct{})
	t.Cleanup(func() {
		close(unblock)
		waitForSignal(t, finished, "runLimited completion")
	})

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()

	_, err := runLimited(ctx, time.Second, &TimeoutError{Op: "stat", Path: "/does/not/matter"}, func() (int, error) {
		defer close(finished)
		<-unblock
		return 1, nil
	})

	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("runLimited err = %v; want timeout", err)
	}
}

func TestOperationLimiterAcquire_NormalizesDeadlineExceeded(t *testing.T) {
	limiter := newOperationLimiter(1)
	limiter.slots <- struct{}{}

	done := make(chan struct{})
	ctx := &fixedErrContext{
		done: done,
		err:  context.DeadlineExceeded,
	}

	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()

	err := limiter.acquire(ctx, timer.C)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("acquire err = %v; want timeout", err)
	}
}

func TestOperationLimiterAcquire_PropagatesCancellation(t *testing.T) {
	limiter := newOperationLimiter(1)
	limiter.slots <- struct{}{}

	done := make(chan struct{})
	ctx := &fixedErrContext{
		done: done,
		err:  context.Canceled,
	}

	timer := time.NewTimer(time.Second)
	defer timer.Stop()

	go func() {
		time.Sleep(10 * time.Millisecond)
		close(done)
	}()

	err := limiter.acquire(ctx, timer.C)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire err = %v; want context.Canceled", err)
	}
}

func TestStat_DoesNotSpawnPastLimiterCapacity(t *testing.T) {
	prevStat := osStat
	prevLimiter := fsOpLimiter
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "limited stat completion", unblock, finished, func() {
		osStat = prevStat
		fsOpLimiter = prevLimiter
	})

	fsOpLimiter = newOperationLimiter(1)

	var calls atomic.Int32
	osStat = func(string) (os.FileInfo, error) {
		calls.Add(1)
		<-unblock
		close(finished)
		return nil, os.ErrNotExist
	}

	_, err := Stat(context.Background(), "/first", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("first Stat err = %v; want timeout", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls after first timeout = %d; want 1", got)
	}
	if got := fsOpLimiter.inflight(); got != 1 {
		t.Fatalf("inflight after first timeout = %d; want 1", got)
	}

	_, err = Stat(context.Background(), "/second", 25*time.Millisecond)
	if err == nil || !errors.Is(err, ErrTimeout) {
		t.Fatalf("second Stat err = %v; want timeout", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls after limiter saturation = %d; want 1", got)
	}
}
