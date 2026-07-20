package storage

import (
	"errors"
	"path/filepath"
	"strings"
)

const bundleSuffix = ".bundle.tar"

// errBackupSidecarDeleteOnly marks a delete where the backup data archive itself
// was removed but one or more associated sidecar files (.sha256/.metadata) could
// not be. Retention counting treats it as a successful deletion (the archive IS
// gone) rather than over-reporting freed space or remaining backups.
var errBackupSidecarDeleteOnly = errors.New("backup archive deleted; associated file(s) could not be removed")

// isBackupSidecar reports whether a candidate path is an associated sidecar
// (checksum, metadata, or manifest) rather than the backup data archive itself.
//
// Scope: this is the source of truth ONLY for CLASSIFICATION ("is this a standalone
// backup?"), shared by every storage List filter (local/secondary/cloud) and by the
// retention delete accounting. It is NOT the source of truth for the set of files a
// backup enumerates to create or delete.
//
// Anti-drift: adding a new sidecar suffix means updating THREE places, not one:
//  1. here (classification),
//  2. buildBackupCandidatePaths below (the delete/retention enumeration),
//  3. Orchestrator.removeAssociatedFiles in internal/orchestrator (raw-workspace cleanup).
//
// PS-BH-002 was exactly this drift: .manifest.json was added to the cloud upload set
// but omitted here, so retention could not delete it (orphan accumulation) and List
// counted it as a phantom backup.
func isBackupSidecar(path string) bool {
	return strings.HasSuffix(path, ".sha256") ||
		strings.HasSuffix(path, ".metadata") ||
		strings.HasSuffix(path, ".manifest.json")
}

// isBackupTempArtifact reports whether a candidate path is an in-flight temp or
// partial artifact (a .tmp-<...> temp copy, or a <name>.partial archive being
// written before promotion) rather than a completed backup. Such files must
// never be counted as backups by any List filter.
func isBackupTempArtifact(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, ".tmp-") || strings.HasSuffix(path, ".partial")
}

// trimBundleSuffix removes the .bundle.tar suffix from a path if present.
// It returns the trimmed path and whether the suffix was removed.
func trimBundleSuffix(path string) (string, bool) {
	if strings.HasSuffix(path, bundleSuffix) {
		return strings.TrimSuffix(path, bundleSuffix), true
	}
	return path, false
}

func normalizeBundleBasePath(path string) string {
	for {
		trimmed, ok := trimBundleSuffix(path)
		if !ok {
			return path
		}
		path = trimmed
	}
}

// bundlePathFor returns the canonical bundle path for either a raw archive path
// or a path that already points to a bundle.
func bundlePathFor(path string) string {
	return normalizeBundleBasePath(path) + bundleSuffix
}

// buildBackupCandidatePaths returns the list of files that belong to a backup.
// When includeBundle is true, both the bundle and the legacy single-file layout
// are included so retention can clean up either form.
func buildBackupCandidatePaths(base string, includeBundle bool) []string {
	base = normalizeBundleBasePath(base)
	seen := make(map[string]struct{})
	add := func(path string) bool {
		if path == "" {
			return false
		}
		if _, ok := seen[path]; ok {
			return false
		}
		seen[path] = struct{}{}
		return true
	}

	candidates := []string{
		base,
		base + ".sha256",
		base + ".manifest.json",
		base + ".metadata",
		base + ".metadata.sha256",
	}
	// Cap: every candidate plus at most one bundle path. Derived from len so it
	// cannot re-diverge when a suffix is added to the list above.
	files := make([]string, 0, len(candidates)+1)
	if includeBundle {
		bundlePath := bundlePathFor(base)
		if add(bundlePath) {
			files = append(files, bundlePath)
		}
	}
	for _, c := range candidates {
		if add(c) {
			files = append(files, c)
		}
	}
	return files
}
