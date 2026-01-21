package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestNewAppSetsTheme(t *testing.T) {
	_ = NewApp()

	if tview.Styles.BorderColor != ProxmoxOrange {
		t.Fatalf("expected border color %v, got %v", ProxmoxOrange, tview.Styles.BorderColor)
	}
	if tview.Styles.PrimaryTextColor != tcell.ColorWhite {
		t.Fatalf("expected primary text color %v, got %v", tcell.ColorWhite, tview.Styles.PrimaryTextColor)
	}
}

func TestSetRootWithTitleStylesBox(t *testing.T) {
	app := NewApp()
	box := tview.NewBox()

	got := app.SetRootWithTitle(box, "Hello")
	if got != app {
		t.Fatalf("expected SetRootWithTitle to return app pointer")
	}
	if box.GetTitle() != " Hello " {
		t.Fatalf("title=%q; want %q", box.GetTitle(), " Hello ")
	}
	if box.GetBorderColor() != ProxmoxOrange {
		t.Fatalf("border color=%v; want %v", box.GetBorderColor(), ProxmoxOrange)
	}
}
