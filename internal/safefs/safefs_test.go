package safefs

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func waitForSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for %s", name)
	}
}

func TestStat_ReturnsTimeoutError(t *testing.T) {
	prev := osStat
	unblock := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		close(unblock)
		waitForSignal(t, finished, "stat completion")
		osStat = prev
	}()

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
	defer func() {
		close(unblock)
		waitForSignal(t, finished, "readdir completion")
		osReadDir = prev
	}()

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
	defer func() {
		close(unblock)
		waitForSignal(t, finished, "statfs completion")
		syscallStatfs = prev
	}()

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

func TestStat_PropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Stat(ctx, "/does/not/matter", 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stat err = %v; want context.Canceled", err)
	}
}

func TestStat_DoesNotSpawnPastLimiterCapacity(t *testing.T) {
	prevStat := osStat
	prevLimiter := fsOpLimiter
	defer func() {
		osStat = prevStat
		fsOpLimiter = prevLimiter
	}()

	fsOpLimiter = newOperationLimiter(1)

	unblock := make(chan struct{})
	finished := make(chan struct{})
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

	close(unblock)
	waitForSignal(t, finished, "limited stat completion")
}
