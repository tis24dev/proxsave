package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

func (u *tuiWorkflowUI) SelectRestoreMode(ctx context.Context, systemType SystemType) (RestoreMode, error) {
	return selectRestoreModeTUI(systemType, u.configPath, u.buildSig, strings.TrimSpace(u.selectedBackupSummary))
}

func (u *tuiWorkflowUI) SelectCategories(ctx context.Context, available []Category, systemType SystemType) ([]Category, error) {
	return selectCategoriesTUI(available, systemType, u.configPath, u.buildSig)
}

func (u *tuiWorkflowUI) ShowRestorePlan(ctx context.Context, config *SelectiveRestoreConfig) error {
	return showRestorePlanTUI(config, u.configPath, u.buildSig)
}

func (u *tuiWorkflowUI) ConfirmRestore(ctx context.Context) (bool, error) {
	return confirmRestoreTUI(u.configPath, u.buildSig)
}

func (u *tuiWorkflowUI) ConfirmCompatibility(ctx context.Context, warning error) (bool, error) {
	return promptCompatibilityTUI(u.configPath, u.buildSig, warning)
}

func (u *tuiWorkflowUI) SelectClusterRestoreMode(ctx context.Context) (ClusterRestoreMode, error) {
	choice, err := promptClusterRestoreModeTUI(u.configPath, u.buildSig)
	if err != nil {
		return ClusterRestoreAbort, err
	}
	switch choice {
	case 1:
		return ClusterRestoreSafe, nil
	case 2:
		return ClusterRestoreRecovery, nil
	default:
		return ClusterRestoreAbort, nil
	}
}

func (u *tuiWorkflowUI) ConfirmContinueWithoutSafetyBackup(ctx context.Context, cause error) (bool, error) {
	return promptContinueWithoutSafetyBackupTUI(u.configPath, u.buildSig, cause)
}

func (u *tuiWorkflowUI) ConfirmContinueWithPBSServicesRunning(ctx context.Context) (bool, error) {
	return promptContinueWithPBSServicesTUI(u.configPath, u.buildSig)
}

func (u *tuiWorkflowUI) ConfirmFstabMerge(ctx context.Context, title, message string, timeout time.Duration, defaultYes bool) (bool, error) {
	recommended := "Recommended action: Skip"
	if defaultYes {
		recommended = "Recommended action: Apply"
	}

	msg := strings.TrimSpace(message)
	if msg != "" {
		msg = fmt.Sprintf("%s\n\n%s", recommended, msg)
	} else {
		msg = recommended
	}

	return promptYesNoTUIWithCountdown(ctx, u.logger, title, u.configPath, u.buildSig, msg, "Apply", "Skip", timeout)
}

func (u *tuiWorkflowUI) SelectExportNode(ctx context.Context, exportRoot, currentNode string, exportNodes []string) (string, error) {
	app := newTUIApp()
	var selected string
	var cancelled bool

	list := tview.NewList().ShowSecondaryText(true)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	for _, node := range exportNodes {
		qemuCount, lxcCount := countVMConfigsForNode(exportRoot, node)
		list.AddItem(node, fmt.Sprintf("qemu=%d lxc=%d", qemuCount, lxcCount), 0, nil)
	}
	list.AddItem("Skip VM/CT apply", "Do not apply VM/CT configs via API", 0, nil)

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(exportNodes) {
			selected = exportNodes[index]
		} else {
			selected = ""
		}
		app.Stop()
	})
	list.SetDoneFunc(func() {
		cancelled = true
		app.Stop()
	})

	form := components.NewForm(app)
	listItem := components.NewListFormItem(list).
		SetLabel(fmt.Sprintf("Current node: %s", strings.TrimSpace(currentNode))).
		SetFieldHeight(8)
	form.Form.AddFormItem(listItem)
	form.Form.SetFocus(0)

	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := buildRestoreWizardPage("Select export node", u.configPath, u.buildSig, form.Form)
	form.SetParentView(page)

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return "", err
	}
	if cancelled {
		return "", nil
	}
	return selected, nil
}

func (u *tuiWorkflowUI) ConfirmApplyVMConfigs(ctx context.Context, sourceNode, currentNode string, count int) (bool, error) {
	sourceNode = strings.TrimSpace(sourceNode)
	currentNode = strings.TrimSpace(currentNode)
	message := ""
	if sourceNode == "" || sourceNode == currentNode {
		message = fmt.Sprintf("Found %d VM/CT configs for node %s.\n\nApply them via pvesh now?", count, currentNode)
	} else {
		message = fmt.Sprintf("Found %d VM/CT configs for exported node %s.\nThey will be applied to current node %s.\n\nApply them via pvesh now?", count, sourceNode, currentNode)
	}
	return promptYesNoTUIFunc("Apply VM/CT configs", u.configPath, u.buildSig, message, "Apply via API", "Skip")
}

func (u *tuiWorkflowUI) ConfirmApplyStorageCfg(ctx context.Context, storageCfgPath string) (bool, error) {
	message := fmt.Sprintf("Storage configuration found:\n\n%s\n\nApply storage.cfg via pvesh now?", strings.TrimSpace(storageCfgPath))
	return promptYesNoTUIFunc("Apply storage.cfg", u.configPath, u.buildSig, message, "Apply via API", "Skip")
}

func (u *tuiWorkflowUI) ConfirmApplyDatacenterCfg(ctx context.Context, datacenterCfgPath string) (bool, error) {
	message := fmt.Sprintf("Datacenter configuration found:\n\n%s\n\nApply datacenter.cfg via pvesh now?", strings.TrimSpace(datacenterCfgPath))
	return promptYesNoTUIFunc("Apply datacenter.cfg", u.configPath, u.buildSig, message, "Apply via API", "Skip")
}

