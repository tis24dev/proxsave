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
