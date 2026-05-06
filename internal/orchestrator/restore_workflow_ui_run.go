// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

type restoreUIWorkflowRun struct {
	ctx                         context.Context
	cfg                         *config.Config
	logger                      *logging.Logger
	version                     string
	ui                          RestoreWorkflowUI
	candidate                   *backupCandidate
	prepared                    *preparedBundle
	destRoot                    string
	systemType                  SystemType
	availableCategories         []Category
	decisionInfo                *RestoreDecisionInfo
	mode                        RestoreMode
	plan                        *RestorePlan
	restoreHadWarnings          bool
	safetyBackup                *SafetyBackupResult
	networkRollbackBackup       *SafetyBackupResult
	firewallRollbackBackup      *SafetyBackupResult
	haRollbackBackup            *SafetyBackupResult
	accessControlRollbackBackup *SafetyBackupResult
	stageLogPath                string
	stageRoot                   string
	stageRootForNetworkApply    string
	detailedLogPath             string
	exportLogPath               string
	exportRoot                  string
	needsClusterRestore         bool
	clusterServicesStopped      bool
	pbsServicesStopped          bool
	needsPBSServices            bool
	needsFilesystemRestore      bool
}

func newRestoreUIWorkflowRun(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) *restoreUIWorkflowRun {
	return &restoreUIWorkflowRun{
		ctx:      ctx,
		cfg:      cfg,
		logger:   logger,
		version:  version,
		ui:       ui,
		destRoot: "/",
	}
}

func (w *restoreUIWorkflowRun) run() error {
	fallbackToFullRestore, err := w.prepareBundleAndPlan()
	if err != nil {
		return err
	}
	defer w.prepared.Cleanup()
	if fallbackToFullRestore {
		return runFullRestoreWithUI(w.ctx, w.ui, w.candidate, w.prepared, w.destRoot, w.logger, w.cfg.DryRun)
	}
	return w.runSelectiveRestore()
}

func (w *restoreUIWorkflowRun) runSelectiveRestore() error {
	if err := w.confirmRestorePlan(); err != nil {
		return err
	}
	if err := w.createRollbackBackups(); err != nil {
		return err
	}
	cleanupServices, err := w.prepareRestoreServices()
	if err != nil {
		return err
	}
	defer cleanupServices()
	if err := w.prepareAndRestoreSelectedPayloads(); err != nil {
		return err
	}
	if err := w.runPostRestoreApplyWorkflows(); err != nil {
		return err
	}
	w.logRestoreCompletion()
	w.logServiceRestartAdvice()
	w.checkZFSPoolsAfterRestore()
	w.logRebootRecommendation()
	return nil
}

func (w *restoreUIWorkflowRun) prepareAndRestoreSelectedPayloads() error {
	w.interceptFilesystemCategory()
	if err := w.extractNormalCategories(); err != nil {
		return err
	}
	if err := w.smartMergeFilesystemCategory(); err != nil {
		return err
	}
	if err := w.exportCategories(); err != nil {
		return err
	}
	if err := w.runClusterSafeApply(); err != nil {
		return err
	}
	if err := w.stageAndApplySensitiveCategories(); err != nil {
		return err
	}
	return nil
}

func (w *restoreUIWorkflowRun) runPostRestoreApplyWorkflows() error {
	w.verifyPBSNotificationsAfterRestore()
	if err := w.installNetworkConfigFromStage(); err != nil {
		return err
	}
	w.recreateStorageDirectories()
	if err := w.repairDNSAfterRestore(); err != nil {
		return err
	}
	if err := w.applyNetworkConfig(); err != nil {
		return err
	}
	if err := w.applyFirewallConfig(); err != nil {
		return err
	}
	if err := w.applyHAConfig(); err != nil {
		return err
	}
	return nil
}
