// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import (
	"context"
	"fmt"
)

// BrickID identifies one behavior-preserving collection step within a backup recipe.
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
	brickSystemStorageRuntimeMounts     BrickID = "system_storage_runtime_mounts"
	brickSystemStorageRuntimeBlock      BrickID = "system_storage_runtime_block_devices"
	brickSystemComputeRuntimeMemoryCPU  BrickID = "system_compute_runtime_memory_cpu"
	brickSystemComputeRuntimeBusInv     BrickID = "system_compute_runtime_bus_inventory"
	brickSystemServicesRuntime          BrickID = "system_services_runtime"
	brickSystemPackagesRuntimeInstalled BrickID = "system_packages_runtime_installed"
	brickSystemPackagesRuntimeAPTPolicy BrickID = "system_packages_runtime_apt_policy"
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

func brick(id BrickID, description string, run func(context.Context, *collectionState) error) collectionBrick {
	return collectionBrick{ID: id, Description: description, Run: run}
}

func collectorBrick(id BrickID, description string, run func(*Collector, context.Context) error) collectionBrick {
	return brick(id, description, func(ctx context.Context, state *collectionState) error {
		return run(state.collector, ctx)
	})
}

func pbsCommandBrick(id BrickID, description string, run func(*Collector, context.Context, string) error) collectionBrick {
	return brick(id, description, func(ctx context.Context, state *collectionState) error {
		commandsDir, err := state.ensurePBSCommandsDir()
		if err != nil {
			return err
		}
		return run(state.collector, ctx, commandsDir)
	})
}

func systemCommandBrick(id BrickID, description string, run func(*Collector, context.Context, string) error) collectionBrick {
	return brick(id, description, func(ctx context.Context, state *collectionState) error {
		commandsDir, err := state.ensureSystemCommandsDir()
		if err != nil {
			return err
		}
		return run(state.collector, ctx, commandsDir)
	})
}

func pbsInventoryBrick(id BrickID, description string, run func(*Collector, context.Context, *pbsInventoryState) error) collectionBrick {
	return brick(id, description, func(ctx context.Context, state *collectionState) error {
		return run(state.collector, ctx, state.ensurePBSInventoryState())
	})
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

func newDualRecipe() recipe {
	bricks := append([]collectionBrick{}, newPVERecipe().Bricks...)
	bricks = append(bricks, newPBSRecipe().Bricks...)
	return recipe{
		Name:   "dual",
		Bricks: bricks,
	}
}
