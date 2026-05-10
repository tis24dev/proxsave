// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func (w *restoreUIWorkflowRun) prepareBundleAndPlan() (fallbackToFullRestore bool, err error) {
	if err := w.prepareBundle(); err != nil {
		return false, err
	}
	cleanupOnFailure := true
	defer func() {
		if cleanupOnFailure && w.prepared != nil {
			w.prepared.Cleanup()
		}
	}()

	fallbackToFullRestore, err = w.planPreparedBundle()
	if err != nil {
		return fallbackToFullRestore, err
	}
	cleanupOnFailure = false
	return fallbackToFullRestore, nil
}

func (w *restoreUIWorkflowRun) planPreparedBundle() (bool, error) {
	w.detectTargetSystem()
	fallbackToFullRestore, err := w.analyzeArchive()
	if err != nil {
		return false, err
	}
	if err := w.confirmCompatibility(); err != nil || fallbackToFullRestore {
		return fallbackToFullRestore, err
	}
	if err := w.selectRestorePlan(); err != nil {
		return false, err
	}
	return false, w.configurePlanForRuntime()
}

func (w *restoreUIWorkflowRun) prepareBundle() error {
	candidate, prepared, err := prepareRestoreBundleFunc(w.ctx, w.cfg, w.logger, w.version, w.ui)
	if err != nil {
		return err
	}
	w.candidate = candidate
	w.prepared = prepared
	w.logger.Info("Restore target: system root (/) — files will be written back to their original paths")
	return nil
}

func (w *restoreUIWorkflowRun) detectTargetSystem() {
	w.systemType = restoreSystem.DetectCurrentSystem()
	w.logger.Info("Detected system type: %s", GetSystemTypeString(w.systemType))
}

func (w *restoreUIWorkflowRun) analyzeArchive() (bool, error) {
	available, decisionInfo, err := analyzeRestoreArchiveFunc(w.prepared.ArchivePath, w.logger)
	if err == nil {
		w.availableCategories = available
		w.decisionInfo = ensureRestoreDecisionInfo(decisionInfo)
		return false, nil
	}

	w.logger.Warning("Could not analyze categories: %v", err)
	w.availableCategories = nil
	w.decisionInfo = fallbackRestoreDecisionInfoFromManifest(w.candidate.Manifest)
	w.logger.Info("Falling back to full restore mode")
	return true, nil
}

func ensureRestoreDecisionInfo(info *RestoreDecisionInfo) *RestoreDecisionInfo {
	if info != nil {
		return info
	}
	return &RestoreDecisionInfo{}
}

func (w *restoreUIWorkflowRun) confirmCompatibility() error {
	warn := ValidateCompatibility(w.systemType, w.decisionInfo.BackupType)
	if warn == nil {
		return nil
	}
	w.logger.Warning("Compatibility check: %v", warn)
	proceed, err := w.ui.ConfirmCompatibility(w.ctx, warn)
	if err != nil {
		return err
	}
	if !proceed {
		return ErrRestoreAborted
	}
	return nil
}

func (w *restoreUIWorkflowRun) selectRestorePlan() error {
	categories, mode, err := w.selectModeAndCategories()
	if err != nil {
		return err
	}
	if mode == RestoreModeCustom {
		categories, err = maybeAddRecommendedCategoriesForTFA(w.ctx, w.ui, w.logger, categories, w.availableCategories)
		if err != nil {
			return err
		}
	}
	w.mode = mode
	w.plan = PlanRestore(w.decisionInfo.ClusterPayload, categories, w.systemType, mode)
	return nil
}

func (w *restoreUIWorkflowRun) selectModeAndCategories() ([]Category, RestoreMode, error) {
	for {
		mode, err := w.ui.SelectRestoreMode(w.ctx, w.systemType)
		if err != nil {
			return nil, mode, err
		}
		if mode != RestoreModeCustom {
			return GetCategoriesForMode(mode, w.systemType, w.availableCategories), mode, nil
		}

		categories, err := w.ui.SelectCategories(w.ctx, w.availableCategories, w.systemType)
		if errors.Is(err, errRestoreBackToMode) {
			continue
		}
		return categories, mode, err
	}
}

func (w *restoreUIWorkflowRun) configurePlanForRuntime() error {
	if err := w.selectPBSRestoreBehavior(); err != nil {
		return err
	}
	if err := w.selectClusterRestoreMode(); err != nil {
		return err
	}
	w.warnAccessControlHostnameMismatch()
	w.collapseStagingWhenUnavailable()
	return nil
}

func (w *restoreUIWorkflowRun) selectPBSRestoreBehavior() error {
	if !w.planNeedsPBSBehavior() {
		return nil
	}
	behavior, err := w.ui.SelectPBSRestoreBehavior(w.ctx)
	if err != nil {
		return err
	}
	w.plan.PBSRestoreBehavior = behavior
	w.logger.Info("PBS restore behavior: %s", behavior.DisplayName())
	return nil
}

func (w *restoreUIWorkflowRun) planNeedsPBSBehavior() bool {
	return w.plan.SystemType.SupportsPBS() &&
		(w.plan.HasCategoryID("pbs_host") ||
			w.plan.HasCategoryID("datastore_pbs") ||
			w.plan.HasCategoryID("pbs_remotes") ||
			w.plan.HasCategoryID("pbs_jobs") ||
			w.plan.HasCategoryID("pbs_notifications") ||
			w.plan.HasCategoryID("pbs_access_control") ||
			w.plan.HasCategoryID("pbs_tape"))
}

func (w *restoreUIWorkflowRun) selectClusterRestoreMode() error {
	if !w.plan.NeedsClusterRestore || !w.plan.ClusterBackup {
		return nil
	}
	w.logger.Info("Cluster payload detected in backup; enabling guarded restore options for pve_cluster")
	choice, err := w.ui.SelectClusterRestoreMode(w.ctx)
	if err != nil {
		return err
	}
	return w.applyClusterRestoreChoice(choice)
}

func (w *restoreUIWorkflowRun) applyClusterRestoreChoice(choice ClusterRestoreMode) error {
	switch choice {
	case ClusterRestoreAbort:
		return ErrRestoreAborted
	case ClusterRestoreSafe:
		w.plan.ApplyClusterSafeMode(true)
		w.logger.Info("Selected SAFE cluster restore: /var/lib/pve-cluster will be exported only, not written to system")
	case ClusterRestoreRecovery:
		w.plan.ApplyClusterSafeMode(false)
		w.logger.Warning("Selected RECOVERY cluster restore: full cluster database will be restored; ensure other nodes are isolated")
	default:
		return fmt.Errorf("invalid cluster restore mode selected")
	}
	return nil
}

func (w *restoreUIWorkflowRun) warnAccessControlHostnameMismatch() {
	if !w.plan.HasCategoryID("pve_access_control") && !w.plan.HasCategoryID("pbs_access_control") {
		return
	}
	currentHost, err := os.Hostname()
	backupHost := strings.TrimSpace(w.decisionInfo.BackupHostname)
	if err != nil || backupHost == "" || strings.TrimSpace(currentHost) == "" {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(currentHost), backupHost) {
		w.logger.Warning("Access control/TFA: backup hostname=%s current hostname=%s; WebAuthn users may require re-enrollment if the UI origin (FQDN/port) changes", backupHost, currentHost)
	}
}

func (w *restoreUIWorkflowRun) collapseStagingWhenUnavailable() {
	if w.destRoot == "/" && isRealRestoreFS(restoreFS) {
		return
	}
	if len(w.plan.StagedCategories) == 0 {
		return
	}
	logging.DebugStep(w.logger, "restore", "Staging disabled (destRoot=%s realFS=%v): extracting %d staged category(ies) directly", w.destRoot, isRealRestoreFS(restoreFS), len(w.plan.StagedCategories))
	w.plan.NormalCategories = append(w.plan.NormalCategories, w.plan.StagedCategories...)
	w.plan.StagedCategories = nil
}

func (w *restoreUIWorkflowRun) confirmRestorePlan() error {
	if w.plan == nil {
		return ErrRestoreAborted
	}
	restoreConfig := &SelectiveRestoreConfig{
		Mode:       w.mode,
		SystemType: w.systemType,
		Metadata:   w.candidate.Manifest,
	}
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, w.plan.NormalCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, w.plan.StagedCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, w.plan.ExportCategories...)

	if err := w.ui.ShowRestorePlan(w.ctx, restoreConfig); err != nil {
		return err
	}
	confirmed, err := w.ui.ConfirmRestore(w.ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		w.logger.Info("Restore operation cancelled by user")
		return ErrRestoreAborted
	}
	return nil
}
