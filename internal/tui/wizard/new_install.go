package wizard

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

var confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
	return app.SetRoot(root, true).SetFocus(focus).Run()
}

// ConfirmNewInstall shows a TUI confirmation before wiping baseDir for --new-install.
func ConfirmNewInstall(baseDir string, buildSig string) (bool, error) {
	app := tui.NewApp()
	proceed := false

	// Header text (align with main install wizard)
	welcomeText := tview.NewTextView().
		SetText("Welcome to ProxSave Installation Wizard - By TIS24DEV\n\n" +
			"This wizard will guide you through configuring your backup system for Proxmox.\n" +
			"All settings can be changed later by editing the configuration file.").
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	// Build signature line
	buildSigText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", buildSig)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)
	buildSigText.SetBorder(false)

	// Navigation instructions
	navInstructions := tview.NewTextView().
		SetText("[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to open dropdowns | ←→ on buttons | ENTER to submit | Mouse clicks enabled").
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	navInstructions.SetBorder(false)

	// Separator
	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

	// Confirmation modal
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Base directory to reset:\n[yellow]%s[white]\n\nThis keeps [yellow]build/ env/ identity/[white]\nbut deletes everything else.\n\nContinue?", baseDir)).
		AddButtons([]string{"Continue", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if buttonLabel == "Continue" {
				proceed = true
			}
			app.Stop()
		})

	modal.SetBorder(true).
		SetTitle(" Confirm New Install ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.WarningYellow).
		SetBorderColor(tui.WarningYellow).
		SetBackgroundColor(tcell.ColorBlack)

	// Layout
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(welcomeText, 5, 0, false).
		AddItem(navInstructions, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(modal, 0, 1, true).
		AddItem(buildSigText, 1, 0, false)

	flex.SetBorder(true).
		SetTitle(" ProxSave New Install ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	// Run the app - ignore errors from normal app termination
	_ = confirmNewInstallRunner(app, flex, modal)

	return proceed, nil
}
