package logging

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestOpenLogFileTimeoutFallsBackToStdout: a wedged open (dead/stale mount) returns
// a timeout error and the logger degrades to stdout-only instead of hanging.
func TestOpenLogFileTimeoutFallsBackToStdout(t *testing.T) {
	prev := osOpenFile
	unblock := make(chan struct{})
	finished := make(chan struct{})
	// Order matters: unblock + wait for the abandoned worker to finish BEFORE
	// restoring the package var, so the worker's read of osOpenFile happens-before
	// the restore (otherwise -race flags it). The wait is BOUNDED so a regression
	// that never reaches the stub surfaces as a normal t.Error instead of wedging
	// CI; that timeout branch is a failure path (test already red) where the
	// happens-before for the restore is not guaranteed. We restore anyway to avoid
	// leaking the stub into later tests in the package.
	t.Cleanup(func() {
		close(unblock)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("timed out waiting for the abandoned open worker to finish")
		}
		osOpenFile = prev
	})
	osOpenFile = func(string, int, os.FileMode) (*os.File, error) {
		<-unblock
		close(finished)
		return nil, nil
	}

	logger := New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetIOTimeout(25 * time.Millisecond)

	start := time.Now()
	err := logger.OpenLogFile("/does/not/matter.log")
	if err == nil || !errors.Is(err, safefs.ErrTimeout) {
		t.Fatalf("OpenLogFile err = %v; want safefs.ErrTimeout", err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("OpenLogFile took too long: %s", time.Since(start))
	}
	if logger.GetLogFilePath() != "" {
		t.Fatal("no file sink expected after an open timeout")
	}
	logger.Info("still works")
	if !strings.Contains(buf.String(), "still works") {
		t.Fatalf("stdout logging must continue; buf=%q", buf.String())
	}
}

// TestLogWriteTimeoutDisablesSink: a mid-run write that wedges (mount died after a
// successful open) must NOT deadlock logging; the sink is disabled after one
// timeout and subsequent lines skip the file.
func TestLogWriteTimeoutDisablesSink(t *testing.T) {
	dir := t.TempDir()
	logger := New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := logger.OpenLogFile(filepath.Join(dir, "x.log")); err != nil {
		t.Fatal(err)
	}
	logger.SetIOTimeout(25 * time.Millisecond)

	prev := fileWrite
	var mu sync.Mutex
	calls := 0
	unblock := make(chan struct{})
	finished := make(chan struct{})
	// Order matters: unblock + wait for the abandoned worker before restoring the
	// package var (happens-before, otherwise -race flags the var access). The wait
	// is BOUNDED so a regression that never reaches the stub surfaces as a normal
	// t.Error instead of wedging CI; that timeout branch is a failure path (test
	// already red) where the happens-before is not guaranteed. We restore anyway to
	// avoid leaking the stub into later tests in the package.
	t.Cleanup(func() {
		close(unblock)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("timed out waiting for the abandoned write worker to finish")
		}
		fileWrite = prev
	})
	fileWrite = func(w io.Writer, s string) (int, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			<-unblock // first write wedges like a dead mount
			close(finished)
			return 0, nil
		}
		return prev(w, s)
	}

	done := make(chan struct{})
	go func() { logger.Info("first"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("log write deadlocked on a wedged mount")
	}

	if logger.GetLogFilePath() != "" {
		t.Fatal("file sink should be disabled after a write timeout")
	}
	if !logger.HasWarnings() {
		t.Fatal("disabling the sink should record a warning")
	}

	logger.Info("second")
	mu.Lock()
	c := calls
	mu.Unlock()
	if c != 1 {
		t.Fatalf("fileWrite called %d times; want 1 (sink disabled after timeout)", c)
	}

	// Closing after a disable is a no-op (logFile already nil) and must not panic.
	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile after disable: %v", err)
	}
}

// TestLogFileHealthyBoundedStillWrites: with a positive timeout on a healthy mount,
// writes still go to the file and the sink is not disabled.
func TestLogFileHealthyBoundedStillWrites(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.log")
	logger := New(types.LogLevelInfo, false)
	logger.SetOutput(&bytes.Buffer{})
	logger.SetIOTimeout(2 * time.Second)
	if err := logger.OpenLogFile(p); err != nil {
		t.Fatal(err)
	}
	logger.Info("alpha")
	logger.Warning("beta")
	if err := logger.CloseLogFile(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "alpha") || !strings.Contains(s, "beta") {
		t.Fatalf("bounded healthy writes missing from file: %q", s)
	}
	if logger.fileSinkDisabled {
		t.Fatal("a healthy run must not disable the sink")
	}
}

// TestLoggerConcurrentBoundedNoRace exercises the bounded write path under
// concurrency (run with -race).
func TestLoggerConcurrentBoundedNoRace(t *testing.T) {
	dir := t.TempDir()
	logger := New(types.LogLevelInfo, false)
	logger.SetOutput(&bytes.Buffer{})
	logger.SetIOTimeout(2 * time.Second)
	if err := logger.OpenLogFile(filepath.Join(dir, "c.log")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				logger.Info("x %d", i)
			}
		}()
	}
	wg.Wait()
	if err := logger.CloseLogFile(); err != nil {
		t.Fatal(err)
	}
}
