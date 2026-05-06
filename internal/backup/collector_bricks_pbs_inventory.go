// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import "context"

func newPBSInventoryBricks() []collectionBrick {
	bricks := []collectionBrick{}
	bricks = append(bricks, newPBSInventoryInitBricks()...)
	bricks = append(bricks, newPBSInventoryFileBricks()...)
	bricks = append(bricks, newPBSInventoryHostCommandBricks()...)
	bricks = append(bricks, newPBSInventoryFinalizeBricks()...)
	return bricks
}

func newPBSInventoryInitBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickPBSInventoryInit, "Initialize the PBS datastore inventory state", func(ctx context.Context, state *collectionState) error {
			inventory, err := state.collector.initPBSDatastoreInventoryState(ctx, state.pbs.datastores)
			if err != nil {
				return err
			}
			state.pbs.inventory = inventory
			return nil
		}),
	}
}

func newPBSInventoryFileBricks() []collectionBrick {
	return []collectionBrick{
		pbsInventoryBrick(brickPBSInventoryMountFiles, "Populate PBS inventory mount files", (*Collector).populatePBSInventoryMountFiles),
		pbsInventoryBrick(brickPBSInventoryOSFiles, "Populate PBS inventory OS files", (*Collector).populatePBSInventoryOSFiles),
		pbsInventoryBrick(brickPBSInventoryMultipathFiles, "Populate PBS inventory multipath files", (*Collector).populatePBSInventoryMultipathFiles),
		pbsInventoryBrick(brickPBSInventoryISCSIFiles, "Populate PBS inventory iSCSI files", (*Collector).populatePBSInventoryISCSIFiles),
		pbsInventoryBrick(brickPBSInventoryAutofsFiles, "Populate PBS inventory autofs files", (*Collector).populatePBSInventoryAutofsFiles),
		pbsInventoryBrick(brickPBSInventoryZFSFiles, "Populate PBS inventory ZFS files", (*Collector).populatePBSInventoryZFSFiles),
		pbsInventoryBrick(brickPBSInventoryLVMDirs, "Populate PBS inventory LVM directories", (*Collector).populatePBSInventoryLVMDirs),
		pbsInventoryBrick(brickPBSInventorySystemdMountUnits, "Populate PBS inventory systemd mount units", (*Collector).populatePBSInventorySystemdMountUnits),
		pbsInventoryBrick(brickPBSInventoryReferencedFiles, "Populate PBS inventory referenced files", (*Collector).populatePBSInventoryReferencedFiles),
	}
}

func newPBSInventoryHostCommandBricks() []collectionBrick {
	return []collectionBrick{
		pbsInventoryBrick(brickPBSInventoryHostCommandsCore, "Populate PBS inventory core host commands", (*Collector).populatePBSInventoryHostCommandsCore),
		pbsInventoryBrick(brickPBSInventoryHostCommandsDMSetup, "Populate PBS inventory dmsetup host commands", (*Collector).populatePBSInventoryHostCommandsDMSetup),
		pbsInventoryBrick(brickPBSInventoryHostCommandsLVM, "Populate PBS inventory LVM host commands", (*Collector).populatePBSInventoryHostCommandsLVM),
		pbsInventoryBrick(brickPBSInventoryHostCommandsMDADM, "Populate PBS inventory mdadm host commands", (*Collector).populatePBSInventoryHostCommandsMDADM),
		pbsInventoryBrick(brickPBSInventoryHostCommandsMultipath, "Populate PBS inventory multipath host commands", (*Collector).populatePBSInventoryHostCommandsMultipath),
		pbsInventoryBrick(brickPBSInventoryHostCommandsISCSI, "Populate PBS inventory iSCSI host commands", (*Collector).populatePBSInventoryHostCommandsISCSI),
		pbsInventoryBrick(brickPBSInventoryHostCommandsZFS, "Populate PBS inventory ZFS host commands", (*Collector).populatePBSInventoryHostCommandsZFS),
	}
}

func newPBSInventoryFinalizeBricks() []collectionBrick {
	return []collectionBrick{
		brick(brickPBSInventoryCommandFiles, "Populate PBS inventory with collected PBS command files", func(_ context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			return state.collector.populatePBSInventoryCommandFiles(state.ensurePBSInventoryState(), commandsDir)
		}),
		pbsInventoryBrick(brickPBSInventoryDatastores, "Populate PBS datastore inventory entries", (*Collector).populatePBSDatastoreInventoryEntries),
		brick(brickPBSInventoryWrite, "Write the PBS datastore inventory report", func(_ context.Context, state *collectionState) error {
			commandsDir, err := state.ensurePBSCommandsDir()
			if err != nil {
				return err
			}
			return state.collector.writePBSInventoryState(state.ensurePBSInventoryState(), commandsDir)
		}),
	}
}
