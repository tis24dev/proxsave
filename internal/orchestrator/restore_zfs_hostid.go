package orchestrator

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// The zfs category restores /etc/hostid. A pool records the hostid it was imported
// under, and the initramfs refuses to import one last used under another: reproduced
// on a PVE 9.2 VM with root on ZFS, the old host's /etc/hostid let the host boot once
// (zpool status reported ZFS-8000-EY), and the next update-initramfs, which any
// kernel or ZFS upgrade runs, stopped it at "cannot import 'rpool': pool was
// previously in use from another system". On a host that has pools imported under
// its own hostid, the backup's is therefore not written. With no pool imported
// (disks moved from the old host and not imported yet, or no ZFS in use) it is: the
// pools those disks carry were last used under it.

const hostidArchivePath = "etc/hostid"

// formatHostid renders /etc/hostid the way `hostid` prints it: the 4 bytes are the
// id in little-endian order.
func formatHostid(b []byte) string {
	switch len(b) {
	case 0:
		return "none"
	case 4:
		return fmt.Sprintf("%08x", binary.LittleEndian.Uint32(b))
	default:
		return hex.EncodeToString(b)
	}
}

// hostidRestoreDecision reports whether the backup's /etc/hostid may be written over
// live, given the pools this host has imported. It is written when it changes
// nothing or when no pool is imported; not when pools are imported under another
// hostid, and not when the pools cannot be listed, since keeping the host's own
// never leaves it unbootable.
func hostidRestoreDecision(backup, live []byte, pools []string, listErr error) (write bool, reason string) {
	if string(backup) == string(live) {
		return true, ""
	}
	if listErr != nil {
		return false, fmt.Sprintf("this host's pools cannot be listed (%v)", listErr)
	}
	if len(pools) == 0 {
		return true, ""
	}
	sorted := append([]string{}, pools...)
	sort.Strings(sorted)
	return false, fmt.Sprintf("pools %s use this host's hostid %s, and an initramfs rebuilt with another one refuses to import them",
		strings.Join(sorted, ", "), formatHostid(live))
}

// listImportedZFSPools returns the pools this host has imported. A host without the
// zpool tool has none.
func listImportedZFSPools(ctx context.Context) ([]string, error) {
	if _, err := restoreCmd.Run(ctx, "which", "zpool"); err != nil {
		return nil, nil
	}
	out, err := restoreCmd.Run(ctx, "zpool", "list", "-H", "-o", "name")
	if err != nil {
		return nil, err
	}
	var pools []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name != "" && name != "no pools available" {
			pools = append(pools, name)
		}
	}
	return pools, nil
}

// decideHostidRestore sets skipHostid when the restore is about to write the
// backup's /etc/hostid over a host whose pools use another one. writes says whether
// the extraction that follows would write /etc/hostid at all.
func (w *restoreUIWorkflowRun) decideHostidRestore(writes bool) error {
	w.skipHostid = false
	if !writes {
		return nil
	}
	backup, found, err := readArchiveFile(w.ctx, w.logger, w.prepared.ArchivePath, hostidArchivePath)
	if err != nil {
		if restoreAbortOrInput(err) || w.ctx.Err() != nil {
			return err
		}
		w.skipHostid = true
		w.restoreHadWarnings = true
		w.logger.Warning("ZFS hostid - backup value not written: cannot read it from the backup (%v)", err)
		return nil
	}
	if !found {
		return nil
	}
	live, _ := restoreFS.ReadFile(filepath.Join(w.destRoot, hostidArchivePath))
	pools, listErr := listImportedZFSPools(w.ctx)
	if write, reason := hostidRestoreDecision(backup, live, pools, listErr); !write {
		w.skipHostid = true
		w.restoreHadWarnings = true
		w.logger.Warning("ZFS hostid - backup value %s not written: %s", formatHostid(backup), reason)
	}
	return nil
}
