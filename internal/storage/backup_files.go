package storage

import (
	"errors"
	"strings"
)

const bundleSuffix = ".bundle.tar"

// errBackupSidecarDeleteOnly marks a delete where the backup data archive itself
// was removed but one or more associated sidecar files (.sha256/.metadata) could
// not be. Retention counting treats it as a successful deletion (the archive IS
// gone) rather than over-reporting freed space or remaining backups.
var errBackupSidecarDeleteOnly = errors.New("backup archive deleted; associated file(s) could not be removed")

// isBackupSidecar reports whether a candidate path is an associated sidecar
// (checksum/metadata) rather than the backup data archive itself.
func isBackupSidecar(path string) bool {
	return strings.HasSuffix(path, ".sha256") || strings.HasSuffix(path, ".metadata")
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

	files := make([]string, 0, 5)
	if includeBundle {
		bundlePath := bundlePathFor(base)
		if add(bundlePath) {
			files = append(files, bundlePath)
		}
	}
	candidates := []string{
		base,
		base + ".sha256",
		base + ".metadata",
		base + ".metadata.sha256",
	}
	for _, c := range candidates {
		if add(c) {
			files = append(files, c)
		}
	}
	return files
}
