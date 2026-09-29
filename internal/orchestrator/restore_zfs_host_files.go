package orchestrator

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

// decideZFSHostFiles sets skipHostid, skipZFSCaches and skipZFSConf before an
// extraction that would write the backup's /etc/hostid (writesHostid), its pool cache
// files (writesCaches) or its /etc/modprobe.d/zfs.conf (writesZFSConf) over this host.
// The archive is read once for all of them, and the pools are listed only when the
// archive holds the hostid or a cache file.
func (w *restoreUIWorkflowRun) decideZFSHostFiles(writesHostid, writesCaches, writesZFSConf bool) error {
	w.skipHostid, w.skipZFSCaches, w.skipZFSConf = false, false, false
	if !writesHostid && !writesCaches && !writesZFSConf {
		return nil
	}
	dir, cleanup, err := extractArchiveSubset(w.ctx, w.logger, w.prepared.ArchivePath,
		[]string{"./" + hostidArchivePath, "./" + zfsHostCachePaths[0], "./" + zfsHostCachePaths[1] + "/", "./" + zfsARCConfArchivePath})
	if err != nil {
		if restoreAbortOrInput(err) || w.ctx.Err() != nil {
			return err
		}
		w.skipHostid, w.skipZFSCaches, w.skipZFSConf = writesHostid, writesCaches, writesZFSConf
		w.restoreHadWarnings = true
		w.logger.Warning("ZFS host files - backup /etc/hostid, pool cache and zfs.conf not written: cannot read them from the backup (%v)", err)
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
	backupConf, confFound, _ := readExtractedFile(dir, zfsARCConfArchivePath)
	checkHostid := writesHostid && hostidFound
	checkCaches := writesCaches && len(caches) > 0
	if writesZFSConf && confFound {
		w.decideZFSARCConf(backupConf)
	}
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

// The services category restores /etc/modprobe.d, and with it zfs.conf, where the PVE
// installer writes zfs_arc_max as 10% of the RAM it finds. Measured: 3184 MiB on a
// 31.1 GiB host and 6403 MiB on a 62.5 GiB one, a file no package owns. Written onto a
// new machine, the old machine's limit caps the ARC on more RAM and takes memory from
// the guests on less, and the boot rebuild makes it hold from the first boot. A host
// with its own zfs.conf keeps it; one without gets the backup's, the only limit anyone
// chose for it. A kept backup copy goes to the export directory.
const zfsARCConfArchivePath = "etc/modprobe.d/zfs.conf"

// zfsARCConfRestoreDecision reports whether the backup's zfs.conf may be written: when
// it changes nothing, or when this host has none of its own.
func zfsARCConfRestoreDecision(backup, live []byte, liveFound bool) bool {
	return !liveFound || string(backup) == string(live)
}

var zfsARCMaxOption = regexp.MustCompile(`(?m)^[ \t]*options[ \t]+zfs[ \t].*\bzfs_arc_max=([0-9]+)`)

// formatZFSARCMax renders the zfs_arc_max a zfs.conf sets, in MiB, or "not set".
func formatZFSARCMax(conf []byte) string {
	m := zfsARCMaxOption.FindSubmatch(conf)
	if m == nil {
		return "not set"
	}
	n, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil {
		return string(m[1])
	}
	return fmt.Sprintf("%d MiB", n>>20)
}

// decideZFSARCConf sets skipZFSConf when this host has a zfs.conf of its own that
// differs from the backup's. A live file that exists but cannot be read counts as
// this host's own: keeping it never changes the host.
func (w *restoreUIWorkflowRun) decideZFSARCConf(backup []byte) {
	live, err := restoreFS.ReadFile(filepath.Join(w.destRoot, zfsARCConfArchivePath))
	liveFound := err == nil || !errors.Is(err, os.ErrNotExist)
	if zfsARCConfRestoreDecision(backup, live, liveFound) {
		return
	}
	w.skipZFSConf = true
	w.restoreHadWarnings = true
	w.logger.Warning("ZFS ARC limit - backup /%s not written: this host keeps its own (zfs_arc_max %s here, %s in the backup)%s",
		zfsARCConfArchivePath, formatZFSARCMax(live), formatZFSARCMax(backup), w.exportKeptHostFile(zfsARCConfArchivePath, backup))
}

// exportKeptHostFile writes a backup file this host kept its own copy of into the
// export directory, so the old value stays at hand, and returns the clause naming
// where it went, or why it did not.
func (w *restoreUIWorkflowRun) exportKeptHostFile(entry string, data []byte) string {
	if w.exportRoot == "" {
		w.exportRoot = exportDestRoot(w.cfg.BaseDir)
	}
	dest := filepath.Join(w.exportRoot, entry)
	if err := restoreFS.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Sprintf("; its copy could not be exported (%v)", err)
	}
	if err := restoreFS.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Sprintf("; its copy could not be exported (%v)", err)
	}
	return "; the backup's copy is in " + dest
}
