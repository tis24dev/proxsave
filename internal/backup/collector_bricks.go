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
	brickPVEDirectories        BrickID = "pve_directories"
	brickPVECommands           BrickID = "pve_commands"
	brickPVEVMConfigs          BrickID = "pve_vm_configs"
	brickPVEJobs               BrickID = "pve_jobs"
	brickPVESchedules          BrickID = "pve_schedules"
	brickPVEReplication        BrickID = "pve_replication"
	brickPVEStorageMetadata    BrickID = "pve_storage_metadata"
	brickPVECeph               BrickID = "pve_ceph"
	brickPVEFinalize           BrickID = "pve_finalize"

	brickPBSValidate           BrickID = "pbs_validate"
	brickPBSDirectories        BrickID = "pbs_directories"
	brickPBSDatastoreDiscovery BrickID = "pbs_datastore_discovery"
	brickPBSCommands           BrickID = "pbs_commands"
	brickPBSDatastoreInventory BrickID = "pbs_datastore_inventory"
	brickPBSDatastoreConfigs   BrickID = "pbs_datastore_configs"
	brickPBSUserConfigs        BrickID = "pbs_user_configs"
	brickPBSPXAR               BrickID = "pbs_pxar"
	brickPBSFinalize           BrickID = "pbs_finalize"

	brickSystemDirectories   BrickID = "system_directories"
	brickSystemCommands      BrickID = "system_commands"
	brickSystemKernel        BrickID = "system_kernel"
	brickSystemHardware      BrickID = "system_hardware"
	brickSystemCriticalFiles BrickID = "system_critical_files"
	brickSystemConfigFile    BrickID = "system_config_file"
	brickSystemCustomPaths   BrickID = "system_custom_paths"
	brickSystemScriptDirs    BrickID = "system_script_dirs"
	brickSystemScriptRepo    BrickID = "system_script_repo"
	brickSystemSSHKeys       BrickID = "system_ssh_keys"
	brickSystemRootHome      BrickID = "system_root_home"
	brickSystemUserHomes     BrickID = "system_user_homes"
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
}

type pveContext struct {
	clustered   bool
	runtimeInfo *pveRuntimeInfo
}

type pbsContext struct {
	datastores []pbsDatastore
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
				ID:          brickPVEDirectories,
				Description: "Collect PVE configuration directories",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting PVE directories (clustered=%v)", state.pve.clustered)
					if err := c.collectPVEDirectories(ctx, state.pve.clustered); err != nil {
						return fmt.Errorf("failed to collect PVE directories: %w", err)
					}
					c.logger.Debug("PVE directory collection completed")
					return nil
				},
			},
			{
				ID:          brickPVECommands,
				Description: "Collect PVE runtime commands",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting PVE command outputs and runtime state")
					runtimeInfo, err := c.collectPVECommands(ctx, state.pve.clustered)
					if err != nil {
						return fmt.Errorf("failed to collect PVE commands: %w", err)
					}
					state.pve.runtimeInfo = runtimeInfo
					c.logger.Debug("PVE command output collection completed")
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
				ID:          brickPBSDirectories,
				Description: "Collect PBS configuration directories",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					pbsConfigPath := c.pbsConfigPath()
					c.logger.Debug("Collecting PBS configuration directories")
					if err := c.collectPBSDirectories(ctx, pbsConfigPath); err != nil {
						return fmt.Errorf("failed to collect PBS directories: %w", err)
					}
					c.logger.Debug("PBS directory collection completed")
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
			{
				ID:          brickPBSCommands,
				Description: "Collect PBS runtime commands",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting PBS command outputs and state")
					if err := c.collectPBSCommands(ctx, state.pbs.datastores); err != nil {
						return fmt.Errorf("failed to collect PBS commands: %w", err)
					}
					c.logger.Debug("PBS command output collection completed")
					return nil
				},
			},
			{
				ID:          brickPBSDatastoreInventory,
				Description: "Collect PBS datastore inventory",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting PBS datastore inventory report")
					if err := c.collectPBSDatastoreInventory(ctx, state.pbs.datastores); err != nil {
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
			{
				ID:          brickSystemDirectories,
				Description: "Collect system directories",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting system directories (network, apt, cron, services, ssl, kernel, firewall, etc.)")
					if err := c.collectSystemDirectories(ctx); err != nil {
						return fmt.Errorf("failed to collect system directories: %w", err)
					}
					c.logger.Debug("System directories collection completed")
					return nil
				},
			},
			{
				ID:          brickSystemCommands,
				Description: "Collect system command outputs",
				Run: func(ctx context.Context, state *collectionState) error {
					c := state.collector
					c.logger.Debug("Collecting system command outputs and runtime state")
					if err := c.collectSystemCommands(ctx); err != nil {
						return fmt.Errorf("failed to collect system commands: %w", err)
					}
					c.logger.Debug("System command collection completed")
					return nil
				},
			},
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
