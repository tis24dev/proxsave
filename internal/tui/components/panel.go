package components

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

// Panel is a styled box with Proxmox theming
type Panel struct {
	*tview.Box
}

// NewPanel creates a new panel with Proxmox styling
func NewPanel() *Panel {
	box := tview.NewBox().
		SetBorder(true).
		SetBorderColor(tui.ProxmoxOrange).
		SetTitleColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	return &Panel{Box: box}
}

// SetTitle sets the panel title
func (p *Panel) SetTitle(title string) *Panel {
	p.Box.SetTitle(" " + title + " ")
	return p
}

// SetStatus sets a status indicator in the title
func (p *Panel) SetStatus(status string) *Panel {
	symbol := tui.StatusSymbol(status)

	title := p.GetTitle()
	p.Box.SetTitle(title + " " + symbol)
	return p
}

// InfoPanel creates a styled info panel
func InfoPanel(title, message string) *Panel {
	panel := NewPanel().SetTitle(title)
	panel.SetBackgroundColor(tui.ProxmoxDark)
	return panel
}

// SuccessPanel creates a success-styled panel
func SuccessPanel(title, message string) *Panel {
	panel := NewPanel().SetTitle(title)
	panel.SetBorderColor(tui.SuccessGreen)
	panel.SetTitleColor(tui.SuccessGreen)
	return panel
}

// ErrorPanel creates an error-styled panel
func ErrorPanel(title, message string) *Panel {
	panel := NewPanel().SetTitle(title)
	panel.SetBorderColor(tui.ErrorRed)
	panel.SetTitleColor(tui.ErrorRed)
	return panel
}

// WarningPanel creates a warning-styled panel
func WarningPanel(title, message string) *Panel {
	panel := NewPanel().SetTitle(title)
	panel.SetBorderColor(tui.WarningYellow)
	panel.SetTitleColor(tui.WarningYellow)
	return panel
}
