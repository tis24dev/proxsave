package tui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func primitiveContainsText(p tview.Primitive, want string) bool {
	switch v := p.(type) {
	case nil:
		return false
	case *tview.TextView:
		return strings.Contains(v.GetText(false), want)
	case *tview.Flex:
		for i := 0; i < v.GetItemCount(); i++ {
			if primitiveContainsText(v.GetItem(i), want) {
				return true
			}
		}
	}
	return false
}

func TestBuildScreenOmitsEmptyOptionalFooters(t *testing.T) {
	page := BuildScreen(ScreenSpec{
		Title:           "Title",
		HeaderText:      "Header",
		NavText:         "Navigation",
		ConfigPath:      "",
		BuildSig:        "sig",
		TitleColor:      ProxmoxOrange,
		BorderColor:     ProxmoxOrange,
		BackgroundColor: tcell.ColorBlack,
	}, tview.NewBox())

	if primitiveContainsText(page, "Configuration file:") {
		t.Fatalf("did not expect configuration footer when ConfigPath is empty")
	}
	if !primitiveContainsText(page, "Build Signature:") {
		t.Fatalf("expected build signature footer")
	}
}
