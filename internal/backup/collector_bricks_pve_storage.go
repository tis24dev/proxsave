// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

func newPVEStorageResolveBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPVEStorageProbeBricks() []collectionBrick {
	return []collectionBrick{
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
				ioTimeout := c.pveStorageIOTimeout()
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
	}
}

func newPVEStorageMetadataJSONBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEStorageMetadataJSON,
			Description: "Write JSON metadata for probed PVE storages",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
					return nil
				}
				ioTimeout := c.pveStorageIOTimeout()
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
	}
}

func newPVEStorageMetadataTextBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEStorageMetadataText,
			Description: "Write text metadata for probed PVE storages",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
					return nil
				}
				ioTimeout := c.pveStorageIOTimeout()
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
	}
}

func newPVEStorageAnalysisBricks() []collectionBrick {
	return []collectionBrick{
		{
			ID:          brickPVEStorageBackupAnalysis,
			Description: "Analyze PVE backup files for probed storages",
			Run: func(ctx context.Context, state *collectionState) error {
				c := state.collector
				if !c.config.BackupPVEBackupFiles || state.pve.storageCollectionAborted || len(state.pve.probedStorages) == 0 {
					return nil
				}
				ioTimeout := c.pveStorageIOTimeout()
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
	}
}

func newPVEStorageSummaryBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}
