// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
)

func newPVERecipe() recipe {
	bricks := []collectionBrick{}
	bricks = append(bricks, newPVEValidationBricks()...)
	bricks = append(bricks, newPVESnapshotBricks()...)
	bricks = append(bricks, newPVERuntimeBricks()...)
	bricks = append(bricks, newPVEGuestBricks()...)
	bricks = append(bricks, newPVEBackupJobBricks()...)
	bricks = append(bricks, newPVEScheduleBricks()...)
	bricks = append(bricks, newPVEReplicationBricks()...)
	bricks = append(bricks, newPVEStorageResolveBricks()...)
	bricks = append(bricks, newPVEStorageProbeBricks()...)
	bricks = append(bricks, newPVEStorageMetadataJSONBricks()...)
	bricks = append(bricks, newPVEStorageMetadataTextBricks()...)
	bricks = append(bricks, newPVEStorageAnalysisBricks()...)
	bricks = append(bricks, newPVEStorageSummaryBricks()...)
	bricks = append(bricks, newPVECephBricks()...)
	bricks = append(bricks, newPVEAliasBricks()...)
	bricks = append(bricks, newPVEAggregateBricks()...)
	bricks = append(bricks, newPVEVersionBricks()...)
	bricks = append(bricks, newPVEManifestBricks()...)
	return recipe{Name: "pve", Bricks: bricks}
}

func newPVEValidationBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPVESnapshotBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPVERuntimeBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPVEGuestBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}
