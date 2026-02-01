package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

var prepareRestoreBundleFunc = prepareRestoreBundleWithUI

func prepareRestoreBundleWithUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*decryptCandidate, *preparedBundle, error) {
	candidate, err := selectBackupCandidateWithUI(ctx, ui, cfg, logger, false)
	if err != nil {
		return nil, nil, err
	}

	prepared, err := preparePlainBundleWithUI(ctx, candidate, version, logger, ui)
	if err != nil {
		return nil, nil, err
	}
	return candidate, prepared, nil
}

func runRestoreWorkflowWithUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (err error) {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	done := logging.DebugStart(logger, "restore workflow (ui)", "version=%s", version)
	defer func() { done(err) }()

	restoreHadWarnings := false
	defer func() {
		if err == nil {
			return
		}
		if err == io.EOF {
			logger.Warning("Restore input closed unexpectedly (EOF). This usually means the interactive UI lost access to stdin/TTY (e.g., SSH disconnect or non-interactive execution). Re-run with --restore --cli from an interactive shell.")
			err = ErrRestoreAborted
			return
		}
		if errors.Is(err, input.ErrInputAborted) ||
			errors.Is(err, ErrDecryptAborted) ||
			errors.Is(err, ErrAgeRecipientSetupAborted) ||
			errors.Is(err, context.Canceled) ||
			(ctx != nil && ctx.Err() != nil) {
			err = ErrRestoreAborted
		}
	}()

	candidate, prepared, err := prepareRestoreBundleFunc(ctx, cfg, logger, version, ui)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	destRoot := "/"
	logger.Info("Restore target: system root (/) — files will be written back to their original paths")

	systemType := restoreSystem.DetectCurrentSystem()
	logger.Info("Detected system type: %s", GetSystemTypeString(systemType))

	if warn := ValidateCompatibility(candidate.Manifest); warn != nil {
		logger.Warning("Compatibility check: %v", warn)
		proceed, perr := ui.ConfirmCompatibility(ctx, warn)
		if perr != nil {
			return perr
		}
		if !proceed {
			return ErrRestoreAborted
		}
	}

	logger.Info("Analyzing backup contents...")
	availableCategories, err := AnalyzeBackupCategories(prepared.ArchivePath, logger)
	if err != nil {
		logger.Warning("Could not analyze categories: %v", err)
		logger.Info("Falling back to full restore mode")
		return runFullRestoreWithUI(ctx, ui, candidate, prepared, destRoot, logger, cfg.DryRun)
	}

	var (
		mode               RestoreMode
		selectedCategories []Category
	)
	for {
		mode, err = ui.SelectRestoreMode(ctx, systemType)
		if err != nil {
			return err
		}

		if mode != RestoreModeCustom {
			selectedCategories = GetCategoriesForMode(mode, systemType, availableCategories)
			break
		}

		selectedCategories, err = ui.SelectCategories(ctx, availableCategories, systemType)
		if err != nil {
			if errors.Is(err, errRestoreBackToMode) {
				continue
			}
			return err
		}
		break
	}

	if mode == RestoreModeCustom {
		selectedCategories, err = maybeAddRecommendedCategoriesForTFA(ctx, ui, logger, selectedCategories, availableCategories)
		if err != nil {
			return err
		}
	}

	plan := PlanRestore(candidate.Manifest, selectedCategories, systemType, mode)

	clusterBackup := strings.EqualFold(strings.TrimSpace(candidate.Manifest.ClusterMode), "cluster")
	if plan.NeedsClusterRestore && clusterBackup {
		logger.Info("Backup marked as cluster node; enabling guarded restore options for pve_cluster")
		choice, promptErr := ui.SelectClusterRestoreMode(ctx)
		if promptErr != nil {
			return promptErr
		}
		switch choice {
		case ClusterRestoreAbort:
			return ErrRestoreAborted
		case ClusterRestoreSafe:
			plan.ApplyClusterSafeMode(true)
			logger.Info("Selected SAFE cluster restore: /var/lib/pve-cluster will be exported only, not written to system")
		case ClusterRestoreRecovery:
			plan.ApplyClusterSafeMode(false)
			logger.Warning("Selected RECOVERY cluster restore: full cluster database will be restored; ensure other nodes are isolated")
		default:
			return fmt.Errorf("invalid cluster restore mode selected")
		}
	}

	if plan.HasCategoryID("pve_access_control") || plan.HasCategoryID("pbs_access_control") {
		currentHost, hostErr := os.Hostname()
		if hostErr == nil && strings.TrimSpace(candidate.Manifest.Hostname) != "" && strings.TrimSpace(currentHost) != "" {
			backupHost := strings.TrimSpace(candidate.Manifest.Hostname)
			if !strings.EqualFold(strings.TrimSpace(currentHost), backupHost) {
				logger.Warning("Access control/TFA: backup hostname=%s current hostname=%s; WebAuthn users may require re-enrollment if the UI origin (FQDN/port) changes", backupHost, currentHost)
			}
		}
	}

	if destRoot != "/" || !isRealRestoreFS(restoreFS) {
		if len(plan.StagedCategories) > 0 {
			logging.DebugStep(logger, "restore", "Staging disabled (destRoot=%s realFS=%v): extracting %d staged category(ies) directly", destRoot, isRealRestoreFS(restoreFS), len(plan.StagedCategories))
			plan.NormalCategories = append(plan.NormalCategories, plan.StagedCategories...)
			plan.StagedCategories = nil
		}
	}

	restoreConfig := &SelectiveRestoreConfig{
		Mode:       mode,
		SystemType: systemType,
		Metadata:   candidate.Manifest,
	}
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.NormalCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.StagedCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.ExportCategories...)

	if err := ui.ShowRestorePlan(ctx, restoreConfig); err != nil {
		return err
	}

	confirmed, err := ui.ConfirmRestore(ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		logger.Info("Restore operation cancelled by user")
		return ErrRestoreAborted
	}

	var safetyBackup *SafetyBackupResult
	var networkRollbackBackup *SafetyBackupResult
	var firewallRollbackBackup *SafetyBackupResult
	var haRollbackBackup *SafetyBackupResult
	var accessControlRollbackBackup *SafetyBackupResult
	systemWriteCategories := append([]Category{}, plan.NormalCategories...)
	systemWriteCategories = append(systemWriteCategories, plan.StagedCategories...)
	if len(systemWriteCategories) > 0 {
		logger.Info("")
		safetyBackup, err = CreateSafetyBackup(logger, systemWriteCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create safety backup: %v", err)
			cont, perr := ui.ConfirmContinueWithoutSafetyBackup(ctx, err)
			if perr != nil {
				return perr
			}
			if !cont {
				return ErrRestoreAborted
			}
		} else {
			logger.Info("Safety backup location: %s", safetyBackup.BackupPath)
			logger.Info("You can restore from this backup if needed using: tar -xzf %s -C /", safetyBackup.BackupPath)
		}
	}

	if plan.HasCategoryID("network") {
		logger.Info("")
		logging.DebugStep(logger, "restore", "Create network-only rollback backup for transactional network apply")
		networkRollbackBackup, err = CreateNetworkRollbackBackup(logger, systemWriteCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create network rollback backup: %v", err)
		} else if networkRollbackBackup != nil && strings.TrimSpace(networkRollbackBackup.BackupPath) != "" {
			logger.Info("Network rollback backup location: %s", networkRollbackBackup.BackupPath)
			logger.Info("This backup is used for the %ds network rollback timer and only includes network paths.", int(defaultNetworkRollbackTimeout.Seconds()))
		}
	}
	if plan.HasCategoryID("pve_firewall") {
		logger.Info("")
		logging.DebugStep(logger, "restore", "Create firewall-only rollback backup for transactional firewall apply")
		firewallRollbackBackup, err = CreateFirewallRollbackBackup(logger, systemWriteCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create firewall rollback backup: %v", err)
		} else if firewallRollbackBackup != nil && strings.TrimSpace(firewallRollbackBackup.BackupPath) != "" {
			logger.Info("Firewall rollback backup location: %s", firewallRollbackBackup.BackupPath)
			logger.Info("This backup is used for the %ds firewall rollback timer and only includes firewall paths.", int(defaultFirewallRollbackTimeout.Seconds()))
		}
	}
	if plan.HasCategoryID("pve_ha") {
		logger.Info("")
		logging.DebugStep(logger, "restore", "Create HA-only rollback backup for transactional HA apply")
		haRollbackBackup, err = CreateHARollbackBackup(logger, systemWriteCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create HA rollback backup: %v", err)
		} else if haRollbackBackup != nil && strings.TrimSpace(haRollbackBackup.BackupPath) != "" {
			logger.Info("HA rollback backup location: %s", haRollbackBackup.BackupPath)
			logger.Info("This backup is used for the %ds HA rollback timer and only includes HA paths.", int(defaultHARollbackTimeout.Seconds()))
		}
	}
	if plan.SystemType == SystemTypePVE && plan.ClusterBackup && !plan.NeedsClusterRestore && plan.HasCategoryID("pve_access_control") {
		logger.Info("")
		logging.DebugStep(logger, "restore", "Create access-control-only rollback backup for optional cluster-safe access control apply")
		accessControlRollbackBackup, err = CreatePVEAccessControlRollbackBackup(logger, systemWriteCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create access control rollback backup: %v", err)
		} else if accessControlRollbackBackup != nil && strings.TrimSpace(accessControlRollbackBackup.BackupPath) != "" {
			logger.Info("Access control rollback backup location: %s", accessControlRollbackBackup.BackupPath)
			logger.Info("This backup is used for the %ds access control rollback timer and only includes access control paths.", int(defaultAccessControlRollbackTimeout.Seconds()))
		}
	}

	needsClusterRestore := plan.NeedsClusterRestore
	clusterServicesStopped := false
	pbsServicesStopped := false
	needsPBSServices := plan.NeedsPBSServices

	if needsClusterRestore {
		logger.Info("")
		logger.Info("Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve")
		if err := stopPVEClusterServices(ctx, logger); err != nil {
			return err
		}
		clusterServicesStopped = true
		defer func() {
			restartCtx, cancel := context.WithTimeout(context.Background(), 2*serviceStartTimeout+2*serviceVerifyTimeout+10*time.Second)
			defer cancel()
			if err := startPVEClusterServices(restartCtx, logger); err != nil {
				logger.Warning("Failed to restart PVE services after restore: %v", err)
			}
		}()

		if err := unmountEtcPVE(ctx, logger); err != nil {
			logger.Warning("Could not unmount /etc/pve: %v", err)
		}
	}

	if needsPBSServices {
		logger.Info("")
		logger.Info("Preparing PBS system for restore: stopping proxmox-backup services")
		if err := stopPBSServices(ctx, logger); err != nil {
			logger.Warning("Unable to stop PBS services automatically: %v", err)
			cont, perr := ui.ConfirmContinueWithPBSServicesRunning(ctx)
			if perr != nil {
				return perr
			}
			if !cont {
				return ErrRestoreAborted
			}
			logger.Warning("Continuing restore with PBS services still running")
		} else {
			pbsServicesStopped = true
			defer func() {
				restartCtx, cancel := context.WithTimeout(context.Background(), 2*serviceStartTimeout+2*serviceVerifyTimeout+10*time.Second)
				defer cancel()
				if err := startPBSServices(restartCtx, logger); err != nil {
					logger.Warning("Failed to restart PBS services after restore: %v", err)
				}
			}()
		}
	}

	var detailedLogPath string

	needsFilesystemRestore := false
	if plan.HasCategoryID("filesystem") {
		needsFilesystemRestore = true
		var filtered []Category
		for _, cat := range plan.NormalCategories {
			if cat.ID != "filesystem" {
				filtered = append(filtered, cat)
			}
		}
		plan.NormalCategories = filtered
		logging.DebugStep(logger, "restore", "Filesystem category intercepted: enabling Smart Merge workflow (skipping generic extraction)")
	}

	if len(plan.NormalCategories) > 0 {
		logger.Info("")
		categoriesForExtraction := plan.NormalCategories
		if needsClusterRestore {
			logging.DebugStep(logger, "restore", "Cluster RECOVERY shadow-guard: sanitize categories to avoid /etc/pve shadow writes")
			sanitized, removed := sanitizeCategoriesForClusterRecovery(categoriesForExtraction)
			removedPaths := 0
			for _, paths := range removed {
				removedPaths += len(paths)
			}
			logging.DebugStep(
				logger,
				"restore",
				"Cluster RECOVERY shadow-guard: categories_before=%d categories_after=%d removed_categories=%d removed_paths=%d",
				len(categoriesForExtraction),
				len(sanitized),
				len(removed),
				removedPaths,
			)
			if len(removed) > 0 {
				logger.Warning("Cluster RECOVERY restore: skipping direct restore of /etc/pve paths to prevent shadowing while pmxcfs is stopped/unmounted")
				for _, cat := range categoriesForExtraction {
					if paths, ok := removed[cat.ID]; ok && len(paths) > 0 {
						logger.Warning("  - %s (%s): %s", cat.Name, cat.ID, strings.Join(paths, ", "))
					}
				}
				logger.Info("These paths are expected to be restored from config.db and become visible after /etc/pve is remounted.")
			} else {
				logging.DebugStep(logger, "restore", "Cluster RECOVERY shadow-guard: no /etc/pve paths detected in selected categories")
			}
			categoriesForExtraction = sanitized
		}

		if len(categoriesForExtraction) == 0 {
			logging.DebugStep(logger, "restore", "Skip system-path extraction: no categories remain after shadow-guard")
			logger.Info("No system-path categories remain after cluster shadow-guard; skipping system-path extraction.")
		} else {
			detailedLogPath, err = extractSelectiveArchive(ctx, prepared.ArchivePath, destRoot, categoriesForExtraction, mode, logger)
			if err != nil {
				logger.Error("Restore failed: %v", err)
				if safetyBackup != nil {
					logger.Info("You can rollback using the safety backup at: %s", safetyBackup.BackupPath)
				}
				return err
			}
		}
	} else {
		logger.Info("")
		logger.Info("No system-path categories selected for restore (only export categories will be processed).")
	}

	// Mount-first: restore /etc/fstab (Smart Merge) before applying PBS datastore configs.
	if needsFilesystemRestore {
		logger.Info("")
		fsTempDir, err := restoreFS.MkdirTemp("", "proxsave-fstab-")
		if err != nil {
			restoreHadWarnings = true
			logger.Warning("Failed to create temp dir for fstab merge: %v", err)
		} else {
			defer restoreFS.RemoveAll(fsTempDir)
			fsCat := GetCategoryByID("filesystem", availableCategories)
			if fsCat == nil {
				logger.Warning("Filesystem category not available in analyzed backup contents; skipping fstab merge")
			} else {
				fsCategory := []Category{*fsCat}
				if _, err := extractSelectiveArchive(ctx, prepared.ArchivePath, fsTempDir, fsCategory, RestoreModeCustom, logger); err != nil {
					if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
						return err
					}
					restoreHadWarnings = true
					logger.Warning("Failed to extract filesystem config for merge: %v", err)
				} else {
					// Best-effort: extract ProxSave inventory files used for stable fstab device remapping.
					// (e.g., blkid/lsblk JSON from var/lib/proxsave-info).
					invCategory := []Category{{
						ID:   "fstab_inventory",
						Name: "Fstab inventory (device mapping)",
						Paths: []string{
							"./var/lib/proxsave-info/commands/system/blkid.txt",
							"./var/lib/proxsave-info/commands/system/lsblk_json.json",
							"./var/lib/proxsave-info/commands/system/lsblk.txt",
							"./var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json",
						},
					}}
					if err := extractArchiveNative(ctx, prepared.ArchivePath, fsTempDir, logger, invCategory, RestoreModeCustom, nil, "", nil); err != nil {
						logger.Debug("Failed to extract fstab inventory data (continuing): %v", err)
					}

					currentFstab := filepath.Join(destRoot, "etc", "fstab")
					backupFstab := filepath.Join(fsTempDir, "etc", "fstab")
					if err := smartMergeFstabWithUI(ctx, logger, ui, currentFstab, backupFstab, cfg.DryRun); err != nil {
						if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
							logger.Info("Restore aborted by user during Smart Filesystem Configuration Merge.")
							return err
						}
						restoreHadWarnings = true
						logger.Warning("Smart Fstab Merge failed: %v", err)
					}
				}
			}
		}
	}

	exportLogPath := ""
	exportRoot := ""
	if len(plan.ExportCategories) > 0 {
		exportRoot = exportDestRoot(cfg.BaseDir)
		logger.Info("")
		logger.Info("Exporting %d export-only category(ies) to: %s", len(plan.ExportCategories), exportRoot)
		if err := restoreFS.MkdirAll(exportRoot, 0o755); err != nil {
			return fmt.Errorf("failed to create export directory %s: %w", exportRoot, err)
		}

		if exportLog, exErr := extractSelectiveArchive(ctx, prepared.ArchivePath, exportRoot, plan.ExportCategories, RestoreModeCustom, logger); exErr != nil {
			if errors.Is(exErr, ErrRestoreAborted) || input.IsAborted(exErr) {
				return exErr
			}
			restoreHadWarnings = true
			logger.Warning("Export completed with errors: %v", exErr)
		} else {
			exportLogPath = exportLog
		}
	}

	if plan.ClusterSafeMode {
		if exportRoot == "" {
			logger.Warning("Cluster SAFE mode selected but export directory not available; skipping automatic pvesh apply")
		} else {
			// Best-effort: extract extra SAFE apply inventory (pools/mappings) used by pvesh apply workflows.
			// This keeps SAFE apply usable even when the user did not explicitly export proxsave_info or /etc/pve.
			safeInvCategory := []Category{{
				ID:   "safe_apply_inventory",
				Name: "SAFE apply inventory (pools/mappings)",
				Paths: []string{
					"./etc/pve/user.cfg",
					"./var/lib/proxsave-info/commands/pve/mapping_pci.json",
					"./var/lib/proxsave-info/commands/pve/mapping_usb.json",
					"./var/lib/proxsave-info/commands/pve/mapping_dir.json",
				},
			}}
			if err := extractArchiveNative(ctx, prepared.ArchivePath, exportRoot, logger, safeInvCategory, RestoreModeCustom, nil, "", nil); err != nil {
				logger.Debug("Failed to extract SAFE apply inventory (continuing): %v", err)
			}

			if err := runSafeClusterApplyWithUI(ctx, ui, exportRoot, logger, plan); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("Cluster SAFE apply completed with errors: %v", err)
		}
		}
	}

	stageLogPath := ""
	stageRoot := ""
	if len(plan.StagedCategories) > 0 {
		stageRoot = stageDestRoot()
		logger.Info("")
		logger.Info("Staging %d sensitive category(ies) to: %s", len(plan.StagedCategories), stageRoot)
		if err := restoreFS.MkdirAll(stageRoot, 0o755); err != nil {
			return fmt.Errorf("failed to create staging directory %s: %w", stageRoot, err)
		}

		if stageLog, err := extractSelectiveArchive(ctx, prepared.ArchivePath, stageRoot, plan.StagedCategories, RestoreModeCustom, logger); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("Staging completed with errors: %v", err)
		} else {
			stageLogPath = stageLog
		}

		if err := maybeApplyPBSDatastoreMountGuards(ctx, logger, plan, stageRoot, destRoot, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("PBS mount guard: %v", err)
		}

		logger.Info("")
		if err := maybeApplyPBSConfigsFromStage(ctx, logger, plan, stageRoot, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("PBS staged config apply: %v", err)
		}
		if err := maybeApplyPVEConfigsFromStage(ctx, logger, plan, stageRoot, destRoot, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("PVE staged config apply: %v", err)
		}
		if err := maybeApplyPVESDNFromStage(ctx, logger, plan, stageRoot, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("PVE SDN staged apply: %v", err)
		}
		if err := maybeApplyAccessControlWithUI(ctx, ui, logger, plan, safetyBackup, accessControlRollbackBackup, stageRoot, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			if errors.Is(err, ErrAccessControlApplyNotCommitted) {
				var notCommitted *AccessControlApplyNotCommittedError
				rollbackLog := ""
				rollbackArmed := false
				deadline := time.Time{}
				if errors.As(err, &notCommitted) && notCommitted != nil {
					rollbackLog = strings.TrimSpace(notCommitted.RollbackLog)
					rollbackArmed = notCommitted.RollbackArmed
					deadline = notCommitted.RollbackDeadline
				}
				if rollbackArmed {
					logger.Warning("Access control apply not committed; rollback is ARMED and will run automatically.")
				} else {
					logger.Warning("Access control apply not committed; rollback has executed (or marker cleared).")
				}
				if !deadline.IsZero() {
					logger.Info("Rollback deadline: %s", deadline.Format(time.RFC3339))
				}
				if rollbackLog != "" {
					logger.Info("Rollback log: %s", rollbackLog)
				}
			} else {
				logger.Warning("Access control staged apply: %v", err)
			}
		}
		if err := maybeApplyNotificationsFromStage(ctx, logger, plan, stageRoot, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("Notifications staged apply: %v", err)
		}
	}

	stageRootForNetworkApply := stageRoot
	if installed, err := maybeInstallNetworkConfigFromStage(ctx, logger, plan, stageRoot, prepared.ArchivePath, networkRollbackBackup, cfg.DryRun); err != nil {
		if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
			return err
		}
		restoreHadWarnings = true
		logger.Warning("Network staged install: %v", err)
	} else if installed {
		stageRootForNetworkApply = ""
		logging.DebugStep(logger, "restore", "Network staged install completed: configuration written to /etc (no reload); live apply will use system paths")
	}

	logger.Info("")
	categoriesForDirRecreate := append([]Category{}, plan.NormalCategories...)
	categoriesForDirRecreate = append(categoriesForDirRecreate, plan.StagedCategories...)
	if shouldRecreateDirectories(systemType, categoriesForDirRecreate) {
		if err := RecreateDirectoriesFromConfig(systemType, logger); err != nil {
			restoreHadWarnings = true
			logger.Warning("Failed to recreate directory structures: %v", err)
			logger.Warning("You may need to manually create storage/datastore directories")
		}
	} else {
		logger.Debug("Skipping datastore/storage directory recreation (category not selected)")
	}

	logger.Info("")
	if plan.HasCategoryID("network") {
		logger.Info("")
		if err := maybeRepairResolvConfAfterRestore(ctx, logger, prepared.ArchivePath, cfg.DryRun); err != nil {
			if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
				return err
			}
			restoreHadWarnings = true
			logger.Warning("DNS resolver repair: %v", err)
		}
	}

	logger.Info("")
	if err := maybeApplyNetworkConfigWithUI(ctx, ui, logger, plan, safetyBackup, networkRollbackBackup, stageRootForNetworkApply, prepared.ArchivePath, cfg.DryRun); err != nil {
		if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
			logger.Info("Restore aborted by user during network apply prompt.")
			return err
		}
		restoreHadWarnings = true
		if errors.Is(err, ErrNetworkApplyNotCommitted) {
			var notCommitted *NetworkApplyNotCommittedError
			observedIP := "unknown"
			rollbackLog := ""
			rollbackArmed := false
			if errors.As(err, &notCommitted) && notCommitted != nil {
				if strings.TrimSpace(notCommitted.RestoredIP) != "" {
					observedIP = strings.TrimSpace(notCommitted.RestoredIP)
				}
				rollbackLog = strings.TrimSpace(notCommitted.RollbackLog)
				rollbackArmed = notCommitted.RollbackArmed
				lastRestoreAbortInfo = &RestoreAbortInfo{
					NetworkRollbackArmed:  rollbackArmed,
					NetworkRollbackLog:    rollbackLog,
					NetworkRollbackMarker: strings.TrimSpace(notCommitted.RollbackMarker),
					OriginalIP:            notCommitted.OriginalIP,
					CurrentIP:             observedIP,
					RollbackDeadline:      notCommitted.RollbackDeadline,
				}
			}
			if rollbackArmed {
				logger.Warning("Network apply not committed; rollback is ARMED and will run automatically. Current IP: %s", observedIP)
			} else {
				logger.Warning("Network apply not committed; rollback has executed (or marker cleared). Current IP: %s", observedIP)
			}
			if rollbackLog != "" {
				logger.Info("Rollback log: %s", rollbackLog)
			}
		} else {
			logger.Warning("Network apply step skipped or failed: %v", err)
		}
	}

	logger.Info("")
	if err := maybeApplyPVEFirewallWithUI(ctx, ui, logger, plan, safetyBackup, firewallRollbackBackup, stageRoot, cfg.DryRun); err != nil {
		if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
			logger.Info("Restore aborted by user during firewall apply prompt.")
			return err
		}
		restoreHadWarnings = true
		if errors.Is(err, ErrFirewallApplyNotCommitted) {
			var notCommitted *FirewallApplyNotCommittedError
			rollbackLog := ""
			rollbackArmed := false
			deadline := time.Time{}
			if errors.As(err, &notCommitted) && notCommitted != nil {
				rollbackLog = strings.TrimSpace(notCommitted.RollbackLog)
				rollbackArmed = notCommitted.RollbackArmed
				deadline = notCommitted.RollbackDeadline
			}
			if rollbackArmed {
				logger.Warning("Firewall apply not committed; rollback is ARMED and will run automatically.")
			} else {
				logger.Warning("Firewall apply not committed; rollback has executed (or marker cleared).")
			}
			if !deadline.IsZero() {
				logger.Info("Rollback deadline: %s", deadline.Format(time.RFC3339))
			}
			if rollbackLog != "" {
				logger.Info("Rollback log: %s", rollbackLog)
			}
		} else {
			logger.Warning("Firewall apply step skipped or failed: %v", err)
		}
	}

	logger.Info("")
	if err := maybeApplyPVEHAWithUI(ctx, ui, logger, plan, safetyBackup, haRollbackBackup, stageRoot, cfg.DryRun); err != nil {
		if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
			logger.Info("Restore aborted by user during HA apply prompt.")
			return err
		}
		restoreHadWarnings = true
		if errors.Is(err, ErrHAApplyNotCommitted) {
			var notCommitted *HAApplyNotCommittedError
			rollbackLog := ""
			rollbackArmed := false
			deadline := time.Time{}
			if errors.As(err, &notCommitted) && notCommitted != nil {
				rollbackLog = strings.TrimSpace(notCommitted.RollbackLog)
				rollbackArmed = notCommitted.RollbackArmed
				deadline = notCommitted.RollbackDeadline
			}
			if rollbackArmed {
				logger.Warning("HA apply not committed; rollback is ARMED and will run automatically.")
			} else {
				logger.Warning("HA apply not committed; rollback has executed (or marker cleared).")
			}
			if !deadline.IsZero() {
				logger.Info("Rollback deadline: %s", deadline.Format(time.RFC3339))
			}
			if rollbackLog != "" {
				logger.Info("Rollback log: %s", rollbackLog)
			}
		} else {
			logger.Warning("HA apply step skipped or failed: %v", err)
		}
	}

	logger.Info("")
	if restoreHadWarnings {
		logger.Warning("Restore completed with warnings.")
	} else {
		logger.Info("Restore completed successfully.")
	}
	logger.Info("Temporary decrypted bundle removed.")

	if detailedLogPath != "" {
		logger.Info("Detailed restore log: %s", detailedLogPath)
	}
	if exportRoot != "" {
		logger.Info("Export directory: %s", exportRoot)
	}
	if exportLogPath != "" {
		logger.Info("Export detailed log: %s", exportLogPath)
	}
	if stageRoot != "" {
		logger.Info("Staging directory: %s", stageRoot)
	}
	if stageLogPath != "" {
		logger.Info("Staging detailed log: %s", stageLogPath)
	}

	if safetyBackup != nil {
		logger.Info("Safety backup preserved at: %s", safetyBackup.BackupPath)
		logger.Info("Remove it manually if restore was successful: rm %s", safetyBackup.BackupPath)
	}

	logger.Info("")
	logger.Info("IMPORTANT: You may need to restart services for changes to take effect.")
	if systemType == SystemTypePVE {
		if needsClusterRestore && clusterServicesStopped {
			logger.Info("  PVE services were stopped/restarted during restore; verify status with: pvecm status")
		} else {
			logger.Info("  PVE services: systemctl restart pve-cluster pvedaemon pveproxy")
		}
	} else if systemType == SystemTypePBS {
		if pbsServicesStopped {
			logger.Info("  PBS services were stopped/restarted during restore; verify status with: systemctl status proxmox-backup proxmox-backup-proxy")
		} else {
			logger.Info("  PBS services: systemctl restart proxmox-backup-proxy proxmox-backup")
		}

		if hasCategoryID(plan.NormalCategories, "zfs") {
			logger.Info("")
			if err := checkZFSPoolsAfterRestore(logger); err != nil {
				logger.Warning("ZFS pool check: %v", err)
			}
		} else {
			logger.Debug("Skipping ZFS pool verification (ZFS category not selected)")
		}
	}

	logger.Info("")
	logger.Warning("⚠ SYSTEM REBOOT RECOMMENDED")
	logger.Info("Reboot the node (or at least restart networking and system services) to ensure all restored configurations take effect cleanly.")

	return nil
}

func maybeAddRecommendedCategoriesForTFA(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, selected []Category, available []Category) ([]Category, error) {
	if ui == nil || logger == nil {
		return selected, nil
	}
	if !hasCategoryID(selected, "pve_access_control") && !hasCategoryID(selected, "pbs_access_control") {
		return selected, nil
	}

	var missing []string
	if !hasCategoryID(selected, "network") {
		missing = append(missing, "network")
	}
	if !hasCategoryID(selected, "ssl") {
		missing = append(missing, "ssl")
	}
	if len(missing) == 0 {
		return selected, nil
	}

	var addCategories []Category
	var addNames []string
	for _, id := range missing {
		cat := GetCategoryByID(id, available)
		if cat == nil || !cat.IsAvailable || cat.ExportOnly {
			continue
		}
		addCategories = append(addCategories, *cat)
		addNames = append(addNames, cat.Name)
	}
	if len(addCategories) == 0 {
		return selected, nil
	}

	message := fmt.Sprintf(
		"You selected Access Control without restoring: %s\n\n"+
			"If TFA includes WebAuthn/FIDO2, changing the UI origin (FQDN/hostname or port) may require re-enrollment.\n\n"+
			"For maximum 1:1 compatibility, ProxSave recommends restoring these categories too.\n\n"+
			"Add recommended categories now?",
		strings.Join(addNames, ", "),
	)
	addNow, err := ui.ConfirmAction(ctx, "TFA/WebAuthn compatibility", message, "Add recommended", "Keep current", 0, true)
	if err != nil {
		return nil, err
	}
	if !addNow {
		logger.Warning("Access control selected without %s; WebAuthn users may require re-enrollment if the UI origin changes", strings.Join(addNames, ", "))
		return selected, nil
	}

	selected = append(selected, addCategories...)
	return dedupeCategoriesByID(selected), nil
}

func dedupeCategoriesByID(categories []Category) []Category {
	if len(categories) == 0 {
		return categories
	}
	seen := make(map[string]struct{}, len(categories))
	out := make([]Category, 0, len(categories))
	for _, cat := range categories {
		id := strings.TrimSpace(cat.ID)
		if id == "" {
			out = append(out, cat)
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, cat)
	}
	return out
}

func runFullRestoreWithUI(ctx context.Context, ui RestoreWorkflowUI, candidate *decryptCandidate, prepared *preparedBundle, destRoot string, logger *logging.Logger, dryRun bool) error {
	if candidate == nil || prepared == nil || prepared.Manifest.ArchivePath == "" {
		return fmt.Errorf("invalid restore candidate")
	}

	if err := ui.ShowMessage(ctx, "Full restore", "Backup category analysis failed; ProxSave will run a full restore (no selective modes)."); err != nil {
		return err
	}

	confirmed, err := ui.ConfirmRestore(ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrRestoreAborted
	}

	safeFstabMerge := destRoot == "/" && isRealRestoreFS(restoreFS)
	skipFn := func(name string) bool {
		if !safeFstabMerge {
			return false
		}
		clean := strings.TrimPrefix(strings.TrimSpace(name), "./")
		clean = strings.TrimPrefix(clean, "/")
		return clean == "etc/fstab"
	}

	if safeFstabMerge {
		logger.Warning("Full restore safety: /etc/fstab will not be overwritten; Smart Merge will be applied after extraction.")
	}

	if err := extractPlainArchive(ctx, prepared.ArchivePath, destRoot, logger, skipFn); err != nil {
		return err
	}

	if safeFstabMerge {
		logger.Info("")
		fsTempDir, err := restoreFS.MkdirTemp("", "proxsave-fstab-")
		if err != nil {
			logger.Warning("Failed to create temp dir for fstab merge: %v", err)
		} else {
			defer restoreFS.RemoveAll(fsTempDir)
			fsCategory := []Category{{
				ID:   "filesystem",
				Name: "Filesystem Configuration",
				Paths: []string{
					"./etc/fstab",
				},
			}}
			if err := extractArchiveNative(ctx, prepared.ArchivePath, fsTempDir, logger, fsCategory, RestoreModeCustom, nil, "", nil); err != nil {
				logger.Warning("Failed to extract filesystem config for merge: %v", err)
			} else {
				currentFstab := filepath.Join(destRoot, "etc", "fstab")
				backupFstab := filepath.Join(fsTempDir, "etc", "fstab")
				if err := smartMergeFstabWithUI(ctx, logger, ui, currentFstab, backupFstab, dryRun); err != nil {
					if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
						logger.Info("Restore aborted by user during Smart Filesystem Configuration Merge.")
						return err
					}
					logger.Warning("Smart Fstab Merge failed: %v", err)
				}
			}
		}
	}

	logger.Info("Restore completed successfully.")
	return nil
}

func runSafeClusterApplyWithUI(ctx context.Context, ui RestoreWorkflowUI, exportRoot string, logger *logging.Logger, plan *RestorePlan) (err error) {
	done := logging.DebugStart(logger, "safe cluster apply (ui)", "export_root=%s", exportRoot)
	defer func() { done(err) }()

	if err := ctx.Err(); err != nil {
		return err
	}

	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}

	pveshPath, lookErr := exec.LookPath("pvesh")
	if lookErr != nil {
		logger.Warning("pvesh not found in PATH; skipping SAFE cluster apply")
		return nil
	}
	logging.DebugStep(logger, "safe cluster apply (ui)", "pvesh=%s", pveshPath)

	currentNode, _ := os.Hostname()
	currentNode = shortHost(currentNode)
	if strings.TrimSpace(currentNode) == "" {
		currentNode = "localhost"
	}
	logging.DebugStep(logger, "safe cluster apply (ui)", "current_node=%s", currentNode)

	logger.Info("")
	logger.Info("SAFE cluster restore: applying configs via pvesh (node=%s)", currentNode)

	// Datacenter-wide objects (SAFE apply):
	// - resource mappings (used by VM configs via mapping=<id>)
	// - resource pools (definitions + membership)
	if mapErr := maybeApplyPVEClusterResourceMappingsWithUI(ctx, ui, logger, exportRoot); mapErr != nil {
		logger.Warning("SAFE apply: resource mappings: %v", mapErr)
	}

	pools, poolsErr := readPVEPoolsFromExportUserCfg(exportRoot)
	if poolsErr != nil {
		logger.Warning("SAFE apply: failed to parse pools from export: %v", poolsErr)
		pools = nil
	}
	applyPools := false
	allowPoolMove := false
	if len(pools) > 0 {
		poolNames := summarizePoolIDs(pools, 10)
		message := fmt.Sprintf("Found %d pool(s) in exported user.cfg.\n\nPools: %s\n\nApply pool definitions now? (Membership will be applied later in this SAFE apply flow.)", len(pools), poolNames)
		ok, promptErr := ui.ConfirmAction(ctx, "Apply PVE resource pools (merge)", message, "Apply now", "Skip apply", 0, false)
		if promptErr != nil {
			return promptErr
		}
		applyPools = ok
		logging.DebugStep(logger, "safe cluster apply (ui)", "User choice: apply_pools=%v (pools=%d)", applyPools, len(pools))
		if applyPools {
			if anyPoolHasVMs(pools) {
				moveMsg := "Allow moving guests from other pools to match the backup? This may change the current pool assignment of existing VMs/CTs."
				move, moveErr := ui.ConfirmAction(ctx, "Pools: allow move (VM/CT)", moveMsg, "Allow move", "Don't move", 0, false)
				if moveErr != nil {
					return moveErr
				}
				allowPoolMove = move
			}

			applied, failed, applyErr := applyPVEPoolsDefinitions(ctx, logger, pools)
			if applyErr != nil {
				logger.Warning("Pools apply (definitions) encountered errors: %v", applyErr)
			}
			logger.Info("Pools apply (definitions) completed: ok=%d failed=%d", applied, failed)
		}
	}

	sourceNode := currentNode
	logging.DebugStep(logger, "safe cluster apply (ui)", "List exported node directories under %s", filepath.Join(exportRoot, "etc/pve/nodes"))
	exportNodes, nodesErr := listExportNodeDirs(exportRoot)
	if nodesErr != nil {
		logger.Warning("Failed to inspect exported node directories: %v", nodesErr)
	} else if len(exportNodes) > 0 {
		logging.DebugStep(logger, "safe cluster apply (ui)", "export_nodes=%s", strings.Join(exportNodes, ","))
	} else {
		logging.DebugStep(logger, "safe cluster apply (ui)", "No exported node directories found")
	}

	if len(exportNodes) > 0 && !stringSliceContains(exportNodes, sourceNode) {
		logging.DebugStep(logger, "safe cluster apply (ui)", "Node mismatch: current_node=%s export_nodes=%s", currentNode, strings.Join(exportNodes, ","))
		logger.Warning("SAFE cluster restore: VM/CT configs not found for current node %s in export; available nodes: %s", currentNode, strings.Join(exportNodes, ", "))
		if len(exportNodes) == 1 {
			sourceNode = exportNodes[0]
			logging.DebugStep(logger, "safe cluster apply (ui)", "Auto-select source node: %s", sourceNode)
			logger.Info("SAFE cluster restore: using exported node %s as VM/CT source, applying to current node %s", sourceNode, currentNode)
		} else {
			for _, node := range exportNodes {
				qemuCount, lxcCount := countVMConfigsForNode(exportRoot, node)
				logging.DebugStep(logger, "safe cluster apply (ui)", "Export node candidate: %s (qemu=%d, lxc=%d)", node, qemuCount, lxcCount)
			}
			selected, selErr := ui.SelectExportNode(ctx, exportRoot, currentNode, exportNodes)
			if selErr != nil {
				return selErr
			}
			if strings.TrimSpace(selected) == "" {
				logging.DebugStep(logger, "safe cluster apply (ui)", "User selected: skip VM/CT apply (no source node)")
				logger.Info("Skipping VM/CT apply (no source node selected)")
				sourceNode = ""
			} else {
				sourceNode = selected
				logging.DebugStep(logger, "safe cluster apply (ui)", "User selected source node: %s", sourceNode)
				logger.Info("SAFE cluster restore: selected exported node %s as VM/CT source, applying to current node %s", sourceNode, currentNode)
			}
		}
	}
	logging.DebugStep(logger, "safe cluster apply (ui)", "Selected VM/CT source node: %q (current_node=%q)", sourceNode, currentNode)

	var vmEntries []vmEntry
	if strings.TrimSpace(sourceNode) != "" {
		logging.DebugStep(logger, "safe cluster apply (ui)", "Scan VM/CT configs in export (source_node=%s)", sourceNode)
		vmEntries, err = scanVMConfigs(exportRoot, sourceNode)
		if err != nil {
			logger.Warning("Failed to scan VM configs: %v", err)
			vmEntries = nil
		} else {
			logging.DebugStep(logger, "safe cluster apply (ui)", "VM/CT configs found=%d (source_node=%s)", len(vmEntries), sourceNode)
		}
	}

	if len(vmEntries) > 0 {
		applyVMs, promptErr := ui.ConfirmApplyVMConfigs(ctx, sourceNode, currentNode, len(vmEntries))
		if promptErr != nil {
			return promptErr
		}
		logging.DebugStep(logger, "safe cluster apply (ui)", "User choice: apply_vms=%v (entries=%d)", applyVMs, len(vmEntries))
		if applyVMs {
			applied, failed := applyVMConfigs(ctx, vmEntries, logger)
			logger.Info("VM/CT apply completed: ok=%d failed=%d", applied, failed)
		} else {
			logger.Info("Skipping VM/CT apply")
		}
	} else {
		if strings.TrimSpace(sourceNode) == "" {
			logger.Info("No VM/CT configs applied (no source node selected)")
		} else {
			logger.Info("No VM/CT configs found for node %s in export", sourceNode)
		}
	}

	skipStorageDatacenter := plan != nil && plan.HasCategoryID("storage_pve")
	if skipStorageDatacenter {
		logging.DebugStep(logger, "safe cluster apply (ui)", "Skip storage/datacenter apply: handled by storage_pve staged restore")
		logger.Info("Skipping storage/datacenter apply (handled by storage_pve staged restore)")
	} else {
		storageCfg := filepath.Join(exportRoot, "etc/pve/storage.cfg")
		logging.DebugStep(logger, "safe cluster apply (ui)", "Check export: storage.cfg (%s)", storageCfg)
		storageInfo, storageErr := restoreFS.Stat(storageCfg)
		if storageErr == nil && !storageInfo.IsDir() {
			logging.DebugStep(logger, "safe cluster apply (ui)", "storage.cfg found (size=%d)", storageInfo.Size())
			applyStorage, promptErr := ui.ConfirmApplyStorageCfg(ctx, storageCfg)
			if promptErr != nil {
				return promptErr
			}
			logging.DebugStep(logger, "safe cluster apply (ui)", "User choice: apply_storage=%v", applyStorage)
			if applyStorage {
				applied, failed, err := applyStorageCfg(ctx, storageCfg, logger)
				logging.DebugStep(logger, "safe cluster apply (ui)", "Storage apply result: ok=%d failed=%d err=%v", applied, failed, err)
				if err != nil {
					logger.Warning("Storage apply encountered errors: %v", err)
				}
				logger.Info("Storage apply completed: ok=%d failed=%d", applied, failed)
			} else {
				logger.Info("Skipping storage.cfg apply")
			}
		} else {
			logging.DebugStep(logger, "safe cluster apply (ui)", "storage.cfg not found (err=%v)", storageErr)
			logger.Info("No storage.cfg found in export")
		}

		dcCfg := filepath.Join(exportRoot, "etc/pve/datacenter.cfg")
		logging.DebugStep(logger, "safe cluster apply (ui)", "Check export: datacenter.cfg (%s)", dcCfg)
		dcInfo, dcErr := restoreFS.Stat(dcCfg)
		if dcErr == nil && !dcInfo.IsDir() {
			logging.DebugStep(logger, "safe cluster apply (ui)", "datacenter.cfg found (size=%d)", dcInfo.Size())
			applyDC, promptErr := ui.ConfirmApplyDatacenterCfg(ctx, dcCfg)
			if promptErr != nil {
				return promptErr
			}
			logging.DebugStep(logger, "safe cluster apply (ui)", "User choice: apply_datacenter=%v", applyDC)
			if applyDC {
				logging.DebugStep(logger, "safe cluster apply (ui)", "Apply datacenter.cfg via pvesh")
				if err := runPvesh(ctx, logger, []string{"set", "/cluster/config", "-conf", dcCfg}); err != nil {
					logger.Warning("Failed to apply datacenter.cfg: %v", err)
				} else {
					logger.Info("datacenter.cfg applied successfully")
				}
			} else {
				logger.Info("Skipping datacenter.cfg apply")
			}
		} else {
			logging.DebugStep(logger, "safe cluster apply (ui)", "datacenter.cfg not found (err=%v)", dcErr)
			logger.Info("No datacenter.cfg found in export")
		}
	}

	// Apply pool membership after VM configs and storage/datacenter apply.
	if applyPools && len(pools) > 0 {
		applied, failed, applyErr := applyPVEPoolsMembership(ctx, logger, pools, allowPoolMove)
		if applyErr != nil {
			logger.Warning("Pools apply (membership) encountered errors: %v", applyErr)
		}
		logger.Info("Pools apply (membership) completed: ok=%d failed=%d", applied, failed)
	}

	return nil
}

func smartMergeFstabWithUI(ctx context.Context, logger *logging.Logger, ui RestoreWorkflowUI, currentFstabPath, backupFstabPath string, dryRun bool) error {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	logger.Info("")
	logger.Step("Smart Filesystem Configuration Merge")
	logger.Debug("[FSTAB_MERGE] Starting analysis of %s vs backup %s...", currentFstabPath, backupFstabPath)

	currentEntries, currentRaw, err := parseFstab(currentFstabPath)
	if err != nil {
		return fmt.Errorf("failed to parse current fstab: %w", err)
	}
	backupEntries, _, err := parseFstab(backupFstabPath)
	if err != nil {
		return fmt.Errorf("failed to parse backup fstab: %w", err)
	}

	remappedCount := 0
	backupRoot := fstabBackupRootFromPath(backupFstabPath)
	if backupRoot != "" {
		if remapped, count := remapFstabDevicesFromInventory(logger, backupEntries, backupRoot); count > 0 {
			backupEntries = remapped
			remappedCount = count
			logger.Info("Fstab device remap: converted %d entry(ies) from /dev/* to stable UUID/PARTUUID/LABEL based on ProxSave inventory", count)
		} else {
			backupEntries = remapped
		}
	}

	analysis := analyzeFstabMerge(logger, currentEntries, backupEntries)
	if len(analysis.ProposedMounts) == 0 {
		logger.Info("No new safe mounts found to restore. Keeping current fstab.")
		return nil
	}

	defaultYes := analysis.RootComparable && analysis.RootMatch && (!analysis.SwapComparable || analysis.SwapMatch)

	var msg strings.Builder
	msg.WriteString("ProxSave found missing mounts in /etc/fstab.\n\n")
	if analysis.RootComparable && !analysis.RootMatch {
		msg.WriteString("⚠ Root UUID mismatch: the backup appears to come from a different machine.\n")
	}
	if analysis.SwapComparable && !analysis.SwapMatch {
		msg.WriteString("⚠ Swap mismatch: the current swap configuration will be kept.\n")
	}
	if remappedCount > 0 {
		fmt.Fprintf(&msg, "✓ Remapped %d fstab entry(ies) from /dev/* to stable UUID/PARTUUID/LABEL using ProxSave inventory.\n", remappedCount)
	}
	msg.WriteString("\nProposed mounts (safe):\n")
	for _, mount := range analysis.ProposedMounts {
		fmt.Fprintf(&msg, "  - %s -> %s (%s)\n", mount.Device, mount.MountPoint, mount.Type)
	}
	if len(analysis.SkippedMounts) > 0 {
		msg.WriteString("\nMounts found but not auto-proposed:\n")
		for _, mount := range analysis.SkippedMounts {
			fmt.Fprintf(&msg, "  - %s -> %s (%s)\n", mount.Device, mount.MountPoint, mount.Type)
		}
		msg.WriteString("\nHint: verify disks/UUIDs and options (nofail/_netdev) before adding them.\n")
	}

	confirmMsg := "Do you want to add the missing mounts (NFS/CIFS and data mounts with verified UUID/LABEL)?"
	if strings.TrimSpace(confirmMsg) != "" {
		msg.WriteString("\n")
		msg.WriteString(confirmMsg)
	}

	confirmed, err := ui.ConfirmFstabMerge(ctx, "Smart fstab merge", msg.String(), 90*time.Second, defaultYes)
	if err != nil {
		return err
	}
	if !confirmed {
		logger.Info("Fstab merge skipped by user.")
		return nil
	}

	return applyFstabMerge(ctx, logger, currentRaw, currentFstabPath, analysis.ProposedMounts, dryRun)
}
