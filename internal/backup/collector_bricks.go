package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

type BrickID string

const (
	brickPVEValidateAndCluster         BrickID = "pve_validate_and_cluster"
	brickPVEConfigSnapshot             BrickID = "pve_config_snapshot"
	brickPVEClusterSnapshot            BrickID = "pve_cluster_snapshot"
	brickPVEFirewallSnapshot           BrickID = "pve_firewall_snapshot"
	brickPVEVZDumpSnapshot             BrickID = "pve_vzdump_snapshot"
	brickPVERuntimeCore                BrickID = "pve_runtime_core"
	brickPVERuntimeACL                 BrickID = "pve_runtime_acl"
	brickPVERuntimeCluster             BrickID = "pve_runtime_cluster"
	brickPVERuntimeStorage             BrickID = "pve_runtime_storage"
	brickPVEVMQEMUConfigs              BrickID = "pve_vm_qemu_configs"
	brickPVEVMLXCConfigs               BrickID = "pve_vm_lxc_configs"
	brickPVEGuestInventory             BrickID = "pve_guest_inventory"
	brickPVEBackupJobDefs              BrickID = "pve_backup_job_definitions"
	brickPVEBackupJobHistory           BrickID = "pve_backup_job_history"
	brickPVEVZDumpCron                 BrickID = "pve_vzdump_cron_snapshot"
	brickPVEScheduleCrontab            BrickID = "pve_schedule_crontab"
	brickPVEScheduleTimers             BrickID = "pve_schedule_timers"
	brickPVEScheduleCronFiles          BrickID = "pve_schedule_cron_files"
	brickPVEReplicationDefs            BrickID = "pve_replication_definitions"
	brickPVEReplicationStatus          BrickID = "pve_replication_status"
	brickPVEStorageResolve             BrickID = "pve_storage_resolve"
	brickPVEStorageProbe               BrickID = "pve_storage_probe"
	brickPVEStorageMetadataJSON        BrickID = "pve_storage_metadata_json"
	brickPVEStorageMetadataText        BrickID = "pve_storage_metadata_text"
	brickPVEStorageBackupAnalysis      BrickID = "pve_storage_backup_analysis"
	brickPVEStorageSummary             BrickID = "pve_storage_summary"
	brickPVECephConfigSnapshot         BrickID = "pve_ceph_config_snapshot"
	brickPVECephRuntime                BrickID = "pve_ceph_runtime"
	brickPVEAliasCore                  BrickID = "pve_alias_core"
	brickPVEAggregateBackupHistory     BrickID = "pve_aggregate_backup_history"
	brickPVEAggregateReplicationStatus BrickID = "pve_aggregate_replication_status"
	brickPVEVersionInfo                BrickID = "pve_version_info"
	brickPVEManifestFinalize           BrickID = "pve_manifest_finalize"

	brickPBSValidate                       BrickID = "pbs_validate"
	brickPBSConfigDirectoryCopy            BrickID = "pbs_config_directory_copy"
	brickPBSManifestInit                   BrickID = "pbs_manifest_init"
	brickPBSDatastoreDiscovery             BrickID = "pbs_datastore_discovery"
	brickPBSManifestDatastore              BrickID = "pbs_manifest_datastore"
	brickPBSManifestS3                     BrickID = "pbs_manifest_s3"
	brickPBSManifestNode                   BrickID = "pbs_manifest_node"
	brickPBSManifestACMEAccounts           BrickID = "pbs_manifest_acme_accounts"
	brickPBSManifestACMEPlugins            BrickID = "pbs_manifest_acme_plugins"
	brickPBSManifestMetricServers          BrickID = "pbs_manifest_metric_servers"
	brickPBSManifestTrafficControl         BrickID = "pbs_manifest_traffic_control"
	brickPBSManifestNotifications          BrickID = "pbs_manifest_notifications"
	brickPBSManifestNotificationsPriv      BrickID = "pbs_manifest_notifications_priv"
	brickPBSManifestAccess                 BrickID = "pbs_manifest_access"
	brickPBSManifestRemote                 BrickID = "pbs_manifest_remote"
	brickPBSManifestSyncJobs               BrickID = "pbs_manifest_sync_jobs"
	brickPBSManifestVerificationJobs       BrickID = "pbs_manifest_verification_jobs"
	brickPBSManifestTape                   BrickID = "pbs_manifest_tape"
	brickPBSManifestNetwork                BrickID = "pbs_manifest_network"
	brickPBSManifestPrune                  BrickID = "pbs_manifest_prune"
	brickPBSRuntimeCore                    BrickID = "pbs_runtime_core"
	brickPBSRuntimeNode                    BrickID = "pbs_runtime_node"
	brickPBSRuntimeDatastoreList           BrickID = "pbs_runtime_datastore_list"
	brickPBSRuntimeDatastoreStatus         BrickID = "pbs_runtime_datastore_status"
	brickPBSRuntimeACMEAccountsList        BrickID = "pbs_runtime_acme_accounts_list"
	brickPBSRuntimeACMEAccountInfo         BrickID = "pbs_runtime_acme_account_info"
	brickPBSRuntimeACMEPluginsList         BrickID = "pbs_runtime_acme_plugins_list"
	brickPBSRuntimeACMEPluginConfig        BrickID = "pbs_runtime_acme_plugin_config"
	brickPBSRuntimeNotificationTargets     BrickID = "pbs_runtime_notification_targets"
	brickPBSRuntimeNotificationMatchers    BrickID = "pbs_runtime_notification_matchers"
	brickPBSRuntimeNotificationEndpoints   BrickID = "pbs_runtime_notification_endpoints"
	brickPBSRuntimeNotificationSummary     BrickID = "pbs_runtime_notification_summary"
	brickPBSRuntimeAccessUsers             BrickID = "pbs_runtime_access_users"
	brickPBSRuntimeAccessRealms            BrickID = "pbs_runtime_access_realms"
	brickPBSRuntimeAccessACL               BrickID = "pbs_runtime_access_acl"
	brickPBSRuntimeAccessUserTokens        BrickID = "pbs_runtime_access_user_tokens"
	brickPBSRuntimeAccessTokensAggregate   BrickID = "pbs_runtime_access_tokens_aggregate"
	brickPBSRuntimeRemotes                 BrickID = "pbs_runtime_remotes"
	brickPBSRuntimeSyncJobs                BrickID = "pbs_runtime_sync_jobs"
	brickPBSRuntimeVerificationJobs        BrickID = "pbs_runtime_verification_jobs"
	brickPBSRuntimePruneJobs               BrickID = "pbs_runtime_prune_jobs"
	brickPBSRuntimeGCJobs                  BrickID = "pbs_runtime_gc_jobs"
	brickPBSRuntimeTapeDetect              BrickID = "pbs_runtime_tape_detect"
	brickPBSRuntimeTapeDrives              BrickID = "pbs_runtime_tape_drives"
	brickPBSRuntimeTapeChangers            BrickID = "pbs_runtime_tape_changers"
	brickPBSRuntimeTapePools               BrickID = "pbs_runtime_tape_pools"
	brickPBSRuntimeNetwork                 BrickID = "pbs_runtime_network"
	brickPBSRuntimeDisks                   BrickID = "pbs_runtime_disks"
	brickPBSRuntimeCertInfo                BrickID = "pbs_runtime_cert_info"
	brickPBSRuntimeTrafficControl          BrickID = "pbs_runtime_traffic_control"
	brickPBSRuntimeRecentTasks             BrickID = "pbs_runtime_recent_tasks"
	brickPBSRuntimeS3Endpoints             BrickID = "pbs_runtime_s3_endpoints"
	brickPBSRuntimeS3EndpointBuckets       BrickID = "pbs_runtime_s3_endpoint_buckets"
	brickPBSStorageStackDirsSnapshot       BrickID = "pbs_storage_stack_dirs_snapshot"
	brickPBSStorageStackMountUnitsSnapshot BrickID = "pbs_storage_stack_mount_units_snapshot"
	brickPBSStorageStackAutofsSnapshot     BrickID = "pbs_storage_stack_autofs_snapshot"
	brickPBSStorageStackReferencedFiles    BrickID = "pbs_storage_stack_referenced_files_snapshot"
	brickPBSInventoryInit                  BrickID = "pbs_inventory_init"
	brickPBSInventoryBaseFiles             BrickID = "pbs_inventory_base_files"
	brickPBSInventoryBaseDirs              BrickID = "pbs_inventory_base_dirs"
	brickPBSInventoryMountUnits            BrickID = "pbs_inventory_mount_units"
	brickPBSInventoryReferencedFiles       BrickID = "pbs_inventory_referenced_files"
	brickPBSInventoryHostCommands          BrickID = "pbs_inventory_host_commands"
	brickPBSInventoryCommandFiles          BrickID = "pbs_inventory_command_files"
	brickPBSInventoryDatastores            BrickID = "pbs_inventory_datastores"
	brickPBSInventoryWrite                 BrickID = "pbs_inventory_write"
	brickPBSDatastoreConfigs               BrickID = "pbs_datastore_configs"
	brickPBSPXAR                           BrickID = "pbs_pxar"
	brickPBSFinalizeSummary                BrickID = "pbs_finalize_summary"

	brickSystemNetworkStatic        BrickID = "system_network_static"
	brickSystemIdentityStatic       BrickID = "system_identity_static"
	brickSystemAptStatic            BrickID = "system_apt_static"
	brickSystemCronStatic           BrickID = "system_cron_static"
	brickSystemServicesStatic       BrickID = "system_services_static"
	brickSystemLoggingStatic        BrickID = "system_logging_static"
	brickSystemSSLStatic            BrickID = "system_ssl_static"
	brickSystemSysctlStatic         BrickID = "system_sysctl_static"
	brickSystemKernelModulesStatic  BrickID = "system_kernel_modules_static"
	brickSystemZFSStatic            BrickID = "system_zfs_static"
	brickSystemFirewallStatic       BrickID = "system_firewall_static"
	brickSystemRuntimeLeases        BrickID = "system_runtime_leases"
	brickSystemCoreRuntime          BrickID = "system_core_runtime"
	brickSystemNetworkRuntime       BrickID = "system_network_runtime"
	brickSystemStorageRuntime       BrickID = "system_storage_runtime"
	brickSystemComputeRuntime       BrickID = "system_compute_runtime"
	brickSystemServicesRuntime      BrickID = "system_services_runtime"
	brickSystemPackagesRuntime      BrickID = "system_packages_runtime"
	brickSystemFirewallRuntime      BrickID = "system_firewall_runtime"
	brickSystemKernelModulesRuntime BrickID = "system_kernel_modules_runtime"
	brickSystemSysctlRuntime        BrickID = "system_sysctl_runtime"
	brickSystemZFSRuntime           BrickID = "system_zfs_runtime"
	brickSystemLVMRuntime           BrickID = "system_lvm_runtime"
	brickSystemNetworkReport        BrickID = "system_network_report"
	brickSystemKernel               BrickID = "system_kernel"
	brickSystemHardware             BrickID = "system_hardware"
	brickSystemCriticalFiles        BrickID = "system_critical_files"
	brickSystemConfigFile           BrickID = "system_config_file"
	brickSystemCustomPaths          BrickID = "system_custom_paths"
	brickSystemScriptDirs           BrickID = "system_script_dirs"
	brickSystemScriptRepo           BrickID = "system_script_repo"
	brickSystemSSHKeys              BrickID = "system_ssh_keys"
	brickSystemRootHome             BrickID = "system_root_home"
	brickSystemUserHomes            BrickID = "system_user_homes"
)

type collectionBrick struct {
	ID          BrickID
	Description string
	Run         func(context.Context, *collectionState) error
}

type recipe struct {
	Name   string
	Bricks []collectionBrick
}

type collectionState struct {
	collector *Collector
	pve       pveContext
	pbs       pbsContext
	system    systemContext
}

type pveContext struct {
	clustered                    bool
	runtimeInfo                  *pveRuntimeInfo
	commandsDir                  string
	resolvedStorages             []pveStorageEntry
	probedStorages               []pveStorageEntry
	storageScanResults           map[string]*pveStorageScanResult
	guestCollectionAborted       bool
	jobCollectionAborted         bool
	scheduleCollectionAborted    bool
	replicationCollectionAborted bool
	storageCollectionAborted     bool
	cephCollectionAborted        bool
	finalizeCollectionAborted    bool
}

type pbsContext struct {
	datastores       []pbsDatastore
	commandsDir      string
	userIDs          []string
	acmeAccountNames []string
	acmePluginIDs    []string
	s3EndpointIDs    []string
	tapeSupportKnown bool
	tapeSupported    bool
	inventory        *pbsInventoryState
}

type systemContext struct {
	commandsDir string
}

func newCollectionState(c *Collector) *collectionState {
	return &collectionState{collector: c}
}

func runRecipe(ctx context.Context, r recipe, state *collectionState) error {
	if state == nil {
		return fmt.Errorf("collection state is required")
	}
	for _, brick := range r.Bricks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if brick.Run == nil {
			return fmt.Errorf("recipe %s brick %s has no runner", r.Name, brick.ID)
		}
		if err := brick.Run(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func recipeBrickIDs(r recipe) []BrickID {
	ids := make([]BrickID, 0, len(r.Bricks))
	for _, brick := range r.Bricks {
		ids = append(ids, brick.ID)
	}
	return ids
}

func (s *collectionState) ensurePVECommandsDir() (string, error) {
	if s.pve.commandsDir != "" {
		return s.pve.commandsDir, nil
	}
	dir, err := s.collector.ensureCommandsDir("pve")
	if err != nil {
		return "", err
	}
	s.pve.commandsDir = dir
	return dir, nil
}

func (s *collectionState) ensurePBSCommandsDir() (string, error) {
	if s.pbs.commandsDir != "" {
		return s.pbs.commandsDir, nil
	}
	dir, err := s.collector.ensureCommandsDir("pbs")
	if err != nil {
		return "", err
	}
	s.pbs.commandsDir = dir
	return dir, nil
}

func (s *collectionState) ensureSystemCommandsDir() (string, error) {
	if s.system.commandsDir != "" {
		return s.system.commandsDir, nil
	}
	dir, err := s.collector.ensureCommandsDir("system")
	if err != nil {
		return "", err
	}
	s.system.commandsDir = dir
	return dir, nil
}

func (s *collectionState) ensurePBSInventoryState() *pbsInventoryState {
	if s.pbs.inventory == nil {
		s.pbs.inventory = &pbsInventoryState{}
	}
	return s.pbs.inventory
}

func (s *collectionState) ensurePVERuntimeInfo() *pveRuntimeInfo {
	if s.pve.runtimeInfo == nil {
		s.pve.runtimeInfo = &pveRuntimeInfo{
			Nodes:    make([]string, 0),
			Storages: make([]pveStorageEntry, 0),
		}
	}
	return s.pve.runtimeInfo
}

func newPVERecipe() recipe {
	return recipe{
		Name: "pve",
		Bricks: []collectionBrick{
			{
				ID:          brickPVEValidateAndCluster,
				Description: "Validate PVE environment and detect cluster state",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Validating PVE environment and cluster state prior to collection")

					pveConfigPath := c.effectivePVEConfigPath()
					if _, err := os.Stat(pveConfigPath); err != nil {
						if errors.Is(err, os.ErrNotExist) {
							return fmt.Errorf("not a PVE system: %s not found", pveConfigPath)
						}
						return fmt.Errorf("failed to access PVE config path %s: %w", pveConfigPath, err)
					}
					c.logger.Debug("%s detected, continuing with PVE collection", pveConfigPath)

					clustered := false
					if isClustered, err := c.isClusteredPVE(ctx); err != nil {
						if ctx.Err() != nil {
							return err
						}
						c.logger.Debug("Cluster detection failed, assuming standalone node: %v", err)
					} else {
						clustered = isClustered
						c.logger.Debug("Cluster detection completed: clustered=%v", clustered)
					}

					state.pve.clustered = clustered
					c.clusteredPVE = clustered
					return nil
				},
			},
			{
				ID:          brickPVEConfigSnapshot,
				Description: "Collect base PVE configuration snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPVEConfigSnapshot(ctx)
				},
			},
			{
				ID:          brickPVEClusterSnapshot,
				Description: "Collect cluster-specific PVE snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPVEClusterSnapshot(ctx, state.pve.clustered)
				},
			},
			{
				ID:          brickPVEFirewallSnapshot,
				Description: "Collect PVE firewall snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPVEFirewallSnapshot(ctx)
				},
			},
			{
				ID:          brickPVEVZDumpSnapshot,
				Description: "Collect VZDump snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPVEVZDumpSnapshot(ctx)
				},
			},
			{
				ID:          brickPVERuntimeCore,
				Description: "Collect core PVE runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					commandsDir, err := state.ensurePVECommandsDir()
					if err != nil {
						return err
					}
					c.logger.Debug("Collecting PVE core runtime state")
					return c.collectPVECoreRuntime(ctx, commandsDir, state.ensurePVERuntimeInfo())
				},
			},
			{
				ID:          brickPVERuntimeACL,
				Description: "Collect PVE ACL runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePVECommandsDir()
					if err != nil {
						return err
					}
					state.collector.collectPVEACLRuntime(ctx, commandsDir)
					return nil
				},
			},
			{
				ID:          brickPVERuntimeCluster,
				Description: "Collect PVE cluster runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePVECommandsDir()
					if err != nil {
						return err
					}
					state.collector.collectPVEClusterRuntime(ctx, commandsDir, state.pve.clustered)
					return nil
				},
			},
			{
				ID:          brickPVERuntimeStorage,
				Description: "Collect PVE storage runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					commandsDir, err := state.ensurePVECommandsDir()
					if err != nil {
						return err
					}
					if err := c.collectPVEStorageRuntime(ctx, commandsDir, state.ensurePVERuntimeInfo()); err != nil {
						return err
					}
					c.finalizePVERuntimeInfo(state.ensurePVERuntimeInfo())
					return nil
				},
			},
			{
				ID:          brickPVEVMQEMUConfigs,
				Description: "Collect QEMU VM configurations",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupVMConfigs {
						c.logger.Skip("VM/container configuration backup disabled.")
						return nil
					}
					if state.pve.guestCollectionAborted {
						return nil
					}
					c.logger.Info("Collecting VM and container configurations")
					if err := c.collectPVEQEMUConfigs(ctx); err != nil {
						c.logger.Warning("Failed to collect QEMU VM configs: %v", err)
						state.pve.guestCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEVMLXCConfigs,
				Description: "Collect LXC container configurations",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupVMConfigs || state.pve.guestCollectionAborted {
						return nil
					}
					if err := c.collectPVELXCConfigs(ctx); err != nil {
						c.logger.Warning("Failed to collect LXC configs: %v", err)
						state.pve.guestCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEGuestInventory,
				Description: "Collect guest inventory",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupVMConfigs || state.pve.guestCollectionAborted {
						return nil
					}
					if err := c.collectPVEGuestInventory(ctx); err != nil {
						c.logger.Warning("Failed to collect guest inventory: %v", err)
						state.pve.guestCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEBackupJobDefs,
				Description: "Collect PVE backup job definitions",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEJobs || state.pve.jobCollectionAborted {
						return nil
					}
					c.logger.Debug("Collecting PVE job definitions for nodes: %v", state.pve.runtimeNodes())
					if err := c.collectPVEBackupJobDefinitions(ctx); err != nil {
						c.logger.Warning("Failed to collect PVE backup job definitions: %v", err)
						state.pve.jobCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEBackupJobHistory,
				Description: "Collect PVE backup job history",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEJobs || state.pve.jobCollectionAborted {
						return nil
					}
					if err := c.collectPVEBackupJobHistory(ctx, state.pve.runtimeNodes()); err != nil {
						c.logger.Warning("Failed to collect PVE backup history: %v", err)
						state.pve.jobCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEVZDumpCron,
				Description: "Collect VZDump cron snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEJobs || state.pve.jobCollectionAborted {
						return nil
					}
					if err := c.collectPVEVZDumpCronSnapshot(ctx); err != nil {
						c.logger.Warning("Failed to collect VZDump cron snapshot: %v", err)
						state.pve.jobCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEScheduleCrontab,
				Description: "Collect root crontab schedule data",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVESchedules || state.pve.scheduleCollectionAborted {
						return nil
					}
					if err := c.collectPVEScheduleCrontab(ctx); err != nil {
						c.logger.Warning("Failed to collect PVE crontab schedules: %v", err)
						state.pve.scheduleCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEScheduleTimers,
				Description: "Collect systemd timer schedule data",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVESchedules || state.pve.scheduleCollectionAborted {
						return nil
					}
					if err := c.collectPVEScheduleTimers(ctx); err != nil {
						c.logger.Warning("Failed to collect PVE timer schedules: %v", err)
						state.pve.scheduleCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEScheduleCronFiles,
				Description: "Collect PVE-related cron files",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVESchedules || state.pve.scheduleCollectionAborted {
						return nil
					}
					if err := c.collectPVEScheduleCronFiles(ctx); err != nil {
						c.logger.Warning("Failed to collect PVE cron schedule files: %v", err)
						state.pve.scheduleCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEReplicationDefs,
				Description: "Collect PVE replication definitions",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEReplication || state.pve.replicationCollectionAborted {
						return nil
					}
					c.logger.Debug("Collecting PVE replication settings for nodes: %v", state.pve.runtimeNodes())
					if err := c.collectPVEReplicationDefinitions(ctx); err != nil {
						c.logger.Warning("Failed to collect PVE replication definitions: %v", err)
						state.pve.replicationCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEReplicationStatus,
				Description: "Collect PVE replication status",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEReplication || state.pve.replicationCollectionAborted {
						return nil
					}
					if err := c.collectPVEReplicationStatus(ctx, state.pve.runtimeNodes()); err != nil {
						c.logger.Warning("Failed to collect PVE replication status: %v", err)
						state.pve.replicationCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEStorageResolve,
				Description: "Resolve PVE storage list for backup analysis",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEBackupFiles {
						return nil
					}
					if state.pve.storageCollectionAborted {
						return nil
					}
					if err := ctx.Err(); err != nil {
						return err
					}
					c.logger.Info("Collecting PVE datastore information using auto-detection")
					c.logger.Debug("Collecting datastore metadata for %d storages", len(state.pve.runtimeStorages()))
					state.pve.resolvedStorages = c.resolvePVEStorages(state.pve.runtimeStorages())
					if len(state.pve.resolvedStorages) == 0 {
						c.logger.Info("Found 0 PVE datastore(s) via auto-detection")
						c.logger.Info("No PVE datastores detected - skipping metadata collection")
						return nil
					}
					c.logger.Info("Found %d PVE datastore(s) via auto-detection", len(state.pve.resolvedStorages))
					return nil
				},
			},
			{
				ID:          brickPVEStorageProbe,
				Description: "Probe resolved PVE storages",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.resolvedStorages) == 0 {
						return nil
					}
					baseDir := c.pveDatastoresBaseDir()
					if err := c.ensureDir(baseDir); err != nil {
						c.logger.Warning("Failed to create datastore metadata directory: %v", err)
						state.pve.storageCollectionAborted = true
						return nil
					}
					ioTimeout := c.pveStorageIOTimout()
					state.pve.probedStorages = nil
					state.pve.storageScanResults = nil
					for _, storage := range state.pve.resolvedStorages {
						result, err := c.preparePVEStorageScan(ctx, storage, baseDir, ioTimeout)
						if err != nil {
							c.logger.Warning("Failed to probe PVE datastore %s: %v", storage.Name, err)
							state.pve.storageCollectionAborted = true
							return nil
						}
						if result == nil {
							continue
						}
						state.pve.probedStorages = append(state.pve.probedStorages, storage)
						state.pve.ensureStorageScanResults()[storage.pathKey()] = result
					}
					return nil
				},
			},
			{
				ID:          brickPVEStorageMetadataJSON,
				Description: "Write JSON metadata for probed PVE storages",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
						return nil
					}
					ioTimeout := c.pveStorageIOTimout()
					for _, storage := range state.pve.probedStorages {
						result := state.pve.storageResult(storage)
						if result == nil || result.SkipRemaining {
							continue
						}
						if err := c.collectPVEStorageMetadataJSONStep(ctx, result, ioTimeout); err != nil {
							c.logger.Warning("Failed to write PVE datastore JSON metadata for %s: %v", storage.Name, err)
							state.pve.storageCollectionAborted = true
							return nil
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVEStorageMetadataText,
				Description: "Write text metadata for probed PVE storages",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
						return nil
					}
					ioTimeout := c.pveStorageIOTimout()
					for _, storage := range state.pve.probedStorages {
						result := state.pve.storageResult(storage)
						if result == nil || result.SkipRemaining {
							continue
						}
						if err := c.collectPVEStorageMetadataTextStep(ctx, result, ioTimeout); err != nil {
							c.logger.Warning("Failed to write PVE datastore text metadata for %s: %v", storage.Name, err)
							state.pve.storageCollectionAborted = true
							return nil
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVEStorageBackupAnalysis,
				Description: "Analyze PVE backup files for probed storages",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
						return nil
					}
					ioTimeout := c.pveStorageIOTimout()
					for _, storage := range state.pve.probedStorages {
						result := state.pve.storageResult(storage)
						if result == nil || result.SkipRemaining {
							continue
						}
						if err := c.collectPVEStorageBackupAnalysisStep(ctx, result, ioTimeout); err != nil {
							c.logger.Warning("Detailed backup analysis for %s failed: %v", storage.Name, err)
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVEStorageSummary,
				Description: "Write PVE datastore summary",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
						return nil
					}
					if err := c.writePVEStorageSummary(ctx, state.pve.probedStorages); err != nil {
						c.logger.Warning("Failed to write PVE datastore summary: %v", err)
						state.pve.storageCollectionAborted = true
						return nil
					}
					c.logger.Debug("PVE datastore metadata collection completed (%d processed)", len(state.pve.probedStorages))
					return nil
				},
			},
			{
				ID:          brickPVECephConfigSnapshot,
				Description: "Collect Ceph configuration snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupCephConfig || state.pve.cephCollectionAborted {
						return nil
					}
					c.logger.Debug("Collecting Ceph configuration and status")
					if err := c.collectPVECephConfigSnapshot(ctx); err != nil {
						c.logger.Warning("Failed to collect Ceph configuration snapshot: %v", err)
						state.pve.cephCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVECephRuntime,
				Description: "Collect Ceph runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if !c.config.BackupCephConfig || state.pve.cephCollectionAborted {
						return nil
					}
					if err := c.collectPVECephRuntime(ctx); err != nil {
						c.logger.Warning("Failed to collect Ceph runtime information: %v", err)
						state.pve.cephCollectionAborted = true
					} else {
						c.logger.Debug("Ceph information collection completed")
					}
					return nil
				},
			},
			{
				ID:          brickPVEAliasCore,
				Description: "Create core PVE aliases",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Creating PVE info aliases under /var/lib/pve-cluster/info")
					if err := c.createPVECoreAliases(ctx); err != nil {
						c.logger.Warning("Failed to create PVE core aliases: %v", err)
						state.pve.finalizeCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEAggregateBackupHistory,
				Description: "Aggregate backup history aliases",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if state.pve.finalizeCollectionAborted {
						return nil
					}
					if err := c.createPVEBackupHistoryAggregate(ctx); err != nil {
						c.logger.Warning("Failed to aggregate PVE backup history: %v", err)
						state.pve.finalizeCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEAggregateReplicationStatus,
				Description: "Aggregate replication status aliases",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if state.pve.finalizeCollectionAborted {
						return nil
					}
					if err := c.createPVEReplicationAggregate(ctx); err != nil {
						c.logger.Warning("Failed to aggregate PVE replication status: %v", err)
						state.pve.finalizeCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEVersionInfo,
				Description: "Write PVE version alias information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if state.pve.finalizeCollectionAborted {
						return nil
					}
					if err := c.createPVEVersionInfo(ctx); err != nil {
						c.logger.Warning("Failed to write PVE version info: %v", err)
						state.pve.finalizeCollectionAborted = true
					}
					return nil
				},
			},
			{
				ID:          brickPVEManifestFinalize,
				Description: "Finalize the PVE manifest",
				Run: func(_ context.Context, state *collectionState) error {
					state.collector.populatePVEManifest()
					return nil
				},
			},
		},
	}
}

func newPBSRecipe() recipe {
	bricks := []collectionBrick{
		{
			ID:          brickPBSValidate,
			Description: "Validate PBS environment",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				c.logger.Debug("Validating PBS environment before collection")

				pbsConfigPath := c.pbsConfigPath()
				if _, err := os.Stat(pbsConfigPath); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("not a PBS system: %s not found", pbsConfigPath)
					}
					return fmt.Errorf("failed to access PBS config path %s: %w", pbsConfigPath, err)
				}
				c.logger.Debug("Detected %s, proceeding with PBS collection", pbsConfigPath)
				return nil
			},
		},
		{
			ID:          brickPBSConfigDirectoryCopy,
			Description: "Copy the PBS configuration directory",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSConfigSnapshot(ctx, state.collector.pbsConfigPath())
			},
		},
		{
			ID:          brickPBSManifestInit,
			Description: "Initialize the PBS manifest",
			Run: func(_ context.Context, state *collectionState) error {
				state.collector.initPBSManifest()
				return nil
			},
		},
		{
			ID:          brickPBSDatastoreDiscovery,
			Description: "Discover PBS datastores",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				datastores, err := c.getDatastoreList(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return err
					}
					return fmt.Errorf("failed to detect PBS datastores: %w", err)
				}
				state.pbs.datastores = datastores
				c.logger.Debug("Detected %d PBS datastores", len(datastores))

				if len(datastores) == 0 {
					c.logger.Info("Found 0 PBS datastore(s) via auto-detection")
				} else {
					summary := make([]string, 0, len(datastores))
					for _, ds := range datastores {
						if ds.Path != "" {
							summary = append(summary, fmt.Sprintf("%s (%s)", ds.Name, ds.Path))
						} else {
							summary = append(summary, ds.Name)
						}
					}
					c.logger.Info("Found %d PBS datastore(s) via auto-detection: %s", len(datastores), strings.Join(summary, ", "))
				}
				return nil
			},
		},
	}
	bricks = append(bricks, newPBSManifestBricks("")...)
	bricks = append(bricks, newPBSRuntimeBricks()...)
	bricks = append(bricks, newPBSStorageStackBricks()...)
	bricks = append(bricks, newPBSInventoryBricks()...)
	bricks = append(bricks, newPBSFeatureBricks()...)
	bricks = append(bricks, newPBSFinalizeBricks()...)
	return recipe{Name: "pbs", Bricks: bricks}
}

func newPBSDirectoryRecipe(root string) recipe {
	bricks := []collectionBrick{
		{
			ID:          brickPBSConfigDirectoryCopy,
			Description: "Copy the PBS configuration directory",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSConfigSnapshot(ctx, root)
			},
		},
		{
			ID:          brickPBSManifestInit,
			Description: "Initialize the PBS manifest",
			Run: func(_ context.Context, state *collectionState) error {
				state.collector.initPBSManifest()
				return nil
			},
		},
	}
	bricks = append(bricks, newPBSManifestBricks(root)...)
	return recipe{Name: "pbs-directories", Bricks: bricks}
}

func newPBSCommandsRecipe() recipe {
	return recipe{Name: "pbs-commands", Bricks: newPBSRuntimeBricks()}
}

func newPBSDatastoreInventoryRecipe() recipe {
	bricks := append([]collectionBrick{}, newPBSStorageStackBricks()...)
	bricks = append(bricks, newPBSInventoryBricks()...)
	return recipe{Name: "pbs-inventory", Bricks: bricks}
}

func newPBSManifestBricks(rootOverride string) []collectionBrick {
	resolveRoot := func(state *collectionState) string {
		if strings.TrimSpace(rootOverride) != "" {
			return rootOverride
		}
		return state.collector.pbsConfigPath()
	}

	return []collectionBrick{
		{
			ID:          brickPBSManifestDatastore,
			Description: "Collect PBS datastore manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestDatastore(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestS3,
			Description: "Collect PBS S3 manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestS3(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestNode,
			Description: "Collect PBS node manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestNode(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestACMEAccounts,
			Description: "Collect PBS ACME account manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestACMEAccounts(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestACMEPlugins,
			Description: "Collect PBS ACME plugin manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestACMEPlugins(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestMetricServers,
			Description: "Collect PBS metric server manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestMetricServers(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestTrafficControl,
			Description: "Collect PBS traffic control manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestTrafficControl(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestNotifications,
			Description: "Collect PBS notification manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestNotifications(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestNotificationsPriv,
			Description: "Collect PBS notification secret manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestNotificationsPriv(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestAccess,
			Description: "Collect PBS access manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestAccess(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestRemote,
			Description: "Collect PBS remote manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestRemote(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestSyncJobs,
			Description: "Collect PBS sync-job manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestSyncJobs(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestVerificationJobs,
			Description: "Collect PBS verification-job manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestVerificationJobs(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestTape,
			Description: "Collect PBS tape manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestTape(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestNetwork,
			Description: "Collect PBS network manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestNetwork(ctx, resolveRoot(state))
			},
		},
		{
			ID:          brickPBSManifestPrune,
			Description: "Collect PBS prune manifest entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSManifestPrune(ctx, resolveRoot(state))
			},
		},
	}
}

func newPBSRuntimeBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSRuntimeCore,
			Description: "Collect core PBS runtime information",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSCoreRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNode,
			Description: "Collect PBS node runtime information",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNodeRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeDatastoreList,
			Description: "Collect PBS datastore list",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSDatastoreListRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeDatastoreStatus,
			Description: "Collect PBS datastore status details",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSDatastoreStatusRuntime(ctx, commandsDir, state.pbs.datastores)
			},
		},
		{
			ID:          brickPBSRuntimeACMEAccountsList,
			Description: "Collect the PBS ACME account list",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				ids, err := state.collector.collectPBSAcmeAccountsListRuntime(ctx, commandsDir)
				if err != nil {
					return err
				}
				state.pbs.acmeAccountNames = ids
				return nil
			},
		},
		{
			ID:          brickPBSRuntimeACMEAccountInfo,
			Description: "Collect PBS ACME account details",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAcmeAccountInfoRuntime(ctx, commandsDir, state.pbs.acmeAccountNames)
			},
		},
		{
			ID:          brickPBSRuntimeACMEPluginsList,
			Description: "Collect the PBS ACME plugin list",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				ids, err := state.collector.collectPBSAcmePluginsListRuntime(ctx, commandsDir)
				if err != nil {
					return err
				}
				state.pbs.acmePluginIDs = ids
				return nil
			},
		},
		{
			ID:          brickPBSRuntimeACMEPluginConfig,
			Description: "Collect PBS ACME plugin configuration",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAcmePluginConfigRuntime(ctx, commandsDir, state.pbs.acmePluginIDs)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationTargets,
			Description: "Collect PBS notification targets",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationTargetsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationMatchers,
			Description: "Collect PBS notification matchers",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationMatchersRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationEndpoints,
			Description: "Collect PBS notification endpoints",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationEndpointsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationSummary,
			Description: "Write the PBS notification summary",
			Run: func(_ context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				state.collector.writePBSNotificationSummary(commandsDir)
				return nil
			},
		},
		{
			ID:          brickPBSRuntimeAccessUsers,
			Description: "Collect the PBS user list",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				ids, err := state.collector.collectPBSAccessUsersRuntime(ctx, commandsDir)
				if err != nil {
					return err
				}
				state.pbs.userIDs = ids
				return nil
			},
		},
		{
			ID:          brickPBSRuntimeAccessRealms,
			Description: "Collect PBS access realm definitions",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessRealmsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeAccessACL,
			Description: "Collect PBS ACL definitions",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessACLRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeAccessUserTokens,
			Description: "Collect PBS API token snapshots",
			Run: func(ctx context.Context, state *collectionState) error {
				if !state.collector.config.BackupUserConfigs || len(state.pbs.userIDs) == 0 {
					return nil
				}
				usersDir, err := state.collector.ensurePBSAccessControlDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessUserTokensRuntime(ctx, usersDir, state.pbs.userIDs)
			},
		},
		{
			ID:          brickPBSRuntimeAccessTokensAggregate,
			Description: "Aggregate PBS API token snapshots",
			Run: func(_ context.Context, state *collectionState) error {
				if !state.collector.config.BackupUserConfigs || len(state.pbs.userIDs) == 0 {
					return nil
				}
				usersDir, err := state.collector.ensurePBSAccessControlDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessTokensAggregateRuntime(usersDir, state.pbs.userIDs)
			},
		},
		{
			ID:          brickPBSRuntimeRemotes,
			Description: "Collect PBS remote definitions",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSRemotesRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeSyncJobs,
			Description: "Collect PBS sync jobs",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSSyncJobsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeVerificationJobs,
			Description: "Collect PBS verification jobs",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSVerificationJobsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimePruneJobs,
			Description: "Collect PBS prune jobs",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSPruneJobsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeGCJobs,
			Description: "Collect PBS garbage collection jobs",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSGCJobsRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeTapeDetect,
			Description: "Detect PBS tape support",
			Run: func(ctx context.Context, state *collectionState) error {
				if !state.collector.config.BackupTapeConfigs {
					state.pbs.tapeSupportKnown = true
					state.pbs.tapeSupported = false
					return nil
				}
				supported, err := state.collector.detectPBSTapeSupport(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return err
					}
					state.collector.logger.Debug("Skipping tape details collection: %v", err)
					state.pbs.tapeSupportKnown = true
					state.pbs.tapeSupported = false
					return nil
				}
				state.pbs.tapeSupportKnown = true
				state.pbs.tapeSupported = supported
				return nil
			},
		},
		{
			ID:          brickPBSRuntimeTapeDrives,
			Description: "Collect PBS tape drive inventory",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSTapeDrivesRuntime(ctx, commandsDir, state.pbs.tapeSupportKnown && state.pbs.tapeSupported)
			},
		},
		{
			ID:          brickPBSRuntimeTapeChangers,
			Description: "Collect PBS tape changer inventory",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSTapeChangersRuntime(ctx, commandsDir, state.pbs.tapeSupportKnown && state.pbs.tapeSupported)
			},
		},
		{
			ID:          brickPBSRuntimeTapePools,
			Description: "Collect PBS tape pool inventory",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSTapePoolsRuntime(ctx, commandsDir, state.pbs.tapeSupportKnown && state.pbs.tapeSupported)
			},
		},
		{
			ID:          brickPBSRuntimeNetwork,
			Description: "Collect PBS network runtime information",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNetworkRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeDisks,
			Description: "Collect the PBS disk inventory",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSDisksRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeCertInfo,
			Description: "Collect the PBS certificate summary",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSCertInfoRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeTrafficControl,
			Description: "Collect PBS traffic control runtime information",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSTrafficControlRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeRecentTasks,
			Description: "Collect recent PBS tasks",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSRecentTasksRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeS3Endpoints,
			Description: "Collect PBS S3 endpoints",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				ids, err := state.collector.collectPBSS3EndpointsRuntime(ctx, commandsDir)
				if err != nil {
					return err
				}
				state.pbs.s3EndpointIDs = ids
				return nil
			},
		},
		{
			ID:          brickPBSRuntimeS3EndpointBuckets,
			Description: "Collect PBS S3 endpoint bucket inventories",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSS3EndpointBucketsRuntime(ctx, commandsDir, state.pbs.s3EndpointIDs)
			},
		},
	}
}

func newPBSStorageStackBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSStorageStackDirsSnapshot,
			Description: "Collect PBS storage-stack directories",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSStorageStackDirsSnapshot(ctx)
			},
		},
		{
			ID:          brickPBSStorageStackMountUnitsSnapshot,
			Description: "Collect PBS storage-stack mount units",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSStorageStackMountUnitsSnapshot(ctx)
			},
		},
		{
			ID:          brickPBSStorageStackAutofsSnapshot,
			Description: "Collect PBS storage-stack autofs data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSStorageStackAutofsSnapshot(ctx)
			},
		},
		{
			ID:          brickPBSStorageStackReferencedFiles,
			Description: "Collect PBS storage-stack referenced files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectPBSStorageStackReferencedFilesSnapshot(ctx)
			},
		},
	}
}

func newPBSInventoryBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSInventoryInit,
			Description: "Initialize the PBS datastore inventory state",
			Run: func(ctx context.Context, state *collectionState) error {
				inventory, err := state.collector.initPBSDatastoreInventoryState(ctx, state.pbs.datastores)
				if err != nil {
					return err
				}
				state.pbs.inventory = inventory
				return nil
			},
		},
		{
			ID:          brickPBSInventoryBaseFiles,
			Description: "Populate PBS inventory base files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryBaseFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryBaseDirs,
			Description: "Populate PBS inventory base directories",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryBaseDirs(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryMountUnits,
			Description: "Populate PBS inventory mount-unit snapshots",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryMountUnits(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryReferencedFiles,
			Description: "Populate PBS inventory referenced files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryReferencedFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommands,
			Description: "Populate PBS inventory host-command snapshots",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommands(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryCommandFiles,
			Description: "Populate PBS inventory with collected PBS command files",
			Run: func(_ context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.populatePBSInventoryCommandFiles(state.ensurePBSInventoryState(), commandsDir)
			},
		},
		{
			ID:          brickPBSInventoryDatastores,
			Description: "Populate PBS datastore inventory entries",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSDatastoreInventoryEntries(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryWrite,
			Description: "Write the PBS datastore inventory report",
			Run: func(_ context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.writePBSInventoryState(state.ensurePBSInventoryState(), commandsDir)
			},
		},
	}
}

func newPBSFeatureBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSDatastoreConfigs,
			Description: "Collect PBS datastore configuration files",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if c.config.BackupDatastoreConfigs {
					c.logger.Debug("Collecting datastore configuration files and namespaces")
					if err := c.collectDatastoreConfigs(ctx, state.pbs.datastores); err != nil {
						c.logger.Warning("Failed to collect datastore configs: %v", err)
					} else {
						c.logger.Debug("Datastore configuration collection completed")
					}
				} else {
					c.logger.Skip("PBS datastore configuration backup disabled.")
				}
				return nil
			},
		},
		{
			ID:          brickPBSPXAR,
			Description: "Collect PBS PXAR metadata",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if c.config.BackupPxarFiles {
					c.logger.Debug("Collecting PXAR metadata for datastores")
					if err := c.collectPBSPxarMetadata(ctx, state.pbs.datastores); err != nil {
						c.logger.Warning("Failed to collect PBS PXAR metadata: %v", err)
					} else {
						c.logger.Debug("PXAR metadata collection completed")
					}
				} else {
					c.logger.Skip("PBS PXAR metadata collection disabled.")
				}
				return nil
			},
		},
	}
}

func newPBSFinalizeBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSFinalizeSummary,
			Description: "Finalize PBS collection state",
			Run: func(_ context.Context, state *collectionState) error {
				c := state.collector
				c.logger.Info("PBS collection summary:")
				c.logger.Info("  Files collected: %d", c.stats.FilesProcessed)
				c.logger.Info("  Files not found: %d", c.stats.FilesNotFound)
				if c.stats.FilesFailed > 0 {
					c.logger.Warning("  Files failed: %d", c.stats.FilesFailed)
				}
				c.logger.Debug("  Files skipped: %d", c.stats.FilesSkipped)
				c.logger.Debug("  Bytes collected: %d", c.stats.BytesCollected)
				return nil
			},
		},
	}
}

func newSystemRecipe() recipe {
	return recipe{
		Name: "system",
		Bricks: []collectionBrick{
			{ID: brickSystemNetworkStatic, Description: "Collect static network configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemNetworkStatic(ctx)
			}},
			{ID: brickSystemIdentityStatic, Description: "Collect static identity files", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemIdentityStatic(ctx)
			}},
			{ID: brickSystemAptStatic, Description: "Collect static APT configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemAptStatic(ctx)
			}},
			{ID: brickSystemCronStatic, Description: "Collect static cron configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemCronStatic(ctx)
			}},
			{ID: brickSystemServicesStatic, Description: "Collect static service configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemServicesStatic(ctx)
			}},
			{ID: brickSystemLoggingStatic, Description: "Collect static logging configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemLoggingStatic(ctx)
			}},
			{ID: brickSystemSSLStatic, Description: "Collect static SSL configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemSSLStatic(ctx)
			}},
			{ID: brickSystemSysctlStatic, Description: "Collect static sysctl configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemSysctlStatic(ctx)
			}},
			{ID: brickSystemKernelModulesStatic, Description: "Collect static kernel module configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemKernelModuleStatic(ctx)
			}},
			{ID: brickSystemZFSStatic, Description: "Collect static ZFS configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemZFSStatic(ctx)
			}},
			{ID: brickSystemFirewallStatic, Description: "Collect static firewall configuration", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemFirewallStatic(ctx)
			}},
			{ID: brickSystemRuntimeLeases, Description: "Collect runtime lease snapshots", Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectSystemRuntimeLeases(ctx)
			}},
			{ID: brickSystemCoreRuntime, Description: "Collect core system runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemCoreRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemNetworkRuntime, Description: "Collect network runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemNetworkRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemStorageRuntime, Description: "Collect storage runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemStorageRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemComputeRuntime, Description: "Collect compute runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemComputeRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemServicesRuntime, Description: "Collect service runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemServicesRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemPackagesRuntime, Description: "Collect package runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemPackagesRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemFirewallRuntime, Description: "Collect firewall runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemFirewallRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemKernelModulesRuntime, Description: "Collect kernel module runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemKernelModulesRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemSysctlRuntime, Description: "Collect sysctl runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemSysctlRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemZFSRuntime, Description: "Collect ZFS runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemZFSRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemLVMRuntime, Description: "Collect LVM runtime information", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectSystemLVMRuntime(ctx, commandsDir)
			}},
			{ID: brickSystemNetworkReport, Description: "Finalize derived system reports", Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				if err := state.collector.finalizeSystemRuntimeReports(ctx, commandsDir); err != nil {
					state.collector.logger.Debug("Network report generation failed: %v", err)
				}
				return nil
			}},
			{
				ID:          brickSystemKernel,
				Description: "Collect kernel information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting kernel information (uname/modules)")
					if err := c.collectKernelInfo(ctx); err != nil {
						c.logger.Warning("Failed to collect kernel info: %v", err)
					} else {
						c.logger.Debug("Kernel information collected successfully")
					}
					return nil
				},
			},
			{
				ID:          brickSystemHardware,
				Description: "Collect hardware information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting hardware inventory (CPU/memory/devices)")
					if err := c.collectHardwareInfo(ctx); err != nil {
						c.logger.Warning("Failed to collect hardware info: %v", err)
					} else {
						c.logger.Debug("Hardware inventory collected successfully")
					}
					return nil
				},
			},
			{
				ID:          brickSystemCriticalFiles,
				Description: "Collect critical system files",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupCriticalFiles {
						c.logger.Debug("Collecting critical files specified in configuration")
						if err := c.collectCriticalFiles(ctx); err != nil {
							c.logger.Warning("Failed to collect critical files: %v", err)
						} else {
							c.logger.Debug("Critical files collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemConfigFile,
				Description: "Collect backup configuration file",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupConfigFile {
						c.logger.Debug("Collecting backup configuration file")
						if err := c.collectConfigFile(ctx); err != nil {
							c.logger.Warning("Failed to collect backup configuration file: %v", err)
						} else {
							c.logger.Debug("Backup configuration file collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemCustomPaths,
				Description: "Collect custom backup paths",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if len(c.config.CustomBackupPaths) > 0 {
						c.logger.Debug("Collecting custom paths: %v", c.config.CustomBackupPaths)
						if err := c.collectCustomPaths(ctx); err != nil {
							c.logger.Warning("Failed to collect custom paths: %v", err)
						} else {
							c.logger.Debug("Custom paths collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemScriptDirs,
				Description: "Collect script directories",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupScriptDir {
						c.logger.Debug("Collecting script directories (/usr/local/bin,/usr/local/sbin)")
						if err := c.collectScriptDirectories(ctx); err != nil {
							c.logger.Warning("Failed to collect script directories: %v", err)
						} else {
							c.logger.Debug("Script directories collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemScriptRepo,
				Description: "Collect script repository",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupScriptRepository {
						c.logger.Debug("Collecting script repository from %s", c.config.ScriptRepositoryPath)
						if err := c.collectScriptRepository(ctx); err != nil {
							c.logger.Warning("Failed to collect script repository: %v", err)
						} else {
							c.logger.Debug("Script repository collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemSSHKeys,
				Description: "Collect SSH keys",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupSSHKeys {
						c.logger.Debug("Collecting SSH keys for root and users")
						if err := c.collectSSHKeys(ctx); err != nil {
							c.logger.Warning("Failed to collect SSH keys: %v", err)
						} else {
							c.logger.Debug("SSH keys collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemRootHome,
				Description: "Collect root home directory",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupRootHome {
						c.logger.Debug("Collecting /root home directory")
						if err := c.collectRootHome(ctx); err != nil {
							c.logger.Warning("Failed to collect root home files: %v", err)
						} else {
							c.logger.Debug("Root home directory collected successfully")
						}
					}
					return nil
				},
			},
			{
				ID:          brickSystemUserHomes,
				Description: "Collect user home directories",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupUserHomes {
						c.logger.Debug("Collecting user home directories under /home")
						if err := c.collectUserHomes(ctx); err != nil {
							c.logger.Warning("Failed to collect user home directories: %v", err)
						} else {
							c.logger.Debug("User home directories collected successfully")
						}
					}
					return nil
				},
			},
		},
	}
}

func (p pveContext) runtimeNodes() []string {
	if p.runtimeInfo == nil {
		return nil
	}
	return p.runtimeInfo.Nodes
}

func (p pveContext) runtimeStorages() []pveStorageEntry {
	if p.runtimeInfo == nil {
		return nil
	}
	return p.runtimeInfo.Storages
}

func (p *pveContext) ensureStorageScanResults() map[string]*pveStorageScanResult {
	if p.storageScanResults == nil {
		p.storageScanResults = make(map[string]*pveStorageScanResult)
	}
	return p.storageScanResults
}

func (p pveContext) storageResult(storage pveStorageEntry) *pveStorageScanResult {
	if p.storageScanResults == nil {
		return nil
	}
	return p.storageScanResults[storage.pathKey()]
}
