package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/tui"
	"github.com/tis24dev/proxmox-backup/internal/tui/components"
)

type restoreSelection struct {
	Candidate *decryptCandidate
}

const (
	restoreWizardSubtitle = "Restore Backup Workflow"
	restoreNavText        = "[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit screens | Mouse clicks enabled"
	restoreErrorModalPage = "restore-error-modal"
)

var errRestoreBackToMode = errors.New("restore mode back")

// RunRestoreWorkflowTUI runs the restore workflow using a TUI flow.
func RunRestoreWorkflowTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) error {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	candidate, prepared, err := prepareDecryptedBackupTUI(ctx, cfg, logger, version, configPath, buildSig)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	destRoot := "/"
	logger.Info("Restore target: system root (/) — files will be written back to their original paths")

	// Detect system type
	systemType := restoreSystem.DetectCurrentSystem()
	logger.Info("Detected system type: %s", GetSystemTypeString(systemType))

	// Validate compatibility
	if err := ValidateCompatibility(candidate.Manifest); err != nil {
		logger.Warning("Compatibility check: %v", err)
		proceed, perr := promptCompatibilityTUI(configPath, buildSig, err)
		if perr != nil {
			return perr
		}
		if !proceed {
			return fmt.Errorf("restore aborted due to incompatibility")
		}
	}

	// Analyze available categories in the backup
	logger.Info("Analyzing backup contents...")
	availableCategories, err := AnalyzeBackupCategories(prepared.ArchivePath, logger)
	if err != nil {
		logger.Warning("Could not analyze categories: %v", err)
		logger.Info("Falling back to full restore mode")
		return runFullRestoreTUI(ctx, candidate, prepared, destRoot, logger, configPath, buildSig)
	}

	// Restore mode selection (loop to allow going back from category selection)
	var (
		mode               RestoreMode
		selectedCategories []Category
	)

	for {
		backupSummary := fmt.Sprintf(
			"%s (%s)",
			candidate.DisplayBase,
			candidate.Manifest.CreatedAt.Format("2006-01-02 15:04:05"),
		)

		mode, err = selectRestoreModeTUI(systemType, configPath, buildSig, backupSummary)
		if err != nil {
			if errors.Is(err, ErrRestoreAborted) {
				return ErrRestoreAborted
			}
			return err
		}

		if mode != RestoreModeCustom {
			selectedCategories = GetCategoriesForMode(mode, systemType, availableCategories)
			break
		}

		selectedCategories, err = selectCategoriesTUI(availableCategories, systemType, configPath, buildSig)
		if err != nil {
			if errors.Is(err, ErrRestoreAborted) {
				return ErrRestoreAborted
			}
			if errors.Is(err, errRestoreBackToMode) {
				// User chose "Back" from category selection: re-open restore mode selection.
				continue
			}
			return err
		}
		break
	}

	plan := PlanRestore(candidate.Manifest, selectedCategories, systemType, mode)

	// Cluster safety prompt (SAFE vs RECOVERY)
	clusterBackup := strings.EqualFold(strings.TrimSpace(candidate.Manifest.ClusterMode), "cluster")
	if plan.NeedsClusterRestore && clusterBackup {
		logger.Info("Backup marked as cluster node; enabling guarded restore options for pve_cluster")
		choice, promptErr := promptClusterRestoreModeTUI(configPath, buildSig)
		if promptErr != nil {
			if errors.Is(promptErr, ErrRestoreAborted) {
				return ErrRestoreAborted
			}
			return promptErr
		}
		if choice == 0 {
			return ErrRestoreAborted
		}
		if choice == 1 {
			plan.ApplyClusterSafeMode(true)
			logger.Info("Selected SAFE cluster restore: /var/lib/pve-cluster will be exported only, not written to system")
		} else {
			plan.ApplyClusterSafeMode(false)
			logger.Warning("Selected RECOVERY cluster restore: full cluster database will be restored; ensure other nodes are isolated")
		}
	}

	// Create restore configuration
	restoreConfig := &SelectiveRestoreConfig{
		Mode:       mode,
		SystemType: systemType,
		Metadata:   candidate.Manifest,
	}
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.NormalCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.ExportCategories...)

	// Show detailed restore plan
	if err := showRestorePlanTUI(restoreConfig, configPath, buildSig); err != nil {
		if errors.Is(err, ErrRestoreAborted) {
			return ErrRestoreAborted
		}
		return err
	}

	// Confirm operation (RESTORE)
	confirmed, err := confirmRestoreTUI(configPath, buildSig)
	if err != nil {
		return err
	}
	if !confirmed {
		logger.Info("Restore operation cancelled by user")
		return ErrRestoreAborted
	}

	// Create safety backup of current configuration (only for categories that will write to system paths)
	var safetyBackup *SafetyBackupResult
	if len(plan.NormalCategories) > 0 {
		logger.Info("")
		safetyBackup, err = CreateSafetyBackup(logger, plan.NormalCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create safety backup: %v", err)
			cont, perr := promptContinueWithoutSafetyBackupTUI(configPath, buildSig, err)
			if perr != nil {
				return perr
			}
			if !cont {
				return fmt.Errorf("restore aborted: safety backup failed")
			}
		} else {
			logger.Info("Safety backup location: %s", safetyBackup.BackupPath)
			logger.Info("You can restore from this backup if needed using: tar -xzf %s -C /", safetyBackup.BackupPath)
		}
	}

	// If we are restoring cluster database, stop PVE services and unmount /etc/pve before writing
	needsClusterRestore := plan.NeedsClusterRestore
	clusterServicesStopped := false
	pbsServicesStopped := false
	needsPBSServices := plan.NeedsPBSServices
	if needsClusterRestore {
		logger.Info("")
		logger.Info("Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve")
		if err := stopPVEClusterServices(ctx, logger); err != nil {
			return err
		}
		clusterServicesStopped = true
		defer func() {
			if err := startPVEClusterServices(ctx, logger); err != nil {
				logger.Warning("Failed to restart PVE services after restore: %v", err)
			}
		}()

		if err := unmountEtcPVE(ctx, logger); err != nil {
			logger.Warning("Could not unmount /etc/pve: %v", err)
		}
	}

	// For PBS restores, stop PBS services before applying configuration/datastore changes if relevant categories are selected
	if needsPBSServices {
		logger.Info("")
		logger.Info("Preparing PBS system for restore: stopping proxmox-backup services")
		if err := stopPBSServices(ctx, logger); err != nil {
			logger.Warning("Unable to stop PBS services automatically: %v", err)
			cont, perr := promptContinueWithPBSServicesTUI(configPath, buildSig)
			if perr != nil {
				return perr
			}
			if !cont {
				return fmt.Errorf("restore aborted: PBS services still running")
			}
			logger.Info("Continuing restore without stopping PBS services")
		} else {
			pbsServicesStopped = true
			defer func() {
				if err := startPBSServices(ctx, logger); err != nil {
					logger.Warning("Failed to restart PBS services after restore: %v", err)
				}
			}()
		}
	}

	// Perform selective extraction for normal categories
	var detailedLogPath string
	if len(plan.NormalCategories) > 0 {
		logger.Info("")
		detailedLogPath, err = extractSelectiveArchive(ctx, prepared.ArchivePath, destRoot, plan.NormalCategories, mode, logger)
		if err != nil {
			logger.Error("Restore failed: %v", err)
			if safetyBackup != nil {
				logger.Info("You can rollback using the safety backup at: %s", safetyBackup.BackupPath)
			}
			return err
		}
	} else {
		logger.Info("")
		logger.Info("No system-path categories selected for restore (only export categories will be processed).")
	}

	// Handle export-only categories (/etc/pve) by extracting them to a separate directory
	exportLogPath := ""
	exportRoot := ""
	if len(plan.ExportCategories) > 0 {
		exportRoot = exportDestRoot(cfg.BaseDir)
		logger.Info("")
		logger.Info("Exporting /etc/pve contents to: %s", exportRoot)
		if err := restoreFS.MkdirAll(exportRoot, 0o755); err != nil {
			return fmt.Errorf("failed to create export directory %s: %w", exportRoot, err)
		}

		if exportLog, exErr := extractSelectiveArchive(ctx, prepared.ArchivePath, exportRoot, plan.ExportCategories, RestoreModeCustom, logger); exErr != nil {
			logger.Warning("Export of /etc/pve contents completed with errors: %v", exErr)
		} else {
			exportLogPath = exportLog
		}
	}

	// SAFE cluster mode: offer applying configs via pvesh without touching config.db
	if plan.ClusterSafeMode {
		if exportRoot == "" {
			logger.Warning("Cluster SAFE mode selected but export directory not available; skipping automatic pvesh apply")
		} else if err := runSafeClusterApply(ctx, bufio.NewReader(os.Stdin), exportRoot, logger); err != nil {
			// Note: runSafeClusterApply currently uses console prompts; this step remains non-TUI.
			logger.Warning("Cluster SAFE apply completed with errors: %v", err)
		}
	}

	// Recreate directory structures from configuration files if relevant categories were restored
	logger.Info("")
	if shouldRecreateDirectories(systemType, plan.NormalCategories) {
		if err := RecreateDirectoriesFromConfig(systemType, logger); err != nil {
			logger.Warning("Failed to recreate directory structures: %v", err)
			logger.Warning("You may need to manually create storage/datastore directories")
		}
	} else {
		logger.Debug("Skipping datastore/storage directory recreation (category not selected)")
	}

	logger.Info("")
	logger.Info("Restore completed successfully.")
	logger.Info("Temporary decrypted bundle removed.")

	if detailedLogPath != "" {
		logger.Info("Detailed restore log: %s", detailedLogPath)
	}
	if exportLogPath != "" {
		logger.Info("Exported /etc/pve files are available at: %s", exportLogPath)
	}

	if safetyBackup != nil {
		logger.Info("Safety backup preserved at: %s", safetyBackup.BackupPath)
		logger.Info("Remove it manually if restore was successful: rm %s", safetyBackup.BackupPath)
	}

	logger.Info("")
	logger.Info("IMPORTANT: You may need to restart services for changes to take effect.")
	if systemType == SystemTypePVE {
		if needsClusterRestore && clusterServicesStopped {
			logger.Info("  PVE services were stopped/restarted during restore; verify status with: pvecm status")
		} else {
			logger.Info("  PVE services: systemctl restart pve-cluster pvedaemon pveproxy")
		}
	} else if systemType == SystemTypePBS {
		if pbsServicesStopped {
			logger.Info("  PBS services were stopped/restarted during restore; verify status with: systemctl status proxmox-backup proxmox-backup-proxy")
		} else {
			logger.Info("  PBS services: systemctl restart proxmox-backup-proxy proxmox-backup")
		}

		// Check ZFS pool status for PBS systems only when ZFS category was restored
		if hasCategoryID(plan.NormalCategories, "zfs") {
			logger.Info("")
			if err := checkZFSPoolsAfterRestore(logger); err != nil {
				logger.Warning("ZFS pool check: %v", err)
			}
		} else {
			logger.Debug("Skipping ZFS pool verification (ZFS category not selected)")
		}
	}

	return nil
}

func prepareDecryptedBackupTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) (*decryptCandidate, *preparedBundle, error) {
	candidate, err := runRestoreSelectionWizard(cfg, configPath, buildSig)
	if err != nil {
		return nil, nil, err
	}

	prepared, err := preparePlainBundleTUI(ctx, candidate, version, logger, configPath, buildSig)
	if err != nil {
		return nil, nil, err
	}

	return candidate, prepared, nil
}

func runRestoreSelectionWizard(cfg *config.Config, configPath, buildSig string) (*decryptCandidate, error) {
	options := buildDecryptPathOptions(cfg)
	if len(options) == 0 {
		return nil, fmt.Errorf("no backup paths configured in backup.env")
	}

	app := tui.NewApp()
	pages := tview.NewPages()

	selection := &restoreSelection{}
	var selectionErr error

	pathList := tview.NewList().ShowSecondaryText(false)
	pathList.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	for _, opt := range options {
		// Use parentheses instead of square brackets (tview interprets [] as color tags)
		label := fmt.Sprintf("%s (%s)", opt.Label, opt.Path)
		pathList.AddItem(label, "", 0, nil)
	}

	pathList.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(options) {
			return
		}
		selectedOption := options[index]
		pages.SwitchToPage("paths-loading")
		go func() {
			candidates, err := discoverBackupCandidates(logging.GetDefaultLogger(), selectedOption.Path)
			app.QueueUpdateDraw(func() {
				if err != nil {
					message := fmt.Sprintf("Failed to inspect %s: %v", selectedOption.Path, err)
					showRestoreErrorModal(app, pages, configPath, buildSig, message, func() {
						pages.SwitchToPage("paths")
					})
					return
				}
				if len(candidates) == 0 {
					message := "No backup bundles found in selected path."
					showRestoreErrorModal(app, pages, configPath, buildSig, message, func() {
						pages.SwitchToPage("paths")
					})
					return
				}

				showRestoreCandidatePage(app, pages, candidates, configPath, buildSig, func(c *decryptCandidate) {
					selection.Candidate = c
					app.Stop()
				}, func() {
					selectionErr = ErrRestoreAborted
					app.Stop()
				})
			})
		}()
	})
	pathList.SetDoneFunc(func() {
		selectionErr = ErrRestoreAborted
		app.Stop()
	})

	form := components.NewForm(app)
	listHeight := len(options)
	if listHeight < 8 {
		listHeight = 8
	}
	if listHeight > 14 {
		listHeight = 14
	}
	listItem := components.NewListFormItem(pathList).
		SetLabel("Available backup sources").
		SetFieldHeight(listHeight)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		selectionErr = ErrRestoreAborted
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	pathPage := buildRestoreWizardPage("Select backup source", configPath, buildSig, form.Form)
	pages.AddPage("paths", pathPage, true, true)

	loadingText := tview.NewTextView().
		SetText("Scanning backup path...").
		SetTextAlign(tview.AlignCenter)

	loadingForm := components.NewForm(app)
	loadingForm.SetOnCancel(func() {
		selectionErr = ErrRestoreAborted
	})
	loadingForm.AddCancelButton("Cancel")
	loadingContent := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(loadingText, 0, 1, false).
		AddItem(loadingForm.Form, 3, 0, false)
	loadingPage := buildRestoreWizardPage("Loading backups", configPath, buildSig, loadingContent)
	pages.AddPage("paths-loading", loadingPage, true, false)

	app.SetRoot(pages, true).SetFocus(form.Form)
	if err := app.Run(); err != nil {
		return nil, err
	}
	if selectionErr != nil {
		return nil, selectionErr
	}
	if selection.Candidate == nil {
		return nil, ErrRestoreAborted
	}
	return selection.Candidate, nil
}

func showRestoreErrorModal(app *tui.App, pages *tview.Pages, configPath, buildSig, message string, onDismiss func()) {
	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s %s\n\n[yellow]Press ENTER to continue[white]", tui.SymbolError, message)).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if pages.HasPage(restoreErrorModalPage) {
				pages.RemovePage(restoreErrorModalPage)
			}
			if onDismiss != nil {
				onDismiss()
			}
		})

	modal.SetBorder(true).
		SetTitle(" Restore Error ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ErrorRed).
		SetBorderColor(tui.ErrorRed).
		SetBackgroundColor(tcell.ColorBlack)

	page := buildRestoreWizardPage("Error", configPath, buildSig, modal)
	if pages.HasPage(restoreErrorModalPage) {
		pages.RemovePage(restoreErrorModalPage)
	}
	pages.AddPage(restoreErrorModalPage, page, true, true)
	app.SetFocus(modal)
}

func showRestoreCandidatePage(app *tui.App, pages *tview.Pages, candidates []*decryptCandidate, configPath, buildSig string, onSelect func(*decryptCandidate), onCancel func()) {
	list := tview.NewList().ShowSecondaryText(false)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	type row struct {
		created     string
		mode        string
		tool        string
		targets     string
		compression string
	}

	rows := make([]row, len(candidates))
	var maxMode, maxTool, maxTargets, maxComp int

	for idx, cand := range candidates {
		created := cand.Manifest.CreatedAt.Format("2006-01-02 15:04:05")

		mode := strings.ToUpper(statusFromManifest(cand.Manifest))
		if mode == "" {
			mode = "UNKNOWN"
		}

		toolVersion := strings.TrimSpace(cand.Manifest.ScriptVersion)
		if toolVersion == "" {
			toolVersion = "unknown"
		}
		tool := "Tool " + toolVersion

		targets := buildTargetInfo(cand.Manifest)

		comp := ""
		if c := strings.TrimSpace(cand.Manifest.CompressionType); c != "" {
			comp = strings.ToUpper(c)
		}

		rows[idx] = row{
			created:     created,
			mode:        mode,
			tool:        tool,
			targets:     targets,
			compression: comp,
		}

		if len(mode) > maxMode {
			maxMode = len(mode)
		}
		if len(tool) > maxTool {
			maxTool = len(tool)
		}
		if len(targets) > maxTargets {
			maxTargets = len(targets)
		}
		if len(comp) > maxComp {
			maxComp = len(comp)
		}
	}

	for idx, r := range rows {
		line := fmt.Sprintf(
			"%2d) %s  %-*s  %-*s  %-*s",
			idx+1,
			r.created,
			maxMode, r.mode,
			maxTool, r.tool,
			maxTargets, r.targets,
		)
		if maxComp > 0 {
			line = fmt.Sprintf("%s  %-*s", line, maxComp, r.compression)
		}
		list.AddItem(line, "", 0, nil)
	}

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(candidates) {
			return
		}
		onSelect(candidates[index])
	})
	list.SetDoneFunc(func() {
		pages.SwitchToPage("paths")
	})

	form := components.NewForm(app)
	listHeight := len(candidates)
	if listHeight < 8 {
		listHeight = 8
	}
	if listHeight > 14 {
		listHeight = 14
	}
	listItem := components.NewListFormItem(list).
		SetLabel("Available backups").
		SetFieldHeight(listHeight)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		if onCancel != nil {
			onCancel()
		}
	})

	// Back goes on the left, Cancel on the right (order of AddButton calls)
	form.Form.AddButton("Back", func() {
		pages.SwitchToPage("paths")
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := buildRestoreWizardPage("Select backup to restore", configPath, buildSig, form.Form)
	if pages.HasPage("candidates") {
		pages.RemovePage("candidates")
	}
	pages.AddPage("candidates", page, true, true)
}

func selectRestoreModeTUI(systemType SystemType, configPath, buildSig, backupSummary string) (RestoreMode, error) {
	app := tui.NewApp()
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
		storageText = "STORAGE only - PVE cluster + storage configuration + VM configs + jobs"
	case SystemTypePBS:
		storageText = "DATASTORE only - PBS config + datastore definitions + sync/verify/prune jobs"
	default:
		storageText = "STORAGE/DATASTORE only - Storage or datastore configuration"
	}
	baseText := "SYSTEM BASE only - Network + SSL + SSH + services"
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

func selectCategoriesTUI(available []Category, systemType SystemType, configPath, buildSig string) ([]Category, error) {
	// Filter categories by system type (same logic as ShowCategorySelectionMenu)
	relevant := make([]Category, 0)
	for _, cat := range available {
		if cat.Type == CategoryTypeCommon ||
			(systemType == SystemTypePVE && cat.Type == CategoryTypePVE) ||
			(systemType == SystemTypePBS && cat.Type == CategoryTypePBS) {
			relevant = append(relevant, cat)
		}
	}

	if len(relevant) == 0 {
		return nil, fmt.Errorf("no categories available for this system type")
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

	app := tui.NewApp()
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
	return promptYesNoTUI(
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
	return promptYesNoTUI(
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
	return promptYesNoTUI(
		"PBS services running",
		configPath,
		buildSig,
		message,
		"Continue restore",
		"Abort restore",
	)
}

func promptClusterRestoreModeTUI(configPath, buildSig string) (int, error) {
	app := tui.NewApp()
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

func showRestorePlanTUI(config *SelectiveRestoreConfig, configPath, buildSig string) error {
	if config == nil {
		return fmt.Errorf("restore configuration not available")
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
			modeName = "STORAGE only (PVE cluster + storage + jobs)"
		} else {
			modeName = "DATASTORE only (PBS config + datastores + jobs)"
		}
	case RestoreModeBase:
		modeName = "SYSTEM BASE only (network + SSL + SSH + services)"
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

	textView := tview.NewTextView().
		SetText(b.String()).
		SetScrollable(true).
		SetWrap(false).
		SetTextColor(tcell.ColorWhite)

	app := tui.NewApp()
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
	app := tui.NewApp()
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

func runFullRestoreTUI(ctx context.Context, candidate *decryptCandidate, prepared *preparedBundle, destRoot string, logger *logging.Logger, configPath, buildSig string) error {
	if candidate == nil || prepared == nil || prepared.Manifest.ArchivePath == "" {
		return fmt.Errorf("invalid restore candidate")
	}

	app := tui.NewApp()
	manifest := candidate.Manifest

	var b strings.Builder
	fmt.Fprintf(&b, "Selected backup: %s (%s)\n",
		candidate.DisplayBase,
		manifest.CreatedAt.Format("2006-01-02 15:04:05"),
	)
	b.WriteString("Restore destination: / (system root; original paths will be preserved)\n")
	b.WriteString("WARNING: This operation will overwrite configuration files on this system.\n\n")
	b.WriteString("Press RESTORE to start the restore process, or Cancel to abort.\nYou will be asked for explicit confirmation before overwriting files.\n")

	infoText := tview.NewTextView().
		SetText(b.String()).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite)

	form := components.NewForm(app)
	var confirmed bool
	var aborted bool
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
		AddItem(infoText, 4, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := buildRestoreWizardPage("Full restore confirmation", configPath, buildSig, content)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return err
	}
	if aborted || !confirmed {
		return ErrRestoreAborted
	}

	ok, err := confirmOverwriteTUI(configPath, buildSig)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRestoreAborted
	}

	if err := extractPlainArchive(ctx, prepared.ArchivePath, destRoot, logger); err != nil {
		return err
	}

	logger.Info("Restore completed successfully.")
	return nil
}

func promptYesNoTUI(title, configPath, buildSig, message, yesLabel, noLabel string) (bool, error) {
	app := tui.NewApp()
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

func confirmOverwriteTUI(configPath, buildSig string) (bool, error) {
	message := "This operation will overwrite existing configuration files on this system.\n\nAre you sure you want to proceed with the restore?"
	return promptYesNoTUI(
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
		SetText(fmt.Sprintf("PROXMOX SYSTEM BACKUP - By TIS24DEV\n%s\n", restoreWizardSubtitle)).
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
