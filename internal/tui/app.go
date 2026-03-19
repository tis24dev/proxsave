package tui

import (
	"context"
	"sync/atomic"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// App wraps tview.Application with Proxmox-specific configuration
type App struct {
	*tview.Application
	stopHook func()
}

// NewApp creates a new TUI application with Proxmox theme
func NewApp() *App {
	app := &App{
		Application: tview.NewApplication(),
	}

	// Enable mouse support for easier navigation/clicks.
	app.EnableMouse(true)

	// Set global theme colors
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorBlack
	tview.Styles.ContrastBackgroundColor = tcell.ColorBlack
	tview.Styles.MoreContrastBackgroundColor = tcell.ColorDarkSlateGray
	tview.Styles.BorderColor = ProxmoxOrange
	tview.Styles.TitleColor = ProxmoxOrange
	tview.Styles.GraphicsColor = ProxmoxOrange
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.SecondaryTextColor = tcell.ColorLightGray
	tview.Styles.TertiaryTextColor = tcell.ColorGray
	tview.Styles.InverseTextColor = tcell.ColorBlack
	tview.Styles.ContrastSecondaryTextColor = tcell.ColorWhite

	bindAbortContext(app)
	return app
}

func (a *App) Stop() {
	if a == nil {
		return
	}
	if a.stopHook != nil {
		a.stopHook()
		return
	}
	if a.Application != nil {
		a.Application.Stop()
	}
}

func (a *App) RunWithContext(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		return a.Run()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan struct{})
	defer close(done)

	var state atomic.Int32
	go func() {
		select {
		case <-ctx.Done():
			if state.CompareAndSwap(0, 1) {
				a.Stop()
			}
		case <-done:
		}
	}()

	if err := a.Run(); err != nil {
		if state.CompareAndSwap(0, 2) {
			return err
		}
		return ctx.Err()
	}
	if state.CompareAndSwap(0, 2) {
		return nil
	}
	return ctx.Err()
}

// SetRootWithTitle sets the root primitive with a styled title
func (a *App) SetRootWithTitle(root tview.Primitive, title string) *App {
	if box, ok := root.(*tview.Box); ok {
		box.SetBorder(true).
			SetTitle(" " + title + " ").
			SetTitleAlign(tview.AlignCenter).
			SetTitleColor(ProxmoxOrange).
			SetBorderColor(ProxmoxOrange)
	}
	a.SetRoot(root, true)
	return a
}
