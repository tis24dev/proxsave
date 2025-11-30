package components

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

var modalCreatedHook func(*tview.Modal)

func notifyModalCreated(modal *tview.Modal) {
	if modalCreatedHook != nil {
		modalCreatedHook(modal)
	}
}

// ShowConfirm displays a Yes/No confirmation modal
func ShowConfirm(app *tui.App, title, message string, onYes, onNo func()) {
	// Add navigation instructions if not already present
	if !strings.Contains(message, "[yellow]") {
		message = message + "\n\n[yellow]Use TAB or ←→ Arrows to switch | Press ENTER to select[white]"
	}

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Yes", "No"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if buttonLabel == "Yes" && onYes != nil {
				onYes()
			} else if buttonLabel == "No" && onNo != nil {
				onNo()
			}
			app.Stop()
		})

	notifyModalCreated(modal)

	modal.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetRoot(modal, true).SetFocus(modal)
}

// ShowInfo displays an informational modal
func ShowInfo(app *tui.App, title, message string) {
	message = message + "\n\n[yellow]Press ENTER to continue[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.Stop()
		})

	notifyModalCreated(modal)

	modal.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.InfoBlue).
		SetBorderColor(tui.InfoBlue).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetRoot(modal, true).SetFocus(modal)
}

// ShowSuccess displays a success modal
func ShowSuccess(app *tui.App, title, message string) {
	message = tui.SymbolSuccess + " " + message + "\n\n[yellow]Press ENTER to continue[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.Stop()
		})

	notifyModalCreated(modal)

	modal.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.SuccessGreen).
		SetBorderColor(tui.SuccessGreen).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetRoot(modal, true).SetFocus(modal)
}

// ShowError displays an error modal
func ShowError(app *tui.App, title, message string) {
	message = tui.SymbolError + " " + message + "\n\n[yellow]Press ENTER to continue[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.Stop()
		})

	notifyModalCreated(modal)

	modal.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ErrorRed).
		SetBorderColor(tui.ErrorRed).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetRoot(modal, true).SetFocus(modal)
}

// ShowErrorInline displays an error modal that returns to the previous screen instead of stopping the app
func ShowErrorInline(app *tui.App, title, message string, returnTo tview.Primitive) {
	message = tui.SymbolError + " " + message + "\n\n[yellow]Press ENTER to continue[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.SetRoot(returnTo, true).SetFocus(returnTo)
		})

	notifyModalCreated(modal)

	modal.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ErrorRed).
		SetBorderColor(tui.ErrorRed).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetRoot(modal, true).SetFocus(modal)
}

// ShowWarning displays a warning modal
func ShowWarning(app *tui.App, title, message string) {
	message = tui.SymbolWarning + " " + message + "\n\n[yellow]Press ENTER to continue[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.Stop()
		})

	notifyModalCreated(modal)

	modal.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.WarningYellow).
		SetBorderColor(tui.WarningYellow).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetRoot(modal, true).SetFocus(modal)
}
