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
// Fail-closed only when neither source yields a host, and whenever this machine
// cannot name itself: retention then leaves the entry alone rather than delete on a
// guess. Legacy "proxmox-backup-*" names have no token and land here.
func backupBelongsToHost(meta *types.BackupMetadata, hostname string) bool {
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
	return strings.EqualFold(owner, hostname)
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
func scopeRetentionToHost(backups []*types.BackupMetadata, hostname string) (owned, foreign []*types.BackupMetadata) {
	for _, b := range backups {
		if b == nil {
			continue
		}
		if backupBelongsToHost(b, hostname) {
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
// backups (correct, and the point of this filter), but a manifest that cannot be
// read lands here too, and that must not look like silence.
func applyRetentionHostScope(location, hostname string, backups []*types.BackupMetadata, logger retentionScopeLogger) []*types.BackupMetadata {
	if strings.TrimSpace(hostname) == "" {
		if logger != nil {
			logger.Warning("%s: the local hostname is unknown; retention will not delete anything this run", location)
		}
		return nil
	}

	owned, foreign := scopeRetentionToHost(backups, hostname)
	if len(foreign) > 0 && logger != nil {
		logger.Warning("%s: retention ignored %d backup(s) that do not belong to %s (other hosts, or no readable manifest)", location, len(foreign), hostname)
		for _, b := range foreign {
			logger.Debug("%s: retention out of scope: %s (manifest hostname=%q)", location, b.BackupFile, b.Hostname)
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
