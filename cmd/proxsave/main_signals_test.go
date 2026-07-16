package main

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// F01-02: an external SIGINT/SIGTERM must cancel the run context WITHOUT
// closing os.Stdin. Closing it races bubbletea's term.Restore(os.Stdin.Fd())
// and leaves the terminal in raw mode. There is no PTY in a unit test, so the
// proxy assertion is that os.Stdin stays open after the handler fires.
func TestSetupRunContextDoesNotCloseStdinOnSignal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(true)

	sigChan := make(chan os.Signal, 1)
	ctx, cancel := setupRunContextWithSignals(bootstrap, sigChan)
	t.Cleanup(cancel)

	sigChan <- syscall.SIGTERM

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("signal handler did not cancel the context")
	}

	// The handler closes os.Stdin (pre-fix) right after cancel(), in the same
	// goroutine. Poll long enough that the pre-fix close always lands (serial
	// tests, no CPU contention); the fixed code never closes it, so the whole
	// window elapses with stdin open.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stdin.Stat(); statErr != nil {
			t.Fatalf("signal handler closed os.Stdin (%v); it must stay open so bubbletea can restore the terminal", statErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
