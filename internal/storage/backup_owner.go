package storage

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// retentionHostname is a seam so a test can pin the local hostname.
var retentionHostname = os.Hostname

// resolveRetentionHostname reads the local hostname once, at storage construction,
// so every retention pass of that backend uses a stable value. It is resolved
// per-instance rather than per-call because the tests run in parallel and a shared
// mutable global would let one backend's fixture host leak into another's.
func resolveRetentionHostname() string {
	host, err := retentionHostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(host)
}

// backupBelongsToHost reports whether a listed backup was produced by the given
// host, using the manifest's "hostname" - the same authoritative value the restore
// selector renders as its Hostname column (formatBackupCandidateHostname). The
// filename is never parsed for this: it merely embeds the host token, and matching
// it with a wildcard (the "*-backup-*" glob, or the "-backup-" substring on the
// cloud side) accepts every host, which is exactly how retention ended up able to
// delete another machine's archives.
//
// The manifest is preferred but not required. When it cannot be read - a corrupt
// or missing .metadata next to an archive that still carries a valid .sha256 - the
// owner falls back to the host token the filename embeds ("<host>-backup-<ts>").
// That fallback matters: such a backup counts as Verified today and IS pruned, so
// refusing to attribute it would silently stop rotating it and let the location
// grow without bound. The token is still host-specific, so the cross-host guarantee
// survives the degradation.
//
// A machine does not always spell its own name the same way. The writer records
// what "hostname -f" returns (pve.home.arpa) while os.Hostname() reports the
// kernel short name (pve), so retention is told both: hostname is what the kernel
// reports and aliases are the names this run's writer actually stamped into
// archives. Ownership is still an exact match against one of those names. It is
// never a fold to the first label: a host that cannot resolve its own domain must
// not claim "pve.siteB.example" just because it is called "pve", or one machine's
// retention would prune another machine's archives.
//
// Fail-closed only when neither source yields a host, and whenever this machine
// cannot name itself: retention then leaves the entry alone rather than delete on a
// guess. Legacy "proxmox-backup-*" names have no token and land here.
func backupBelongsToHost(meta *types.BackupMetadata, hostname string, aliases ...string) bool {
	if meta == nil {
		return false
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return false
	}
	if isLegacyBackupName(meta.BackupFile) && strings.TrimSpace(meta.Hostname) == "" {
		return true
	}
	owner := backupOwnerHost(meta)
	if owner == "" {
		return false
	}
	return hostOwnsName(owner, hostname, aliases...)
}

// unresolvedHostname is what the writer stamps into an archive when the machine
// could not name itself (resolveHostname falls back to it). It names no host, so it
// never joins the set of names retention answers to: two machines that both failed
// to resolve would otherwise each claim the other's archives.
const unresolvedHostname = "unknown"

// retentionHostAliases reduces the names this run's writer used into the extra names
// retention accepts as this machine's own. Blanks, the "unknown" sentinel and
// repeats of the local hostname are dropped, so a machine with no domain ends up
// with no aliases and keeps exactly the strict behaviour it has today.
func retentionHostAliases(local string, written []string) []string {
	localKey := types.NormalizeHostname(local)
	var aliases []string
	for _, name := range written {
		key := types.NormalizeHostname(name)
		if key == "" || key == unresolvedHostname || key == localKey {
			continue
		}
		duplicate := false
		for _, seen := range aliases {
			if seen == key {
				duplicate = true
				break
			}
		}
		if !duplicate {
			aliases = append(aliases, key)
		}
	}
	return aliases
}

// hostOwnsName reports whether owner is one of the names this machine answers to:
// the name the kernel reports plus the names this run's writer stamped into the
// archives it produced. The match is exact once spelling is normalised. It is
// deliberately not a fold to the first label: "pve" and "pve.siteB.example" are two
// machines unless this machine itself answers to both, and folding them would let
// one host's retention prune the other's archives.
func hostOwnsName(owner, hostname string, aliases ...string) bool {
	key := types.NormalizeHostname(owner)
	if key == "" {
		return false
	}
	if key == types.NormalizeHostname(hostname) {
		return true
	}
	for _, alias := range aliases {
		// The sentinel is refused again here, not only in retentionHostAliases: an
		// alias is a name the writer produced, and "unknown" is what it produces
		// when it could not name the machine at all. Two machines that both failed
		// to resolve must not become owners of each other's archives, whichever
		// path assembled the list. The local hostname is left alone, so a machine
		// the kernel really does call "unknown" keeps rotating its own archives
		// exactly as it does today.
		if alias := types.NormalizeHostname(alias); alias != "" && alias != unresolvedHostname && key == alias {
			return true
		}
	}
	return false
}

// hostShortLabel returns the first label of a hostname, or the whole string when it
// carries no domain. Reporting only: ownership never consults it. A leading dot is
// degenerate input and keeps the whole string, so malformed names do not all
// collapse onto one empty label.
func hostShortLabel(host string) string {
	if idx := strings.IndexByte(host, '.'); idx > 0 {
		return host[:idx]
	}
	return host
}

// retentionSpellingMismatches counts entries that look like this host's own work
// under a different spelling of its name: the owner shares the local short label
// without being one of the names this machine answers to. They are usually archives
// written while "hostname -f" resolved and it no longer does, so they have stopped
// rotating. They are reported, never claimed: from here they are indistinguishable
// from a second machine with the same short name, and claiming them would delete
// that machine's backups.
func retentionSpellingMismatches(foreign []*types.BackupMetadata, hostname string) int {
	local := hostShortLabel(types.NormalizeHostname(hostname))
	if local == "" {
		return 0
	}
	count := 0
	for _, b := range foreign {
		if b == nil {
			continue
		}
		if hostShortLabel(types.NormalizeHostname(backupOwnerHost(b))) == local {
			count++
		}
	}
	return count
}

// backupOwnerHost resolves the owning host of a listed backup: the manifest's
// "hostname" when it was readable, otherwise the token the filename embeds.
func backupOwnerHost(meta *types.BackupMetadata) string {
	if owner := strings.TrimSpace(meta.Hostname); owner != "" {
		return owner
	}
	if isLegacyBackupName(meta.BackupFile) {
		// "proxmox-backup-<ts>" predates the "<host>-backup-<ts>" scheme, so its
		// leading token is the product name, not a host. Reading it as one would
		// attribute every legacy archive to a machine called "proxmox" and stop
		// rotating them everywhere else, turning a deletion bug into an
		// unbounded-growth one. They stay owned by whoever lists them.
		return ""
	}
	if host, _, ok := extractLogKeyFromBackup(meta.BackupFile); ok {
		return strings.TrimSpace(host)
	}
	return ""
}

// isLegacyBackupName reports whether the archive uses the pre-Go naming scheme.
func isLegacyBackupName(backupFile string) bool {
	return strings.HasPrefix(filepath.Base(strings.TrimSpace(backupFile)), legacyBackupPrefix)
}

// legacyBackupPrefix is the pre-Go archive name, which carries no host token.
const legacyBackupPrefix = "proxmox-backup-"

// scopeRetentionToHost splits a listing into the entries this host owns and the
// ones it must not touch. Every backend runs its retention through this so the
// three storage locations behave identically on a directory - or a remote prefix -
// that several ProxSave hosts share.
func scopeRetentionToHost(backups []*types.BackupMetadata, hostname string, aliases ...string) (owned, foreign []*types.BackupMetadata) {
	for _, b := range backups {
		if b == nil {
			continue
		}
		if backupBelongsToHost(b, hostname, aliases...) {
			owned = append(owned, b)
			continue
		}
		foreign = append(foreign, b)
	}
	return owned, foreign
}

// retentionScopeLogger is the subset of the logger the scope reporting needs.
type retentionScopeLogger interface {
	Warning(format string, args ...interface{})
	Debug(format string, args ...interface{})
}

// applyRetentionHostScope narrows a listing to this host's own backups and reports
// what it declined to consider. The exclusions are logged at WARNING because they
// change what retention will prune: on a shared location they are other machines'
// backups (correct, and the point of this filter), but two other kinds land here
// too, and neither must look like silence. An archive whose name and manifest name
// no host cannot be attributed at all, and an archive written under a spelling of
// this host's own name that this run cannot confirm ("hostname -f" resolved then and
// does not now) is left alone rather than claimed on a guess.
func applyRetentionHostScope(location, hostname string, aliases []string, backups []*types.BackupMetadata, logger retentionScopeLogger) []*types.BackupMetadata {
	if strings.TrimSpace(hostname) == "" {
		if logger != nil {
			logger.Warning("%s: the local hostname is unknown; retention will not delete anything this run", location)
		}
		return nil
	}

	owned, foreign := scopeRetentionToHost(backups, hostname, aliases...)
	if len(foreign) > 0 && logger != nil {
		logger.Warning("%s: retention ignored %d backup(s) that do not belong to %s (other hosts, or a name that carries no host)", location, len(foreign), hostname)
		if n := retentionSpellingMismatches(foreign, hostname); n > 0 {
			logger.Warning("%s: %d of those carry this host's short name spelled differently (an FQDN, or another machine with the same short name); retention leaves them alone rather than guess, so they are no longer rotating. Check that \"hostname -f\" resolves the way it did when they were written.", location, n)
		}
		logger.Debug("%s: retention answers to %s", location, strings.Join(append([]string{hostname}, aliases...), ", "))
		for _, b := range foreign {
			logger.Debug("%s: retention out of scope: %s (owner=%q, manifest hostname=%q)", location, b.BackupFile, backupOwnerHost(b), b.Hostname)
		}
	}
	return owned
}

// manifestHostnameFromLocalArchive returns the owning host recorded in the manifest
// that sits next to (or inside) a local-filesystem archive. It mirrors what
// LocalStorage.loadMetadata already does, so the secondary location - which lists
// with stat() only and never opened a manifest - can attribute its backups the same
// way without duplicating the bundle/sidecar handling.
//
// Best-effort by design: "" means "cannot attribute", which backupBelongsToHost
// then treats as not-ours. Bounded through safefs so a dead or stale secondary
// mount cannot wedge the retention pass.
func manifestHostnameFromLocalArchive(ctx context.Context, archivePath string, timeout time.Duration) string {
	if strings.HasSuffix(archivePath, bundleSuffix) {
		manifest, err := safefs.Run(ctx, "bundle-manifest-host", archivePath, timeout, func() (*backup.Manifest, error) {
			return manifestFromBundle(archivePath)
		})
		if err != nil || manifest == nil {
			return ""
		}
		return strings.TrimSpace(manifest.Hostname)
	}

	metadataFile := archivePath + ".metadata"
	if _, err := safefs.Stat(ctx, metadataFile, timeout); err != nil {
		return ""
	}
	manifest, err := safefs.Run(ctx, "manifest-host", metadataFile, timeout, func() (*backup.Manifest, error) {
		return backup.LoadManifest(metadataFile)
	})
	if err != nil || manifest == nil {
		return ""
	}
	return strings.TrimSpace(manifest.Hostname)
}

// manifestFromBundle extracts the manifest entry from a bundle tar.
//
// The bundle path is built from a directory listing, so it reaches os as a variable.
// It is opened through safefs.OpenFileUnderRoot, which confines the open to the
// bundle's own parent directory at the syscall level: gosec G304 is answered by the
// structure rather than by a suppression, and a final component that is an absolute
// symlink - or one escaping that directory - is refused instead of followed. Reading
// the parent directory is already a precondition here, since that is where the
// listing came from.
func manifestFromBundle(bundlePath string) (*backup.Manifest, error) {
	file, err := safefs.OpenFileUnderRoot(bundlePath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	tr := tar.NewReader(file)
	expectedName := strings.TrimSuffix(filepath.Base(bundlePath), bundleSuffix) + ".metadata"
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("metadata %s not found in bundle %s", expectedName, filepath.Base(bundlePath))
		}
		if err != nil {
			return nil, fmt.Errorf("read bundle %s: %w", filepath.Base(bundlePath), err)
		}
		if filepath.Base(hdr.Name) != expectedName {
			continue
		}
		var manifest backup.Manifest
		if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
			return nil, fmt.Errorf("parse manifest from bundle %s: %w", filepath.Base(bundlePath), err)
		}
		return &manifest, nil
	}
}
