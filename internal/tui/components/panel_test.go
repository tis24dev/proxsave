package components

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestPanelDefaultStyling(t *testing.T) {
	panel := NewPanel()
	if panel.GetBorderColor() != tui.ProxmoxOrange {
		t.Fatalf("expected border color %v, got %v", tui.ProxmoxOrange, panel.GetBorderColor())
	}
	if panel.GetBackgroundColor() != tcell.ColorBlack {
		t.Fatalf("expected black background, got %v", panel.GetBackgroundColor())
	}
}

func TestPanelTitleAndStatus(t *testing.T) {
	panel := NewPanel().SetTitle("Status").SetStatus("success")
	if got := panel.GetTitle(); got != " Status  "+tui.StatusSymbol("success") {
		t.Fatalf("unexpected title %q", got)
	}
}

func TestPanelVariantsAdjustColors(t *testing.T) {
	info := InfoPanel("Info", "msg")
	if info.GetBackgroundColor() != tui.ProxmoxDark {
		t.Fatalf("info panel background should be %v", tui.ProxmoxDark)
	}

	success := SuccessPanel("OK", "done")
	if success.GetBorderColor() != tui.SuccessGreen {
		t.Fatalf("success panel border should be %v", tui.SuccessGreen)
	}

	errPanel := ErrorPanel("Err", "fail")
	if errPanel.GetBorderColor() != tui.ErrorRed {
		t.Fatalf("error panel border should be %v", tui.ErrorRed)
	}

	warnPanel := WarningPanel("Warn", "look")
	if warnPanel.GetBorderColor() != tui.WarningYellow {
		t.Fatalf("warning panel border should be %v", tui.WarningYellow)
	}
}
