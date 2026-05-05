// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
)

func setupRunContext(bootstrap *logging.BootstrapLogger) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	tui.SetAbortContext(ctx)

	var closeStdinOnce sync.Once
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		defer signal.Stop(sigChan)
		select {
		case sig := <-sigChan:
			logging.DebugStepBootstrap(bootstrap, "signal", "received=%v", sig)
			bootstrap.Info("\nReceived signal %v, initiating graceful shutdown...", sig)
			cancel()
			closeStdinOnce.Do(func() {
				if file := os.Stdin; file != nil {
					_ = file.Close()
				}
			})
		case <-ctx.Done():
		}
	}()

	return ctx, cancel
}
