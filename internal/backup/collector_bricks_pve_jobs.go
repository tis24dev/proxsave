// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

func newPVEBackupJobBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPVEScheduleBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}

func newPVEReplicationBricks() []collectionBrick {
	return []collectionBrick{
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
	}
}
