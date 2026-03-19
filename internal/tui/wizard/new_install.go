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

func formatPreservedEntries(entries []string) string {
	formatted := make([]string, 0, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if !strings.HasSuffix(trimmed, "/") {
			trimmed += "/"
		}
		formatted = append(formatted, trimmed)
	}
	if len(formatted) == 0 {
		return "(none)"
	}
	return strings.Join(formatted, " ")
}

// ConfirmNewInstall shows a TUI confirmation before wiping baseDir for --new-install.
func ConfirmNewInstall(baseDir string, buildSig string, preservedEntries []string) (bool, error) {
	app := tui.NewApp()
	proceed := false
	preservedText := formatPreservedEntries(preservedEntries)

	// Confirmation modal
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Base directory to reset:\n[yellow]%s[white]\n\nThis keeps [yellow]%s[white]\nbut deletes everything else.\n\nContinue?", baseDir, preservedText)).
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

	flex := buildWizardScreen(
		"ProxSave New Install",
		"Welcome to ProxSave Installation Wizard - By TIS24DEV\n\n"+
			"This wizard will guide you through configuring your backup system for Proxmox.\n"+
			"All settings can be changed later by editing the configuration file.",
		"[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to open dropdowns | ←→ on buttons | ENTER to submit | Mouse clicks enabled",
		"",
		buildSig,
		modal,
	)

	if err := confirmNewInstallRunner(app, flex, modal); err != nil {
		return false, err
	}

	return proceed, nil
}
