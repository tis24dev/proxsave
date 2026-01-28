package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

const (
	restoreWizardSubtitle = "Restore Backup Workflow"
	restoreNavText        = "[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit screens | Mouse clicks enabled"
)

var errRestoreBackToMode = errors.New("restore mode back")
var promptYesNoTUIFunc = promptYesNoTUI

// RunRestoreWorkflowTUI runs the restore workflow using a TUI flow.
func RunRestoreWorkflowTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) (err error) {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	done := logging.DebugStart(logger, "restore workflow (tui)", "version=%s", version)
	defer func() { done(err) }()

	ui := newTUIRestoreWorkflowUI(configPath, buildSig, logger)
	if err := runRestoreWorkflowWithUI(ctx, cfg, logger, version, ui); err != nil {
		if errors.Is(err, ErrRestoreAborted) {
			return ErrRestoreAborted
		}
		return err
	}
	return nil
}

func selectRestoreModeTUI(systemType SystemType, configPath, buildSig, backupSummary string) (RestoreMode, error) {
	app := newTUIApp()
	var selected RestoreMode
	var aborted bool

	list := tview.NewList().ShowSecondaryText(true)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	fullText := "FULL restore - Restore everything from backup"
	storageText := ""
	switch systemType {
	case SystemTypePVE:
		storageText = "STORAGE only - PVE cluster + storage + jobs + mounts"
	case SystemTypePBS:
		storageText = "DATASTORE only - PBS datastore definitions + sync/verify/prune jobs + mounts"
	default:
		storageText = "STORAGE/DATASTORE only - Storage or datastore configuration"
	}
	baseText := "SYSTEM BASE only - Network + SSL + SSH + services + filesystem"
	customText := "CUSTOM selection - Choose specific categories"

	list.AddItem("1) "+fullText, "", 0, nil)
	list.AddItem("2) "+storageText, "", 0, nil)
	list.AddItem("3) "+baseText, "", 0, nil)
	list.AddItem("4) "+customText, "", 0, nil)

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		switch index {
		case 0:
			selected = RestoreModeFull
		case 1:
			selected = RestoreModeStorage
		case 2:
			selected = RestoreModeBase
		case 3:
			selected = RestoreModeCustom
		default:
			selected = ""
		}
		if selected != "" {
			app.Stop()
		}
	})
	list.SetDoneFunc(func() {
		aborted = true
		app.Stop()
	})

	form := components.NewForm(app)
	listItem := components.NewListFormItem(list).
		SetLabel("Select restore mode").
		SetFieldHeight(8)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		aborted = true
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	// Selected backup summary
	summaryText := strings.TrimSpace(backupSummary)
	var summaryView tview.Primitive
	if summaryText != "" {
		summary := tview.NewTextView().
			SetText(fmt.Sprintf("Selected backup: %s", summaryText)).
			SetWrap(true).
			SetTextColor(tcell.ColorWhite)
		summary.SetBorder(false)
		summaryView = summary
	} else {
		summaryView = tview.NewBox()
	}

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(summaryView, 2, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := buildRestoreWizardPage("Select restore mode", configPath, buildSig, content)
	app.SetRoot(page, true).SetFocus(form.Form)
	if err := app.Run(); err != nil {
		return "", err
	}
	if aborted || selected == "" {
		return "", ErrRestoreAborted
	}
	return selected, nil
}

func filterAndSortCategoriesForSystem(available []Category, systemType SystemType) []Category {
	relevant := make([]Category, 0, len(available))
	for _, cat := range available {
		if cat.Type == CategoryTypeCommon ||
			(systemType == SystemTypePVE && cat.Type == CategoryTypePVE) ||
			(systemType == SystemTypePBS && cat.Type == CategoryTypePBS) {
			relevant = append(relevant, cat)
		}
	}

	// Sort categories: PVE/PBS first, then common
	sort.Slice(relevant, func(i, j int) bool {
		if relevant[i].Type != relevant[j].Type {
			if relevant[i].Type == CategoryTypeCommon {
				return false
			}
			if relevant[j].Type == CategoryTypeCommon {
				return true
			}
		}
		return relevant[i].Name < relevant[j].Name
	})

	return relevant
}

func selectCategoriesTUI(available []Category, systemType SystemType, configPath, buildSig string) ([]Category, error) {
	relevant := filterAndSortCategoriesForSystem(available, systemType)

	if len(relevant) == 0 {
		return nil, fmt.Errorf("no categories available for this system type")
	}

	app := newTUIApp()
	form := components.NewForm(app)
	var dropdownOpen bool

	// Helper text
	helper := tview.NewTextView().
		SetText("Select which categories to restore using the dropdowns below (Yes/No).").
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	// Create one dropdown per category, defaulting to "No"
	for _, cat := range relevant {
		dropdown := tview.NewDropDown().
			SetLabel(cat.Name).
			SetOptions([]string{"No", "Yes"}, nil).
			SetCurrentOption(0)

		dropdown.SetFieldTextColor(tcell.ColorWhite)
		dropdown.SetFieldBackgroundColor(tui.ProxmoxDark)

		dropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event == nil {
				return event
			}
			switch event.Key() {
			case tcell.KeyEnter:
				dropdownOpen = !dropdownOpen
			case tcell.KeyEscape:
				dropdownOpen = false
			}
			return event
		})

		form.Form.AddFormItem(dropdown)

		if strings.TrimSpace(cat.Description) != "" {
			desc := tview.NewInputField().
				SetLabel("  └─ " + cat.Description).
				SetFieldWidth(0).
				SetText("").
				SetDisabled(true)
			form.Form.AddFormItem(desc)
		}
	}

	var chosen []Category
	var aborted bool
	var goBack bool

	form.SetOnSubmit(func(values map[string]string) error {
		var out []Category
		for _, cat := range relevant {
			value := strings.TrimSpace(values[cat.Name])
			if strings.EqualFold(value, "Yes") {
				out = append(out, cat)
			}
		}
		if len(out) == 0 {
			return fmt.Errorf("please select at least one category")
		}
		chosen = out
		return nil
	})
	form.SetOnCancel(func() {
		aborted = true
	})

	// Buttons: Back, Continue, Cancel
	form.Form.AddButton("Back", func() {
		goBack = true
		app.Stop()
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, &dropdownOpen)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(helper, 3, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := buildRestoreWizardPage("Select restore categories", configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return nil, err
	}
	if goBack {
		return nil, errRestoreBackToMode
	}
	if aborted {
		return nil, ErrRestoreAborted
	}
	if len(chosen) == 0 {
		return nil, ErrRestoreAborted
	}
	return chosen, nil
}

func promptCompatibilityTUI(configPath, buildSig string, compatErr error) (bool, error) {
	message := fmt.Sprintf("Compatibility check reported:\n\n[red]%v[white]\n\nContinuing may cause system instability.\n\nDo you want to continue anyway?", compatErr)
	return promptYesNoTUIFunc(
		"Compatibility warning",
		configPath,
		buildSig,
		message,
		"Continue anyway",
		"Abort restore",
	)
}

func promptContinueWithoutSafetyBackupTUI(configPath, buildSig string, cause error) (bool, error) {
	message := fmt.Sprintf("Failed to create safety backup:\n\n[red]%v[white]\n\nWithout a safety backup, it will be harder to rollback changes.\n\nContinue without safety backup?", cause)
	return promptYesNoTUIFunc(
		"Safety backup failed",
		configPath,
		buildSig,
		message,
		"Continue without safety backup",
		"Abort restore",
	)
}

func promptContinueWithPBSServicesTUI(configPath, buildSig string) (bool, error) {
	message := "Unable to stop Proxmox Backup Server services automatically.\n\nContinuing the restore while services are running may lead to inconsistent state.\n\nContinue restore with PBS services still running?"
	return promptYesNoTUIFunc(
		"PBS services running",
		configPath,
		buildSig,
		message,
		"Continue restore",
		"Abort restore",
	)
}

func maybeRepairNICNamesTUI(ctx context.Context, logger *logging.Logger, archivePath, configPath, buildSig string) *nicRepairResult {
	logging.DebugStep(logger, "NIC repair", "Plan NIC name repair (archive=%s)", strings.TrimSpace(archivePath))
	plan, err := planNICNameRepair(ctx, archivePath)
	if err != nil {
		logger.Warning("NIC name repair plan failed: %v", err)
		return nil
	}
	if plan == nil {
		return nil
	}
	logging.DebugStep(logger, "NIC repair", "Plan result: mappingEntries=%d safe=%d conflicts=%d skippedReason=%q", len(plan.Mapping.Entries), len(plan.SafeMappings), len(plan.Conflicts), strings.TrimSpace(plan.SkippedReason))

	if plan.SkippedReason != "" && !plan.HasWork() {
		return &nicRepairResult{AppliedAt: nowRestore(), SkippedReason: plan.SkippedReason}
	}

	if plan != nil && !plan.Mapping.IsEmpty() {
		logging.DebugStep(logger, "NIC repair", "Detect persistent NIC naming overrides (udev/systemd)")
		overrides, err := detectNICNamingOverrideRules(logger)
		if err != nil {
			logger.Debug("NIC naming override detection failed: %v", err)
		} else if overrides.Empty() {
			logging.DebugStep(logger, "NIC repair", "No persistent NIC naming overrides detected")
		} else {
			logging.DebugStep(logger, "NIC repair", "Naming overrides detected: %s", overrides.Summary())
			logging.DebugStep(logger, "NIC repair", "Naming override details:\n%s", overrides.Details(32))
			var b strings.Builder
			b.WriteString("Detected persistent NIC naming rules (udev/systemd).\n\n")
			b.WriteString("If these rules are intended to keep legacy interface names, ProxSave NIC repair may rewrite /etc/network/interfaces* to different names.\n\n")
			if details := strings.TrimSpace(overrides.Details(8)); details != "" {
				b.WriteString(details)
				b.WriteString("\n\n")
			}
			b.WriteString("Skip NIC name repair and keep restored interface names?")

			skip, err := promptYesNoTUIFunc(
				"NIC naming overrides",
				configPath,
				buildSig,
				b.String(),
				"Skip NIC repair",
				"Proceed",
			)
			if err != nil {
				logger.Warning("NIC naming override prompt failed: %v", err)
			} else if skip {
				logging.DebugStep(logger, "NIC repair", "User choice: skip NIC repair due to naming overrides")
				logger.Info("NIC name repair skipped due to persistent naming rules")
				return &nicRepairResult{AppliedAt: nowRestore(), SkippedReason: "skipped due to persistent NIC naming rules (user choice)"}
			} else {
				logging.DebugStep(logger, "NIC repair", "User choice: proceed with NIC repair despite naming overrides")
			}
		}
	}

	includeConflicts := false
	if len(plan.Conflicts) > 0 {
		logging.DebugStep(logger, "NIC repair", "Conflicts detected: %d", len(plan.Conflicts))
		for i, conflict := range plan.Conflicts {
			if i >= 32 {
				logging.DebugStep(logger, "NIC repair", "Conflict details truncated (showing first 32)")
				break
			}
			logging.DebugStep(logger, "NIC repair", "Conflict: %s", conflict.Details())
		}
		var b strings.Builder
		b.WriteString("Detected NIC name conflicts.\n\n")
		b.WriteString("These interface names exist on the current system but map to different NICs in the backup inventory:\n\n")
		for _, conflict := range plan.Conflicts {
			b.WriteString(conflict.Details())
			b.WriteString("\n")
		}
		b.WriteString("\nApply NIC rename mapping even for conflicts?")

		ok, err := promptYesNoTUIFunc(
			"NIC name conflicts",
			configPath,
			buildSig,
			b.String(),
			"Apply conflicts",
			"Skip conflicts",
		)
		if err != nil {
			logger.Warning("NIC conflict prompt failed: %v", err)
		} else if ok {
			includeConflicts = true
		}
	}
	logging.DebugStep(logger, "NIC repair", "Apply conflicts=%v (conflictCount=%d)", includeConflicts, len(plan.Conflicts))

	logging.DebugStep(logger, "NIC repair", "Apply NIC rename mapping to /etc/network/interfaces*")
	result, err := applyNICNameRepair(logger, plan, includeConflicts)
	if err != nil {
		logger.Warning("NIC name repair failed: %v", err)
		return nil
	}
	if result != nil {
		logging.DebugStep(logger, "NIC repair", "Result: applied=%v changedFiles=%d skippedReason=%q", result.Applied(), len(result.ChangedFiles), strings.TrimSpace(result.SkippedReason))
	}
	return result
}

func promptClusterRestoreModeTUI(configPath, buildSig string) (int, error) {
	app := newTUIApp()
	var choice int
	var aborted bool

	list := tview.NewList().ShowSecondaryText(true)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	list.AddItem("1) SAFE", "Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only.", 0, nil)
	list.AddItem("2) RECOVERY", "Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.", 0, nil)
	list.AddItem("0) Exit", "Abort cluster restore", 0, nil)

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		switch index {
		case 0:
			choice = 1
		case 1:
			choice = 2
		case 2:
			choice = 0
		default:
			choice = 0
		}
		app.Stop()
	})
	list.SetDoneFunc(func() {
		aborted = true
		app.Stop()
	})

	form := components.NewForm(app)
	listItem := components.NewListFormItem(list).
		SetLabel("Cluster restore mode").
		SetFieldHeight(6)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		aborted = true
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := buildRestoreWizardPage("Cluster restore mode", configPath, buildSig, form.Form)
	app.SetRoot(page, true).SetFocus(form.Form)
	if err := app.Run(); err != nil {
		return 0, err
	}
	if aborted {
		return 0, ErrRestoreAborted
	}
	return choice, nil
}

func buildRestorePlanText(config *SelectiveRestoreConfig) string {
	if config == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString("═══════════════════════════════════════════════════════════════\n")
	b.WriteString("RESTORE PLAN\n")
	b.WriteString("═══════════════════════════════════════════════════════════════\n\n")

	modeName := ""
	switch config.Mode {
	case RestoreModeFull:
		modeName = "FULL restore (all categories)"
	case RestoreModeStorage:
		if config.SystemType == SystemTypePVE {
			modeName = "STORAGE only (cluster + storage + jobs + mounts)"
		} else {
			modeName = "DATASTORE only (datastores + jobs + mounts)"
		}
	case RestoreModeBase:
		modeName = "SYSTEM BASE only (network + SSL + SSH + services + filesystem)"
	case RestoreModeCustom:
		modeName = fmt.Sprintf("CUSTOM selection (%d categories)", len(config.SelectedCategories))
	default:
		modeName = "Unknown mode"
	}

	fmt.Fprintf(&b, "Restore mode: %s\n", modeName)
	fmt.Fprintf(&b, "System type:  %s\n\n", GetSystemTypeString(config.SystemType))

	b.WriteString("Categories to restore:\n")
	for i, cat := range config.SelectedCategories {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, cat.Name)
		fmt.Fprintf(&b, "     %s\n", cat.Description)
	}

	b.WriteString("\nFiles/directories that will be restored:\n")
	allPaths := GetSelectedPaths(config.SelectedCategories)
	sort.Strings(allPaths)
	for _, path := range allPaths {
		fsPath := strings.TrimPrefix(path, "./")
		fmt.Fprintf(&b, "  • /%s\n", fsPath)
	}

	b.WriteString("\n⚠ WARNING:\n")
	b.WriteString("  • Existing files at these locations will be OVERWRITTEN\n")
	b.WriteString("  • A safety backup will be created before restoration\n")
	b.WriteString("  • Services may need to be restarted after restoration\n\n")
	if (hasCategoryID(config.SelectedCategories, "pve_access_control") || hasCategoryID(config.SelectedCategories, "pbs_access_control")) &&
		(!hasCategoryID(config.SelectedCategories, "network") || !hasCategoryID(config.SelectedCategories, "ssl")) {
		b.WriteString("  • TFA/WebAuthn: for best 1:1 compatibility keep the same UI origin (FQDN/hostname and port) and restore 'network' + 'ssl'\n\n")
	}

	return b.String()
}

func showRestorePlanTUI(config *SelectiveRestoreConfig, configPath, buildSig string) error {
	if config == nil {
		return fmt.Errorf("restore configuration not available")
	}

	planText := buildRestorePlanText(config)
	textView := tview.NewTextView().
		SetText(planText).
		SetScrollable(true).
		SetWrap(false).
		SetTextColor(tcell.ColorWhite)

	app := newTUIApp()
	form := components.NewForm(app)
	var proceed bool
	var aborted bool

	form.SetOnSubmit(func(values map[string]string) error {
		proceed = true
		return nil
	})
	form.SetOnCancel(func() {
		aborted = true
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(textView, 0, 1, false).
		AddItem(form.Form, 3, 0, true)

	page := buildRestoreWizardPage("Restore plan", configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return err
	}
	if aborted || !proceed {
		return ErrRestoreAborted
	}
	return nil
}

func confirmRestoreTUI(configPath, buildSig string) (bool, error) {
	app := newTUIApp()
	var confirmed bool
	var aborted bool

	infoMessage := "Review the restore plan. Press [yellow]RESTORE[white] to start the restore process, or Cancel to abort.\nYou will be asked for explicit confirmation before overwriting files."
	infoText := tview.NewTextView().
		SetText(infoMessage).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	form := components.NewForm(app)
	form.SetOnSubmit(func(values map[string]string) error {
		confirmed = true
		return nil
	})
	form.SetOnCancel(func() {
		aborted = true
	})
	form.AddSubmitButton("RESTORE")
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 3, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := buildRestoreWizardPage("Confirm restore", configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return false, err
	}
	if aborted {
		return false, ErrRestoreAborted
	}
	if !confirmed {
		return false, ErrRestoreAborted
	}
	// Second-stage explicit overwrite confirmation
	ok, err := confirmOverwriteTUI(configPath, buildSig)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	return true, nil
}

func promptYesNoTUI(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
	app := newTUIApp()
	var result bool
	var cancelled bool

	infoText := tview.NewTextView().
		SetText(message).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	form := components.NewForm(app)
	form.SetOnSubmit(func(values map[string]string) error {
		result = true
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton(yesLabel)
	form.AddCancelButton(noLabel)
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 0, 1, false).
		AddItem(form.Form, 3, 0, true)

	page := buildRestoreWizardPage(title, configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return false, err
	}
	if cancelled {
		return false, nil
	}
	return result, nil
}

func promptYesNoTUIWithCountdown(ctx context.Context, logger *logging.Logger, title, configPath, buildSig, message, yesLabel, noLabel string, timeout time.Duration) (bool, error) {
	app := newTUIApp()
	var result bool
	var cancelled bool
	var timedOut bool

	infoText := tview.NewTextView().
		SetText(message).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	countdownText := tview.NewTextView().
		SetWrap(false).
		SetTextColor(tcell.ColorYellow).
		SetDynamicColors(true)

	deadline := time.Now().Add(timeout)
	updateCountdown := func() {
		left := time.Until(deadline)
		if left < 0 {
			left = 0
		}
		countdownText.SetText(fmt.Sprintf("Auto-skip in %ds (default: %s)", int(left.Seconds()), noLabel))
	}
	updateCountdown()

	form := components.NewForm(app)
	form.SetOnSubmit(func(values map[string]string) error {
		result = true
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton(yesLabel)
	form.AddCancelButton(noLabel)
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 0, 1, false).
		AddItem(countdownText, 1, 0, false).
		AddItem(form.Form, 3, 0, true)

	page := buildRestoreWizardPage(title, configPath, buildSig, content)
	form.SetParentView(page)

	stopCh := make(chan struct{})
	defer close(stopCh)

	if timeout > 0 {
		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ctx.Done():
					cancelled = true
					app.Stop()
					return
				case <-ticker.C:
					left := time.Until(deadline)
					if left <= 0 {
						timedOut = true
						cancelled = true
						app.Stop()
						return
					}
					app.QueueUpdateDraw(func() { updateCountdown() })
				}
			}
		}()
	}

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return false, err
	}
	if timedOut {
		logging.DebugStep(logger, "prompt yes/no (tui)", "Timeout expired (%s): proceeding with No", timeout)
		return false, nil
	}
	if cancelled {
		return false, nil
	}
	return result, nil
}

func promptOkTUI(title, configPath, buildSig, message, okLabel string) error {
	app := newTUIApp()

	infoText := tview.NewTextView().
		SetText(message).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	form := components.NewForm(app)
	form.SetOnSubmit(func(values map[string]string) error {
		return nil
	})
	form.SetOnCancel(func() {})
	form.AddSubmitButton(okLabel)
	form.AddCancelButton("Close")
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 0, 1, false).
		AddItem(form.Form, 3, 0, true)

	page := buildRestoreWizardPage(title, configPath, buildSig, content)
	form.SetParentView(page)

	return app.SetRoot(page, true).SetFocus(form.Form).Run()
}

func promptNetworkCommitTUI(timeout time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir, configPath, buildSig string) (bool, error) {
	app := newTUIApp()
	var committed bool
	var cancelled bool
	var timedOut bool

	remaining := int(timeout.Seconds())
	if remaining <= 0 {
		return false, nil
	}

	infoText := tview.NewTextView().
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	healthColor := func(sev networkHealthSeverity) string {
		switch sev {
		case networkHealthCritical:
			return "red"
		case networkHealthWarn:
			return "yellow"
		default:
			return "green"
		}
	}

	healthDetails := func(report networkHealthReport) string {
		var b strings.Builder
		for _, check := range report.Checks {
			color := healthColor(check.Severity)
			b.WriteString(fmt.Sprintf("- [%s]%s[white] %s: %s\n", color, check.Severity.String(), check.Name, check.Message))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	repairHeader := func(r *nicRepairResult) string {
		if r == nil {
			return ""
		}
		if r.Applied() {
			return fmt.Sprintf("NIC repair: [green]APPLIED[white] (%d file(s))", len(r.ChangedFiles))
		}
		if r.SkippedReason != "" {
			return fmt.Sprintf("NIC repair: [yellow]SKIPPED[white] (%s)", r.SkippedReason)
		}
		return ""
	}

	repairDetails := func(r *nicRepairResult) string {
		if r == nil || len(r.AppliedNICMap) == 0 {
			return ""
		}
		var b strings.Builder
		for _, m := range r.AppliedNICMap {
			b.WriteString(fmt.Sprintf("- %s -> %s\n", m.OldName, m.NewName))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	updateText := func(value int) {
		repairInfo := repairHeader(nicRepair)
		if details := repairDetails(nicRepair); details != "" {
			repairInfo += "\n" + details
		}
		if repairInfo != "" {
			repairInfo += "\n\n"
		}

		recommendation := ""
		if health.Severity == networkHealthCritical {
			recommendation = "\n\n[red]Recommendation:[white] do NOT commit (let rollback run)."
		}

		diagInfo := ""
		if strings.TrimSpace(diagnosticsDir) != "" {
			diagInfo = fmt.Sprintf("\n\nDiagnostics saved under:\n%s", diagnosticsDir)
		}

		infoText.SetText(fmt.Sprintf("Rollback in [yellow]%ds[white].\n\n%sNetwork health: [%s]%s[white]\n%s%s\n\nType COMMIT or press the button to keep the new network configuration.\nIf you do nothing, rollback will be automatic.",
			value,
			repairInfo,
			healthColor(health.Severity),
			health.Severity.String(),
			healthDetails(health)+recommendation,
			diagInfo,
		))
	}
	updateText(remaining)

	form := components.NewForm(app)
	form.SetOnSubmit(func(values map[string]string) error {
		committed = true
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton("COMMIT")
	form.AddCancelButton("Let rollback run")
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 0, 1, false).
		AddItem(form.Form, 3, 0, true)

	page := buildRestoreWizardPage("Network apply", configPath, buildSig, content)
	form.SetParentView(page)

	stopCh := make(chan struct{})
	done := make(chan struct{})
	ticker := time.NewTicker(1 * time.Second)
	go func() {
		defer close(done)
		for {
			select {
			case <-ticker.C:
				remaining--
				if remaining <= 0 {
					timedOut = true
					app.Stop()
					return
				}
				value := remaining
				app.QueueUpdateDraw(func() {
					updateText(value)
				})
			case <-stopCh:
				return
			}
		}
	}()

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		close(stopCh)
		ticker.Stop()
		return false, err
	}
	close(stopCh)
	ticker.Stop()
	<-done

	if timedOut || cancelled {
		return false, nil
	}
	return committed, nil
}

func confirmOverwriteTUI(configPath, buildSig string) (bool, error) {
	message := "This operation will overwrite existing configuration files on this system.\n\nAre you sure you want to proceed with the restore?"
	return promptYesNoTUIFunc(
		"Confirm overwrite",
		configPath,
		buildSig,
		message,
		"Overwrite and restore",
		"Cancel",
	)
}

func buildRestoreWizardPage(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
	welcomeText := tview.NewTextView().
		SetText(fmt.Sprintf("ProxSave - By TIS24DEV\n%s\n", restoreWizardSubtitle)).
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	navInstructions := tview.NewTextView().
		SetText("\n" + restoreNavText).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	navInstructions.SetBorder(false)

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

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(welcomeText, 5, 0, false).
		AddItem(navInstructions, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(content, 0, 1, true).
		AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	flex.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s ", title)).
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	return flex
}
