// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

func (w *restoreUIWorkflowRun) systemWriteCategories() []Category {
	categories := append([]Category{}, w.plan.NormalCategories...)
	return append(categories, w.plan.StagedCategories...)
}

func (w *restoreUIWorkflowRun) createRollbackBackups() error {
	systemWriteCategories := w.systemWriteCategories()
	if err := w.createSafetyBackup(systemWriteCategories); err != nil {
		return err
	}
	w.createNetworkRollbackBackup(systemWriteCategories)
	w.createFirewallRollbackBackup(systemWriteCategories)
	w.createHARollbackBackup(systemWriteCategories)
	w.createAccessControlRollbackBackup(systemWriteCategories)
	return nil
}

func (w *restoreUIWorkflowRun) createSafetyBackup(categories []Category) error {
	if len(categories) == 0 {
		return nil
	}
	w.logger.Info("")
	backup, err := CreateSafetyBackup(w.logger, categories, w.destRoot)
	if err != nil {
		w.logger.Warning("Failed to create safety backup: %v", err)
		cont, promptErr := w.ui.ConfirmContinueWithoutSafetyBackup(w.ctx, err)
		if promptErr != nil {
			return promptErr
		}
		if !cont {
			return ErrRestoreAborted
		}
		return nil
	}
	w.safetyBackup = backup
	w.logger.Info("Safety backup location: %s", backup.BackupPath)
	w.logger.Info("You can restore from this backup if needed using: tar -xzf %s -C /", backup.BackupPath)
	return nil
}

func (w *restoreUIWorkflowRun) createNetworkRollbackBackup(categories []Category) {
	if !w.plan.HasCategoryID("network") {
		return
	}
	w.logger.Info("")
	logging.DebugStep(w.logger, "restore", "Create network-only rollback backup for transactional network apply")
	backup, err := CreateNetworkRollbackBackup(w.logger, categories, w.destRoot)
	if err != nil {
		w.logger.Warning("Failed to create network rollback backup: %v", err)
		return
	}
	w.networkRollbackBackup = backup
	if backup != nil && strings.TrimSpace(backup.BackupPath) != "" {
		w.logger.Info("Network rollback backup location: %s", backup.BackupPath)
		w.logger.Info("This backup is used for the %ds network rollback timer and only includes network paths.", int(defaultNetworkRollbackTimeout.Seconds()))
	}
}

func (w *restoreUIWorkflowRun) createFirewallRollbackBackup(categories []Category) {
	if !w.plan.HasCategoryID("pve_firewall") {
		return
	}
	w.logger.Info("")
	logging.DebugStep(w.logger, "restore", "Create firewall-only rollback backup for transactional firewall apply")
	backup, err := CreateFirewallRollbackBackup(w.logger, categories, w.destRoot)
	if err != nil {
		w.logger.Warning("Failed to create firewall rollback backup: %v", err)
		return
	}
	w.firewallRollbackBackup = backup
	if backup != nil && strings.TrimSpace(backup.BackupPath) != "" {
		w.logger.Info("Firewall rollback backup location: %s", backup.BackupPath)
		w.logger.Info("This backup is used for the %ds firewall rollback timer and only includes firewall paths.", int(defaultFirewallRollbackTimeout.Seconds()))
	}
}

func (w *restoreUIWorkflowRun) createHARollbackBackup(categories []Category) {
	if !w.plan.HasCategoryID("pve_ha") {
		return
	}
	w.logger.Info("")
	logging.DebugStep(w.logger, "restore", "Create HA-only rollback backup for transactional HA apply")
	backup, err := CreateHARollbackBackup(w.logger, categories, w.destRoot)
	if err != nil {
		w.logger.Warning("Failed to create HA rollback backup: %v", err)
		return
	}
	w.haRollbackBackup = backup
	if backup != nil && strings.TrimSpace(backup.BackupPath) != "" {
		w.logger.Info("HA rollback backup location: %s", backup.BackupPath)
		w.logger.Info("This backup is used for the %ds HA rollback timer and only includes HA paths.", int(defaultHARollbackTimeout.Seconds()))
	}
}

func (w *restoreUIWorkflowRun) createAccessControlRollbackBackup(categories []Category) {
	if !w.shouldCreateAccessControlRollbackBackup() {
		return
	}
	w.logger.Info("")
	logging.DebugStep(w.logger, "restore", "Create access-control-only rollback backup for optional cluster-safe access control apply")
	backup, err := CreatePVEAccessControlRollbackBackup(w.logger, categories, w.destRoot)
	if err != nil {
		w.logger.Warning("Failed to create access control rollback backup: %v", err)
		return
	}
	w.accessControlRollbackBackup = backup
	if backup != nil && strings.TrimSpace(backup.BackupPath) != "" {
		w.logger.Info("Access control rollback backup location: %s", backup.BackupPath)
		w.logger.Info("This backup is used for the %ds access control rollback timer and only includes access control paths.", int(defaultAccessControlRollbackTimeout.Seconds()))
	}
}

func (w *restoreUIWorkflowRun) shouldCreateAccessControlRollbackBackup() bool {
	return w.plan.SystemType.SupportsPVE() &&
		w.plan.ClusterBackup &&
		!w.plan.NeedsClusterRestore &&
		w.plan.HasCategoryID("pve_access_control")
}

func (w *restoreUIWorkflowRun) prepareRestoreServices() (func(), error) {
	var cleanups []func()
	if cleanup, err := w.preparePVEClusterRestore(); err != nil {
		return nil, err
	} else if cleanup != nil {
		cleanups = append(cleanups, cleanup)
	}
	if cleanup, err := w.preparePBSServices(); err != nil {
		return nil, err
	} else if cleanup != nil {
		cleanups = append(cleanups, cleanup)
	}
	return func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}, nil
}

func (w *restoreUIWorkflowRun) preparePVEClusterRestore() (func(), error) {
	w.needsClusterRestore = w.plan.NeedsClusterRestore
	if !w.needsClusterRestore {
		return nil, nil
	}
	w.logger.Info("")
	w.logger.Info("Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve")
	if err := stopPVEClusterServices(w.ctx, w.logger); err != nil {
		return nil, err
	}
	w.clusterServicesStopped = true
	if err := unmountEtcPVE(w.ctx, w.logger); err != nil {
		w.logger.Warning("Could not unmount /etc/pve: %v", err)
	}
	return w.restartPVEClusterServicesCleanup(), nil
}

func (w *restoreUIWorkflowRun) restartPVEClusterServicesCleanup() func() {
	return func() {
		restartCtx, cancel := context.WithTimeout(context.Background(), 2*serviceStartTimeout+2*serviceVerifyTimeout+10*time.Second)
		defer cancel()
		if err := startPVEClusterServices(restartCtx, w.logger); err != nil {
			w.logger.Warning("Failed to restart PVE services after restore: %v", err)
		}
	}
}

func (w *restoreUIWorkflowRun) preparePBSServices() (func(), error) {
	w.needsPBSServices = w.plan.NeedsPBSServices
	if !w.needsPBSServices {
		return nil, nil
	}
	w.logger.Info("")
	w.logger.Info("Preparing PBS system for restore: stopping proxmox-backup services")
	if err := stopPBSServices(w.ctx, w.logger); err != nil {
		return w.confirmContinueWithPBSServicesRunning(err)
	}
	w.pbsServicesStopped = true
	return w.restartPBSServicesCleanup(), nil
}

func (w *restoreUIWorkflowRun) confirmContinueWithPBSServicesRunning(stopErr error) (func(), error) {
	w.logger.Warning("Unable to stop PBS services automatically: %v", stopErr)
	cont, err := w.ui.ConfirmContinueWithPBSServicesRunning(w.ctx)
	if err != nil {
		return nil, err
	}
	if !cont {
		return nil, ErrRestoreAborted
	}
	w.logger.Warning("Continuing restore with PBS services still running")
	return nil, nil
}

func (w *restoreUIWorkflowRun) restartPBSServicesCleanup() func() {
	return func() {
		restartCtx, cancel := context.WithTimeout(context.Background(), 2*serviceStartTimeout+2*serviceVerifyTimeout+10*time.Second)
		defer cancel()
		if err := startPBSServices(restartCtx, w.logger); err != nil {
			w.logger.Warning("Failed to restart PBS services after restore: %v", err)
			return
		}
		if err := maybeVerifyAndRepairPBSNotificationsAfterRestore(restartCtx, w.logger, w.plan, w.stageRoot, w.cfg.DryRun); err != nil {
			w.logger.Warning("PBS notifications verification/repair: %v", err)
		}
	}
}
