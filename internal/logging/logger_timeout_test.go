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

// TestLogWriteTimeoutStrikeCounter: a single transient write timeout must NOT
// disable the sink; only logSinkTimeoutStrikes CONSECUTIVE timeouts latch it, and
// a successful write in between resets the count (F11-04).
func TestLogWriteTimeoutStrikeCounter(t *testing.T) {
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
	wedge := true
	// parked accounts for EVERY worker goroutine that safefs.Run spawns for a
	// fileWrite call while the test is active. The stub's non-wedged branch
	// returns instantly (len(s), nil) with no file I/O at all, so that worker
	// always finishes well inside the tight 25ms SetIOTimeout - it can never be
	// abandoned by safefs, which keeps the worker count per Info call a static,
	// test-controlled fact instead of something that depends on real I/O timing
	// under -race/load. Only the wedged (<-unblock) branch can be abandoned, and
	// that is deliberate (see the strike/latch calls below). Every Add(n) below
	// happens synchronously in this (the test) goroutine, strictly before the
	// logger.Info call that spawns the corresponding worker(s), and strictly
	// before Cleanup's parked.Wait() - so Add always happens-before Wait; Done()
	// fires unconditionally (deferred, both stub branches) exactly once per
	// worker, so Wait cannot return until every worker has finished reading
	// fileWrite.
	var parked sync.WaitGroup
	unblock := make(chan struct{})
	t.Cleanup(func() {
		close(unblock)
		drained := make(chan struct{})
		go func() { parked.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(time.Second):
			t.Error("timed out waiting for abandoned write workers")
		}
		fileWrite = prev
	})
	fileWrite = func(w io.Writer, s string) (int, error) {
		defer parked.Done() // unconditional: covers both the wedged and the healthy branch
		mu.Lock()
		park := wedge
		mu.Unlock()
		if park {
			<-unblock // parked like a dead mount; safefs abandons this worker at ~25ms
			return 0, nil
		}
		// Instant, deterministic success: no real file I/O (prev is unused here).
		// This test asserts sink STATE (GetLogFilePath/HasWarnings), never on-disk
		// content, so it does not need a real write, and returning immediately
		// guarantees this branch always lands inside the 25ms timeout - the
		// "healthy-reset" write below can never be timed out or abandoned.
		return len(s), nil
	}
	// Each Info call while the sink is armed spawns exactly one fileWrite worker,
	// EXCEPT the strike that crosses the threshold: that call also drives
	// disableFileSinkLocked's best-effort marker write, a second, nested
	// safefs.Run/fileWrite invocation. Account the exact worker count before the
	// call (synchronously, in this goroutine) so parked.Add always happens-before
	// the eventual parked.Wait in Cleanup.
	wedgeInfo := func(msg string, workers int) { parked.Add(workers); logger.Info("%s", msg) }

	// Strikes 1..(N-1): sink stays armed.
	for i := 0; i < logSinkTimeoutStrikes-1; i++ {
		wedgeInfo("strike", 1)
	}
	if logger.GetLogFilePath() == "" {
		t.Fatalf("sink must stay armed after %d timeouts (< %d strikes)", logSinkTimeoutStrikes-1, logSinkTimeoutStrikes)
	}

	// A successful write resets the strike counter. It still spawns one fileWrite
	// worker (the healthy, non-wedged write), which must be tracked too. That
	// worker returns instantly and never touches the filesystem, so it always
	// completes inside the 25ms timeout: unlike a wedged write it can never be
	// abandoned, so exactly one completing worker is spawned here and Add(1) is
	// always correct.
	mu.Lock()
	wedge = false
	mu.Unlock()
	wedgeInfo("healthy-reset", 1)
	if logger.GetLogFilePath() == "" {
		t.Fatal("a successful write must keep the sink armed")
	}

	// logSinkTimeoutStrikes consecutive timeouts now latch the sink.
	mu.Lock()
	wedge = true
	mu.Unlock()
	for i := 0; i < logSinkTimeoutStrikes; i++ {
		parks := 1
		if i == logSinkTimeoutStrikes-1 {
			parks = 2 // this strike also fires the (wedged) marker write
		}
		wedgeInfo("latch", parks)
	}
	if logger.GetLogFilePath() != "" {
		t.Fatalf("sink must be disabled after %d consecutive timeouts", logSinkTimeoutStrikes)
	}
	if !logger.HasWarnings() {
		t.Fatal("disabling the sink should record a warning")
	}
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

// TestLogSinkDisableWritesMarker: when the sink latches, a best-effort marker line
// is written to the on-disk log so the shipped copy self-documents the truncation
// (F11-04). Here the strike writes wedge, then the mount "recovers" exactly for the
// marker write, so the marker lands on disk.
func TestLogSinkDisableWritesMarker(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "x.log")
	logger := New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatal(err)
	}
	// Generous on purpose (unlike the tight 25ms used elsewhere in this file): the
	// strike writes below are wedged on a channel, so they still time out at this
	// bound no matter how large it is, but the marker write is a REAL fmt.Fprint to
	// a tmpfile that this test's content assertion depends on landing on disk. A
	// wider bound gives that real write ample margin under -race/host load so it is
	// never abandoned, without changing anything about how/whether the wedged
	// strikes time out.
	logger.SetIOTimeout(200 * time.Millisecond)

	prev := fileWrite
	var mu sync.Mutex
	// parked accounts for EVERY worker safefs.Run spawns for a fileWrite call, not
	// just the wedged strike writes. The marker write that fires on the crossing
	// call takes the REAL (non-wedged) branch below by design (that is the whole
	// point of the test: the marker must land on disk). Its own Done() fires
	// unconditionally as soon as that call returns, regardless of whether safefs.Run
	// on the caller side decided to time out - disableFileSinkLocked ignores the
	// marker call's error and never issues a further nested fileWrite call, so this
	// accounting cannot go negative even if the marker write is ever abandoned.
	// Every Add(n) happens synchronously in this goroutine before the logger.Info
	// call that spawns the corresponding worker(s), and Done() is unconditional
	// (deferred, both branches) so Wait cannot return until every worker has
	// finished reading fileWrite.
	var parked sync.WaitGroup
	unblock := make(chan struct{})
	writeCount := 0
	t.Cleanup(func() {
		close(unblock)
		drained := make(chan struct{})
		go func() { parked.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(time.Second):
			t.Error("timed out waiting for abandoned write workers")
		}
		fileWrite = prev
	})
	fileWrite = func(w io.Writer, s string) (int, error) {
		defer parked.Done() // unconditional: covers both the wedged and the real-write branch
		mu.Lock()
		n := writeCount
		writeCount++
		mu.Unlock()
		if n < logSinkTimeoutStrikes {
			<-unblock // the strike writes wedge and time out
			return 0, nil
		}
		return prev(w, s) // the marker write (and beyond) go through
	}

	// Each strike Info call spawns exactly one fileWrite worker, EXCEPT the one
	// that crosses the threshold: that call also drives disableFileSinkLocked's
	// marker write, a second, nested fileWrite invocation (the real-write branch
	// above). Account the exact worker count before the call.
	for i := 0; i < logSinkTimeoutStrikes; i++ {
		workers := 1
		if i == logSinkTimeoutStrikes-1 {
			workers = 2 // this strike also fires the marker write
		}
		parked.Add(workers)
		logger.Info("strike")
	}
	if logger.GetLogFilePath() != "" {
		t.Fatalf("sink must be disabled after %d strikes", logSinkTimeoutStrikes)
	}
	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "on-disk log truncated here") {
		t.Fatalf("expected the truncation marker in the on-disk log, got %q", string(data))
	}
}
