package orchestrator

import (
	"github.com/gdamore/tcell/v2"

	"github.com/tis24dev/proxsave/internal/tui/components"
)

func enableFormNavigation(form *components.Form, dropdownOpen *bool) {
	if form == nil || form.Form == nil {
		return
	}
	form.Form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event == nil {
			return event
		}
		if dropdownOpen != nil && *dropdownOpen {
			return event
		}

		formItemIndex, buttonIndex := form.Form.GetFocusedItemIndex()
		isOnButton := formItemIndex < 0 && buttonIndex >= 0
		isOnField := formItemIndex >= 0

		if isOnButton {
			switch event.Key() {
			case tcell.KeyLeft, tcell.KeyUp:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			case tcell.KeyRight, tcell.KeyDown:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			}
		} else if isOnField {
			// If focused item is a ListFormItem, let it handle navigation internally.
			if _, ok := form.Form.GetFormItem(formItemIndex).(*components.ListFormItem); ok {
				return event
			}
			// For other form fields, convert arrows to tab navigation.
			switch event.Key() {
			case tcell.KeyUp:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			case tcell.KeyDown:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			}
		}
		return event
	})
}
