// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type networkConfigUIApplyFlow struct {
	ctx                   context.Context
	ui                    RestoreWorkflowUI
	logger                *logging.Logger
	plan                  *RestorePlan
	safetyBackup          *SafetyBackupResult
	networkRollbackBackup *SafetyBackupResult
	stageRoot             string
	archivePath           string
	dryRun                bool
	networkRollbackPath   string
	fullRollbackPath      string
}

type networkConfigUIApplyRequest struct {
	plan                  *RestorePlan
	safetyBackup          *SafetyBackupResult
	networkRollbackBackup *SafetyBackupResult
	stageRoot             string
	archivePath           string
	dryRun                bool
}

func maybeApplyNetworkConfigWithUI(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, req networkConfigUIApplyRequest) (err error) {
	if !shouldAttemptNetworkApply(req.plan) {
		if logger != nil {
			logger.Debug("Network safe apply (UI): skipped (network category not selected)")
		}
		return nil
	}
	done := logging.DebugStart(logger, "network safe apply (ui)", "dryRun=%v euid=%d stage=%s archive=%s", req.dryRun, os.Geteuid(), strings.TrimSpace(req.stageRoot), strings.TrimSpace(req.archivePath))
	defer func() { done(err) }()

	flow := &networkConfigUIApplyFlow{
		ctx:                   ctx,
		ui:                    ui,
		logger:                logger,
		plan:                  req.plan,
		safetyBackup:          req.safetyBackup,
		networkRollbackBackup: req.networkRollbackBackup,
		stageRoot:             req.stageRoot,
		archivePath:           req.archivePath,
		dryRun:                req.dryRun,
	}
	return flow.run()
}

func (f *networkConfigUIApplyFlow) run() error {
	if err := f.validateRuntime(); err != nil {
		return normalizeNetworkApplyRuntimeError(err)
	}
	f.resolveRollbackPaths()
	if f.networkRollbackPath == "" && f.fullRollbackPath == "" {
		return f.handleMissingRollbackBackup()
	}
	return f.confirmAndRunNetworkApply()
}

func normalizeNetworkApplyRuntimeError(err error) error {
	if errors.Is(err, errNetworkApplySkipped) {
		return nil
	}
	return err
}

func (f *networkConfigUIApplyFlow) confirmAndRunNetworkApply() error {
	applyNow, err := f.confirmApplyNow()
	if err != nil {
		return err
	}
	if !applyNow {
		return f.handleApplySkipped()
	}
	return f.runConfirmedNetworkApply()
}

func (f *networkConfigUIApplyFlow) runConfirmedNetworkApply() error {
	rollbackPath, err := f.selectRollbackPath()
	if err != nil || rollbackPath == "" {
		return err
	}
	systemType, suppressPVEChecks := f.networkApplyOptions()
	return applyNetworkWithRollbackWithUI(f.ctx, f.ui, f.logger, networkRollbackUIApplyRequest{
		rollbackBackupPath:  rollbackPath,
		networkRollbackPath: f.networkRollbackPath,
		stageRoot:           f.stageRoot,
		archivePath:         f.archivePath,
		timeout:             defaultNetworkRollbackTimeout,
		systemType:          systemType,
		suppressPVEChecks:   suppressPVEChecks,
	})
}

func (f *networkConfigUIApplyFlow) validateRuntime() error {
	if f.ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if !isRealRestoreFS(restoreFS) {
		f.debug("Skipping live network apply: non-system filesystem in use")
		return errNetworkApplySkipped
	}
	if f.dryRun {
		f.info("Dry run enabled: skipping live network apply")
		return errNetworkApplySkipped
	}
	if os.Geteuid() != 0 {
		f.warning("Skipping live network apply: requires root privileges")
		return errNetworkApplySkipped
	}
	return nil
}

func (f *networkConfigUIApplyFlow) resolveRollbackPaths() {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Resolve rollback backup paths")
	if f.networkRollbackBackup != nil {
		f.networkRollbackPath = strings.TrimSpace(f.networkRollbackBackup.BackupPath)
	}
	if f.safetyBackup != nil {
		f.fullRollbackPath = strings.TrimSpace(f.safetyBackup.BackupPath)
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "Rollback backup resolved: network=%q full=%q", f.networkRollbackPath, f.fullRollbackPath)
}

func (f *networkConfigUIApplyFlow) handleMissingRollbackBackup() error {
	f.warning("Skipping live network apply: rollback backup not available")
	if strings.TrimSpace(f.stageRoot) != "" {
		f.info("Network configuration is staged; skipping NIC repair/apply due to missing rollback backup.")
		return nil
	}
	if err := f.promptNICRepair(); err != nil {
		return err
	}
	f.info("Skipping live network apply (you can reboot or apply manually later).")
	return nil
}

func (f *networkConfigUIApplyFlow) confirmApplyNow() (bool, error) {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Prompt: apply network now with rollback timer")
	sourceLine := "Source: /etc/network (will be applied)"
	if strings.TrimSpace(f.stageRoot) != "" {
		sourceLine = fmt.Sprintf("Source: %s (will be copied to /etc and applied)", strings.TrimSpace(f.stageRoot))
	}
	message := fmt.Sprintf(
		"Network restore: a restored network configuration is ready to apply.\n%s\n\nThis will reload networking immediately (no reboot).\n\nWARNING: This may change the active IP and disconnect SSH/Web sessions.\n\nAfter applying, type COMMIT within %ds or ProxSave will roll back automatically.\n\nRecommendation: run this step from the local console/IPMI, not over SSH.\n\nApply network configuration now?",
		sourceLine,
		int(defaultNetworkRollbackTimeout.Seconds()),
	)
	applyNow, err := f.ui.ConfirmAction(f.ctx, "Apply network configuration", message, "Apply now", "Skip apply", defaultNetworkApplyConfirmTimeout, false)
	logging.DebugStep(f.logger, "network safe apply (ui)", "User choice: applyNow=%v", applyNow)
	return applyNow, err
}

func (f *networkConfigUIApplyFlow) handleApplySkipped() error {
	if strings.TrimSpace(f.stageRoot) == "" {
		if err := f.promptNICRepair(); err != nil {
			return err
		}
	} else {
		f.info("Network configuration is staged (not yet written to /etc); skipping NIC repair prompt.")
	}
	f.info("Skipping live network apply (you can apply later).")
	return nil
}

func (f *networkConfigUIApplyFlow) selectRollbackPath() (string, error) {
	if f.networkRollbackPath != "" {
		logging.DebugStep(f.logger, "network safe apply (ui)", "Selected rollback backup: %s", f.networkRollbackPath)
		return f.networkRollbackPath, nil
	}

	ok, err := f.confirmFullRollbackFallback()
	if err != nil || !ok {
		return f.handleFullRollbackFallbackDeclined(err)
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "Selected rollback backup: %s", f.fullRollbackPath)
	return f.fullRollbackPath, nil
}

func (f *networkConfigUIApplyFlow) confirmFullRollbackFallback() (bool, error) {
	logging.DebugStep(f.logger, "network safe apply (ui)", "Prompt: network-only rollback missing; allow full rollback backup fallback")
	ok, err := f.ui.ConfirmAction(
		f.ctx,
		"Network-only rollback not available",
		"Network-only rollback backup is not available.\n\nIf you proceed, the rollback timer will use the full safety backup, which may revert other restored categories.\n\nProceed anyway?",
		"Proceed with full rollback",
		"Skip apply",
		0,
		false,
	)
	logging.DebugStep(f.logger, "network safe apply (ui)", "User choice: allowFullRollback=%v", ok)
	return ok, err
}

func (f *networkConfigUIApplyFlow) handleFullRollbackFallbackDeclined(err error) (string, error) {
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(f.stageRoot) == "" {
		if repairErr := f.promptNICRepair(); repairErr != nil {
			return "", repairErr
		}
	}
	f.info("Skipping live network apply (you can reboot or apply manually later).")
	return "", nil
}

func (f *networkConfigUIApplyFlow) promptNICRepair() error {
	repairNow, err := f.ui.ConfirmAction(
		f.ctx,
		"NIC name repair (recommended)",
		"Attempt NIC name repair in restored network config files now (no reload)?\n\nThis will only rewrite /etc/network/interfaces and /etc/network/interfaces.d/* when safe mappings are found.",
		"Repair now",
		"Skip repair",
		0,
		false,
	)
	if err != nil {
		return err
	}
	logging.DebugStep(f.logger, "network safe apply (ui)", "User choice: repairNow=%v", repairNow)
	if !repairNow {
		return nil
	}

	repair, err := f.ui.RepairNICNames(f.ctx, f.archivePath)
	if err != nil {
		return err
	}
	if repair != nil && strings.TrimSpace(repair.Summary()) != "" {
		_ = f.ui.ShowMessage(f.ctx, "NIC repair result", repair.Summary())
	}
	return nil
}

func (f *networkConfigUIApplyFlow) networkApplyOptions() (SystemType, bool) {
	if f.plan == nil {
		return SystemTypeUnknown, false
	}
	return f.plan.SystemType, f.plan.SystemType.SupportsPVE() && f.plan.NeedsClusterRestore
}

func (f *networkConfigUIApplyFlow) debug(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Debug(format, args...)
	}
}

func (f *networkConfigUIApplyFlow) info(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Info(format, args...)
	}
}

func (f *networkConfigUIApplyFlow) warning(format string, args ...interface{}) {
	if f.logger != nil {
		f.logger.Warning(format, args...)
	}
}
