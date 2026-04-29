// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

func newPBSRecipe() recipe {
	bricks := []collectionBrick{
		{
			ID:          brickPBSValidate,
			Description: "Validate PBS environment",
			Run: func(_ context.Context, state *collectionState) error {
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
	bricks := []collectionBrick{}
	bricks = append(bricks, newPBSManifestDatastoreNodeBricks()...)
	bricks = append(bricks, newPBSManifestACMEAndMetricsBricks()...)
	bricks = append(bricks, newPBSManifestNotificationAccessBricks()...)
	bricks = append(bricks, newPBSManifestRemoteJobBricks()...)
	bricks = append(bricks, newPBSManifestTapeAndNetworkBricks()...)
	return bricks
}
func newPBSManifestDatastoreNodeBricks() []collectionBrick {
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
	}
}

func newPBSManifestACMEAndMetricsBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPBSManifestNotificationAccessBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPBSManifestRemoteJobBricks() []collectionBrick {
	return []collectionBrick{
		{ID: brickPBSManifestRemote, Description: "Collect PBS remote manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestRemote(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestSyncJobs, Description: "Collect PBS sync-job manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestSyncJobs(ctx, state.collector.pbsConfigPath())
		}},
		{ID: brickPBSManifestVerificationJobs, Description: "Collect PBS verification-job manifest entries", Run: func(ctx context.Context, state *collectionState) error {
			return state.collector.collectPBSManifestVerificationJobs(ctx, state.collector.pbsConfigPath())
		}},
	}
}

func newPBSManifestTapeAndNetworkBricks() []collectionBrick {
	return []collectionBrick{
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
