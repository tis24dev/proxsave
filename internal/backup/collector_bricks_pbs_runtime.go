// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

func newPBSRuntimeBricks() []collectionBrick {
	bricks := []collectionBrick{}
	bricks = append(bricks, newPBSRuntimeCoreBricks()...)
	bricks = append(bricks, newPBSRuntimeACMEBricks()...)
	bricks = append(bricks, newPBSRuntimeNotificationBricks()...)
	bricks = append(bricks, newPBSRuntimeAccessBricks()...)
	bricks = append(bricks, newPBSRuntimeJobBricks()...)
	bricks = append(bricks, newPBSRuntimeTapeBricks()...)
	bricks = append(bricks, newPBSRuntimeSystemBricks()...)
	bricks = append(bricks, newPBSRuntimeS3Bricks()...)
	return bricks
}

func newPBSRuntimeCoreBricks() []collectionBrick {
	return []collectionBrick{
		pbsCommandBrick(brickPBSRuntimeCore, "Collect core PBS runtime information", (*Collector).collectPBSCoreRuntime),
		pbsCommandBrick(brickPBSRuntimeNode, "Collect PBS node runtime information", (*Collector).collectPBSNodeRuntime),
		pbsCommandBrick(brickPBSRuntimeDatastoreList, "Collect PBS datastore list", (*Collector).collectPBSDatastoreListRuntime),
		brick(brickPBSRuntimeDatastoreStatus, "Collect PBS datastore status details", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			return state.collector.collectPBSDatastoreStatusRuntime(ctx, commandsDir, state.pbs.datastores)
		}),
	}
}

func newPBSRuntimeACMEBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickPBSRuntimeACMEAccountsList, "Collect the PBS ACME account list", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			ids, err := state.collector.collectPBSAcmeAccountsListRuntime(ctx, commandsDir)
			if err != nil {
				return err
			}
			state.pbs.acmeAccountNames = ids
			return nil
		}),
		brick(brickPBSRuntimeACMEAccountInfo, "Collect PBS ACME account details", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			return state.collector.collectPBSAcmeAccountInfoRuntime(ctx, commandsDir, state.pbs.acmeAccountNames)
		}),
		brick(brickPBSRuntimeACMEPluginsList, "Collect the PBS ACME plugin list", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			ids, err := state.collector.collectPBSAcmePluginsListRuntime(ctx, commandsDir)
			if err != nil {
				return err
			}
			state.pbs.acmePluginIDs = ids
			return nil
		}),
		brick(brickPBSRuntimeACMEPluginConfig, "Collect PBS ACME plugin configuration", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			return state.collector.collectPBSAcmePluginConfigRuntime(ctx, commandsDir, state.pbs.acmePluginIDs)
		}),
	}
}

func newPBSRuntimeNotificationBricks() []collectionBrick {
	return []collectionBrick{
		pbsCommandBrick(brickPBSRuntimeNotificationTargets, "Collect PBS notification targets", (*Collector).collectPBSNotificationTargetsRuntime),
		pbsCommandBrick(brickPBSRuntimeNotificationMatchers, "Collect PBS notification matchers", (*Collector).collectPBSNotificationMatchersRuntime),
		pbsCommandBrick(brickPBSRuntimeNotificationEndpointSMTP, "Collect PBS SMTP notification endpoints", (*Collector).collectPBSNotificationEndpointSMTPRuntime),
		pbsCommandBrick(brickPBSRuntimeNotificationEndpointSendmail, "Collect PBS sendmail notification endpoints", (*Collector).collectPBSNotificationEndpointSendmailRuntime),
		pbsCommandBrick(brickPBSRuntimeNotificationEndpointGotify, "Collect PBS gotify notification endpoints", (*Collector).collectPBSNotificationEndpointGotifyRuntime),
		pbsCommandBrick(brickPBSRuntimeNotificationEndpointWebhook, "Collect PBS webhook notification endpoints", (*Collector).collectPBSNotificationEndpointWebhookRuntime),
		brick(brickPBSRuntimeNotificationSummary, "Write the PBS notification summary", func(_ context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			state.collector.writePBSNotificationSummary(commandsDir)
			return nil
		}),
	}
}

func newPBSRuntimeAccessBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickPBSRuntimeAccessUsers, "Collect the PBS user list", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			ids, err := state.collector.collectPBSAccessUsersRuntime(ctx, commandsDir)
			if err != nil {
				return err
			}
			state.pbs.userIDs = ids
			return nil
		}),
		pbsCommandBrick(brickPBSRuntimeAccessRealmsLDAP, "Collect PBS LDAP realm definitions", (*Collector).collectPBSAccessRealmLDAPRuntime),
		pbsCommandBrick(brickPBSRuntimeAccessRealmsAD, "Collect PBS Active Directory realm definitions", (*Collector).collectPBSAccessRealmADRuntime),
		pbsCommandBrick(brickPBSRuntimeAccessRealmsOpenID, "Collect PBS OpenID realm definitions", (*Collector).collectPBSAccessRealmOpenIDRuntime),
		pbsCommandBrick(brickPBSRuntimeAccessACL, "Collect PBS ACL definitions", (*Collector).collectPBSAccessACLRuntime),
		brick(brickPBSRuntimeAccessUserTokens, "Collect PBS API token snapshots", func(ctx context.Context, state *collectionState) error {
			if !state.collector.config.BackupUserConfigs || len(state.pbs.userIDs) == 0 {
				return nil
			}
			usersDir, err := state.collector.ensurePBSAccessControlDir()
			if err != nil {
				return err
			}
			return state.collector.collectPBSAccessUserTokensRuntime(ctx, usersDir, state.pbs.userIDs)
		}),
		brick(brickPBSRuntimeAccessTokensAggregate, "Aggregate PBS API token snapshots", func(_ context.Context, state *collectionState) error {
			if !state.collector.config.BackupUserConfigs || len(state.pbs.userIDs) == 0 {
				return nil
			}
			usersDir, err := state.collector.ensurePBSAccessControlDir()
			if err != nil {
				return err
			}
			return state.collector.collectPBSAccessTokensAggregateRuntime(usersDir, state.pbs.userIDs)
		}),
	}
}

func newPBSRuntimeJobBricks() []collectionBrick {
	return []collectionBrick{
		pbsCommandBrick(brickPBSRuntimeRemotes, "Collect PBS remote definitions", (*Collector).collectPBSRemotesRuntime),
		pbsCommandBrick(brickPBSRuntimeSyncJobs, "Collect PBS sync jobs", (*Collector).collectPBSSyncJobsRuntime),
		pbsCommandBrick(brickPBSRuntimeVerificationJobs, "Collect PBS verification jobs", (*Collector).collectPBSVerificationJobsRuntime),
		pbsCommandBrick(brickPBSRuntimePruneJobs, "Collect PBS prune jobs", (*Collector).collectPBSPruneJobsRuntime),
		pbsCommandBrick(brickPBSRuntimeGCJobs, "Collect PBS garbage collection jobs", (*Collector).collectPBSGCJobsRuntime),
	}
}

func newPBSRuntimeTapeBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickPBSRuntimeTapeDetect, "Detect PBS tape support", func(ctx context.Context, state *collectionState) error {
			if !state.collector.config.BackupTapeConfigs {
				state.pbs.tapeSupportKnown = true
				state.pbs.tapeSupported = false
				return nil
			}
			supported, err := state.collector.detectPBSTapeSupport(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return err
				}
				state.collector.logger.Debug("Skipping tape details collection: %v", err)
				state.pbs.tapeSupportKnown = true
				state.pbs.tapeSupported = false
				return nil
			}
			state.pbs.tapeSupportKnown = true
			state.pbs.tapeSupported = supported
			return nil
		}),
		pbsTapeCommandBrick(brickPBSRuntimeTapeDrives, "Collect PBS tape drive inventory", (*Collector).collectPBSTapeDrivesRuntime),
		pbsTapeCommandBrick(brickPBSRuntimeTapeChangers, "Collect PBS tape changer inventory", (*Collector).collectPBSTapeChangersRuntime),
		pbsTapeCommandBrick(brickPBSRuntimeTapePools, "Collect PBS tape pool inventory", (*Collector).collectPBSTapePoolsRuntime),
	}
}

func pbsTapeCommandBrick(id BrickID, description string, run func(*Collector, context.Context, string, bool) error) collectionBrick {
	return brick(id, description, func(ctx context.Context, state *collectionState) error {
		commandsDir, err := state.ensurePBSCommandsDir()
		if err != nil {
			return err
		}
		return run(state.collector, ctx, commandsDir, state.pbs.tapeSupportKnown && state.pbs.tapeSupported)
	})
}

func newPBSRuntimeSystemBricks() []collectionBrick {
	return []collectionBrick{
		pbsCommandBrick(brickPBSRuntimeNetwork, "Collect PBS network runtime information", (*Collector).collectPBSNetworkRuntime),
		pbsCommandBrick(brickPBSRuntimeDisks, "Collect the PBS disk inventory", (*Collector).collectPBSDisksRuntime),
		pbsCommandBrick(brickPBSRuntimeCertInfo, "Collect the PBS certificate summary", (*Collector).collectPBSCertInfoRuntime),
		pbsCommandBrick(brickPBSRuntimeTrafficControl, "Collect PBS traffic control runtime information", (*Collector).collectPBSTrafficControlRuntime),
		pbsCommandBrick(brickPBSRuntimeRecentTasks, "Collect recent PBS tasks", (*Collector).collectPBSRecentTasksRuntime),
	}
}

func newPBSRuntimeS3Bricks() []collectionBrick {
	return []collectionBrick{
		brick(brickPBSRuntimeS3Endpoints, "Collect PBS S3 endpoints", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			ids, err := state.collector.collectPBSS3EndpointsRuntime(ctx, commandsDir)
			if err != nil {
				return err
			}
			state.pbs.s3EndpointIDs = ids
			return nil
		}),
		brick(brickPBSRuntimeS3EndpointBuckets, "Collect PBS S3 endpoint bucket inventories", func(ctx context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			return state.collector.collectPBSS3EndpointBucketsRuntime(ctx, commandsDir, state.pbs.s3EndpointIDs)
		}),
	}
}
