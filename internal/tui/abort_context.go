package tui

import (
	"context"
	"sync"
)

var (
	abortContextMu sync.RWMutex
	abortContext   context.Context
)

// SetAbortContext registers a process-wide context used to stop any running TUI
// app (tview) when the context is canceled (e.g. Ctrl+C).
//
// This is intentionally global so all TUIs behave consistently without each
// wizard needing bespoke signal handling.
func SetAbortContext(ctx context.Context) {
	abortContextMu.Lock()
	abortContext = ctx
	abortContextMu.Unlock()
}

func getAbortContext() context.Context {
	abortContextMu.RLock()
	ctx := abortContext
	abortContextMu.RUnlock()
	return ctx
}

func bindAbortContext(app *App) {
	ctx := getAbortContext()
	if ctx == nil {
		return
	}
	go func() {
		<-ctx.Done()
		app.Stop()
	}()
}
