// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/logging"
)

// fstabInventoryCategory is the single definition of the device-identity files the
// fstab merge needs. remapFstabDevicesFromInventory (restore_filesystem.go) reads
// them from the directory the backup's fstab was extracted into, and maps the
// backup's device names onto this system's UUID/LABEL. Without them the remap
// silently finds nothing and the merge proposes the backup's raw device names.
//
// It lives here, alone, because it used to exist in three copies - the selective
// path's, the analysis-failure fallback's, and a dead reader-based one - and the
// fallback's copy had already gone missing.
func fstabInventoryCategory() []Category {
	return []Category{{
		ID:   "fstab_inventory",
		Name: "Fstab inventory (device mapping)",
		Paths: []string{
			"./var/lib/proxsave-info/commands/system/blkid.txt",
			"./var/lib/proxsave-info/commands/system/lsblk_json.json",
			"./var/lib/proxsave-info/commands/system/lsblk.txt",
			"./var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json",
		},
	}}
}

// extractFstabInventoryInto pulls the device-identity files out of the archive next
// to the extracted fstab. Best-effort: an older backup may not carry them, and the
// merge still works without the remap, so a failure is logged at Debug and the
// restore continues.
func extractFstabInventoryInto(ctx context.Context, archivePath, fsTempDir string, logger *logging.Logger) {
	err := extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    fsTempDir,
		logger:      logger,
		categories:  fstabInventoryCategory(),
		mode:        RestoreModeCustom,
	})
	if err != nil {
		logger.Debug("Failed to extract fstab inventory data (continuing): %v", err)
	}
}
