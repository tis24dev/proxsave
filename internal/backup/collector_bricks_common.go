// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

func newCommonFilesystemBricks() []collectionBrick {
	return []collectionBrick{
		collectorBrick(brickCommonFilesystemFstab, "Collect the common filesystem table", (*Collector).collectCommonFilesystemFstab),
	}
}

func newCommonStorageStackBricks() []collectionBrick {
	return []collectionBrick{
		collectorBrick(brickCommonStorageStackCrypttab, "Collect common storage-stack crypttab data", (*Collector).collectCommonStorageStackCrypttab),
		collectorBrick(brickCommonStorageStackISCSISnapshot, "Collect common iSCSI storage-stack data", (*Collector).collectCommonStorageStackISCSISnapshot),
		collectorBrick(brickCommonStorageStackMultipathSnapshot, "Collect common multipath storage-stack data", (*Collector).collectCommonStorageStackMultipathSnapshot),
		collectorBrick(brickCommonStorageStackMDADMSnapshot, "Collect common mdadm storage-stack data", (*Collector).collectCommonStorageStackMDADMSnapshot),
		collectorBrick(brickCommonStorageStackLVMSnapshot, "Collect common LVM storage-stack data", (*Collector).collectCommonStorageStackLVMSnapshot),
		collectorBrick(brickCommonStorageStackMountUnitsSnapshot, "Collect common storage-stack mount units", (*Collector).collectCommonStorageStackMountUnitsSnapshot),
		collectorBrick(brickCommonStorageStackAutofsSnapshot, "Collect common storage-stack autofs data", (*Collector).collectCommonStorageStackAutofsSnapshot),
		collectorBrick(brickCommonStorageStackReferencedFiles, "Collect common storage-stack referenced files", (*Collector).collectCommonStorageStackReferencedFiles),
	}
}
