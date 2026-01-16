package tui

import (
	"context"
	"testing"
	"time"

	"github.com/rivo/tview"
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

func TestNewApp_SetsThemeAndReturnsApplication(t *testing.T) {
	oldTheme := tview.Styles
	t.Cleanup(func() { tview.Styles = oldTheme })

	SetAbortContext(nil)

	app := NewApp()
	if app == nil || app.Application == nil {
		t.Fatalf("expected non-nil app and embedded Application")
	}

	if tview.Styles.BorderColor != ProxmoxOrange {
		t.Fatalf("BorderColor=%v want %v", tview.Styles.BorderColor, ProxmoxOrange)
	}
	if tview.Styles.TitleColor != ProxmoxOrange {
		t.Fatalf("TitleColor=%v want %v", tview.Styles.TitleColor, ProxmoxOrange)
	}
}

func TestAppStop_NilReceiverNoPanic(t *testing.T) {
	var app *App
	app.Stop()
}

func TestAppStop_DelegatesToEmbeddedApplication(t *testing.T) {
	app := &App{Application: tview.NewApplication()}
	app.Stop()
}

func TestSetRootWithTitle_SetsBoxTitleAndBorderColor(t *testing.T) {
	app := &App{Application: tview.NewApplication()}
	box := tview.NewBox()

	app.SetRootWithTitle(box, "Restore")

	if got := box.GetTitle(); got != " Restore " {
		t.Fatalf("title=%q want %q", got, " Restore ")
	}
	if got := box.GetBorderColor(); got != ProxmoxOrange {
		t.Fatalf("borderColor=%v want %v", got, ProxmoxOrange)
	}
}
