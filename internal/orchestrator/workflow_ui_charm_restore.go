package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Restore-side methods of charmWorkflowUI. Parity reference: the deleted
// tview implementation (restore_tui.go / workflow_ui_tui_restore.go at
// commit 543d440). Safety contracts preserved verbatim:
//   - countdown prompts auto-resolve to No on timeout, whatever the default;
//   - the network-commit default (initial focus, bare Enter) is always the
//     safe "Let rollback run" button;
//   - destructive confirms are WithDanger: no single-key y/n shortcuts.

// errSelectorSkip is a local sentinel for selectors whose Esc means "skip"
// (resolve the zero choice without aborting the workflow).
var errSelectorSkip = errors.New("selector skip")

func (u *charmWorkflowUI) backupSummaryPrompt(base string) string {
	if s := strings.TrimSpace(u.selectedBackupSummary); s != "" {
		return base + "\nSelected backup: " + s
	}
	return base
}

// logConfirmTimeout preserves the tview debug line emitted when a countdown
// prompt expired.
func (u *charmWorkflowUI) logConfirmTimeout(res components.ConfirmResult, timeout time.Duration) {
	if res.TimedOut {
		logging.DebugStep(u.logger, "prompt yes/no (ui)", "Timeout expired (%s): proceeding with No", timeout)
	}
}

func (u *charmWorkflowUI) SelectRestoreMode(ctx context.Context, systemType SystemType) (RestoreMode, error) {
	storageText := ""
	switch systemType {
	case SystemTypePVE:
		storageText = "PVE cluster + storage + jobs + mounts"
	case SystemTypePBS:
		storageText = "PBS datastore definitions + sync/verify/prune jobs + mounts"
	case SystemTypeDual:
		storageText = "PVE storage + PBS datastore/jobs + mounts"
	case SystemTypeUnknown:
		storageText = "not available for this system"
	default:
		storageText = "Storage or datastore configuration"
	}
	items := []components.SelectorItem[RestoreMode]{
		{Label: "FULL restore", Description: "Restore everything from backup", Value: RestoreModeFull},
		{Label: "STORAGE/DATASTORE only", Description: storageText, Value: RestoreModeStorage},
		{Label: "SYSTEM BASE only", Description: "Network + SSL + SSH + services + filesystem", Value: RestoreModeBase},
		{Label: "CUSTOM selection", Description: "Choose specific categories", Value: RestoreModeCustom},
	}
	mode, err := shell.Ask(ctx, u.session, components.NewSelector(
		"Select restore mode", items,
		components.WithSelectorPrompt[RestoreMode](u.backupSummaryPrompt("Choose what to restore.")),
		components.WithSelectorBack[RestoreMode](u.abortErr),
	))
	if err != nil {
		return "", u.mapAbort(err)
	}
	return mode, nil
}

func (u *charmWorkflowUI) SelectCategories(ctx context.Context, available []Category, systemType SystemType) ([]Category, error) {
	relevant := filterAndSortCategoriesForSystem(available, systemType)
	if len(relevant) == 0 {
		return nil, fmt.Errorf("no categories available for this system type")
	}
	items := make([]components.MultiSelectItem[Category], 0, len(relevant))
	for _, cat := range relevant {
		items = append(items, components.MultiSelectItem[Category]{
			Label:       cat.Name,
			Description: cat.Description,
			Value:       cat,
		})
	}
	chosen, err := shell.Ask(ctx, u.session, components.NewMultiSelect(
		"Select restore categories", items,
		components.WithMultiSelectPrompt[Category]("Select which categories to restore."),
		components.WithMinSelected[Category](1),
		// Esc goes back to the mode selection, matching the tview Back
		// button; a hard abort stays available via Ctrl+C.
		components.WithMultiSelectBack[Category](errRestoreBackToMode),
	))
	if err != nil {
		if errors.Is(err, errRestoreBackToMode) {
			return nil, errRestoreBackToMode
		}
		return nil, u.mapAbort(err)
	}
	return chosen, nil
}

func (u *charmWorkflowUI) SelectPBSRestoreBehavior(ctx context.Context) (PBSRestoreBehavior, error) {
	items := []components.SelectorItem[PBSRestoreBehavior]{
		{
			Label:       "Merge (existing PBS)",
			Description: "Restore onto an already operational PBS: avoids API-side deletions of existing PBS objects not in the backup",
			Value:       PBSRestoreBehaviorMerge,
		},
		{
			Label:       "Clean 1:1 (fresh PBS install)",
			Description: "Restore onto a new, clean PBS: makes configuration match the backup (may remove objects not in the backup)",
			Value:       PBSRestoreBehaviorClean,
		},
	}
	behavior, err := shell.Ask(ctx, u.session, components.NewSelector(
		"PBS restore behavior", items,
		components.WithSelectorPrompt[PBSRestoreBehavior](u.backupSummaryPrompt("Select PBS restore reconciliation.")),
		components.WithSelectorBack[PBSRestoreBehavior](u.abortErr),
	))
	if err != nil {
		return PBSRestoreBehaviorUnspecified, u.mapAbort(err)
	}
	return behavior, nil
}

func (u *charmWorkflowUI) ShowRestorePlan(ctx context.Context, config *SelectiveRestoreConfig) error {
	if config == nil {
		return fmt.Errorf("restore configuration not available")
	}
	_, err := shell.Ask(ctx, u.session, components.NewPager(
		"Restore plan", buildRestorePlanText(config),
		components.WithPagerAbort(u.abortErr),
		components.WithPagerConfirmLabel("continue"),
	))
	return u.mapAbort(err)
}

func (u *charmWorkflowUI) ConfirmRestore(ctx context.Context) (bool, error) {
	// Stage 1: parity with tview, where the RESTORE button held the initial
	// focus and stage 2 is the destructive guard.
	first, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"Confirm restore",
		"Review the restore plan. Choose RESTORE to start the restore process, or Cancel to abort.\nYou will be asked for explicit confirmation before overwriting files.",
		components.WithLabels("RESTORE", "Cancel"),
		components.WithDefaultYes(true),
	))
	if err != nil {
		return false, u.mapAbort(err)
	}
	if !first.Answer {
		return false, ErrRestoreAborted
	}

	// Stage 2: explicit overwrite confirmation, safe default, no shortcut
	// keys. Declining is a plain "no", not an abort (the engine decides).
	second, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"Confirm overwrite",
		"This operation will overwrite existing configuration files on this system.\n\nAre you sure you want to proceed with the restore?",
		components.WithLabels("Overwrite and restore", "Cancel"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		return false, u.mapAbort(err)
	}
	return second.Answer, nil
}

func (u *charmWorkflowUI) ConfirmCompatibility(ctx context.Context, warning error) (bool, error) {
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"Compatibility warning",
		fmt.Sprintf("Compatibility check reported:\n\n%v\n\nContinuing may cause system instability.\n\nDo you want to continue anyway?", warning),
		components.WithLabels("Continue anyway", "Abort restore"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		return false, u.mapAbort(err)
	}
	return res.Answer, nil
}

func (u *charmWorkflowUI) SelectClusterRestoreMode(ctx context.Context) (ClusterRestoreMode, error) {
	items := []components.SelectorItem[ClusterRestoreMode]{
		{
			Label:       "SAFE",
			Description: "Do NOT write /var/lib/pve-cluster/config.db: export cluster files only",
			Value:       ClusterRestoreSafe,
		},
		{
			Label:       "RECOVERY",
			Description: "Restore full cluster database (/var/lib/pve-cluster): only when cluster is offline/isolated",
			Value:       ClusterRestoreRecovery,
		},
		{
			Label:       "Exit",
			Description: "Abort cluster restore",
			Value:       ClusterRestoreAbort,
		},
	}
	mode, err := shell.Ask(ctx, u.session, components.NewSelector(
		"Cluster restore mode", items,
		components.WithSelectorBack[ClusterRestoreMode](u.abortErr),
	))
	if err != nil {
		return ClusterRestoreAbort, u.mapAbort(err)
	}
	return mode, nil
}

func (u *charmWorkflowUI) ConfirmContinueWithoutSafetyBackup(ctx context.Context, cause error) (bool, error) {
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"Safety backup failed",
		fmt.Sprintf("Failed to create safety backup:\n\n%v\n\nWithout a safety backup, it will be harder to rollback changes.\n\nContinue without safety backup?", cause),
		components.WithLabels("Continue without safety backup", "Abort restore"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		return false, u.mapAbort(err)
	}
	return res.Answer, nil
}

func (u *charmWorkflowUI) ConfirmContinueWithPBSServicesRunning(ctx context.Context) (bool, error) {
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(
		"PBS services running",
		"Unable to stop Proxmox Backup Server services automatically.\n\nContinuing the restore while services are running may lead to inconsistent state.\n\nContinue restore with PBS services still running?",
		components.WithLabels("Continue restore", "Abort restore"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		return false, u.mapAbort(err)
	}
	return res.Answer, nil
}

func (u *charmWorkflowUI) ConfirmFstabMerge(ctx context.Context, title, message string, timeout time.Duration, defaultYes bool) (bool, error) {
	recommended := "Recommended action: Skip"
	if defaultYes {
		recommended = "Recommended action: Apply"
	}
	msg := strings.TrimSpace(message)
	if msg != "" {
		msg = recommended + "\n\n" + msg
	} else {
		msg = recommended
	}
	opts := []components.ConfirmOption{
		components.WithLabels("Apply", "Skip"),
		components.WithDefaultYes(defaultYes),
		components.WithDanger(),
	}
	if timeout > 0 {
		opts = append(opts, components.WithCountdown(timeout))
	}
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(strings.TrimSpace(title), msg, opts...))
	if err != nil {
		return false, u.mapAbort(err)
	}
	u.logConfirmTimeout(res, timeout)
	return res.Answer, nil
}

func (u *charmWorkflowUI) SelectExportNode(ctx context.Context, exportRoot, currentNode string, exportNodes []string) (string, error) {
	items := make([]components.SelectorItem[string], 0, len(exportNodes)+1)
	for _, node := range exportNodes {
		qemuCount, lxcCount := countVMConfigsForNode(exportRoot, node)
		items = append(items, components.SelectorItem[string]{
			Label:       node,
			Description: fmt.Sprintf("qemu=%d lxc=%d", qemuCount, lxcCount),
			Value:       node,
		})
	}
	items = append(items, components.SelectorItem[string]{
		Label:       "Skip VM/CT apply",
		Description: "Do not apply VM/CT configs via API",
		Value:       "",
	})
	node, err := shell.Ask(ctx, u.session, components.NewSelector(
		"Select export node", items,
		components.WithSelectorPrompt[string](fmt.Sprintf("Current node: %s", strings.TrimSpace(currentNode))),
		// Esc means "skip the VM/CT apply", the same non-abort outcome the
		// tview Cancel button produced.
		components.WithSelectorBack[string](errSelectorSkip),
	))
	if err != nil {
		if errors.Is(err, errSelectorSkip) {
			return "", nil
		}
		return "", u.mapAbort(err)
	}
	return node, nil
}

func (u *charmWorkflowUI) ConfirmApplyVMConfigs(ctx context.Context, sourceNode, currentNode string, count int) (bool, error) {
	sourceNode = strings.TrimSpace(sourceNode)
	currentNode = strings.TrimSpace(currentNode)
	message := ""
	if sourceNode == "" || sourceNode == currentNode {
		message = fmt.Sprintf("Found %d VM/CT configs for node %s.\n\nApply them via pvesh now?", count, currentNode)
	} else {
		message = fmt.Sprintf("Found %d VM/CT configs for exported node %s.\nThey will be applied to current node %s.\n\nApply them via pvesh now?", count, sourceNode, currentNode)
	}
	return u.confirmApply(ctx, "Apply VM/CT configs", message)
}

func (u *charmWorkflowUI) ConfirmApplyStorageCfg(ctx context.Context, storageCfgPath string) (bool, error) {
	return u.confirmApply(ctx, "Apply storage.cfg",
		fmt.Sprintf("Storage configuration found:\n\n%s\n\nApply storage.cfg via pvesh now?", strings.TrimSpace(storageCfgPath)))
}

func (u *charmWorkflowUI) ConfirmApplyDatacenterCfg(ctx context.Context, datacenterCfgPath string) (bool, error) {
	return u.confirmApply(ctx, "Apply datacenter.cfg",
		fmt.Sprintf("Datacenter configuration found:\n\n%s\n\nApply datacenter.cfg via pvesh now?", strings.TrimSpace(datacenterCfgPath)))
}

func (u *charmWorkflowUI) confirmApply(ctx context.Context, title, message string) (bool, error) {
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(
		title, message,
		components.WithLabels("Apply via API", "Skip"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		return false, u.mapAbort(err)
	}
	return res.Answer, nil
}

func (u *charmWorkflowUI) ConfirmAction(ctx context.Context, title, message, yesLabel, noLabel string, timeout time.Duration, defaultYes bool) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Confirm"
	}
	opts := []components.ConfirmOption{
		components.WithLabels(yesLabel, noLabel),
		components.WithDefaultYes(defaultYes),
		components.WithDanger(),
	}
	if timeout > 0 {
		opts = append(opts, components.WithCountdown(timeout))
	}
	res, err := shell.Ask(ctx, u.session, components.NewConfirm(title, strings.TrimSpace(message), opts...))
	if err != nil {
		return false, u.mapAbort(err)
	}
	u.logConfirmTimeout(res, timeout)
	return res.Answer, nil
}

func (u *charmWorkflowUI) RepairNICNames(ctx context.Context, archivePath string) (*nicRepairResult, error) {
	return repairNICNamesWithUI(ctx, u, u.logger, archivePath), nil
}

// newNetworkCommitConfirm builds the network-commit screen. Exposed as a
// helper (rather than inlined in PromptNetworkCommit) so the audited tests
// can assert the safety contract on the exact screen the user gets: default
// and focus on "Let rollback run", timeout resolves to not-committed, no
// single-key shortcuts.
func newNetworkCommitConfirm(remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) *components.Confirm {
	var b strings.Builder

	if nicRepair != nil {
		if nicRepair.Applied() {
			fmt.Fprintf(&b, "NIC repair: APPLIED (%d file(s))\n", len(nicRepair.ChangedFiles))
			for _, m := range nicRepair.AppliedNICMap {
				fmt.Fprintf(&b, "- %s -> %s\n", m.OldName, m.NewName)
			}
			b.WriteString("\n")
		} else if nicRepair.SkippedReason != "" {
			fmt.Fprintf(&b, "NIC repair: SKIPPED (%s)\n\n", nicRepair.SkippedReason)
		}
	}

	fmt.Fprintf(&b, "Network health: %s\n", health.Severity.String())
	for _, check := range health.Checks {
		fmt.Fprintf(&b, "- %s %s: %s\n", check.Severity.String(), check.Name, check.Message)
	}
	if health.Severity == networkHealthCritical {
		b.WriteString("\nRecommendation: do NOT commit (let rollback run).\n")
	}
	if strings.TrimSpace(diagnosticsDir) != "" {
		fmt.Fprintf(&b, "\nDiagnostics saved under:\n%s\n", strings.TrimSpace(diagnosticsDir))
	}
	b.WriteString("\nChoose COMMIT to keep the new network configuration.\nIf you do nothing, rollback will be automatic.")

	return components.NewConfirm(
		"Network apply", b.String(),
		components.WithLabels("COMMIT", "Let rollback run"),
		// Safety contract: bare Enter must never commit a possibly broken
		// network configuration.
		components.WithDefaultYes(false),
		components.WithDanger(),
		components.WithCountdown(remaining),
		components.WithCountdownPrefix("Rollback"),
	)
}

func (u *charmWorkflowUI) PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if remaining <= 0 {
		return false, nil
	}
	res, err := shell.Ask(ctx, u.session, newNetworkCommitConfirm(remaining, health, nicRepair, diagnosticsDir))
	if err != nil {
		return false, u.mapAbort(err)
	}
	if res.TimedOut {
		logging.DebugStep(u.logger, "network commit (ui)", "Rollback window expired (%s): not committed", remaining)
		return false, nil
	}
	return res.Answer, nil
}
