// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

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
	bricks := []collectionBrick{}
	bricks = append(bricks, newPBSPXARPrepareBricks()...)
	bricks = append(bricks, newPBSPXARMetadataBricks()...)
	bricks = append(bricks, newPBSPXARSubdirReportBricks()...)
	bricks = append(bricks, newPBSPXARVMListBricks()...)
	bricks = append(bricks, newPBSPXARCTListBricks()...)
	return bricks
}
func newPBSPXARPrepareBricks() []collectionBrick {
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
	}
}

func newPBSPXARMetadataBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPBSPXARSubdirReportBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPBSPXARVMListBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPBSPXARCTListBricks() []collectionBrick {
	return []collectionBrick{
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
