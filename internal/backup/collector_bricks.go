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
	brickPVEValidateAndCluster BrickID = "pve_validate_and_cluster"
	brickPVEConfigSnapshot     BrickID = "pve_config_snapshot"
	brickPVEClusterSnapshot    BrickID = "pve_cluster_snapshot"
	brickPVEFirewallSnapshot   BrickID = "pve_firewall_snapshot"
	brickPVEVZDumpSnapshot     BrickID = "pve_vzdump_snapshot"
	brickPVERuntimeCore        BrickID = "pve_runtime_core"
	brickPVERuntimeACL         BrickID = "pve_runtime_acl"
	brickPVERuntimeCluster     BrickID = "pve_runtime_cluster"
	brickPVERuntimeStorage     BrickID = "pve_runtime_storage"
	brickPVEVMConfigs          BrickID = "pve_vm_configs"
	brickPVEJobs               BrickID = "pve_jobs"
	brickPVESchedules          BrickID = "pve_schedules"
	brickPVEReplication        BrickID = "pve_replication"
	brickPVEStorageMetadata    BrickID = "pve_storage_metadata"
	brickPVECeph               BrickID = "pve_ceph"
	brickPVEFinalize           BrickID = "pve_finalize"

	brickPBSValidate             BrickID = "pbs_validate"
	brickPBSConfigSnapshot       BrickID = "pbs_config_snapshot"
	brickPBSManifestSnapshot     BrickID = "pbs_manifest_snapshot"
	brickPBSDatastoreDiscovery   BrickID = "pbs_datastore_discovery"
	brickPBSRuntimeCore          BrickID = "pbs_runtime_core"
	brickPBSRuntimeNode          BrickID = "pbs_runtime_node"
	brickPBSRuntimeDatastores    BrickID = "pbs_runtime_datastores"
	brickPBSRuntimeACME          BrickID = "pbs_runtime_acme"
	brickPBSRuntimeNotifications BrickID = "pbs_runtime_notifications"
	brickPBSRuntimeAccess        BrickID = "pbs_runtime_access"
	brickPBSRuntimeRemoteJobs    BrickID = "pbs_runtime_remote_jobs"
	brickPBSRuntimeTape          BrickID = "pbs_runtime_tape"
	brickPBSRuntimeNetwork       BrickID = "pbs_runtime_network"
	brickPBSRuntimeHostState     BrickID = "pbs_runtime_host_state"
	brickPBSRuntimeS3            BrickID = "pbs_runtime_s3"
	brickPBSStorageStackSnapshot BrickID = "pbs_storage_stack_snapshot"
	brickPBSDatastoreInventory   BrickID = "pbs_datastore_inventory_report"
	brickPBSDatastoreConfigs     BrickID = "pbs_datastore_configs"
	brickPBSUserConfigs          BrickID = "pbs_user_configs"
	brickPBSPXAR                 BrickID = "pbs_pxar"
	brickPBSFinalize             BrickID = "pbs_finalize"

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
	clustered   bool
	runtimeInfo *pveRuntimeInfo
	commandsDir string
}

type pbsContext struct {
	datastores  []pbsDatastore
	commandsDir string
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
				ID:          brickPVEVMConfigs,
				Description: "Collect VM and container configurations",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupVMConfigs {
						c.logger.Info("Collecting VM and container configurations")
						c.logger.Debug("Collecting VM/CT configuration files")
						if err := c.collectVMConfigs(ctx); err != nil {
							c.logger.Warning("Failed to collect VM configs: %v", err)
						} else {
							c.logger.Debug("VM/CT configuration collection completed")
						}
					} else {
						c.logger.Skip("VM/container configuration backup disabled.")
					}
					return nil
				},
			},
			{
				ID:          brickPVEJobs,
				Description: "Collect PVE job definitions",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupPVEJobs {
						c.logger.Debug("Collecting PVE job definitions for nodes: %v", state.pve.runtimeNodes())
						if err := c.collectPVEJobs(ctx, state.pve.runtimeNodes()); err != nil {
							c.logger.Warning("Failed to collect PVE job information: %v", err)
						} else {
							c.logger.Debug("PVE job collection completed")
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVESchedules,
				Description: "Collect PVE schedules",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupPVESchedules {
						c.logger.Debug("Collecting PVE schedule information")
						if err := c.collectPVESchedules(ctx); err != nil {
							c.logger.Warning("Failed to collect PVE schedules: %v", err)
						} else {
							c.logger.Debug("PVE schedule collection completed")
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVEReplication,
				Description: "Collect PVE replication information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupPVEReplication {
						c.logger.Debug("Collecting PVE replication settings for nodes: %v", state.pve.runtimeNodes())
						if err := c.collectPVEReplication(ctx, state.pve.runtimeNodes()); err != nil {
							c.logger.Warning("Failed to collect PVE replication info: %v", err)
						} else {
							c.logger.Debug("PVE replication collection completed")
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVEStorageMetadata,
				Description: "Collect PVE backup datastore metadata",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupPVEBackupFiles {
						c.logger.Debug("Collecting datastore metadata for PVE backup files")
						if err := c.collectPVEStorageMetadata(ctx, state.pve.runtimeStorages()); err != nil {
							c.logger.Warning("Failed to collect PVE datastore metadata: %v", err)
						} else {
							c.logger.Debug("PVE datastore metadata collection completed")
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVECeph,
				Description: "Collect Ceph information",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupCephConfig {
						c.logger.Debug("Collecting Ceph configuration and status")
						if err := c.collectPVECephInfo(ctx); err != nil {
							c.logger.Warning("Failed to collect Ceph information: %v", err)
						} else {
							c.logger.Debug("Ceph information collection completed")
						}
					}
					return nil
				},
			},
			{
				ID:          brickPVEFinalize,
				Description: "Finalize PVE collection state",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Creating PVE info aliases under /var/lib/pve-cluster/info")
					if err := c.createPVEInfoAliases(ctx); err != nil {
						c.logger.Warning("Failed to create PVE info aliases: %v", err)
					}
					c.populatePVEManifest()
					return nil
				},
			},
		},
	}
}

func newPBSRecipe() recipe {
	return recipe{
		Name: "pbs",
		Bricks: []collectionBrick{
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
				ID:          brickPBSConfigSnapshot,
				Description: "Collect base PBS configuration snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPBSConfigSnapshot(ctx, state.collector.pbsConfigPath())
				},
			},
			{
				ID:          brickPBSManifestSnapshot,
				Description: "Collect PBS manifest snapshot",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPBSManifestSnapshot(ctx, state.collector.pbsConfigPath())
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
				ID:          brickPBSRuntimeDatastores,
				Description: "Collect PBS datastore runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSDatastoreRuntime(ctx, commandsDir, state.pbs.datastores)
				},
			},
			{
				ID:          brickPBSRuntimeACME,
				Description: "Collect PBS ACME runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSAcmeRuntime(ctx, commandsDir)
				},
			},
			{
				ID:          brickPBSRuntimeNotifications,
				Description: "Collect PBS notification runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSNotificationRuntime(ctx, commandsDir)
				},
			},
			{
				ID:          brickPBSRuntimeAccess,
				Description: "Collect PBS access runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSAccessRuntime(ctx, commandsDir)
				},
			},
			{
				ID:          brickPBSRuntimeRemoteJobs,
				Description: "Collect PBS remote and job runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSRemoteJobsRuntime(ctx, commandsDir)
				},
			},
			{
				ID:          brickPBSRuntimeTape,
				Description: "Collect PBS tape runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSTapeRuntime(ctx, commandsDir)
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
				ID:          brickPBSRuntimeHostState,
				Description: "Collect PBS host-state runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSHostStateRuntime(ctx, commandsDir)
				},
			},
			{
				ID:          brickPBSRuntimeS3,
				Description: "Collect PBS S3 runtime information",
				Run: func(ctx context.Context, state *collectionState) error {
					commandsDir, err := state.ensurePBSCommandsDir()
					if err != nil {
						return err
					}
					return state.collector.collectPBSS3Runtime(ctx, commandsDir)
				},
			},
			{
				ID:          brickPBSStorageStackSnapshot,
				Description: "Collect storage-stack artifacts for PBS datastores",
				Run: func(ctx context.Context, state *collectionState) error {
					return state.collector.collectPBSStorageStackSnapshot(ctx)
				},
			},
			{
				ID:          brickPBSDatastoreInventory,
				Description: "Collect PBS datastore inventory report",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting PBS datastore inventory report")
					if err := c.writePBSDatastoreInventoryReport(ctx, state.pbs.datastores); err != nil {
						c.logger.Warning("Failed to collect PBS datastore inventory report: %v", err)
					} else {
						c.logger.Debug("PBS datastore inventory report completed")
					}
					return nil
				},
			},
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
				ID:          brickPBSUserConfigs,
				Description: "Collect PBS user and ACL configurations",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					if c.config.BackupUserConfigs {
						c.logger.Debug("Collecting PBS user and ACL configurations")
						if err := c.collectUserConfigs(ctx); err != nil {
							c.logger.Warning("Failed to collect user configs: %v", err)
						} else {
							c.logger.Debug("User configuration collection completed")
						}
					} else {
						c.logger.Skip("PBS user/ACL backup disabled.")
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
			{
				ID:          brickPBSFinalize,
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
