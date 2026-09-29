package orchestrator

import (
	"strings"
	"sync"
)

// A backup taken under SYSTEM_ROOT_PREFIX before the collector wrote PVE files at their
// host paths holds them under the prefix: ./host/etc/pve/..., ./host/var/lib/pve-cluster/...
// (measured on a Debian 13 appliance: 48 PVE entries under ./host/, 723 system entries at
// their paths). No restore category matches them there. The archive does not record the
// prefix, so it is read off the paths, when the archive's metadata says it covered PVE
// (legacyPVEPrefixAllowed), and those entries are restored at their host paths.

// legacyPVERoots are the PVE paths the collector used to write under the prefix.
var legacyPVERoots = []string{"etc/pve", "var/lib/pve-cluster", "etc/corosync", "etc/vzdump.conf", "etc/ceph"}

// legacyPVEPrefixes maps an analyzed archive to the prefix its PVE files sit under, so
// every later extraction of the same archive restores them at their host paths.
var (
	legacyPVEPrefixesMu sync.Mutex
	legacyPVEPrefixes   = map[string]string{}
)

func rememberLegacyPVEPrefix(archivePath, prefix string) {
	legacyPVEPrefixesMu.Lock()
	defer legacyPVEPrefixesMu.Unlock()
	if prefix == "" {
		delete(legacyPVEPrefixes, archivePath)
		return
	}
	legacyPVEPrefixes[archivePath] = prefix
}

func legacyPVEPrefixFor(archivePath string) string {
	legacyPVEPrefixesMu.Lock()
	defer legacyPVEPrefixesMu.Unlock()
	return legacyPVEPrefixes[archivePath]
}

// legacyPVEPrefixAllowed reports whether the archive's own metadata allows the legacy
// layout: only the PVE recipe wrote PVE files under the prefix, so the backup must say
// it covered PVE (BACKUP_TYPE or BACKUP_TARGETS pve or dual). Without that evidence a
// single X/etc/pve is somebody's copy, for example an extracted PVE archive under a
// PBS host's /home, which every backup collects whole. An archive whose metadata is
// missing or unreadable gets no remap: every version with SYSTEM_ROOT_PREFIX (since
// 3808e6c) writes BACKUP_TYPE.
func legacyPVEPrefixAllowed(metadata *restoreDecisionMetadata, metadataErr error) bool {
	if metadata == nil || metadataErr != nil {
		return false
	}
	return metadata.BackupType.SupportsPVE() || parseSystemTargets(metadata.BackupTargets).SupportsPVE()
}

// detectLegacyPVEPrefix returns the prefix the archive's /etc/pve sits under: the one
// directory X with X/etc/pve entries, when no entry sits at etc/pve itself. Anything else
// (a canonical etc/pve, no etc/pve at all, two candidates) returns "".
func detectLegacyPVEPrefix(archivePaths []string) string {
	candidates := map[string]bool{}
	for _, name := range archivePaths {
		clean := normalizeRestoreEntryPath(name)
		if clean == "etc/pve" || strings.HasPrefix(clean, "etc/pve/") {
			return ""
		}
		if idx := strings.Index(clean, "/etc/pve/"); idx > 0 {
			candidates[clean[:idx]] = true
		} else if strings.HasSuffix(clean, "/etc/pve") {
			candidates[strings.TrimSuffix(clean, "/etc/pve")] = true
		}
	}
	if len(candidates) != 1 {
		return ""
	}
	for prefix := range candidates {
		return prefix
	}
	return ""
}

// remapLegacyPVEEntry returns name with the prefix dropped when it names one of the PVE
// paths under it, keeping a leading "./" or "/". underPrefix reports an entry inside the
// prefix that names no PVE path: the prefix's own directories (./host/, ./host/etc/),
// which exist only because the PVE files were written under them.
func remapLegacyPVEEntry(name, prefix string) (mapped string, underPrefix bool) {
	if prefix == "" {
		return name, false
	}
	lead, rest := "", name
	switch {
	case strings.HasPrefix(rest, "./"):
		lead, rest = "./", rest[2:]
	case strings.HasPrefix(rest, "/"):
		lead, rest = "/", rest[1:]
	}
	trimmed := strings.TrimSuffix(rest, "/")
	if trimmed == prefix {
		return name, true
	}
	if !strings.HasPrefix(rest, prefix+"/") {
		return name, false
	}
	tail := rest[len(prefix)+1:]
	for _, root := range legacyPVERoots {
		if tail == root || strings.HasPrefix(tail, root+"/") {
			return lead + tail, false
		}
	}
	return name, true
}
