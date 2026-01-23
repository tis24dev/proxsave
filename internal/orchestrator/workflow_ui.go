package orchestrator

import (
	"context"
	"time"
)

// ProgressReporter is used by long-running operations (e.g., cloud scans) to provide
// user-facing progress updates.
type ProgressReporter func(message string)

// TaskRunner runs a function while presenting progress feedback to the user (CLI/TUI).
// Implementations may provide a cancel action that cancels the provided context.
type TaskRunner interface {
	RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error
}

type ExistingPathDecision int

const (
	PathDecisionOverwrite ExistingPathDecision = iota
	PathDecisionNewPath
	PathDecisionCancel
)

// BackupSelectionUI groups prompts used to pick a backup source and a specific backup.
type BackupSelectionUI interface {
	TaskRunner
	ShowMessage(ctx context.Context, title, message string) error
	ShowError(ctx context.Context, title, message string) error
	SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error)
	SelectBackupCandidate(ctx context.Context, candidates []*decryptCandidate) (*decryptCandidate, error)
}

// DecryptWorkflowUI groups prompts used by the decrypt workflow.
type DecryptWorkflowUI interface {
	BackupSelectionUI
	PromptDestinationDir(ctx context.Context, defaultDir string) (string, error)
	ResolveExistingPath(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error)
	PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error)
}

type ClusterRestoreMode int

const (
	ClusterRestoreAbort ClusterRestoreMode = iota
	ClusterRestoreSafe
	ClusterRestoreRecovery
)

// RestoreWorkflowUI groups prompts used by the restore workflow.
type RestoreWorkflowUI interface {
	BackupSelectionUI

	PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error)
	SelectRestoreMode(ctx context.Context, systemType SystemType) (RestoreMode, error)
	SelectCategories(ctx context.Context, available []Category, systemType SystemType) ([]Category, error)

	ShowRestorePlan(ctx context.Context, config *SelectiveRestoreConfig) error
	ConfirmRestore(ctx context.Context) (bool, error)
	ConfirmCompatibility(ctx context.Context, warning error) (bool, error)
	SelectClusterRestoreMode(ctx context.Context) (ClusterRestoreMode, error)
	ConfirmContinueWithoutSafetyBackup(ctx context.Context, cause error) (bool, error)
	ConfirmContinueWithPBSServicesRunning(ctx context.Context) (bool, error)

	ConfirmFstabMerge(ctx context.Context, title, message string, timeout time.Duration, defaultYes bool) (bool, error)

	SelectExportNode(ctx context.Context, exportRoot, currentNode string, exportNodes []string) (string, error)
	ConfirmApplyVMConfigs(ctx context.Context, sourceNode, currentNode string, count int) (bool, error)
	ConfirmApplyStorageCfg(ctx context.Context, storageCfgPath string) (bool, error)
	ConfirmApplyDatacenterCfg(ctx context.Context, datacenterCfgPath string) (bool, error)

	ConfirmAction(ctx context.Context, title, message, yesLabel, noLabel string, timeout time.Duration, defaultYes bool) (bool, error)
	RepairNICNames(ctx context.Context, archivePath string) (*nicRepairResult, error)
	PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error)
}
