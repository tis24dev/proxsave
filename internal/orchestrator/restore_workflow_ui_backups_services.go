// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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
	w.logger.Info("Safety backup - holds the current versions of the files this restore overwrites (tar.gz, paths relative to /)")
	for _, c := range categories {
		if c.ID == "accounts" {
			w.logger.Info("Safety backup - also holds the current /etc/passwd, /etc/group, /etc/shadow, /etc/gshadow and /etc/sudoers")
			break
		}
	}
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
	runCleanups := func() {
		for _, cleanup := range slices.Backward(cleanups) {
			cleanup()
		}
	}
	if cleanup, err := w.preparePVEClusterRestore(); err != nil {
		runCleanups()
		return nil, err
	} else if cleanup != nil {
		cleanups = append(cleanups, cleanup)
	}
	if cleanup, err := w.preparePBSServices(); err != nil {
		runCleanups()
		return nil, err
	} else if cleanup != nil {
		cleanups = append(cleanups, cleanup)
	}
	return runCleanups, nil
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
	if err := checkpointPVEClusterDB(w.ctx, w.logger); err != nil {
		// Nothing was restored yet: the services come back on the database as it is.
		w.restartStoppedPVEClusterServices()
		return nil, err
	}
	if err := removePVEClusterDBSidecars(w.logger); err != nil {
		// Still nothing restored: config.db is the checkpointed one.
		w.restartStoppedPVEClusterServices()
		return nil, err
	}
	return w.restartPVEClusterServicesCleanup(), nil
}

// pveClusterDB is the cluster database pmxcfs keeps in WAL mode.
const pveClusterDB = "/var/lib/pve-cluster/config.db"

// checkpointPVEClusterDB folds the WAL into config.db once pve-cluster is stopped.
// pmxcfs does not close the database when its stop times out (measured on pve-test:
// systemd SIGKILLs it), so its last commits live only in config.db-wal: removing the
// WAL lost them whenever the restore ended before writing the archive's config.db
// (cancelled, archive unreadable, no config.db in it). TRUNCATE leaves the WAL at 0
// bytes, which SQLite ignores, so one that cannot be removed afterwards cannot overlay
// the restored database either. sqlite3 is a dependency of pve-cluster. An absent
// config.db has nothing to fold.
func checkpointPVEClusterDB(ctx context.Context, logger *logging.Logger) error {
	if _, err := restoreFS.Stat(pveClusterDB); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logger.Debug("Cluster database restore: %s absent, no checkpoint", pveClusterDB)
			return nil
		}
		return fmt.Errorf("failed to checkpoint the cluster database (%s): %w", pveClusterDB, err)
	}
	output, err := restoreCmd.Run(ctx, "sqlite3", pveClusterDB, "PRAGMA wal_checkpoint(TRUNCATE);")
	result := strings.TrimSpace(string(output))
	logger.Debug("Cluster database restore: sqlite3 wal_checkpoint(TRUNCATE) on %s: output=%q err=%v", pveClusterDB, result, err)
	if err != nil {
		if result == "" {
			return fmt.Errorf("failed to checkpoint the cluster database (%s): %w", pveClusterDB, err)
		}
		return fmt.Errorf("failed to checkpoint the cluster database (%s): %s", pveClusterDB, result)
	}
	// The pragma prints "busy|log|checkpointed"; busy is 0 only when the checkpoint
	// completed.
	if busy, _, _ := strings.Cut(result, "|"); busy != "0" {
		return fmt.Errorf("failed to checkpoint the cluster database (%s): %s", pveClusterDB, result)
	}
	return nil
}

// pveClusterDBSidecars are the SQLite WAL files of the cluster database. pmxcfs runs
// config.db in WAL mode; a WAL left by a pmxcfs that did not close the database is
// replayed by SQLite onto whatever config.db sits beside it at the next open, so it
// would overlay pages of the old database on the one the restore writes. The archive
// never holds them (the collector ships a .backup snapshot), and the safety backup
// taken before keeps the live ones.
var pveClusterDBSidecars = []string{"/var/lib/pve-cluster/config.db-wal", "/var/lib/pve-cluster/config.db-shm"}

// removePVEClusterDBSidecars removes them once pve-cluster is stopped and the WAL is
// folded into config.db (checkpointPVEClusterDB), before the archive's config.db is
// written. One that is there and cannot be removed stops the restore: left beside the
// archive's config.db, it is opened with it.
func removePVEClusterDBSidecars(logger *logging.Logger) error {
	for _, path := range pveClusterDBSidecars {
		err := restoreFS.Remove(path)
		switch {
		case err == nil:
			logger.Debug("Cluster database restore: removed %s, the old database's WAL file", path)
		case !errors.Is(err, os.ErrNotExist):
			logger.Debug("Cluster database restore: cannot remove %s: %v", path, err)
			return fmt.Errorf("failed to remove the old cluster database file (%s): %w", path, err)
		}
	}
	return nil
}

// restartPVEClusterServicesCleanup is the deferred restart. It only does work when
// the run ends before restartStoppedPVEClusterServices ran after the extraction,
// i.e. on an error while the services were still down.
func (w *restoreUIWorkflowRun) restartPVEClusterServicesCleanup() func() {
	return w.restartStoppedPVEClusterServices
}

// restartStoppedPVEClusterServices restarts, once, the services
// preparePVEClusterRestore stopped. The run calls it as soon as extractNormalCategories
// has written the cluster database: that is the only step that needs pmxcfs down,
// and every minute it stays down later (the network apply prompt, the boot rebuild)
// is a minute this node is out of the cluster.
func (w *restoreUIWorkflowRun) restartStoppedPVEClusterServices() {
	if !w.clusterServicesStopped || w.clusterServicesRestarted {
		return
	}
	w.clusterServicesRestarted = true
	restartCtx, cancel := context.WithTimeout(context.Background(), pveClusterRestartTimeout())
	defer cancel()
	if err := startPVEClusterServices(restartCtx, w.logger); err != nil {
		w.logger.Warning("Failed to restart PVE services after restore: %v", err)
		if startErr, ok := errors.AsType[*pveClusterStartError](err); ok {
			w.clusterServicesNotRunning = startErr
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
		// Also reached on an abort during staging: the restart stays, the repair from
		// a partial stage does not.
		if w.skipForIncompleteStage("PBS notifications verification/repair") {
			return
		}
		if err := verifyAndRepairPBSNotificationsFunc(restartCtx, w.logger, w.plan, w.stageRoot, w.cfg.DryRun); err != nil {
			w.logger.Warning("PBS notifications verification/repair: %v", err)
		}
	}
}
