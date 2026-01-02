package components

import (
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestListFormItemInputCaptureNavigation(t *testing.T) {
	list := tview.NewList().
		AddItem("one", "", 0, nil).
		AddItem("two", "", 0, nil)

	item := NewListFormItem(list)

	var keys []tcell.Key
	item.SetFinishedFunc(func(key tcell.Key) {
		keys = append(keys, key)
	})

	if ev := item.inputCapture(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev != nil {
		t.Fatalf("tab should be consumed but got %#v", ev)
	}
	if len(keys) != 1 || keys[0] != tcell.KeyTab {
		t.Fatalf("expected tab callback, got %+v", keys)
	}

	list.SetCurrentItem(0)
	if ev := item.inputCapture(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)); ev != nil {
		t.Fatalf("up should be consumed at top but got %#v", ev)
	}
	if len(keys) != 2 || keys[1] != tcell.KeyBacktab {
		t.Fatalf("expected backtab on up at first item, got %+v", keys)
	}

	list.SetCurrentItem(list.GetItemCount() - 1)
	if ev := item.inputCapture(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)); ev != nil {
		t.Fatalf("down should be consumed at bottom but got %#v", ev)
	}
	if len(keys) != 3 || keys[2] != tcell.KeyTab {
		t.Fatalf("expected tab on down at last item, got %+v", keys)
	}

	item.SetDisabled(true)
	if ev := item.inputCapture(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev == nil {
		t.Fatalf("expected event to pass through when disabled")
	}
}

func TestListFormItemFocusAndBlur(t *testing.T) {
	list := tview.NewList().AddItem("one", "", 0, nil)
	item := NewListFormItem(list)
	item.SetFormAttributes(0, tcell.ColorBlue, tcell.ColorDarkRed, tcell.ColorGreen, tcell.ColorDarkBlue)

	item.Focus(func(p tview.Primitive) {})
	if !item.hasFocus {
		t.Fatalf("expected focus flag set after Focus call")
	}

	item.Blur()
	if item.hasFocus {
		t.Fatalf("expected focus flag to be cleared")
	}
}

func TestListFormItemFieldHeightDefaults(t *testing.T) {
	item := NewListFormItem(nil)
	if got := item.GetFieldHeight(); got != tview.DefaultFormFieldHeight {
		t.Fatalf("expected default height %d, got %d", tview.DefaultFormFieldHeight, got)
	}
	item.SetFieldHeight(5)
	if got := item.GetFieldHeight(); got != 5 {
		t.Fatalf("expected custom height 5, got %d", got)
	}
	item.SetFieldHeight(-1)
	if got := item.GetFieldHeight(); got != tview.DefaultFormFieldHeight {
		t.Fatalf("expected default height reset, got %d", got)
	}
}

func TestListFormItemLabelAndWidthAccessors(t *testing.T) {
	item := NewListFormItem(nil)
	item.SetLabel("Backups").SetFieldWidth(42)

	if got := item.GetLabel(); got != "Backups" {
		t.Fatalf("GetLabel()=%q; want %q", got, "Backups")
	}
	if got := item.GetFieldWidth(); got != 42 {
		t.Fatalf("GetFieldWidth()=%d; want %d", got, 42)
	}
}
