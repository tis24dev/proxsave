package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

type restoreSelection struct {
	Candidate *decryptCandidate
}

const (
	restoreWizardSubtitle = "Restore Backup Workflow"
	restoreNavText        = "[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit screens | Mouse clicks enabled"
	restoreErrorModalPage = "restore-error-modal"
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
	done := logging.DebugStart(logger, "restore workflow (tui)", "version=%s", version)
	defer func() { done(err) }()
	defer func() {
		if err == nil {
			return
		}
		if errors.Is(err, ErrDecryptAborted) ||
			errors.Is(err, ErrAgeRecipientSetupAborted) ||
			errors.Is(err, context.Canceled) ||
			(ctx != nil && ctx.Err() != nil) {
			err = ErrRestoreAborted
		}
	}()
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
		return runFullRestoreTUI(ctx, candidate, prepared, destRoot, logger, cfg.DryRun, configPath, buildSig)
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

	// Staging is designed to protect live systems. In test runs (fake filesystem) or non-root targets,
	// extract staged categories directly to the destination to keep restore semantics predictable.
	if destRoot != "/" || !isRealRestoreFS(restoreFS) {
		if len(plan.StagedCategories) > 0 {
			logging.DebugStep(logger, "restore", "Staging disabled (destRoot=%s realFS=%v): extracting %d staged category(ies) directly", destRoot, isRealRestoreFS(restoreFS), len(plan.StagedCategories))
			plan.NormalCategories = append(plan.NormalCategories, plan.StagedCategories...)
			plan.StagedCategories = nil
		}
	}

	// Create restore configuration
	restoreConfig := &SelectiveRestoreConfig{
		Mode:       mode,
		SystemType: systemType,
		Metadata:   candidate.Manifest,
	}
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.NormalCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.StagedCategories...)
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
	var networkRollbackBackup *SafetyBackupResult
	systemWriteCategories := append([]Category{}, plan.NormalCategories...)
	systemWriteCategories = append(systemWriteCategories, plan.StagedCategories...)
	if len(systemWriteCategories) > 0 {
		logger.Info("")
		safetyBackup, err = CreateSafetyBackup(logger, systemWriteCategories, destRoot)
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

	if plan.HasCategoryID("network") {
		logger.Info("")
		logging.DebugStep(logger, "restore", "Create network-only rollback backup for transactional network apply")
		networkRollbackBackup, err = CreateNetworkRollbackBackup(logger, systemWriteCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create network rollback backup: %v", err)
		} else if networkRollbackBackup != nil && strings.TrimSpace(networkRollbackBackup.BackupPath) != "" {
			logger.Info("Network rollback backup location: %s", networkRollbackBackup.BackupPath)
			logger.Info("This backup is used for the %ds network rollback timer and only includes network paths.", int(defaultNetworkRollbackTimeout.Seconds()))
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
				return ErrRestoreAborted
			}
			logger.Warning("Continuing restore with PBS services still running")
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
		categoriesForExtraction := plan.NormalCategories
		if needsClusterRestore {
			logging.DebugStep(logger, "restore", "Cluster RECOVERY shadow-guard: sanitize categories to avoid /etc/pve shadow writes")
			sanitized, removed := sanitizeCategoriesForClusterRecovery(categoriesForExtraction)
			removedPaths := 0
			for _, paths := range removed {
				removedPaths += len(paths)
			}
			logging.DebugStep(
				logger,
				"restore",
				"Cluster RECOVERY shadow-guard: categories_before=%d categories_after=%d removed_categories=%d removed_paths=%d",
				len(categoriesForExtraction),
				len(sanitized),
				len(removed),
				removedPaths,
			)
			if len(removed) > 0 {
				logger.Warning("Cluster RECOVERY restore: skipping direct restore of /etc/pve paths to prevent shadowing while pmxcfs is stopped/unmounted")
				for _, cat := range categoriesForExtraction {
					if paths, ok := removed[cat.ID]; ok && len(paths) > 0 {
						logger.Warning("  - %s (%s): %s", cat.Name, cat.ID, strings.Join(paths, ", "))
					}
				}
				logger.Info("These paths are expected to be restored from config.db and become visible after /etc/pve is remounted.")
			} else {
				logging.DebugStep(logger, "restore", "Cluster RECOVERY shadow-guard: no /etc/pve paths detected in selected categories")
			}
			categoriesForExtraction = sanitized
			var extractionIDs []string
			for _, cat := range categoriesForExtraction {
				if id := strings.TrimSpace(cat.ID); id != "" {
					extractionIDs = append(extractionIDs, id)
				}
			}
			if len(extractionIDs) > 0 {
				logging.DebugStep(logger, "restore", "Cluster RECOVERY shadow-guard: extraction_categories=%s", strings.Join(extractionIDs, ","))
			} else {
				logging.DebugStep(logger, "restore", "Cluster RECOVERY shadow-guard: extraction_categories=<none>")
			}
		}

		if len(categoriesForExtraction) == 0 {
			logging.DebugStep(logger, "restore", "Skip system-path extraction: no categories remain after shadow-guard")
			logger.Info("No system-path categories remain after cluster shadow-guard; skipping system-path extraction.")
		} else {
			detailedLogPath, err = extractSelectiveArchive(ctx, prepared.ArchivePath, destRoot, categoriesForExtraction, mode, logger)
			if err != nil {
				logger.Error("Restore failed: %v", err)
				if safetyBackup != nil {
					logger.Info("You can rollback using the safety backup at: %s", safetyBackup.BackupPath)
				}
				return err
			}
		}
	} else {
		logger.Info("")
		logger.Info("No system-path categories selected for restore (only export categories will be processed).")
	}

	// Handle export-only categories by extracting them to a separate directory
	exportLogPath := ""
	exportRoot := ""
	if len(plan.ExportCategories) > 0 {
		exportRoot = exportDestRoot(cfg.BaseDir)
		logger.Info("")
		logger.Info("Exporting %d export-only category(ies) to: %s", len(plan.ExportCategories), exportRoot)
		if err := restoreFS.MkdirAll(exportRoot, 0o755); err != nil {
			return fmt.Errorf("failed to create export directory %s: %w", exportRoot, err)
		}

		if exportLog, exErr := extractSelectiveArchive(ctx, prepared.ArchivePath, exportRoot, plan.ExportCategories, RestoreModeCustom, logger); exErr != nil {
			logger.Warning("Export completed with errors: %v", exErr)
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

	// Stage sensitive categories (network, PBS datastore/jobs) to a temporary directory and apply them safely later.
	stageLogPath := ""
	stageRoot := ""
	if len(plan.StagedCategories) > 0 {
		stageRoot = stageDestRoot()
		logger.Info("")
		logger.Info("Staging %d sensitive category(ies) to: %s", len(plan.StagedCategories), stageRoot)
		if err := restoreFS.MkdirAll(stageRoot, 0o755); err != nil {
			return fmt.Errorf("failed to create staging directory %s: %w", stageRoot, err)
		}

		if stageLog, err := extractSelectiveArchive(ctx, prepared.ArchivePath, stageRoot, plan.StagedCategories, RestoreModeCustom, logger); err != nil {
			logger.Warning("Staging completed with errors: %v", err)
		} else {
			stageLogPath = stageLog
		}

		logger.Info("")
		if err := maybeApplyPBSConfigsFromStage(ctx, logger, plan, stageRoot, cfg.DryRun); err != nil {
			logger.Warning("PBS staged config apply: %v", err)
		}
	}

	stageRootForNetworkApply := stageRoot
	if installed, err := maybeInstallNetworkConfigFromStage(ctx, logger, plan, stageRoot, prepared.ArchivePath, networkRollbackBackup, cfg.DryRun); err != nil {
		logger.Warning("Network staged install: %v", err)
	} else if installed {
		stageRootForNetworkApply = ""
		logging.DebugStep(logger, "restore", "Network staged install completed: configuration written to /etc (no reload); live apply will use system paths")
	}

	// Recreate directory structures from configuration files if relevant categories were restored
	logger.Info("")
	categoriesForDirRecreate := append([]Category{}, plan.NormalCategories...)
	categoriesForDirRecreate = append(categoriesForDirRecreate, plan.StagedCategories...)
	if shouldRecreateDirectories(systemType, categoriesForDirRecreate) {
		if err := RecreateDirectoriesFromConfig(systemType, logger); err != nil {
			logger.Warning("Failed to recreate directory structures: %v", err)
			logger.Warning("You may need to manually create storage/datastore directories")
		}
	} else {
		logger.Debug("Skipping datastore/storage directory recreation (category not selected)")
	}

	logger.Info("")
	if plan.HasCategoryID("network") {
		logger.Info("")
		if err := maybeRepairResolvConfAfterRestore(ctx, logger, prepared.ArchivePath, cfg.DryRun); err != nil {
			logger.Warning("DNS resolver repair: %v", err)
		}
	}

	logger.Info("")
	if err := maybeApplyNetworkConfigTUI(ctx, logger, plan, safetyBackup, networkRollbackBackup, stageRootForNetworkApply, prepared.ArchivePath, configPath, buildSig, cfg.DryRun); err != nil {
		logger.Warning("Network apply step skipped or failed: %v", err)
	}

	logger.Info("")
	logger.Info("Restore completed successfully.")
	logger.Info("Temporary decrypted bundle removed.")

	if detailedLogPath != "" {
		logger.Info("Detailed restore log: %s", detailedLogPath)
	}
	if exportRoot != "" {
		logger.Info("Export directory: %s", exportRoot)
	}
	if exportLogPath != "" {
		logger.Info("Export detailed log: %s", exportLogPath)
	}
	if stageRoot != "" {
		logger.Info("Staging directory: %s", stageRoot)
	}
	if stageLogPath != "" {
		logger.Info("Staging detailed log: %s", stageLogPath)
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

	logger.Info("")
	logger.Warning("⚠ SYSTEM REBOOT RECOMMENDED")
	logger.Info("Reboot the node (or at least restart networking and system services) to ensure all restored configurations take effect cleanly.")

	return nil
}

func prepareDecryptedBackupTUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version, configPath, buildSig string) (*decryptCandidate, *preparedBundle, error) {
	candidate, err := runRestoreSelectionWizard(ctx, cfg, logger, configPath, buildSig)
	if err != nil {
		return nil, nil, err
	}

	prepared, err := preparePlainBundleTUI(ctx, candidate, version, logger, configPath, buildSig)
	if err != nil {
		return nil, nil, err
	}

	return candidate, prepared, nil
}

func runRestoreSelectionWizard(ctx context.Context, cfg *config.Config, logger *logging.Logger, configPath, buildSig string) (candidate *decryptCandidate, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	done := logging.DebugStart(logger, "restore selection wizard", "tui=true")
	defer func() { done(err) }()
	options := buildDecryptPathOptions(cfg, logger)
	if len(options) == 0 {
		err = fmt.Errorf("no backup paths configured in backup.env")
		return nil, err
	}
	for _, opt := range options {
		logging.DebugStep(logger, "restore selection wizard", "option label=%q path=%q rclone=%v", opt.Label, opt.Path, opt.IsRclone)
	}

	app := newTUIApp()
	pages := tview.NewPages()

	selection := &restoreSelection{}
	var selectionErr error
	var scan scanController

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
		logging.DebugStep(logger, "restore selection wizard", "selected source label=%q path=%q rclone=%v", selectedOption.Label, selectedOption.Path, selectedOption.IsRclone)
		pages.SwitchToPage("paths-loading")
		go func() {
			scanCtx, finish := scan.Start(ctx)
			defer finish()

			var candidates []*decryptCandidate
			var scanErr error
			scanDone := logging.DebugStart(logger, "scan backup source", "path=%s rclone=%v", selectedOption.Path, selectedOption.IsRclone)
			defer func() { scanDone(scanErr) }()

			if selectedOption.IsRclone {
				candidates, scanErr = discoverRcloneBackups(scanCtx, selectedOption.Path, logger)
			} else {
				candidates, scanErr = discoverBackupCandidates(logger, selectedOption.Path)
			}
			logging.DebugStep(logger, "scan backup source", "candidates=%d", len(candidates))
			if scanCtx.Err() != nil {
				scanErr = scanCtx.Err()
				return
			}
			app.QueueUpdateDraw(func() {
				if scanErr != nil {
					message := fmt.Sprintf("Failed to inspect %s: %v", selectedOption.Path, scanErr)
					showRestoreErrorModal(app, pages, configPath, buildSig, message, func() {
						pages.SwitchToPage("paths")
					})
					return
				}
				if len(candidates) == 0 {
					message := "No backups found in selected path."
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
		logging.DebugStep(logger, "restore selection wizard", "cancel requested (done func)")
		scan.Cancel()
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
		logging.DebugStep(logger, "restore selection wizard", "cancel requested (form)")
		scan.Cancel()
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
		logging.DebugStep(logger, "restore selection wizard", "cancel requested (loading form)")
		scan.Cancel()
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
	if runErr := app.Run(); runErr != nil {
		err = runErr
		return nil, err
	}
	if selectionErr != nil {
		err = selectionErr
		return nil, err
	}
	if selection.Candidate == nil {
		err = ErrRestoreAborted
		return nil, err
	}
	candidate = selection.Candidate
	return candidate, nil
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

func maybeApplyNetworkConfigTUI(ctx context.Context, logger *logging.Logger, plan *RestorePlan, safetyBackup, networkRollbackBackup *SafetyBackupResult, stageRoot, archivePath, configPath, buildSig string, dryRun bool) (err error) {
	if !shouldAttemptNetworkApply(plan) {
		if logger != nil {
			logger.Debug("Network safe apply (TUI): skipped (network category not selected)")
		}
		return nil
	}
	done := logging.DebugStart(logger, "network safe apply (tui)", "dryRun=%v euid=%d stage=%s archive=%s", dryRun, os.Geteuid(), strings.TrimSpace(stageRoot), strings.TrimSpace(archivePath))
	defer func() { done(err) }()

	if !isRealRestoreFS(restoreFS) {
		logger.Debug("Skipping live network apply: non-system filesystem in use")
		return nil
	}
	if dryRun {
		logger.Info("Dry run enabled: skipping live network apply")
		return nil
	}
	if os.Geteuid() != 0 {
		logger.Warning("Skipping live network apply: requires root privileges")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Resolve rollback backup paths")
	networkRollbackPath := ""
	if networkRollbackBackup != nil {
		networkRollbackPath = strings.TrimSpace(networkRollbackBackup.BackupPath)
	}
	fullRollbackPath := ""
	if safetyBackup != nil {
		fullRollbackPath = strings.TrimSpace(safetyBackup.BackupPath)
	}
	logging.DebugStep(logger, "network safe apply (tui)", "Rollback backup resolved: network=%q full=%q", networkRollbackPath, fullRollbackPath)
	if networkRollbackPath == "" && fullRollbackPath == "" {
		logger.Warning("Skipping live network apply: rollback backup not available")
		if strings.TrimSpace(stageRoot) != "" {
			logger.Info("Network configuration is staged; skipping NIC repair/apply due to missing rollback backup.")
			return nil
		}
		repairNow, err := promptYesNoTUIFunc(
			"NIC name repair (recommended)",
			configPath,
			buildSig,
			"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
			"Repair now",
			"Skip repair",
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (tui)", "User choice: repairNow=%v", repairNow)
		if repairNow {
			if repair := maybeRepairNICNamesTUI(ctx, logger, archivePath, configPath, buildSig); repair != nil {
				_ = promptOkTUI("NIC repair result", configPath, buildSig, repair.Details(), "OK")
			}
		}
		logger.Info("Skipping live network apply (you can reboot or apply manually later).")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Prompt: apply network now with rollback timer")
	message := fmt.Sprintf(
		"Apply restored network configuration now with an automatic rollback timer (%ds).\n\nIf you do not commit the changes, the previous network configuration will be restored automatically.\n\nProceed with live network apply?",
		int(defaultNetworkRollbackTimeout.Seconds()),
	)
	applyNow, err := promptYesNoTUIFunc(
		"Apply network configuration",
		configPath,
		buildSig,
		message,
		"Apply now",
		"Skip apply",
	)
	if err != nil {
		return err
	}
	logging.DebugStep(logger, "network safe apply (tui)", "User choice: applyNow=%v", applyNow)
	if !applyNow {
		if strings.TrimSpace(stageRoot) == "" {
			repairNow, err := promptYesNoTUIFunc(
				"NIC name repair (recommended)",
				configPath,
				buildSig,
				"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
				"Repair now",
				"Skip repair",
			)
			if err != nil {
				return err
			}
			logging.DebugStep(logger, "network safe apply (tui)", "User choice: repairNow=%v", repairNow)
			if repairNow {
				if repair := maybeRepairNICNamesTUI(ctx, logger, archivePath, configPath, buildSig); repair != nil {
					_ = promptOkTUI("NIC repair result", configPath, buildSig, repair.Details(), "OK")
				}
			}
		} else {
			logger.Info("Network configuration is staged (not yet written to /etc); skipping NIC repair prompt.")
		}
		logger.Info("Skipping live network apply (you can apply later).")
		return nil
	}

	rollbackPath := networkRollbackPath
	if rollbackPath == "" {
		logging.DebugStep(logger, "network safe apply (tui)", "Prompt: network-only rollback missing; allow full rollback backup fallback")
		ok, err := promptYesNoTUIFunc(
			"Network-only rollback not available",
			configPath,
			buildSig,
			"Network-only rollback backup is not available.\n\nIf you proceed, the rollback timer will use the full safety backup, which may revert other restored categories.\n\nProceed anyway?",
			"Proceed with full rollback",
			"Skip apply",
		)
		if err != nil {
			return err
		}
		logging.DebugStep(logger, "network safe apply (tui)", "User choice: allowFullRollback=%v", ok)
		if !ok {
			repairNow, err := promptYesNoTUIFunc(
				"NIC name repair (recommended)",
				configPath,
				buildSig,
				"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
				"Repair now",
				"Skip repair",
			)
			if err != nil {
				return err
			}
			logging.DebugStep(logger, "network safe apply (tui)", "User choice: repairNow=%v", repairNow)
			if repairNow {
				if repair := maybeRepairNICNamesTUI(ctx, logger, archivePath, configPath, buildSig); repair != nil {
					_ = promptOkTUI("NIC repair result", configPath, buildSig, repair.Details(), "OK")
				}
			}
			logger.Info("Skipping live network apply (you can reboot or apply manually later).")
			return nil
		}
		rollbackPath = fullRollbackPath
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Selected rollback backup: %s", rollbackPath)
	if err := applyNetworkWithRollbackTUI(ctx, logger, rollbackPath, networkRollbackPath, stageRoot, archivePath, configPath, buildSig, defaultNetworkRollbackTimeout, plan.SystemType); err != nil {
		return err
	}
	return nil
}

func applyNetworkWithRollbackTUI(ctx context.Context, logger *logging.Logger, rollbackBackupPath, networkRollbackPath, stageRoot, archivePath, configPath, buildSig string, timeout time.Duration, systemType SystemType) (err error) {
	done := logging.DebugStart(
		logger,
		"network safe apply (tui)",
		"rollbackBackup=%s networkRollback=%s timeout=%s systemType=%s stage=%s",
		strings.TrimSpace(rollbackBackupPath),
		strings.TrimSpace(networkRollbackPath),
		timeout,
		systemType,
		strings.TrimSpace(stageRoot),
	)
	defer func() { done(err) }()

	logging.DebugStep(logger, "network safe apply (tui)", "Create diagnostics directory")
	diagnosticsDir, err := createNetworkDiagnosticsDir()
	if err != nil {
		logger.Warning("Network diagnostics disabled: %v", err)
		diagnosticsDir = ""
	} else {
		logger.Info("Network diagnostics directory: %s", diagnosticsDir)
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Detect management interface (SSH/default route)")
	iface, source := detectManagementInterface(ctx, logger)
	if iface != "" {
		logger.Info("Detected management interface: %s (%s)", iface, source)
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (tui)", "Capture network snapshot (before)")
		if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "before", 3*time.Second); err != nil {
			logger.Debug("Network snapshot before apply failed: %v", err)
		} else {
			logger.Debug("Network snapshot (before): %s", snap)
		}

		logging.DebugStep(logger, "network safe apply (tui)", "Run baseline health checks (before)")
		healthBefore := runNetworkHealthChecks(ctx, networkHealthOptions{
			SystemType:         systemType,
			Logger:             logger,
			CommandTimeout:     3 * time.Second,
			EnableGatewayPing:  false,
			ForceSSHRouteCheck: false,
			EnableDNSResolve:   false,
		})
		if path, err := writeNetworkHealthReportFileNamed(diagnosticsDir, "health_before.txt", healthBefore); err != nil {
			logger.Debug("Failed to write network health (before) report: %v", err)
		} else {
			logger.Debug("Network health (before) report: %s", path)
		}
	}

	if strings.TrimSpace(stageRoot) != "" {
		logging.DebugStep(logger, "network safe apply (tui)", "Apply staged network files to system paths (before NIC repair)")
		applied, err := applyNetworkFilesFromStage(logger, stageRoot)
		if err != nil {
			return err
		}
		if len(applied) > 0 {
			logging.DebugStep(logger, "network safe apply (tui)", "Staged network files written: %d", len(applied))
		}
	}

	logging.DebugStep(logger, "network safe apply (tui)", "NIC name repair (optional)")
	nicRepair := maybeRepairNICNamesTUI(ctx, logger, archivePath, configPath, buildSig)
	if nicRepair != nil {
		if nicRepair.Applied() || nicRepair.SkippedReason != "" {
			logger.Info("%s", nicRepair.Summary())
		} else {
			logger.Debug("%s", nicRepair.Summary())
		}
	}

	if strings.TrimSpace(iface) != "" {
		if cur, err := currentNetworkEndpoint(ctx, iface, 2*time.Second); err == nil {
			if tgt, err := targetNetworkEndpointFromConfig(logger, iface); err == nil {
				logger.Info("Network plan: %s -> %s", cur.summary(), tgt.summary())
			}
		}
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (tui)", "Write network plan (current -> target)")
		if planText, err := buildNetworkPlanReport(ctx, logger, iface, source, 2*time.Second); err != nil {
			logger.Debug("Network plan build failed: %v", err)
		} else if strings.TrimSpace(planText) != "" {
			if path, err := writeNetworkTextReportFile(diagnosticsDir, "plan.txt", planText+"\n"); err != nil {
				logger.Debug("Network plan write failed: %v", err)
			} else {
				logger.Debug("Network plan: %s", path)
			}
		}

		logging.DebugStep(logger, "network safe apply (tui)", "Run ifquery diagnostic (pre-apply)")
		ifqueryPre := runNetworkIfqueryDiagnostic(ctx, 5*time.Second, logger)
		if !ifqueryPre.Skipped {
			if path, err := writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, "ifquery_pre_apply.txt", ifqueryPre); err != nil {
				logger.Debug("Failed to write ifquery (pre-apply) report: %v", err)
			} else {
				logger.Debug("ifquery (pre-apply) report: %s", path)
			}
		}
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Network preflight validation (ifupdown/ifupdown2)")
	preflight := runNetworkPreflightValidation(ctx, 5*time.Second, logger)
	if diagnosticsDir != "" {
		if path, err := writeNetworkPreflightReportFile(diagnosticsDir, preflight); err != nil {
			logger.Debug("Failed to write network preflight report: %v", err)
		} else {
			logger.Debug("Network preflight report: %s", path)
		}
	}
	if !preflight.Ok() {
		message := preflight.Summary()
		if strings.TrimSpace(diagnosticsDir) != "" {
			message += "\n\nDiagnostics saved under:\n" + diagnosticsDir
		}
		if out := strings.TrimSpace(preflight.Output); out != "" {
			message += "\n\nOutput:\n" + out
		}
		if strings.TrimSpace(stageRoot) != "" && strings.TrimSpace(networkRollbackPath) != "" {
			logging.DebugStep(logger, "network safe apply (tui)", "Preflight failed in staged mode: rolling back network files automatically")
			rollbackLog, rbErr := rollbackNetworkFilesNow(ctx, logger, networkRollbackPath, diagnosticsDir)
			if strings.TrimSpace(rollbackLog) != "" {
				logger.Info("Network rollback log: %s", rollbackLog)
			}
			if rbErr != nil {
				logger.Error("Network apply aborted: preflight validation failed (%s) and rollback failed: %v", preflight.CommandLine(), rbErr)
				_ = promptOkTUI("Network rollback failed", configPath, buildSig, rbErr.Error(), "OK")
				return fmt.Errorf("network preflight validation failed; rollback attempt failed: %w", rbErr)
			}
			if diagnosticsDir != "" {
				logging.DebugStep(logger, "network safe apply (tui)", "Capture network snapshot (after rollback)")
				if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "after_rollback", 3*time.Second); err != nil {
					logger.Debug("Network snapshot after rollback failed: %v", err)
				} else {
					logger.Debug("Network snapshot (after rollback): %s", snap)
				}
				logging.DebugStep(logger, "network safe apply (tui)", "Run ifquery diagnostic (after rollback)")
				ifqueryAfterRollback := runNetworkIfqueryDiagnostic(ctx, 5*time.Second, logger)
				if !ifqueryAfterRollback.Skipped {
					if path, err := writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, "ifquery_after_rollback.txt", ifqueryAfterRollback); err != nil {
						logger.Debug("Failed to write ifquery (after rollback) report: %v", err)
					} else {
						logger.Debug("ifquery (after rollback) report: %s", path)
					}
				}
			}
			logger.Warning(
				"Network apply aborted: preflight validation failed (%s). Rolled back /etc/network/*, /etc/hosts, /etc/hostname, /etc/resolv.conf to the pre-restore state (rollback=%s).",
				preflight.CommandLine(),
				strings.TrimSpace(networkRollbackPath),
			)
			_ = promptOkTUI(
				"Network preflight failed",
				configPath,
				buildSig,
				fmt.Sprintf("Network configuration failed preflight and was rolled back automatically.\n\nRollback log:\n%s", strings.TrimSpace(rollbackLog)),
				"OK",
			)
			return fmt.Errorf("network preflight validation failed; network files rolled back")
		}
		if !preflight.Skipped && preflight.ExitError != nil && strings.TrimSpace(networkRollbackPath) != "" {
			message += "\n\nRollback restored network config files to the pre-restore configuration now? (recommended)"
			rollbackNow, err := promptYesNoTUIFunc(
				"Network preflight failed",
				configPath,
				buildSig,
				message,
				"Rollback now",
				"Keep restored files",
			)
			if err != nil {
				return err
			}
			logging.DebugStep(logger, "network safe apply (tui)", "User choice: rollbackNow=%v", rollbackNow)
			if rollbackNow {
				logging.DebugStep(logger, "network safe apply (tui)", "Rollback network files now (backup=%s)", strings.TrimSpace(networkRollbackPath))
				rollbackLog, rbErr := rollbackNetworkFilesNow(ctx, logger, networkRollbackPath, diagnosticsDir)
				if strings.TrimSpace(rollbackLog) != "" {
					logger.Info("Network rollback log: %s", rollbackLog)
				}
				if rbErr != nil {
					_ = promptOkTUI("Network rollback failed", configPath, buildSig, rbErr.Error(), "OK")
					return fmt.Errorf("network preflight validation failed; rollback attempt failed: %w", rbErr)
				}
				_ = promptOkTUI(
					"Network rollback completed",
					configPath,
					buildSig,
					fmt.Sprintf("Network files rolled back to pre-restore configuration.\n\nRollback log:\n%s", strings.TrimSpace(rollbackLog)),
					"OK",
				)
				return fmt.Errorf("network preflight validation failed; network files rolled back")
			}
		} else {
			_ = promptOkTUI("Network preflight failed", configPath, buildSig, message, "OK")
		}
		return fmt.Errorf("network preflight validation failed; aborting live network apply")
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Arm rollback timer BEFORE applying changes")
	handle, err := armNetworkRollback(ctx, logger, rollbackBackupPath, timeout, diagnosticsDir)
	if err != nil {
		return err
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Apply network configuration now")
	if err := applyNetworkConfig(ctx, logger); err != nil {
		logger.Warning("Network apply failed: %v", err)
		return err
	}

	if diagnosticsDir != "" {
		logging.DebugStep(logger, "network safe apply (tui)", "Capture network snapshot (after)")
		if snap, err := writeNetworkSnapshot(ctx, logger, diagnosticsDir, "after", 3*time.Second); err != nil {
			logger.Debug("Network snapshot after apply failed: %v", err)
		} else {
			logger.Debug("Network snapshot (after): %s", snap)
		}

		logging.DebugStep(logger, "network safe apply (tui)", "Run ifquery diagnostic (post-apply)")
		ifqueryPost := runNetworkIfqueryDiagnostic(ctx, 5*time.Second, logger)
		if !ifqueryPost.Skipped {
			if path, err := writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, "ifquery_post_apply.txt", ifqueryPost); err != nil {
				logger.Debug("Failed to write ifquery (post-apply) report: %v", err)
			} else {
				logger.Debug("ifquery (post-apply) report: %s", path)
			}
		}
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Run post-apply health checks")
	health := runNetworkHealthChecks(ctx, networkHealthOptions{
		SystemType:         systemType,
		Logger:             logger,
		CommandTimeout:     3 * time.Second,
		EnableGatewayPing:  true,
		ForceSSHRouteCheck: false,
		EnableDNSResolve:   true,
		LocalPortChecks:    defaultNetworkPortChecks(systemType),
	})
	logNetworkHealthReport(logger, health)
	if diagnosticsDir != "" {
		if path, err := writeNetworkHealthReportFile(diagnosticsDir, health); err != nil {
			logger.Debug("Failed to write network health report: %v", err)
		} else {
			logger.Debug("Network health report: %s", path)
		}
	}

	remaining := handle.remaining(time.Now())
	if remaining <= 0 {
		logger.Warning("Rollback window already expired; leaving rollback armed")
		return nil
	}

	logging.DebugStep(logger, "network safe apply (tui)", "Wait for COMMIT (rollback in %ds)", int(remaining.Seconds()))
	committed, err := promptNetworkCommitTUI(remaining, health, nicRepair, diagnosticsDir, configPath, buildSig)
	if err != nil {
		logger.Warning("Commit prompt error: %v", err)
	}
	logging.DebugStep(logger, "network safe apply (tui)", "User commit result: committed=%v", committed)
	if committed {
		disarmNetworkRollback(ctx, logger, handle)
		logger.Info("Network configuration committed successfully.")
		return nil
	}
	logger.Warning("Network configuration not committed; rollback will run automatically.")
	return nil
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

func runFullRestoreTUI(ctx context.Context, candidate *decryptCandidate, prepared *preparedBundle, destRoot string, logger *logging.Logger, dryRun bool, configPath, buildSig string) error {
	if candidate == nil || prepared == nil || prepared.Manifest.ArchivePath == "" {
		return fmt.Errorf("invalid restore candidate")
	}

	app := newTUIApp()
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

	safeFstabMerge := destRoot == "/" && isRealRestoreFS(restoreFS)
	skipFn := func(name string) bool {
		if !safeFstabMerge {
			return false
		}
		clean := strings.TrimPrefix(strings.TrimSpace(name), "./")
		clean = strings.TrimPrefix(clean, "/")
		return clean == "etc/fstab"
	}

	if safeFstabMerge {
		logger.Warning("Full restore safety: /etc/fstab will not be overwritten; Smart Merge will be offered after extraction.")
	}

	if err := extractPlainArchive(ctx, prepared.ArchivePath, destRoot, logger, skipFn); err != nil {
		return err
	}

	if safeFstabMerge {
		fsTempDir, err := restoreFS.MkdirTemp("", "proxsave-fstab-")
		if err != nil {
			logger.Warning("Failed to create temp dir for fstab merge: %v", err)
		} else {
			defer restoreFS.RemoveAll(fsTempDir)
			fsCategory := []Category{{
				ID:   "filesystem",
				Name: "Filesystem Configuration",
				Paths: []string{
					"./etc/fstab",
				},
			}}
			if err := extractArchiveNative(ctx, prepared.ArchivePath, fsTempDir, logger, fsCategory, RestoreModeCustom, nil, "", nil); err != nil {
				logger.Warning("Failed to extract filesystem config for merge: %v", err)
			} else {
				currentFstab := filepath.Join(destRoot, "etc", "fstab")
				backupFstab := filepath.Join(fsTempDir, "etc", "fstab")
				currentEntries, currentRaw, err := parseFstab(currentFstab)
				if err != nil {
					logger.Warning("Failed to parse current fstab: %v", err)
				} else if backupEntries, _, err := parseFstab(backupFstab); err != nil {
					logger.Warning("Failed to parse backup fstab: %v", err)
				} else {
					analysis := analyzeFstabMerge(logger, currentEntries, backupEntries)
					if len(analysis.ProposedMounts) == 0 {
						logger.Info("No new safe mounts found to restore. Keeping current fstab.")
					} else {
						var msg strings.Builder
						msg.WriteString("ProxSave ha trovato mount mancanti in /etc/fstab.\n\n")
						if analysis.RootComparable && !analysis.RootMatch {
							msg.WriteString("⚠ Root UUID mismatch: il backup sembra provenire da una macchina diversa.\n")
						}
						if analysis.SwapComparable && !analysis.SwapMatch {
							msg.WriteString("⚠ Swap mismatch: verrà mantenuta la configurazione swap attuale.\n")
						}
						msg.WriteString("\nMount proposti (sicuri):\n")
						for _, m := range analysis.ProposedMounts {
							fmt.Fprintf(&msg, "  - %s -> %s (%s)\n", m.Device, m.MountPoint, m.Type)
						}
						if len(analysis.SkippedMounts) > 0 {
							msg.WriteString("\nMount trovati ma non proposti automaticamente:\n")
							for _, m := range analysis.SkippedMounts {
								fmt.Fprintf(&msg, "  - %s -> %s (%s)\n", m.Device, m.MountPoint, m.Type)
							}
							msg.WriteString("\nSuggerimento: verifica dischi/UUID e opzioni (nofail/_netdev) prima di aggiungerli.\n")
						}

						apply, perr := promptYesNoTUIFunc("Smart fstab merge", configPath, buildSig, msg.String(), "Apply", "Skip")
						if perr != nil {
							return perr
						}
						if apply {
							if err := applyFstabMerge(ctx, logger, currentRaw, currentFstab, analysis.ProposedMounts, dryRun); err != nil {
								logger.Warning("Smart Fstab Merge failed: %v", err)
							}
						} else {
							logger.Info("Fstab merge skipped by user.")
						}
					}
				}
			}
		}
	}

	logger.Info("Restore completed successfully.")
	return nil
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
