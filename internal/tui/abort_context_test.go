package tui

import (
	"context"
	"testing"
	"time"
)

func TestSetAbortContext_GetAbortContextRoundTrip(t *testing.T) {
	SetAbortContext(nil)
	if got := getAbortContext(); got != nil {
		t.Fatalf("expected nil abort context, got %v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	SetAbortContext(ctx)
	if got := getAbortContext(); got != ctx {
		t.Fatalf("expected stored context to match")
	}

	SetAbortContext(nil)
	if got := getAbortContext(); got != nil {
		t.Fatalf("expected abort context to be cleared, got %v", got)
	}
}

func TestBindAbortContext_StopsAppOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	SetAbortContext(ctx)
	t.Cleanup(func() { SetAbortContext(nil) })

	stopped := make(chan struct{})
	app := &App{
		stopHook: func() { close(stopped) },
	}

	bindAbortContext(app)
	cancel()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected app.Stop to be called after context cancellation")
	}
}

func TestBindAbortContext_NoContextNoop(t *testing.T) {
	SetAbortContext(nil)

	stopped := make(chan struct{})
	app := &App{
		stopHook: func() { close(stopped) },
	}

	bindAbortContext(app)

	select {
	case <-stopped:
		t.Fatalf("did not expect app.Stop to be called without abort context")
	case <-time.After(50 * time.Millisecond):
	}
}
