package components

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

// ListFormItem wraps a tview.List so it can be used inside a Form.
type ListFormItem struct {
	*tview.List
	label             string
	fieldWidth        int
	fieldHeight       int
	finished          func(tcell.Key)
	disabled          bool
	hasFocus          bool
	bgColor           tcell.Color
	textColor         tcell.Color
	focusedSelectedBg tcell.Color
	blurredSelectedBg tcell.Color
}

// NewListFormItem creates a new list-backed form item.
func NewListFormItem(list *tview.List) *ListFormItem {
	if list == nil {
		list = tview.NewList()
	}
	item := &ListFormItem{
		List:              list,
		fieldHeight:       tview.DefaultFormFieldHeight,
		focusedSelectedBg: tui.ProxmoxOrange,
		blurredSelectedBg: tcell.ColorDarkSlateGray,
	}
	item.List.SetInputCapture(item.inputCapture)
	return item
}

// SetLabel sets the optional label shown before the list within the form.
func (i *ListFormItem) SetLabel(label string) *ListFormItem {
	i.label = label
	return i
}

// SetFieldWidth configures the width used by the list within the form (0 = flexible).
func (i *ListFormItem) SetFieldWidth(width int) *ListFormItem {
	i.fieldWidth = width
	return i
}

// SetFieldHeight configures the height used by the list within the form.
func (i *ListFormItem) SetFieldHeight(height int) *ListFormItem {
	if height <= 0 {
		height = tview.DefaultFormFieldHeight
	}
	i.fieldHeight = height
	return i
}

// GetLabel implements tview.FormItem.
func (i *ListFormItem) GetLabel() string {
	return i.label
}

// SetFormAttributes implements tview.FormItem.
func (i *ListFormItem) SetFormAttributes(labelWidth int, labelColor, bgColor, fieldTextColor, fieldBgColor tcell.Color) tview.FormItem {
	i.bgColor = bgColor
	i.textColor = fieldTextColor
	i.List.
		SetMainTextColor(fieldTextColor).
		SetSecondaryTextColor(fieldTextColor).
		SetBackgroundColor(bgColor)
	return i
}

// GetFieldWidth implements tview.FormItem.
func (i *ListFormItem) GetFieldWidth() int {
	return i.fieldWidth
}

// GetFieldHeight implements tview.FormItem.
func (i *ListFormItem) GetFieldHeight() int {
	if i.fieldHeight <= 0 {
		return tview.DefaultFormFieldHeight
	}
	return i.fieldHeight
}

// SetFinishedFunc implements tview.FormItem.
func (i *ListFormItem) SetFinishedFunc(handler func(key tcell.Key)) tview.FormItem {
	i.finished = handler
	return i
}

// SetDisabled implements tview.FormItem.
func (i *ListFormItem) SetDisabled(disabled bool) tview.FormItem {
	i.disabled = disabled
	return i
}

func (i *ListFormItem) inputCapture(event *tcell.EventKey) *tcell.EventKey {
	if i.disabled || event == nil {
		return event
	}

	switch event.Key() {
	case tcell.KeyTab, tcell.KeyBacktab, tcell.KeyEscape:
		if i.finished != nil {
			i.finished(event.Key())
		}
		return nil
	case tcell.KeyUp:
		if i.finished != nil && i.List.GetItemCount() > 0 && i.List.GetCurrentItem() == 0 {
			i.finished(tcell.KeyBacktab)
			return nil
		}
	case tcell.KeyDown:
		count := i.List.GetItemCount()
		if i.finished != nil && count > 0 && i.List.GetCurrentItem() == count-1 {
			i.finished(tcell.KeyTab)
			return nil
		}
	}

	return event
}

// Focus is called when this primitive receives focus.
func (i *ListFormItem) Focus(delegate func(p tview.Primitive)) {
	i.hasFocus = true
	i.List.SetSelectedBackgroundColor(i.focusedSelectedBg)
	col := i.textColor
	if col == 0 {
		col = tcell.ColorWhite
	}
	i.List.SetSelectedTextColor(col)
	i.List.Focus(delegate)
}

// Blur is called when this primitive loses focus.
func (i *ListFormItem) Blur() {
	i.hasFocus = false
	bg := i.bgColor
	if bg == 0 {
		bg = i.blurredSelectedBg
	}
	i.List.SetSelectedBackgroundColor(bg)
	col := i.textColor
	if col == 0 {
		col = tcell.ColorWhite
	}
	i.List.SetSelectedTextColor(col)
	i.List.Blur()
}
