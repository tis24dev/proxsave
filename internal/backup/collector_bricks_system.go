// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

func newSystemRecipe() recipe {
	bricks := []collectionBrick{}
	bricks = append(bricks, newSystemStaticBricks()...)
	bricks = append(bricks, newCommonFilesystemBricks()...)
	bricks = append(bricks, newCommonStorageStackBricks()...)
	bricks = append(bricks, newSystemPostCommonStaticBricks()...)
	bricks = append(bricks, newSystemRuntimeCommandBricks()...)
	bricks = append(bricks, newSystemReportBricks()...)
	bricks = append(bricks, newSystemFileCollectionBricks()...)
	bricks = append(bricks, newSystemScriptCollectionBricks()...)
	bricks = append(bricks, newSystemHomeCollectionBricks()...)
	return recipe{Name: "system", Bricks: bricks}
}

func newSystemStaticBricks() []collectionBrick {
	return []collectionBrick{
		collectorBrick(brickSystemNetworkStatic, "Collect static network configuration", (*Collector).collectSystemNetworkStatic),
		collectorBrick(brickSystemIdentityStatic, "Collect static identity files", (*Collector).collectSystemIdentityStatic),
		collectorBrick(brickSystemAptStatic, "Collect static APT configuration", (*Collector).collectSystemAptStatic),
		collectorBrick(brickSystemCronStatic, "Collect static cron configuration", (*Collector).collectSystemCronStatic),
		collectorBrick(brickSystemServicesStatic, "Collect static service configuration", (*Collector).collectSystemServicesStatic),
		collectorBrick(brickSystemLoggingStatic, "Collect static logging configuration", (*Collector).collectSystemLoggingStatic),
		collectorBrick(brickSystemSSLStatic, "Collect static SSL configuration", (*Collector).collectSystemSSLStatic),
		collectorBrick(brickSystemSysctlStatic, "Collect static sysctl configuration", (*Collector).collectSystemSysctlStatic),
		collectorBrick(brickSystemKernelModulesStatic, "Collect static kernel module configuration", (*Collector).collectSystemKernelModuleStatic),
	}
}

func newSystemPostCommonStaticBricks() []collectionBrick {
	return []collectionBrick{
		collectorBrick(brickSystemZFSStatic, "Collect static ZFS configuration", (*Collector).collectSystemZFSStatic),
		collectorBrick(brickSystemFirewallStatic, "Collect static firewall configuration", (*Collector).collectSystemFirewallStatic),
		collectorBrick(brickSystemRuntimeLeases, "Collect runtime lease snapshots", (*Collector).collectSystemRuntimeLeases),
	}
}

func newSystemRuntimeCommandBricks() []collectionBrick {
	return []collectionBrick{
		systemCommandBrick(brickSystemCoreRuntime, "Collect core system runtime information", (*Collector).collectSystemCoreRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeAddr, "Collect network address runtime information", (*Collector).collectSystemNetworkAddrRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeRules, "Collect network rule runtime information", (*Collector).collectSystemNetworkRulesRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeRoutes, "Collect network route runtime information", (*Collector).collectSystemNetworkRoutesRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeLinks, "Collect network link runtime information", (*Collector).collectSystemNetworkLinksRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeNeighbors, "Collect network neighbor runtime information", (*Collector).collectSystemNetworkNeighborsRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeBridges, "Collect bridge runtime information", (*Collector).collectSystemNetworkBridgesRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeInventory, "Collect network inventory runtime information", (*Collector).collectSystemNetworkInventoryRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeBonding, "Collect bonding runtime information", (*Collector).collectSystemNetworkBondingRuntime),
		systemCommandBrick(brickSystemNetworkRuntimeDNS, "Collect DNS runtime information", (*Collector).collectSystemNetworkDNSRuntime),
		systemCommandBrick(brickSystemStorageRuntimeMounts, "Collect storage mount runtime information", (*Collector).collectSystemStorageMountsRuntime),
		systemCommandBrick(brickSystemStorageRuntimeBlock, "Collect block device runtime information", (*Collector).collectSystemStorageBlockDevicesRuntime),
		systemCommandBrick(brickSystemComputeRuntimeMemoryCPU, "Collect memory and CPU runtime information", (*Collector).collectSystemComputeMemoryCPURuntime),
		systemCommandBrick(brickSystemComputeRuntimeBusInv, "Collect bus inventory runtime information", (*Collector).collectSystemComputeBusInventoryRuntime),
		systemCommandBrick(brickSystemServicesRuntime, "Collect service runtime information", (*Collector).collectSystemServicesRuntime),
		systemCommandBrick(brickSystemPackagesRuntimeInstalled, "Collect installed package runtime information", (*Collector).collectSystemPackagesInstalledRuntime),
		systemCommandBrick(brickSystemPackagesRuntimeAPTPolicy, "Collect APT policy runtime information", (*Collector).collectSystemPackagesAptPolicyRuntime),
		systemCommandBrick(brickSystemFirewallRuntimeIPTables, "Collect iptables runtime information", (*Collector).collectSystemFirewallIPTablesRuntime),
		systemCommandBrick(brickSystemFirewallRuntimeIP6Tables, "Collect ip6tables runtime information", (*Collector).collectSystemFirewallIP6TablesRuntime),
		systemCommandBrick(brickSystemFirewallRuntimeNFTables, "Collect nftables runtime information", (*Collector).collectSystemFirewallNFTablesRuntime),
		systemCommandBrick(brickSystemFirewallRuntimeUFW, "Collect UFW runtime information", (*Collector).collectSystemFirewallUFWRuntime),
		systemCommandBrick(brickSystemFirewallRuntimeFirewalld, "Collect firewalld runtime information", (*Collector).collectSystemFirewallFirewalldRuntime),
		systemCommandBrick(brickSystemKernelModulesRuntime, "Collect kernel module runtime information", (*Collector).collectSystemKernelModulesRuntime),
		systemCommandBrick(brickSystemSysctlRuntime, "Collect sysctl runtime information", (*Collector).collectSystemSysctlRuntime),
		systemCommandBrick(brickSystemZFSRuntime, "Collect ZFS runtime information", (*Collector).collectSystemZFSRuntime),
		systemCommandBrick(brickSystemLVMRuntime, "Collect LVM runtime information", (*Collector).collectSystemLVMRuntime),
	}
}

func newSystemReportBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickSystemNetworkReport, "Finalize derived system reports", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensureSystemCommandsDir()
			if err != nil {
				return err
			}
			if err := state.collector.finalizeSystemRuntimeReports(ctx, commandsDir); err != nil {
				state.collector.logger.Debug("Network report generation failed: %v", err)
			}
			return nil
		}),
		brick(brickSystemKernel, "Collect kernel information", func(ctx context.Context, state *collectionState) error {
			c := state.collector
			c.logger.Debug("Collecting kernel information (uname/modules)")
			if err := c.collectKernelInfo(ctx); err != nil {
				c.logger.Warning("Failed to collect kernel info: %v", err)
			} else {
				c.logger.Debug("Kernel information collected successfully")
			}
			return nil
		}),
		brick(brickSystemHardware, "Collect hardware information", func(ctx context.Context, state *collectionState) error {
			c := state.collector
			c.logger.Debug("Collecting hardware inventory (CPU/memory/devices)")
			if err := c.collectHardwareInfo(ctx); err != nil {
				c.logger.Warning("Failed to collect hardware info: %v", err)
			} else {
				c.logger.Debug("Hardware inventory collected successfully")
			}
			return nil
		}),
	}
}

func newSystemFileCollectionBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickSystemCriticalFiles, "Collect critical system files", func(ctx context.Context, state *collectionState) error {
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
		}),
		brick(brickSystemConfigFile, "Collect backup configuration file", func(ctx context.Context, state *collectionState) error {
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
		}),
		brick(brickSystemCustomPaths, "Collect custom backup paths", func(ctx context.Context, state *collectionState) error {
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
		}),
	}
}

func newSystemScriptCollectionBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickSystemScriptDirs, "Collect script directories", func(ctx context.Context, state *collectionState) error {
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
		}),
		brick(brickSystemScriptRepo, "Collect script repository", func(ctx context.Context, state *collectionState) error {
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
		}),
		brick(brickSystemSSHKeys, "Collect SSH keys", func(ctx context.Context, state *collectionState) error {
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
		}),
	}
}

func newSystemHomeCollectionBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickSystemRootHome, "Collect root home directory", func(ctx context.Context, state *collectionState) error {
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
		}),
		brick(brickSystemUserHomes, "Collect user home directories", func(ctx context.Context, state *collectionState) error {
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
		}),
	}
}
