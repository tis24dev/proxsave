// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

func setupRunContext(bootstrap *logging.BootstrapLogger) (context.Context, context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	return setupRunContextWithSignals(bootstrap, sigChan)
}

// setupRunContextWithSignals wires an external SIGINT/SIGTERM to ctx
// cancellation. It deliberately does NOT close os.Stdin: cancel() already
// tears the bubbletea TUI down (tea.WithContext restores the terminal) and
// unblocks every CLI prompt (internal/input selects on ctx.Done()). Closing
// os.Stdin instead races bubbletea's term.Restore(os.Stdin.Fd()) and leaves
// the terminal in raw mode on an external signal (F01-02). The sigChan seam
// lets tests drive the handler without a real process signal.
func setupRunContextWithSignals(bootstrap *logging.BootstrapLogger, sigChan chan os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer signal.Stop(sigChan)
		select {
		case sig := <-sigChan:
			logging.DebugStepBootstrap(bootstrap, "signal", "received=%v", sig)
			bootstrap.Info("\nReceived signal %v, initiating graceful shutdown...", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, cancel
}
