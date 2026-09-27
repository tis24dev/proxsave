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

// decideZFSHostFiles sets skipHostid and skipZFSCaches before an extraction that
// would write the backup's /etc/hostid (writesHostid) or its pool cache files
// (writesCaches) over this host. The archive is read once for all of them, and the
// pools are listed only when the archive holds one of the files.
func (w *restoreUIWorkflowRun) decideZFSHostFiles(writesHostid, writesCaches bool) error {
	w.skipHostid, w.skipZFSCaches = false, false
	if !writesHostid && !writesCaches {
		return nil
	}
	dir, cleanup, err := extractArchiveSubset(w.ctx, w.logger, w.prepared.ArchivePath,
		[]string{"./" + hostidArchivePath, "./" + zfsHostCachePaths[0], "./" + zfsHostCachePaths[1] + "/"})
	if err != nil {
		if restoreAbortOrInput(err) || w.ctx.Err() != nil {
			return err
		}
		w.skipHostid, w.skipZFSCaches = writesHostid, writesCaches
		w.restoreHadWarnings = true
		w.logger.Warning("ZFS host files - backup /etc/hostid and pool cache not written: cannot read them from the backup (%v)", err)
		return nil
	}
	defer cleanup()

	backupHostid, hostidFound, _ := readExtractedFile(dir, hostidArchivePath)
	var caches []string
	for _, p := range zfsHostCachePaths {
		if bootPathExists(filepath.Join(dir, p)) {
			caches = append(caches, filepath.Base(p))
		}
	}
	checkHostid := writesHostid && hostidFound
	checkCaches := writesCaches && len(caches) > 0
	if !checkHostid && !checkCaches {
		return nil
	}

	pools, listErr := listImportedZFSPools(w.ctx)
	if checkHostid {
		live, _ := restoreFS.ReadFile(filepath.Join(w.destRoot, hostidArchivePath))
		if write, reason := hostidRestoreDecision(backupHostid, live, pools, listErr); !write {
			w.skipHostid = true
			w.restoreHadWarnings = true
			w.logger.Warning("ZFS hostid - backup value %s not written: %s", formatHostid(backupHostid), reason)
		}
	}
	if checkCaches {
		if write, reason := zfsCacheRestoreDecision(pools, listErr); !write {
			w.skipZFSCaches = true
			w.restoreHadWarnings = true
			w.logger.Warning("ZFS pool cache - backup %s not written: %s", strings.Join(caches, " and "), reason)
		}
	}
	return nil
}

// zfsHostCachePaths describe the pools of the host that wrote them: zpool.cache is
// the list zfs-import-cache.service imports at boot, zfs-list.cache the datasets
// the mount generator turns into mount units. Reproduced on a PVE 9.2 VM with root
// on ZFS and a data pool: another host's zpool.cache made zfs-import-cache.service
// fail ("cannot import 'rpool': pool already exists") and the data pool was not
// imported at boot. PVE brings a pool back when it is a zfspool storage (pvestatd
// imports it, with cachefile=none, so never into the cache again); a pool that is
// not one, such as a PBS datastore or a manual mount, stays out.
var zfsHostCachePaths = []string{"etc/zfs/zpool.cache", "etc/zfs/zfs-list.cache"}

func isZFSHostCachePath(clean string) bool {
	return matchesAnyArchivePrefix(clean, zfsHostCachePaths)
}

// zfsCacheRestoreDecision reports whether the backup's pool cache files may be
// written: only on a host with no pool imported, whose own cache has nothing to
// lose and where the backup's lets disks moved from the old host import at boot.
func zfsCacheRestoreDecision(pools []string, listErr error) (write bool, reason string) {
	if listErr != nil {
		return false, fmt.Sprintf("this host's pools cannot be listed (%v)", listErr)
	}
	if len(pools) == 0 {
		return true, ""
	}
	sorted := append([]string{}, pools...)
	sort.Strings(sorted)
	return false, fmt.Sprintf("pools %s are imported on this host, and at boot zfs-import-cache stops on another host's list",
		strings.Join(sorted, ", "))
}
