// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

func (w *restoreUIWorkflowRun) interceptFilesystemCategory() {
	if !w.plan.HasCategoryID("filesystem") {
		return
	}
	w.needsFilesystemRestore = true
	w.plan.NormalCategories = categoriesWithoutID(w.plan.NormalCategories, "filesystem")
	logging.DebugStep(w.logger, "restore", "Filesystem category intercepted: enabling Smart Merge workflow (skipping generic extraction)")
}

func categoriesWithoutID(categories []Category, id string) []Category {
	var filtered []Category
	for _, cat := range categories {
		if cat.ID != id {
			filtered = append(filtered, cat)
		}
	}
	return filtered
}

func (w *restoreUIWorkflowRun) extractNormalCategories() error {
	if len(w.plan.NormalCategories) == 0 {
		w.logger.Info("")
		w.logger.Info("No system-path categories selected for restore (only export categories will be processed).")
		return nil
	}

	w.logger.Info("")
	categories := w.systemExtractionCategories()
	if len(categories) == 0 {
		logging.DebugStep(w.logger, "restore", "Skip system-path extraction: no categories remain after shadow-guard")
		w.logger.Info("No system-path categories remain after cluster shadow-guard; skipping system-path extraction.")
		return nil
	}

	detailedLogPath, err := extractSelectiveArchive(w.ctx, w.prepared.ArchivePath, w.destRoot, categories, w.mode, w.logger)
	if err != nil {
		w.logger.Error("Restore failed: %v", err)
		if w.safetyBackup != nil {
			w.logger.Info("You can rollback using the safety backup at: %s", w.safetyBackup.BackupPath)
		}
		return err
	}
	w.detailedLogPath = detailedLogPath
	return nil
}

func (w *restoreUIWorkflowRun) systemExtractionCategories() []Category {
	categories := w.plan.NormalCategories
	if !w.needsClusterRestore {
		return categories
	}
	logging.DebugStep(w.logger, "restore", "Cluster RECOVERY shadow-guard: sanitize categories to avoid /etc/pve shadow writes")
	sanitized, removed := sanitizeCategoriesForClusterRecovery(categories)
	w.logClusterShadowGuardResult(categories, sanitized, removed)
	return sanitized
}

func (w *restoreUIWorkflowRun) logClusterShadowGuardResult(before, after []Category, removed map[string][]string) {
	removedPaths := 0
	for _, paths := range removed {
		removedPaths += len(paths)
	}
	logging.DebugStep(w.logger, "restore", "Cluster RECOVERY shadow-guard: categories_before=%d categories_after=%d removed_categories=%d removed_paths=%d", len(before), len(after), len(removed), removedPaths)
	if len(removed) == 0 {
		logging.DebugStep(w.logger, "restore", "Cluster RECOVERY shadow-guard: no /etc/pve paths detected in selected categories")
		return
	}

	w.logger.Warning("Cluster RECOVERY restore: skipping direct restore of /etc/pve paths to prevent shadowing while pmxcfs is stopped/unmounted")
	for _, cat := range before {
		if paths, ok := removed[cat.ID]; ok && len(paths) > 0 {
			w.logger.Warning("  - %s (%s): %s", cat.Name, cat.ID, strings.Join(paths, ", "))
		}
	}
	w.logger.Info("These paths are expected to be restored from config.db and become visible after /etc/pve is remounted.")
}

func (w *restoreUIWorkflowRun) smartMergeFilesystemCategory() error {
	if !w.needsFilesystemRestore {
		return nil
	}
	w.logger.Info("")
	fsTempDir, err := restoreFS.MkdirTemp("", "proxsave-fstab-")
	if err != nil {
		w.restoreHadWarnings = true
		w.logger.Warning("Failed to create temp dir for fstab merge: %v", err)
		return nil
	}
	defer restoreFS.RemoveAll(fsTempDir)
	return w.extractAndMergeFstab(fsTempDir)
}

func (w *restoreUIWorkflowRun) extractAndMergeFstab(fsTempDir string) error {
	fsCat := GetCategoryByID("filesystem", w.availableCategories)
	if fsCat == nil {
		w.logger.Warning("Filesystem category not available in analyzed backup contents; skipping fstab merge")
		return nil
	}
	if _, err := extractSelectiveArchive(w.ctx, w.prepared.ArchivePath, fsTempDir, []Category{*fsCat}, RestoreModeCustom, w.logger); err != nil {
		return w.handleFstabExtractError(err)
	}
	w.extractFstabInventory(fsTempDir)
	currentFstab := filepath.Join(w.destRoot, "etc", "fstab")
	backupFstab := filepath.Join(fsTempDir, "etc", "fstab")
	if err := smartMergeFstabWithUI(w.ctx, w.logger, w.ui, currentFstab, backupFstab, w.cfg.DryRun); err != nil {
		return w.handleFstabMergeError(err)
	}
	return nil
}

func (w *restoreUIWorkflowRun) handleFstabExtractError(err error) error {
	if restoreAbortOrInput(err) {
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("Failed to extract filesystem config for merge: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) extractFstabInventory(fsTempDir string) {
	inventoryCategory := []Category{{
		ID:   "fstab_inventory",
		Name: "Fstab inventory (device mapping)",
		Paths: []string{
			"./var/lib/proxsave-info/commands/system/blkid.txt",
			"./var/lib/proxsave-info/commands/system/lsblk_json.json",
			"./var/lib/proxsave-info/commands/system/lsblk.txt",
			"./var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json",
		},
	}}
	err := extractArchiveNative(w.ctx, restoreArchiveOptions{
		archivePath: w.prepared.ArchivePath,
		destRoot:    fsTempDir,
		logger:      w.logger,
		categories:  inventoryCategory,
		mode:        RestoreModeCustom,
	})
	if err != nil {
		w.logger.Debug("Failed to extract fstab inventory data (continuing): %v", err)
	}
}

func (w *restoreUIWorkflowRun) handleFstabMergeError(err error) error {
	if restoreAbortOrInput(err) {
		w.logger.Info("Restore aborted by user during Smart Filesystem Configuration Merge.")
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("Smart Fstab Merge failed: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) exportCategories() error {
	if len(w.plan.ExportCategories) == 0 {
		return nil
	}
	w.exportRoot = exportDestRoot(w.cfg.BaseDir)
	w.logger.Info("")
	w.logger.Info("Exporting %d export-only category(ies) to: %s", len(w.plan.ExportCategories), w.exportRoot)
	if err := restoreFS.MkdirAll(w.exportRoot, 0o755); err != nil {
		return fmt.Errorf("failed to create export directory %s: %w", w.exportRoot, err)
	}

	exportLog, err := extractSelectiveArchive(w.ctx, w.prepared.ArchivePath, w.exportRoot, w.plan.ExportCategories, RestoreModeCustom, w.logger)
	if err != nil {
		return w.handleExportError(err)
	}
	w.exportLogPath = exportLog
	return nil
}

func (w *restoreUIWorkflowRun) handleExportError(err error) error {
	if restoreAbortOrInput(err) {
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("Export completed with errors: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) runClusterSafeApply() error {
	if !w.plan.ClusterSafeMode {
		return nil
	}
	if w.exportRoot == "" {
		w.logger.Warning("Cluster SAFE mode selected but export directory not available; skipping automatic pvesh apply")
		return nil
	}
	w.extractSafeApplyInventory()
	if err := runSafeClusterApplyWithUI(w.ctx, w.ui, w.exportRoot, w.logger, w.plan); err != nil {
		return w.handleClusterSafeApplyError(err)
	}
	return nil
}

func (w *restoreUIWorkflowRun) extractSafeApplyInventory() {
	safeInvCategory := []Category{{
		ID:   "safe_apply_inventory",
		Name: "SAFE apply inventory (pools/mappings)",
		Paths: []string{
			"./etc/pve/user.cfg",
			"./var/lib/proxsave-info/commands/pve/mapping_pci.json",
			"./var/lib/proxsave-info/commands/pve/mapping_usb.json",
			"./var/lib/proxsave-info/commands/pve/mapping_dir.json",
		},
	}}
	err := extractArchiveNative(w.ctx, restoreArchiveOptions{
		archivePath: w.prepared.ArchivePath,
		destRoot:    w.exportRoot,
		logger:      w.logger,
		categories:  safeInvCategory,
		mode:        RestoreModeCustom,
	})
	if err != nil {
		w.logger.Debug("Failed to extract SAFE apply inventory (continuing): %v", err)
	}
}

func (w *restoreUIWorkflowRun) handleClusterSafeApplyError(err error) error {
	if restoreAbortOrInput(err) {
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("Cluster SAFE apply completed with errors: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) stageAndApplySensitiveCategories() error {
	if len(w.plan.StagedCategories) == 0 {
		return nil
	}
	if err := w.extractStagedCategories(); err != nil {
		return err
	}
	return w.applyStagedCategories()
}

func (w *restoreUIWorkflowRun) extractStagedCategories() error {
	w.stageRoot = stageDestRoot()
	w.logger.Info("")
	w.logger.Info("Staging %d sensitive category(ies) to: %s", len(w.plan.StagedCategories), w.stageRoot)
	if err := restoreFS.MkdirAll(w.stageRoot, 0o755); err != nil {
		return fmt.Errorf("failed to create staging directory %s: %w", w.stageRoot, err)
	}

	stageLog, err := extractSelectiveArchive(w.ctx, w.prepared.ArchivePath, w.stageRoot, w.plan.StagedCategories, RestoreModeCustom, w.logger)
	if err != nil {
		return w.handleStageExtractError(err)
	}
	w.stageLogPath = stageLog
	return nil
}

func (w *restoreUIWorkflowRun) handleStageExtractError(err error) error {
	if restoreAbortOrInput(err) {
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("Staging completed with errors: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) applyStagedCategories() error {
	if err := w.applyPBSMountGuards(); err != nil {
		return err
	}
	w.logger.Info("")
	steps := []restoreStageApplyStep{
		{name: "PBS staged config apply", run: func() error { return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, w.stageRoot, w.cfg.DryRun) }},
		{name: "PVE staged config apply", run: func() error {
			return maybeApplyPVEConfigsFromStage(w.ctx, w.logger, w.plan, w.stageRoot, w.destRoot, w.cfg.DryRun)
		}},
		{name: "PVE SDN staged apply", run: func() error { return maybeApplyPVESDNFromStage(w.ctx, w.logger, w.plan, w.stageRoot, w.cfg.DryRun) }},
		{name: "Access control staged apply", run: w.applyAccessControlFromStage},
		{name: "Notifications staged apply", run: func() error {
			return maybeApplyNotificationsFromStage(w.ctx, w.logger, w.plan, w.stageRoot, w.cfg.DryRun)
		}},
	}
	for _, step := range steps {
		if err := w.runStageApplyStep(step); err != nil {
			return err
		}
	}
	return nil
}

type restoreStageApplyStep struct {
	name string
	run  func() error
}

func (w *restoreUIWorkflowRun) applyPBSMountGuards() error {
	err := maybeApplyPBSDatastoreMountGuards(w.ctx, w.logger, w.plan, w.stageRoot, w.destRoot, w.cfg.DryRun)
	if err == nil {
		return nil
	}
	if restoreAbortOrInput(err) {
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("PBS mount guard: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) runStageApplyStep(step restoreStageApplyStep) error {
	if err := step.run(); err != nil {
		if restoreAbortOrInput(err) {
			return err
		}
		w.restoreHadWarnings = true
		w.logStageApplyWarning(step.name, err)
	}
	return nil
}

func (w *restoreUIWorkflowRun) logStageApplyWarning(name string, err error) {
	if errors.Is(err, ErrAccessControlApplyNotCommitted) {
		w.logAccessControlNotCommitted(err)
		return
	}
	w.logger.Warning("%s: %v", name, err)
}

func restoreAbortOrInput(err error) bool {
	return errors.Is(err, ErrRestoreAborted) || input.IsAborted(err)
}
