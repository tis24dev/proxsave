package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

var (
	postInstallAuditWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return app.SetRoot(root, true).SetFocus(focus).Run()
	}
)

type PostInstallAuditResult struct {
	// Ran indicates whether the user chose to run the post-install check.
	Ran bool
	// Suggestions contains the disable suggestions extracted from the dry-run output.
	Suggestions []PostInstallAuditSuggestion
	// AppliedKeys contains the keys written as KEY=false into backup.env.
	AppliedKeys []string
	// CollectErr is set when the dry-run/suggestion collection failed.
	CollectErr error
}

// RunPostInstallAuditWizard runs an optional post-installation check that:
//  1. runs proxsave --dry-run
//  2. extracts actionable "set KEY=false" hints from warnings
//  3. lets the user disable unused BACKUP_* collectors in backup.env
//
// It returns the audit result. Errors are returned only for unexpected failures
// (e.g., UI setup issues).
func RunPostInstallAuditWizard(ctx context.Context, execPath, configPath, buildSig string) (result PostInstallAuditResult, err error) {
	app := tui.NewApp()

	titleText := tview.NewTextView().
		SetText("ProxSave - Post-install Check\n\n" +
			"Detect optional components that are enabled but not configured on this node.\n" +
			"This helps reduce WARNING noise and exit code 1 runs when features are unused.\n").
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	titleText.SetBorder(false)

	nav := tview.NewTextView().
		SetText("[yellow]Navigation:[white] ↑↓ to move | ENTER/SPACE to toggle | ←→ on buttons | ENTER to select").
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	nav.SetBorder(false)

	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

	configPathText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Configuration file:[white] %s", configPath)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	configPathText.SetBorder(false)

	buildSigText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", buildSig)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	buildSigText.SetBorder(false)

	pages := tview.NewPages()

	confirmRun := false
	var mu sync.Mutex
	var collectedSuggestions []PostInstallAuditSuggestion
	var collectErr error
	applied := []string{}
	confirm := tview.NewModal().
		SetText("Run the post-install check now?\n\n" +
			"ProxSave will execute a dry-run and collect WARNING messages that include a hint like:\n" +
			"  set BACKUP_CLUSTER_CONFIG=false to disable\n\n" +
			"You can then choose which optional components to disable.\n").
		AddButtons([]string{"Run check", "Skip"}).
		SetDoneFunc(func(_ int, label string) {
			confirmRun = (label == "Run check")
			if !confirmRun {
				app.Stop()
				return
			}
			pages.SwitchToPage("running")
			go func() {
				suggestions, suggestionErr := CollectPostInstallDisableSuggestions(ctx, execPath, configPath)
				app.QueueUpdateDraw(func() {
					mu.Lock()
					collectedSuggestions = suggestions
					collectErr = suggestionErr
					mu.Unlock()
					if suggestionErr != nil {
						showAuditDoneModal(app, pages, "Post-install check failed:\n\n"+suggestionErr.Error())
						return
					}
					if len(suggestions) == 0 {
						showAuditDoneModal(app, pages, "No unused components detected.\n\nNo changes are required.")
						return
					}
					showAuditReview(app, pages, configPath, suggestions, &applied)
				})
			}()
		})

	confirm.SetBorder(true).
		SetTitle(" Post-install Check ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	running := tview.NewTextView().
		SetText("Running dry-run...\n\nPlease wait. This may take a minute.").
		SetTextColor(tcell.ColorWhite).
		SetTextAlign(tview.AlignCenter)
	running.SetBorder(true).
		SetTitle(" Running ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.WarningYellow).
		SetBorderColor(tui.WarningYellow).
		SetBackgroundColor(tcell.ColorBlack)

	pages.AddPage("confirm", confirm, true, true)
	pages.AddPage("running", running, true, false)

	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(titleText, 5, 0, false).
		AddItem(nav, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(pages, 0, 1, true).
		AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	layout.SetBorder(true).
		SetTitle(" ProxSave ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	if runErr := postInstallAuditWizardRunner(app, layout, confirm); runErr != nil {
		return PostInstallAuditResult{}, runErr
	}

	result.Ran = confirmRun
	mu.Lock()
	result.Suggestions = collectedSuggestions
	result.CollectErr = collectErr
	mu.Unlock()
	result.AppliedKeys = applied
	return result, nil
}

func showAuditDoneModal(app *tui.App, pages *tview.Pages, message string) {
	done := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Continue"}).
		SetDoneFunc(func(_ int, _ string) {
			app.Stop()
		})
	done.SetBorder(true).
		SetTitle(" Post-install Check ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	pages.AddAndSwitchToPage("done", done, true)
}

func showAuditReview(app *tui.App, pages *tview.Pages, configPath string, suggestions []PostInstallAuditSuggestion, applied *[]string) {
	if applied == nil {
		tmp := []string{}
		applied = &tmp
	}

	selected := make(map[string]bool, len(suggestions))
	list := tview.NewList().
		ShowSecondaryText(false)
	// We render checkbox markers like "[x]" which would otherwise be interpreted
	// as style tags by tview and get stripped.
	list.SetUseStyleTags(false, false)

	details := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true).
		SetTextAlign(tview.AlignLeft)
	details.SetBorder(true).
		SetTitle(" Details ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange)

	updateListItem := func(index int) {
		if index < 0 || index >= len(suggestions) {
			return
		}
		key := suggestions[index].Key
		marker := "[ ]"
		if selected[key] {
			marker = "[x]"
		}
		list.SetItemText(index, fmt.Sprintf("%s %s", marker, key), "")
	}

	updateDetails := func(index int) {
		if index < 0 || index >= len(suggestions) {
			details.SetText("")
			return
		}
		s := suggestions[index]
		var b strings.Builder
		b.WriteString("[yellow]Detected warnings:[white]\n\n")
		for _, msg := range s.Messages {
			b.WriteString("- ")
			b.WriteString(msg)
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("If you don’t use this feature, set [yellow]%s=false[white] to disable.\n", s.Key))
		details.SetText(b.String())
	}

	toggle := func(index int) {
		if index < 0 || index >= len(suggestions) {
			return
		}
		key := suggestions[index].Key
		selected[key] = !selected[key]
		updateListItem(index)
		updateDetails(index)
	}

	for i, s := range suggestions {
		selected[s.Key] = false
		list.AddItem("", "", 0, nil)
		updateListItem(i)
	}

	list.SetChangedFunc(func(index int, _ string, _ string, _ rune) {
		updateDetails(index)
	})
	list.SetSelectedFunc(func(index int, _ string, _ string, _ rune) {
		toggle(index)
	})
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			// Let SetSelectedFunc handle it.
			return event
		}
		if event.Rune() == ' ' {
			toggle(list.GetCurrentItem())
			return nil
		}
		return event
	})

	if len(suggestions) > 0 {
		updateDetails(0)
	}

	buttons := tview.NewForm().
		AddButton("Disable selected", func() {
			keys := make([]string, 0, len(suggestions))
			for _, s := range suggestions {
				if selected[s.Key] {
					keys = append(keys, s.Key)
				}
			}
			sort.Strings(keys)
			if len(keys) == 0 {
				showAuditDoneModal(app, pages, "No changes selected.\n\nNothing was modified.")
				return
			}
			if err := applyAuditDisables(configPath, keys); err != nil {
				showAuditDoneModal(app, pages, "Failed to update configuration:\n\n"+err.Error())
				return
			}
			*applied = keys
			showAuditDoneModal(app, pages, fmt.Sprintf("Configuration updated successfully.\n\nDisabled %d feature(s).", len(keys)))
		}).
		AddButton("Disable all", func() {
			for i := range suggestions {
				selected[suggestions[i].Key] = true
				updateListItem(i)
			}
			updateDetails(list.GetCurrentItem())
		}).
		AddButton("Skip", func() {
			app.Stop()
		})

	buttons.SetBorder(true).
		SetTitle(" Actions ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	list.SetBorder(true).
		SetTitle(" Suggestions ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange)

	mid := tview.NewFlex().
		AddItem(list, 0, 1, true).
		AddItem(details, 0, 2, false)

	review := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(tview.NewTextView().
			SetText("Select which features to disable. This only changes backup.env flags.\n").
			SetTextColor(tcell.ColorWhite), 2, 0, false).
		AddItem(mid, 0, 1, true).
		AddItem(buttons, 7, 0, false)

	review.SetBorder(true).
		SetTitle(" Review & Disable ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	pages.AddAndSwitchToPage("review", review, true)
	app.SetFocus(list)
}

func applyAuditDisables(configPath string, keys []string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path cannot be empty")
	}
	if len(keys) == 0 {
		return nil
	}

	contentBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	content := string(contentBytes)
	for _, key := range keys {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		content = setEnvValue(content, key, "false")
	}

	tmpPath := configPath + ".tmp.audit"
	if err := writeConfigFileAtomic(configPath, tmpPath, content); err != nil {
		return err
	}
	return nil
}

func writeConfigFileAtomic(configPath, tmpPath, content string) error {
	dir := filepath.Dir(strings.TrimSpace(configPath))
	if dir == "" || dir == "." {
		return fmt.Errorf("invalid configuration path: %q", configPath)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create configuration directory: %w", err)
	}
	if err := os.WriteFile(tmpPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write configuration file: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to finalize configuration file: %w", err)
	}
	return nil
}
