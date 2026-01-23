package orchestrator

import (
	"context"
	"fmt"
	"time"
)

type fakeRestoreWorkflowUI struct {
	mode               RestoreMode
	categories         []Category
	confirmRestore     bool
	confirmCompatible  bool
	clusterMode        ClusterRestoreMode
	continueNoSafety   bool
	continuePBSServices bool
	confirmFstabMerge  bool
	exportNode         string
	applyVMConfigs     bool
	applyStorageCfg    bool
	applyDatacenterCfg bool
	confirmAction      bool
	networkCommit      bool

	modeErr               error
	categoriesErr         error
	confirmRestoreErr     error
	confirmCompatibleErr  error
	clusterModeErr        error
	continueNoSafetyErr   error
	continuePBSServicesErr error
	confirmFstabMergeErr  error
	confirmActionErr      error
	repairNICNamesErr     error
	networkCommitErr      error
}

func (f *fakeRestoreWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	return run(ctx, nil)
}

func (f *fakeRestoreWorkflowUI) ShowMessage(ctx context.Context, title, message string) error { return nil }
func (f *fakeRestoreWorkflowUI) ShowError(ctx context.Context, title, message string) error   { return nil }

func (f *fakeRestoreWorkflowUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	return decryptPathOption{}, fmt.Errorf("unexpected SelectBackupSource call")
}

func (f *fakeRestoreWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*decryptCandidate) (*decryptCandidate, error) {
	return nil, fmt.Errorf("unexpected SelectBackupCandidate call")
}

func (f *fakeRestoreWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	return "", fmt.Errorf("unexpected PromptDecryptSecret call")
}

func (f *fakeRestoreWorkflowUI) SelectRestoreMode(ctx context.Context, systemType SystemType) (RestoreMode, error) {
	return f.mode, f.modeErr
}

func (f *fakeRestoreWorkflowUI) SelectCategories(ctx context.Context, available []Category, systemType SystemType) ([]Category, error) {
	return f.categories, f.categoriesErr
}

func (f *fakeRestoreWorkflowUI) ShowRestorePlan(ctx context.Context, config *SelectiveRestoreConfig) error { return nil }

func (f *fakeRestoreWorkflowUI) ConfirmRestore(ctx context.Context) (bool, error) {
	return f.confirmRestore, f.confirmRestoreErr
}

func (f *fakeRestoreWorkflowUI) ConfirmCompatibility(ctx context.Context, warning error) (bool, error) {
	return f.confirmCompatible, f.confirmCompatibleErr
}

func (f *fakeRestoreWorkflowUI) SelectClusterRestoreMode(ctx context.Context) (ClusterRestoreMode, error) {
	return f.clusterMode, f.clusterModeErr
}

func (f *fakeRestoreWorkflowUI) ConfirmContinueWithoutSafetyBackup(ctx context.Context, cause error) (bool, error) {
	return f.continueNoSafety, f.continueNoSafetyErr
}

func (f *fakeRestoreWorkflowUI) ConfirmContinueWithPBSServicesRunning(ctx context.Context) (bool, error) {
	return f.continuePBSServices, f.continuePBSServicesErr
}

func (f *fakeRestoreWorkflowUI) ConfirmFstabMerge(ctx context.Context, title, message string, timeout time.Duration, defaultYes bool) (bool, error) {
	return f.confirmFstabMerge, f.confirmFstabMergeErr
}

func (f *fakeRestoreWorkflowUI) SelectExportNode(ctx context.Context, exportRoot, currentNode string, exportNodes []string) (string, error) {
	return f.exportNode, nil
}

func (f *fakeRestoreWorkflowUI) ConfirmApplyVMConfigs(ctx context.Context, sourceNode, currentNode string, count int) (bool, error) {
	return f.applyVMConfigs, nil
}

func (f *fakeRestoreWorkflowUI) ConfirmApplyStorageCfg(ctx context.Context, storageCfgPath string) (bool, error) {
	return f.applyStorageCfg, nil
}

func (f *fakeRestoreWorkflowUI) ConfirmApplyDatacenterCfg(ctx context.Context, datacenterCfgPath string) (bool, error) {
	return f.applyDatacenterCfg, nil
}

func (f *fakeRestoreWorkflowUI) ConfirmAction(ctx context.Context, title, message, yesLabel, noLabel string, timeout time.Duration, defaultYes bool) (bool, error) {
	return f.confirmAction, f.confirmActionErr
}

func (f *fakeRestoreWorkflowUI) RepairNICNames(ctx context.Context, archivePath string) (*nicRepairResult, error) {
	return nil, f.repairNICNamesErr
}

func (f *fakeRestoreWorkflowUI) PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error) {
	return f.networkCommit, f.networkCommitErr
}

