// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"errors"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

func (w *restoreUIWorkflowRun) applyAccessControlFromStage() error {
	return maybeApplyAccessControlWithUI(w.ctx, w.ui, w.logger, w.plan, w.safetyBackup, w.accessControlRollbackBackup, w.stageRoot, w.cfg.DryRun)
}

func (w *restoreUIWorkflowRun) logAccessControlNotCommitted(err error) {
	var notCommitted *AccessControlApplyNotCommittedError
	rollbackLog := ""
	rollbackArmed := false
	deadline := time.Time{}
	if errors.As(err, &notCommitted) && notCommitted != nil {
		rollbackLog = strings.TrimSpace(notCommitted.RollbackLog)
		rollbackArmed = notCommitted.RollbackArmed
		deadline = notCommitted.RollbackDeadline
	}
	w.logGenericRollbackNotCommitted("Access control apply", rollbackArmed, deadline, rollbackLog)
}

func (w *restoreUIWorkflowRun) verifyPBSNotificationsAfterRestore() {
	if !w.plan.SystemType.SupportsPBS() || !w.plan.HasCategoryID("pbs_notifications") || w.pbsServicesStopped {
		return
	}
	if err := maybeVerifyAndRepairPBSNotificationsAfterRestore(w.ctx, w.logger, w.plan, w.stageRoot, w.cfg.DryRun); err != nil {
		w.restoreHadWarnings = true
		w.logger.Warning("PBS notifications verification/repair: %v", err)
	}
}

func (w *restoreUIWorkflowRun) installNetworkConfigFromStage() error {
	w.stageRootForNetworkApply = w.stageRoot
	installed, err := maybeInstallNetworkConfigFromStage(w.ctx, w.logger, w.plan, w.stageRoot, w.prepared.ArchivePath, w.networkRollbackBackup, w.cfg.DryRun)
	if err != nil {
		if restoreAbortOrInput(err) {
			return err
		}
		w.restoreHadWarnings = true
		w.logger.Warning("Network staged install: %v", err)
		return nil
	}
	if installed {
		w.stageRootForNetworkApply = ""
		logging.DebugStep(w.logger, "restore", "Network staged install completed: configuration written to /etc (no reload); live apply will use system paths")
	}
	return nil
}

func (w *restoreUIWorkflowRun) recreateStorageDirectories() {
	w.logger.Info("")
	categories := append([]Category{}, w.plan.NormalCategories...)
	categories = append(categories, w.plan.StagedCategories...)
	if !shouldRecreateDirectories(w.systemType, categories) {
		w.logger.Debug("Skipping datastore/storage directory recreation (category not selected)")
		return
	}
	if err := RecreateDirectoriesFromConfig(w.systemType, w.logger); err != nil {
		w.restoreHadWarnings = true
		w.logger.Warning("Failed to recreate directory structures: %v", err)
		w.logger.Warning("You may need to manually create storage/datastore directories")
	}
}

func (w *restoreUIWorkflowRun) repairDNSAfterRestore() error {
	w.logger.Info("")
	if !w.plan.HasCategoryID("network") {
		return nil
	}
	w.logger.Info("")
	err := maybeRepairResolvConfAfterRestore(w.ctx, w.logger, w.prepared.ArchivePath, w.cfg.DryRun)
	if err == nil {
		return nil
	}
	if restoreAbortOrInput(err) {
		return err
	}
	w.restoreHadWarnings = true
	w.logger.Warning("DNS resolver repair: %v", err)
	return nil
}

func (w *restoreUIWorkflowRun) applyNetworkConfig() error {
	w.logger.Info("")
	err := maybeApplyNetworkConfigWithUI(w.ctx, w.ui, w.logger, networkConfigUIApplyRequest{
		plan:                  w.plan,
		safetyBackup:          w.safetyBackup,
		networkRollbackBackup: w.networkRollbackBackup,
		stageRoot:             w.stageRootForNetworkApply,
		archivePath:           w.prepared.ArchivePath,
		dryRun:                w.cfg.DryRun,
	})
	if err == nil {
		return nil
	}
	if restoreAbortOrInput(err) {
		w.logger.Info("Restore aborted by user during network apply prompt.")
		return err
	}
	w.restoreHadWarnings = true
	w.logNetworkApplyError(err)
	return nil
}

func (w *restoreUIWorkflowRun) logNetworkApplyError(err error) {
	if !errors.Is(err, ErrNetworkApplyNotCommitted) {
		w.logger.Warning("Network apply step skipped or failed: %v", err)
		return
	}
	var notCommitted *NetworkApplyNotCommittedError
	if !errors.As(err, &notCommitted) || notCommitted == nil {
		w.logger.Warning("Network apply not committed; rollback state unknown.")
		return
	}
	w.saveNetworkAbortInfo(notCommitted)
	observedIP, originalIP := networkNotCommittedIPs(notCommitted)
	reconnectHost := reconnectHostFromOriginalIP(originalIP)
	w.logNetworkRollbackState(notCommitted.RollbackArmed, observedIP, originalIP, reconnectHost)
	if rollbackLog := strings.TrimSpace(notCommitted.RollbackLog); rollbackLog != "" {
		w.logger.Info("Rollback log: %s", rollbackLog)
	}
}

func (w *restoreUIWorkflowRun) saveNetworkAbortInfo(notCommitted *NetworkApplyNotCommittedError) {
	lastRestoreAbortInfo = &RestoreAbortInfo{
		NetworkRollbackArmed:  notCommitted.RollbackArmed,
		NetworkRollbackLog:    strings.TrimSpace(notCommitted.RollbackLog),
		NetworkRollbackMarker: strings.TrimSpace(notCommitted.RollbackMarker),
		OriginalIP:            notCommitted.OriginalIP,
		CurrentIP:             strings.TrimSpace(notCommitted.RestoredIP),
		RollbackDeadline:      notCommitted.RollbackDeadline,
	}
}

func (w *restoreUIWorkflowRun) logNetworkRollbackState(armed bool, observedIP, originalIP, reconnectHost string) {
	if armed {
		w.logger.Warning("Network apply not committed; rollback is ARMED and will run automatically.")
	} else {
		w.logger.Warning("Network apply not committed; rollback has executed (or marker cleared).")
	}
	if reconnectHost != "" && reconnectHost != "unknown" && originalIP != "unknown" {
		w.logger.Warning("IP now (after apply): %s. Expected after rollback: %s. Reconnect using: %s", observedIP, originalIP, reconnectHost)
	} else if originalIP != "unknown" {
		w.logger.Warning("IP now (after apply): %s. Expected after rollback: %s", observedIP, originalIP)
	} else {
		w.logger.Warning("IP now (after apply): %s", observedIP)
	}
}

func (w *restoreUIWorkflowRun) applyFirewallConfig() error {
	w.logger.Info("")
	err := maybeApplyPVEFirewallWithUI(w.ctx, w.ui, w.logger, w.plan, w.safetyBackup, w.firewallRollbackBackup, w.stageRoot, w.cfg.DryRun)
	if err == nil {
		return nil
	}
	if restoreAbortOrInput(err) {
		w.logger.Info("Restore aborted by user during firewall apply prompt.")
		return err
	}
	w.restoreHadWarnings = true
	w.logFirewallApplyError(err)
	return nil
}

func (w *restoreUIWorkflowRun) logFirewallApplyError(err error) {
	if !errors.Is(err, ErrFirewallApplyNotCommitted) {
		w.logger.Warning("Firewall apply step skipped or failed: %v", err)
		return
	}
	armed, deadline, rollbackLog := firewallRollbackSummary(err)
	w.logGenericRollbackNotCommitted("Firewall apply", armed, deadline, rollbackLog)
}

func firewallRollbackSummary(err error) (bool, time.Time, string) {
	var notCommitted *FirewallApplyNotCommittedError
	if errors.As(err, &notCommitted) && notCommitted != nil {
		return notCommitted.RollbackArmed, notCommitted.RollbackDeadline, strings.TrimSpace(notCommitted.RollbackLog)
	}
	return false, time.Time{}, ""
}

func (w *restoreUIWorkflowRun) applyHAConfig() error {
	w.logger.Info("")
	err := maybeApplyPVEHAWithUI(w.ctx, w.ui, w.logger, w.plan, w.safetyBackup, w.haRollbackBackup, w.stageRoot, w.cfg.DryRun)
	if err == nil {
		return nil
	}
	if restoreAbortOrInput(err) {
		w.logger.Info("Restore aborted by user during HA apply prompt.")
		return err
	}
	w.restoreHadWarnings = true
	w.logHAApplyError(err)
	return nil
}

func (w *restoreUIWorkflowRun) logHAApplyError(err error) {
	if !errors.Is(err, ErrHAApplyNotCommitted) {
		w.logger.Warning("HA apply step skipped or failed: %v", err)
		return
	}
	armed, deadline, rollbackLog := haRollbackSummary(err)
	w.logGenericRollbackNotCommitted("HA apply", armed, deadline, rollbackLog)
}

func haRollbackSummary(err error) (bool, time.Time, string) {
	var notCommitted *HAApplyNotCommittedError
	if errors.As(err, &notCommitted) && notCommitted != nil {
		return notCommitted.RollbackArmed, notCommitted.RollbackDeadline, strings.TrimSpace(notCommitted.RollbackLog)
	}
	return false, time.Time{}, ""
}

func (w *restoreUIWorkflowRun) logGenericRollbackNotCommitted(label string, armed bool, deadline time.Time, rollbackLog string) {
	if armed {
		w.logger.Warning("%s not committed; rollback is ARMED and will run automatically.", label)
	} else {
		w.logger.Warning("%s not committed; rollback has executed (or marker cleared).", label)
	}
	if !deadline.IsZero() {
		w.logger.Info("Rollback deadline: %s", deadline.Format(time.RFC3339))
	}
	if rollbackLog != "" {
		w.logger.Info("Rollback log: %s", rollbackLog)
	}
}

func (w *restoreUIWorkflowRun) logRestoreCompletion() {
	w.logger.Info("")
	if w.restoreHadWarnings {
		w.logger.Warning("Restore completed with warnings.")
	} else {
		w.logger.Info("Restore completed successfully.")
	}
	w.logger.Info("Temporary decrypted bundle removed.")
	w.logRestoreArtifacts()
}

func (w *restoreUIWorkflowRun) logRestoreArtifacts() {
	if w.detailedLogPath != "" {
		w.logger.Info("Detailed restore log: %s", w.detailedLogPath)
	}
	if w.exportRoot != "" {
		w.logger.Info("Export directory: %s", w.exportRoot)
	}
	if w.exportLogPath != "" {
		w.logger.Info("Export detailed log: %s", w.exportLogPath)
	}
	if w.stageRoot != "" {
		w.logger.Info("Staging directory: %s", w.stageRoot)
	}
	if w.stageLogPath != "" {
		w.logger.Info("Staging detailed log: %s", w.stageLogPath)
	}
	if w.safetyBackup != nil {
		w.logger.Info("Safety backup preserved at: %s", w.safetyBackup.BackupPath)
		w.logger.Info("Remove it manually if restore was successful: rm %s", w.safetyBackup.BackupPath)
	}
}

func (w *restoreUIWorkflowRun) logServiceRestartAdvice() {
	w.logger.Info("")
	w.logger.Info("IMPORTANT: You may need to restart services for changes to take effect.")
	switch w.systemType {
	case SystemTypeDual:
		w.logPVERestartAdvice()
		w.logPBSRestartAdvice()
	case SystemTypePVE:
		w.logPVERestartAdvice()
	case SystemTypePBS:
		w.logPBSRestartAdvice()
	}
}

func (w *restoreUIWorkflowRun) logPVERestartAdvice() {
	if w.needsClusterRestore && w.clusterServicesStopped {
		w.logger.Info("  PVE services were stopped/restarted during restore; verify status with: pvecm status")
		return
	}
	w.logger.Info("  PVE services: systemctl restart pve-cluster pvedaemon pveproxy")
}

func (w *restoreUIWorkflowRun) logPBSRestartAdvice() {
	if w.pbsServicesStopped {
		w.logger.Info("  PBS services were stopped/restarted during restore; verify status with: systemctl status proxmox-backup proxmox-backup-proxy")
		return
	}
	w.logger.Info("  PBS services: systemctl restart proxmox-backup-proxy proxmox-backup")
}

func (w *restoreUIWorkflowRun) checkZFSPoolsAfterRestore() {
	if hasCategoryID(w.plan.NormalCategories, "zfs") {
		w.logger.Info("")
		if err := checkZFSPoolsAfterRestore(w.ctx, w.logger); err != nil {
			w.logger.Warning("ZFS pool check: %v", err)
		}
		return
	}
	w.logger.Debug("Skipping ZFS pool verification (ZFS category not selected)")
}

func (w *restoreUIWorkflowRun) logRebootRecommendation() {
	w.logger.Info("")
	w.logger.Warning("⚠ SYSTEM REBOOT RECOMMENDED")
	w.logger.Info("Reboot the node (or at least restart networking and system services) to ensure all restored configurations take effect cleanly.")
}
