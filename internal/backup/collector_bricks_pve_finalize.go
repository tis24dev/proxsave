// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

func newPVECephBricks() []collectionBrick {
	return []collectionBrick{
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
					if isContextCancellationError(ctx, err) {
						return err
					}
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
					if isContextCancellationError(ctx, err) {
						return err
					}
					c.logger.Warning("Failed to collect Ceph runtime information: %v", err)
					state.pve.cephCollectionAborted = true
				} else {
					c.logger.Debug("Ceph information collection completed")
				}
				return nil
			},
		},
	}
}

func newPVEAliasBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEAliasCore,
			Description: "Create core PVE aliases",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				c.logger.Debug("Creating PVE info aliases under /var/lib/pve-cluster/info")
				if err := c.createPVECoreAliases(ctx); err != nil {
					if isContextCancellationError(ctx, err) {
						return err
					}
					c.logger.Warning("Failed to create PVE core aliases: %v", err)
					state.pve.finalizeCollectionAborted = true
				}
				return nil
			},
		},
	}
}

func newPVEAggregateBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEAggregateBackupHistory,
			Description: "Aggregate backup history aliases",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if state.pve.finalizeCollectionAborted {
					return nil
				}
				if err := c.createPVEBackupHistoryAggregate(ctx); err != nil {
					if isContextCancellationError(ctx, err) {
						return err
					}
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
					if isContextCancellationError(ctx, err) {
						return err
					}
					c.logger.Warning("Failed to aggregate PVE replication status: %v", err)
					state.pve.finalizeCollectionAborted = true
				}
				return nil
			},
		},
	}
}

func newPVEVersionBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEVersionInfo,
			Description: "Write PVE version alias information",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if state.pve.finalizeCollectionAborted {
					return nil
				}
				if err := c.createPVEVersionInfo(ctx); err != nil {
					if isContextCancellationError(ctx, err) {
						return err
					}
					c.logger.Warning("Failed to write PVE version info: %v", err)
					state.pve.finalizeCollectionAborted = true
				}
				return nil
			},
		},
	}
}

func newPVEManifestBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEManifestFinalize,
			Description: "Finalize the PVE manifest",
			Run: func(_ context.Context, state *collectionState) error {
				state.collector.populatePVEManifest()
				return nil
			},
		},
	}
}

func (p *pveContext) runtimeNodes() []string {
	if p == nil || p.runtimeInfo == nil {
		return nil
	}
	return p.runtimeInfo.Nodes
}

func (p *pveContext) runtimeStorages() []pveStorageEntry {
	if p == nil || p.runtimeInfo == nil {
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

func (p *pveContext) storageResult(storage pveStorageEntry) *pveStorageScanResult {
	if p == nil || p.storageScanResults == nil {
		return nil
	}
	return p.storageScanResults[storage.pathKey()]
}
