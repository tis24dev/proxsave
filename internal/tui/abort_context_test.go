package tui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func newSimulationApp(t *testing.T) (*App, tcell.SimulationScreen, <-chan struct{}) {
	t.Helper()
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen.Init: %v", err)
	}

	app := NewApp()
	started := make(chan struct{})
	var startedOnce sync.Once
	app.SetAfterDrawFunc(func(screen tcell.Screen) {
		startedOnce.Do(func() {
			close(started)
		})
	})
	app.SetScreen(screen)
	app.SetRoot(tview.NewBox(), true)
	return app, screen, started
}

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

func TestAppRunWithContext_CanceledBeforeRun(t *testing.T) {
	app := &App{Application: tview.NewApplication()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := app.RunWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want %v", err, context.Canceled)
	}
}

func TestAppRunWithContext_NilReceiverReturnsNil(t *testing.T) {
	var app *App
	if err := app.RunWithContext(context.Background()); err != nil {
		t.Fatalf("err=%v want nil", err)
	}
}

func TestAppRunWithContext_NilContextRunsUntilStopped(t *testing.T) {
	app, _, started := newSimulationApp(t)
	done := make(chan error, 1)

	go func() {
		done <- app.RunWithContext(nil)
	}()

	select {
	case err := <-done:
		t.Fatalf("RunWithContext(nil) returned before app started: %v", err)
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for app to start")
	}

	select {
	case err := <-done:
		t.Fatalf("RunWithContext(nil) returned before Stop: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	app.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err=%v want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RunWithContext(nil) to return after Stop")
	}
}

func TestAppRunWithContext_ReturnsNilWhenStoppedWithoutCancellation(t *testing.T) {
	app, _, started := newSimulationApp(t)
	done := make(chan error, 1)

	go func() {
		done <- app.RunWithContext(context.Background())
	}()

	select {
	case err := <-done:
		t.Fatalf("RunWithContext(context.Background()) returned before app started: %v", err)
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for app to start")
	}

	select {
	case err := <-done:
		t.Fatalf("RunWithContext(context.Background()) returned before Stop: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	app.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err=%v want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for RunWithContext(context.Background()) to return after Stop")
	}
}

func TestAppRunWithContext_StopsOnCancel(t *testing.T) {
	app, _, _ := newSimulationApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := app.RunWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want %v", err, context.Canceled)
	}
}

func TestAppRunWithContext_PropagatesRunErrorWithoutCancellation(t *testing.T) {
	app, _, _ := newSimulationApp(t)
	runErr := errors.New("run failed")
	eventErr := tcell.NewEventError(runErr)

	go func() {
		time.Sleep(50 * time.Millisecond)
		app.QueueEvent(eventErr)
	}()

	if err := app.RunWithContext(context.Background()); err != eventErr {
		t.Fatalf("err=%v want %v", err, eventErr)
	}
}

func TestAppRunWithContext_PrefersContextErrorWhenCanceledDuringRunError(t *testing.T) {
	app, _, _ := newSimulationApp(t)
	runErr := errors.New("run failed")

	var stopOnce sync.Once
	app.stopHook = func() {
		stopOnce.Do(func() {
			app.stopHook = nil
			app.QueueEvent(tcell.NewEventError(runErr))
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := app.RunWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want %v", err, context.Canceled)
	}
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
