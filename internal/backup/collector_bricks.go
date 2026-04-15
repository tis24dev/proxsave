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

	brickPBSValidate                            BrickID = "pbs_validate"
	brickPBSConfigDirectoryCopy                 BrickID = "pbs_config_directory_copy"
	brickPBSManifestInit                        BrickID = "pbs_manifest_init"
	brickPBSDatastoreDiscovery                  BrickID = "pbs_datastore_discovery"
	brickPBSManifestDatastore                   BrickID = "pbs_manifest_datastore"
	brickPBSManifestS3                          BrickID = "pbs_manifest_s3"
	brickPBSManifestNode                        BrickID = "pbs_manifest_node"
	brickPBSManifestACMEAccounts                BrickID = "pbs_manifest_acme_accounts"
	brickPBSManifestACMEPlugins                 BrickID = "pbs_manifest_acme_plugins"
	brickPBSManifestMetricServers               BrickID = "pbs_manifest_metric_servers"
	brickPBSManifestTrafficControl              BrickID = "pbs_manifest_traffic_control"
	brickPBSManifestNotifications               BrickID = "pbs_manifest_notifications"
	brickPBSManifestNotificationsPriv           BrickID = "pbs_manifest_notifications_priv"
	brickPBSManifestUserCfg                     BrickID = "pbs_manifest_user_cfg"
	brickPBSManifestACLCfg                      BrickID = "pbs_manifest_acl_cfg"
	brickPBSManifestDomainsCfg                  BrickID = "pbs_manifest_domains_cfg"
	brickPBSManifestRemote                      BrickID = "pbs_manifest_remote"
	brickPBSManifestSyncJobs                    BrickID = "pbs_manifest_sync_jobs"
	brickPBSManifestVerificationJobs            BrickID = "pbs_manifest_verification_jobs"
	brickPBSManifestTapeCfg                     BrickID = "pbs_manifest_tape_cfg"
	brickPBSManifestTapeJobs                    BrickID = "pbs_manifest_tape_jobs"
	brickPBSManifestMediaPools                  BrickID = "pbs_manifest_media_pools"
	brickPBSManifestTapeEncryptionKeys          BrickID = "pbs_manifest_tape_encryption_keys"
	brickPBSManifestNetwork                     BrickID = "pbs_manifest_network"
	brickPBSManifestPrune                       BrickID = "pbs_manifest_prune"
	brickPBSRuntimeCore                         BrickID = "pbs_runtime_core"
	brickPBSRuntimeNode                         BrickID = "pbs_runtime_node"
	brickPBSRuntimeDatastoreList                BrickID = "pbs_runtime_datastore_list"
	brickPBSRuntimeDatastoreStatus              BrickID = "pbs_runtime_datastore_status"
	brickPBSRuntimeACMEAccountsList             BrickID = "pbs_runtime_acme_accounts_list"
	brickPBSRuntimeACMEAccountInfo              BrickID = "pbs_runtime_acme_account_info"
	brickPBSRuntimeACMEPluginsList              BrickID = "pbs_runtime_acme_plugins_list"
	brickPBSRuntimeACMEPluginConfig             BrickID = "pbs_runtime_acme_plugin_config"
	brickPBSRuntimeNotificationTargets          BrickID = "pbs_runtime_notification_targets"
	brickPBSRuntimeNotificationMatchers         BrickID = "pbs_runtime_notification_matchers"
	brickPBSRuntimeNotificationEndpointSMTP     BrickID = "pbs_runtime_notification_endpoints_smtp"
	brickPBSRuntimeNotificationEndpointSendmail BrickID = "pbs_runtime_notification_endpoints_sendmail"
	brickPBSRuntimeNotificationEndpointGotify   BrickID = "pbs_runtime_notification_endpoints_gotify"
	brickPBSRuntimeNotificationEndpointWebhook  BrickID = "pbs_runtime_notification_endpoints_webhook"
	brickPBSRuntimeNotificationSummary          BrickID = "pbs_runtime_notification_summary"
	brickPBSRuntimeAccessUsers                  BrickID = "pbs_runtime_access_users"
	brickPBSRuntimeAccessRealmsLDAP             BrickID = "pbs_runtime_access_realms_ldap"
	brickPBSRuntimeAccessRealmsAD               BrickID = "pbs_runtime_access_realms_ad"
	brickPBSRuntimeAccessRealmsOpenID           BrickID = "pbs_runtime_access_realms_openid"
	brickPBSRuntimeAccessACL                    BrickID = "pbs_runtime_access_acl"
	brickPBSRuntimeAccessUserTokens             BrickID = "pbs_runtime_access_user_tokens"
	brickPBSRuntimeAccessTokensAggregate        BrickID = "pbs_runtime_access_tokens_aggregate"
	brickPBSRuntimeRemotes                      BrickID = "pbs_runtime_remotes"
	brickPBSRuntimeSyncJobs                     BrickID = "pbs_runtime_sync_jobs"
	brickPBSRuntimeVerificationJobs             BrickID = "pbs_runtime_verification_jobs"
	brickPBSRuntimePruneJobs                    BrickID = "pbs_runtime_prune_jobs"
	brickPBSRuntimeGCJobs                       BrickID = "pbs_runtime_gc_jobs"
	brickPBSRuntimeTapeDetect                   BrickID = "pbs_runtime_tape_detect"
	brickPBSRuntimeTapeDrives                   BrickID = "pbs_runtime_tape_drives"
	brickPBSRuntimeTapeChangers                 BrickID = "pbs_runtime_tape_changers"
	brickPBSRuntimeTapePools                    BrickID = "pbs_runtime_tape_pools"
	brickPBSRuntimeNetwork                      BrickID = "pbs_runtime_network"
	brickPBSRuntimeDisks                        BrickID = "pbs_runtime_disks"
	brickPBSRuntimeCertInfo                     BrickID = "pbs_runtime_cert_info"
	brickPBSRuntimeTrafficControl               BrickID = "pbs_runtime_traffic_control"
	brickPBSRuntimeRecentTasks                  BrickID = "pbs_runtime_recent_tasks"
	brickPBSRuntimeS3Endpoints                  BrickID = "pbs_runtime_s3_endpoints"
	brickPBSRuntimeS3EndpointBuckets            BrickID = "pbs_runtime_s3_endpoint_buckets"
	brickCommonFilesystemFstab                  BrickID = "common_filesystem_fstab"
	brickCommonStorageStackCrypttab             BrickID = "common_storage_stack_crypttab"
	brickCommonStorageStackISCSISnapshot        BrickID = "common_storage_stack_iscsi"
	brickCommonStorageStackMultipathSnapshot    BrickID = "common_storage_stack_multipath"
	brickCommonStorageStackMDADMSnapshot        BrickID = "common_storage_stack_mdadm"
	brickCommonStorageStackLVMSnapshot          BrickID = "common_storage_stack_lvm"
	brickCommonStorageStackMountUnitsSnapshot   BrickID = "common_storage_stack_mount_units"
	brickCommonStorageStackAutofsSnapshot       BrickID = "common_storage_stack_autofs"
	brickCommonStorageStackReferencedFiles      BrickID = "common_storage_stack_referenced_files"
	brickPBSInventoryInit                       BrickID = "pbs_inventory_init"
	brickPBSInventoryMountFiles                 BrickID = "pbs_inventory_mount_files"
	brickPBSInventoryOSFiles                    BrickID = "pbs_inventory_os_files"
	brickPBSInventoryMultipathFiles             BrickID = "pbs_inventory_multipath_files"
	brickPBSInventoryISCSIFiles                 BrickID = "pbs_inventory_iscsi_files"
	brickPBSInventoryAutofsFiles                BrickID = "pbs_inventory_autofs_files"
	brickPBSInventoryZFSFiles                   BrickID = "pbs_inventory_zfs_files"
	brickPBSInventoryLVMDirs                    BrickID = "pbs_inventory_lvm_dirs"
	brickPBSInventorySystemdMountUnits          BrickID = "pbs_inventory_systemd_mount_units"
	brickPBSInventoryReferencedFiles            BrickID = "pbs_inventory_referenced_files"
	brickPBSInventoryHostCommandsCore           BrickID = "pbs_inventory_host_commands_core"
	brickPBSInventoryHostCommandsDMSetup        BrickID = "pbs_inventory_host_commands_dmsetup"
	brickPBSInventoryHostCommandsLVM            BrickID = "pbs_inventory_host_commands_lvm"
	brickPBSInventoryHostCommandsMDADM          BrickID = "pbs_inventory_host_commands_mdadm"
	brickPBSInventoryHostCommandsMultipath      BrickID = "pbs_inventory_host_commands_multipath"
	brickPBSInventoryHostCommandsISCSI          BrickID = "pbs_inventory_host_commands_iscsi"
	brickPBSInventoryHostCommandsZFS            BrickID = "pbs_inventory_host_commands_zfs"
	brickPBSInventoryCommandFiles               BrickID = "pbs_inventory_command_files"
	brickPBSInventoryDatastores                 BrickID = "pbs_inventory_datastores"
	brickPBSInventoryWrite                      BrickID = "pbs_inventory_write"
	brickPBSDatastoreCLIConfigs                 BrickID = "pbs_datastore_cli_configs"
	brickPBSDatastoreNamespaces                 BrickID = "pbs_datastore_namespaces"
	brickPBSPXARPrepare                         BrickID = "pbs_pxar_prepare"
	brickPBSPXARMetadata                        BrickID = "pbs_pxar_metadata"
	brickPBSPXARSubdirReports                   BrickID = "pbs_pxar_subdir_reports"
	brickPBSPXARVMLists                         BrickID = "pbs_pxar_vm_lists"
	brickPBSPXARCTLists                         BrickID = "pbs_pxar_ct_lists"
	brickPBSFinalizeSummary                     BrickID = "pbs_finalize_summary"

	brickSystemNetworkStatic            BrickID = "system_network_static"
	brickSystemIdentityStatic           BrickID = "system_identity_static"
	brickSystemAptStatic                BrickID = "system_apt_static"
	brickSystemCronStatic               BrickID = "system_cron_static"
	brickSystemServicesStatic           BrickID = "system_services_static"
	brickSystemLoggingStatic            BrickID = "system_logging_static"
	brickSystemSSLStatic                BrickID = "system_ssl_static"
	brickSystemSysctlStatic             BrickID = "system_sysctl_static"
	brickSystemKernelModulesStatic      BrickID = "system_kernel_modules_static"
	brickSystemZFSStatic                BrickID = "system_zfs_static"
	brickSystemFirewallStatic           BrickID = "system_firewall_static"
	brickSystemRuntimeLeases            BrickID = "system_runtime_leases"
	brickSystemCoreRuntime              BrickID = "system_core_runtime"
	brickSystemNetworkRuntimeAddr       BrickID = "system_network_runtime_addr"
	brickSystemNetworkRuntimeRules      BrickID = "system_network_runtime_rules"
	brickSystemNetworkRuntimeRoutes     BrickID = "system_network_runtime_routes"
	brickSystemNetworkRuntimeLinks      BrickID = "system_network_runtime_links"
	brickSystemNetworkRuntimeNeighbors  BrickID = "system_network_runtime_neighbors"
	brickSystemNetworkRuntimeBridges    BrickID = "system_network_runtime_bridges"
	brickSystemNetworkRuntimeInventory  BrickID = "system_network_runtime_inventory"
	brickSystemNetworkRuntimeBonding    BrickID = "system_network_runtime_bonding"
	brickSystemNetworkRuntimeDNS        BrickID = "system_network_runtime_dns"
	brickSystemStorageRuntime           BrickID = "system_storage_runtime"
	brickSystemComputeRuntime           BrickID = "system_compute_runtime"
	brickSystemServicesRuntime          BrickID = "system_services_runtime"
	brickSystemPackagesRuntime          BrickID = "system_packages_runtime"
	brickSystemFirewallRuntimeIPTables  BrickID = "system_firewall_runtime_iptables"
	brickSystemFirewallRuntimeIP6Tables BrickID = "system_firewall_runtime_ip6tables"
	brickSystemFirewallRuntimeNFTables  BrickID = "system_firewall_runtime_nftables"
	brickSystemFirewallRuntimeUFW       BrickID = "system_firewall_runtime_ufw"
	brickSystemFirewallRuntimeFirewalld BrickID = "system_firewall_runtime_firewalld"
	brickSystemKernelModulesRuntime     BrickID = "system_kernel_modules_runtime"
	brickSystemSysctlRuntime            BrickID = "system_sysctl_runtime"
	brickSystemZFSRuntime               BrickID = "system_zfs_runtime"
	brickSystemLVMRuntime               BrickID = "system_lvm_runtime"
	brickSystemNetworkReport            BrickID = "system_network_report"
	brickSystemKernel                   BrickID = "system_kernel"
	brickSystemHardware                 BrickID = "system_hardware"
	brickSystemCriticalFiles            BrickID = "system_critical_files"
	brickSystemConfigFile               BrickID = "system_config_file"
	brickSystemCustomPaths              BrickID = "system_custom_paths"
	brickSystemScriptDirs               BrickID = "system_script_dirs"
	brickSystemScriptRepo               BrickID = "system_script_repo"
	brickSystemSSHKeys                  BrickID = "system_ssh_keys"
	brickSystemRootHome                 BrickID = "system_root_home"
	brickSystemUserHomes                BrickID = "system_user_homes"
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
	datastoreConfig  *pbsDatastoreConfigState
	pxar             *pbsPxarState
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

func (s *collectionState) ensurePBSDatastoreConfigState() (*pbsDatastoreConfigState, error) {
	if s.pbs.datastoreConfig != nil {
		return s.pbs.datastoreConfig, nil
	}
	cfgState, err := s.collector.preparePBSDatastoreConfigState(s.pbs.datastores)
	if err != nil {
		return nil, err
	}
	s.pbs.datastoreConfig = cfgState
	return cfgState, nil
}

func (s *collectionState) ensurePBSPXARState(ctx context.Context) (*pbsPxarState, error) {
	if s.pbs.pxar != nil {
		return s.pbs.pxar, nil
	}
	pxarState, err := s.collector.preparePBSPXARState(ctx, s.pbs.datastores)
	if err != nil {
		return nil, err
	}
	s.pbs.pxar = pxarState
	return pxarState, nil
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
	bricks = append(bricks, newPBSManifestBricks()...)
	bricks = append(bricks, newPBSRuntimeBricks()...)
	bricks = append(bricks, newPBSInventoryBricks()...)
	bricks = append(bricks, newPBSFeatureBricks()...)
	bricks = append(bricks, newPBSFinalizeBricks()...)
	return recipe{Name: "pbs", Bricks: bricks}
}

func newPBSCommandsRecipe() recipe {
	return recipe{Name: "pbs-commands", Bricks: newPBSRuntimeBricks()}
}

func newPBSDatastoreInventoryRecipe() recipe {
	return recipe{Name: "pbs-inventory", Bricks: newPBSInventoryBricks()}
}

func newPBSDatastoreConfigRecipe() recipe {
	return recipe{Name: "pbs-datastore-config", Bricks: newPBSDatastoreConfigBricks()}
}

func newPBSPXARRecipe() recipe {
	return recipe{Name: "pbs-pxar", Bricks: newPBSPXARBricks()}
}

func newPBSUserConfigRecipe() recipe {
	return recipe{
		Name: "pbs-user-config",
		Bricks: []collectionBrick{
			{
				ID:          BrickID("pbs_access_load_user_ids_from_command_file"),
				Description: "Load PBS user IDs from collected command snapshots",
				Run: func(_ context.Context, state *collectionState) error {
					userIDs, err := state.collector.loadPBSUserIDsFromCommandFile(state.collector.proxsaveCommandsDir("pbs"))
					if err != nil {
						if errors.Is(err, os.ErrNotExist) {
							state.collector.logger.Debug("User list not available for token export: %v", err)
							state.pbs.userIDs = nil
							return nil
						}
						state.collector.logger.Debug("Failed to parse user list for token export: %v", err)
						state.pbs.userIDs = nil
						return nil
					}
					state.pbs.userIDs = userIDs
					return nil
				},
			},
			{
				ID:          brickPBSRuntimeAccessUserTokens,
				Description: "Collect PBS API token snapshots",
				Run: func(ctx context.Context, state *collectionState) error {
					if len(state.pbs.userIDs) == 0 {
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
					if len(state.pbs.userIDs) == 0 {
						return nil
					}
					usersDir, err := state.collector.ensurePBSAccessControlDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSAccessTokensAggregateRuntime(usersDir, state.pbs.userIDs)
				},
			},
		},
	}
}

func newPBSManifestBricks() []collectionBrick {
	return []collectionBrick{
		{ID: brickPBSManifestDatastore, Description: "Collect PBS datastore manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestDatastore(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestS3, Description: "Collect PBS S3 manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestS3(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestNode, Description: "Collect PBS node manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestNode(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestACMEAccounts, Description: "Collect PBS ACME account manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestACMEAccounts(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestACMEPlugins, Description: "Collect PBS ACME plugin manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestACMEPlugins(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestMetricServers, Description: "Collect PBS metric server manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestMetricServers(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestTrafficControl, Description: "Collect PBS traffic control manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestTrafficControl(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestNotifications, Description: "Collect PBS notification manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestNotifications(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestNotificationsPriv, Description: "Collect PBS notification secret manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestNotificationsPriv(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestUserCfg, Description: "Collect PBS user manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestUserCfg(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestACLCfg, Description: "Collect PBS ACL manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestACLCfg(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestDomainsCfg, Description: "Collect PBS auth realm manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestDomainsCfg(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestRemote, Description: "Collect PBS remote manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestRemote(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestSyncJobs, Description: "Collect PBS sync-job manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestSyncJobs(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestVerificationJobs, Description: "Collect PBS verification-job manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestVerificationJobs(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestTapeCfg, Description: "Collect PBS tape configuration manifest entry", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestTapeCfg(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestTapeJobs, Description: "Collect PBS tape job manifest entry", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestTapeJobs(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestMediaPools, Description: "Collect PBS media pool manifest entry", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestMediaPools(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestTapeEncryptionKeys, Description: "Collect PBS tape encryption keys manifest entry", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestTapeEncryptionKeys(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestNetwork, Description: "Collect PBS network manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestNetwork(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestPrune, Description: "Collect PBS prune manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestPrune(ctx, state.collector.pbsConfigPath())
		}},
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
			ID:          brickPBSRuntimeNotificationEndpointSMTP,
			Description: "Collect PBS SMTP notification endpoints",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationEndpointSMTPRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationEndpointSendmail,
			Description: "Collect PBS sendmail notification endpoints",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationEndpointSendmailRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationEndpointGotify,
			Description: "Collect PBS gotify notification endpoints",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationEndpointGotifyRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeNotificationEndpointWebhook,
			Description: "Collect PBS webhook notification endpoints",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSNotificationEndpointWebhookRuntime(ctx, commandsDir)
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
			ID:          brickPBSRuntimeAccessRealmsLDAP,
			Description: "Collect PBS LDAP realm definitions",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessRealmLDAPRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeAccessRealmsAD,
			Description: "Collect PBS Active Directory realm definitions",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessRealmADRuntime(ctx, commandsDir)
			},
		},
		{
			ID:          brickPBSRuntimeAccessRealmsOpenID,
			Description: "Collect PBS OpenID realm definitions",
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensurePBSCommandsDir()
				if err != nil {
					return err
				}
				return state.collector.collectPBSAccessRealmOpenIDRuntime(ctx, commandsDir)
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

func newCommonFilesystemBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickCommonFilesystemFstab,
			Description: "Collect the common filesystem table",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonFilesystemFstab(ctx)
			},
		},
	}
}

func newCommonStorageStackBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickCommonStorageStackCrypttab,
			Description: "Collect common storage-stack crypttab data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackCrypttab(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackISCSISnapshot,
			Description: "Collect common iSCSI storage-stack data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackISCSISnapshot(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackMultipathSnapshot,
			Description: "Collect common multipath storage-stack data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackMultipathSnapshot(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackMDADMSnapshot,
			Description: "Collect common mdadm storage-stack data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackMDADMSnapshot(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackLVMSnapshot,
			Description: "Collect common LVM storage-stack data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackLVMSnapshot(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackMountUnitsSnapshot,
			Description: "Collect common storage-stack mount units",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackMountUnitsSnapshot(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackAutofsSnapshot,
			Description: "Collect common storage-stack autofs data",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackAutofsSnapshot(ctx)
			},
		},
		{
			ID:          brickCommonStorageStackReferencedFiles,
			Description: "Collect common storage-stack referenced files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.collectCommonStorageStackReferencedFiles(ctx)
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
			ID:          brickPBSInventoryMountFiles,
			Description: "Populate PBS inventory mount files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryMountFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryOSFiles,
			Description: "Populate PBS inventory OS files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryOSFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryMultipathFiles,
			Description: "Populate PBS inventory multipath files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryMultipathFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryISCSIFiles,
			Description: "Populate PBS inventory iSCSI files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryISCSIFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryAutofsFiles,
			Description: "Populate PBS inventory autofs files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryAutofsFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryZFSFiles,
			Description: "Populate PBS inventory ZFS files",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryZFSFiles(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryLVMDirs,
			Description: "Populate PBS inventory LVM directories",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryLVMDirs(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventorySystemdMountUnits,
			Description: "Populate PBS inventory systemd mount units",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventorySystemdMountUnits(ctx, state.ensurePBSInventoryState())
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
			ID:          brickPBSInventoryHostCommandsCore,
			Description: "Populate PBS inventory core host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsCore(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommandsDMSetup,
			Description: "Populate PBS inventory dmsetup host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsDMSetup(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommandsLVM,
			Description: "Populate PBS inventory LVM host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsLVM(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommandsMDADM,
			Description: "Populate PBS inventory mdadm host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsMDADM(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommandsMultipath,
			Description: "Populate PBS inventory multipath host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsMultipath(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommandsISCSI,
			Description: "Populate PBS inventory iSCSI host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsISCSI(ctx, state.ensurePBSInventoryState())
			},
		},
		{
			ID:          brickPBSInventoryHostCommandsZFS,
			Description: "Populate PBS inventory ZFS host commands",
			Run: func(ctx context.Context, state *collectionState) error {
				return state.collector.populatePBSInventoryHostCommandsZFS(ctx, state.ensurePBSInventoryState())
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

func newPBSDatastoreConfigBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSDatastoreCLIConfigs,
			Description: "Collect PBS datastore CLI configuration files",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if !c.config.BackupDatastoreConfigs {
					c.logger.Skip("PBS datastore configuration backup disabled.")
					return nil
				}
				cfgState, err := state.ensurePBSDatastoreConfigState()
				if err != nil {
					c.logger.Warning("Failed to prepare datastore config state: %v", err)
					return nil
				}
				if err := c.collectPBSDatastoreCLIConfigs(ctx, cfgState); err != nil {
					c.logger.Warning("Failed to collect datastore CLI configs: %v", err)
				}
				return nil
			},
		},
		{
			ID:          brickPBSDatastoreNamespaces,
			Description: "Collect PBS datastore namespace inventories",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if !c.config.BackupDatastoreConfigs {
					return nil
				}
				cfgState, err := state.ensurePBSDatastoreConfigState()
				if err != nil {
					c.logger.Warning("Failed to prepare datastore config state: %v", err)
					return nil
				}
				if err := c.collectPBSDatastoreNamespaces(ctx, cfgState); err != nil {
					c.logger.Warning("Failed to collect datastore namespaces: %v", err)
				}
				return nil
			},
		},
	}
}

func newPBSPXARBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPBSPXARPrepare,
			Description: "Prepare PBS PXAR state",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if !c.config.BackupPxarFiles {
					c.logger.Skip("PBS PXAR metadata collection disabled.")
					return nil
				}
				pxarState, err := state.ensurePBSPXARState(ctx)
				if err != nil {
					c.logger.Warning("Failed to prepare PBS PXAR state: %v", err)
					return nil
				}
				state.pbs.pxar = pxarState
				return nil
			},
		},
		{
			ID:          brickPBSPXARMetadata,
			Description: "Collect PBS PXAR metadata snapshots",
			Run: func(ctx context.Context, state *collectionState) error {
				if !state.collector.config.BackupPxarFiles {
					return nil
				}
				pxarState, err := state.ensurePBSPXARState(ctx)
				if err != nil {
					state.collector.logger.Warning("Failed to prepare PBS PXAR state: %v", err)
					return nil
				}
				if err := state.collector.collectPBSPXARMetadataStep(ctx, pxarState); err != nil {
					state.collector.logger.Warning("Failed to collect PBS PXAR metadata: %v", err)
				}
				return nil
			},
		},
		{
			ID:          brickPBSPXARSubdirReports,
			Description: "Collect PBS PXAR subdirectory reports",
			Run: func(ctx context.Context, state *collectionState) error {
				if !state.collector.config.BackupPxarFiles {
					return nil
				}
				pxarState, err := state.ensurePBSPXARState(ctx)
				if err != nil {
					state.collector.logger.Warning("Failed to prepare PBS PXAR state: %v", err)
					return nil
				}
				if err := state.collector.collectPBSPXARSubdirReportsStep(ctx, pxarState); err != nil {
					state.collector.logger.Warning("Failed to collect PBS PXAR subdir reports: %v", err)
				}
				return nil
			},
		},
		{
			ID:          brickPBSPXARVMLists,
			Description: "Collect PBS PXAR VM reports",
			Run: func(ctx context.Context, state *collectionState) error {
				if !state.collector.config.BackupPxarFiles {
					return nil
				}
				pxarState, err := state.ensurePBSPXARState(ctx)
				if err != nil {
					state.collector.logger.Warning("Failed to prepare PBS PXAR state: %v", err)
					return nil
				}
				if err := state.collector.collectPBSPXARVMListsStep(ctx, pxarState); err != nil {
					state.collector.logger.Warning("Failed to collect PBS PXAR VM lists: %v", err)
				}
				return nil
			},
		},
		{
			ID:          brickPBSPXARCTLists,
			Description: "Collect PBS PXAR CT reports",
			Run: func(ctx context.Context, state *collectionState) error {
				if !state.collector.config.BackupPxarFiles {
					return nil
				}
				pxarState, err := state.ensurePBSPXARState(ctx)
				if err != nil {
					state.collector.logger.Warning("Failed to prepare PBS PXAR state: %v", err)
					return nil
				}
				if err := state.collector.collectPBSPXARCTListsStep(ctx, pxarState); err != nil {
					state.collector.logger.Warning("Failed to collect PBS PXAR CT lists: %v", err)
				}
				return nil
			},
		},
	}
}

func newPBSFeatureBricks() []collectionBrick {
	bricks := append([]collectionBrick{}, newPBSDatastoreConfigBricks()...)
	bricks = append(bricks, newPBSPXARBricks()...)
	return bricks
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
	systemCommandsBrick := func(id BrickID, description string, run func(*Collector, context.Context, string) error) collectionBrick {
		return collectionBrick{
			ID:          id,
			Description: description,
			Run: func(ctx context.Context, state *collectionState) error {
				commandsDir, err := state.ensureSystemCommandsDir()
				if err != nil {
					return err
				}
				return run(state.collector, ctx, commandsDir)
			},
		}
	}

	bricks := []collectionBrick{
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
	}
	bricks = append(bricks, newCommonFilesystemBricks()...)
	bricks = append(bricks, newCommonStorageStackBricks()...)
	bricks = append(bricks, []collectionBrick{
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
		systemCommandsBrick(brickSystemNetworkRuntimeAddr, "Collect network address runtime information", (*Collector).collectSystemNetworkAddrRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeRules, "Collect network rule runtime information", (*Collector).collectSystemNetworkRulesRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeRoutes, "Collect network route runtime information", (*Collector).collectSystemNetworkRoutesRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeLinks, "Collect network link runtime information", (*Collector).collectSystemNetworkLinksRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeNeighbors, "Collect network neighbor runtime information", (*Collector).collectSystemNetworkNeighborsRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeBridges, "Collect bridge runtime information", (*Collector).collectSystemNetworkBridgesRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeInventory, "Collect network inventory runtime information", (*Collector).collectSystemNetworkInventoryRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeBonding, "Collect bonding runtime information", (*Collector).collectSystemNetworkBondingRuntime),
		systemCommandsBrick(brickSystemNetworkRuntimeDNS, "Collect DNS runtime information", (*Collector).collectSystemNetworkDNSRuntime),
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
		systemCommandsBrick(brickSystemFirewallRuntimeIPTables, "Collect iptables runtime information", (*Collector).collectSystemFirewallIPTablesRuntime),
		systemCommandsBrick(brickSystemFirewallRuntimeIP6Tables, "Collect ip6tables runtime information", (*Collector).collectSystemFirewallIP6TablesRuntime),
		systemCommandsBrick(brickSystemFirewallRuntimeNFTables, "Collect nftables runtime information", (*Collector).collectSystemFirewallNFTablesRuntime),
		systemCommandsBrick(brickSystemFirewallRuntimeUFW, "Collect UFW runtime information", (*Collector).collectSystemFirewallUFWRuntime),
		systemCommandsBrick(brickSystemFirewallRuntimeFirewalld, "Collect firewalld runtime information", (*Collector).collectSystemFirewallFirewalldRuntime),
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
	}...)
	return recipe{Name: "system", Bricks: bricks}
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
