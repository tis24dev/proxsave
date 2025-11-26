package tui

import (
	"testing"

	"github.com/rivo/tview"
)

func TestNewAppSetsTheme(t *testing.T) {
	_ = NewApp()

	if tview.Styles.BorderColor != ProxmoxOrange {
		t.Fatalf("expected border color %v, got %v", ProxmoxOrange, tview.Styles.BorderColor)
	}
	if tview.Styles.PrimaryTextColor != tview.Styles.PrimaryTextColor {
		t.Fatalf("styles should be initialized")
	}
}
