package storage

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A CLOUD_REMOTE that is an absolute filesystem path without a colon (a mount point,
// "/mnt/cloud") is a local directory: rclone's local backend takes such a path as is,
// so every reference the cloud backend builds is a plain path, with no "<name>:"
// prefix. The rule is the one restore source discovery has always applied
// (orchestrator buildCloudRemotePath), and every reader of CLOUD_REMOTE goes through
// these helpers so the three readers cannot drift apart again.

// LocalCloudRemote returns CLOUD_REMOTE, trimmed and normalized with filepath.Clean
// (trailing and double slashes, ".." resolved), and whether it names a local
// directory: an absolute path that contains no colon. The remote form ("gdrive",
// "gdrive:path") returns false.
func LocalCloudRemote(cloudRemote string) (string, bool) {
	base := strings.TrimSpace(cloudRemote)
	if base == "" || !filepath.IsAbs(base) || strings.Contains(base, ":") {
		return "", false
	}
	return filepath.Clean(base), true
}

// LocalCloudRemoteDir is the directory the backups live in when CLOUD_REMOTE is a
// local directory: CLOUD_REMOTE joined with CLOUD_REMOTE_PATH, cleaned. ok is false for
// the remote form.
func LocalCloudRemoteDir(cloudRemote, cloudRemotePath string) (dir string, ok bool) {
	base, ok := LocalCloudRemote(cloudRemote)
	if !ok {
		return "", false
	}
	return filepath.Join(base, strings.TrimSpace(cloudRemotePath)), true
}

// LocalCloudLogDir is the cloud log directory when CLOUD_REMOTE is a local directory
// and CLOUD_LOG_PATH has no colon: CLOUD_LOG_PATH inside the CLOUD_REMOTE directory
// ("/mnt/cloud" + "/proxsave/log" = "/mnt/cloud/proxsave/log"), CLOUD_REMOTE_PATH not
// involved, the way the remote form puts it under the remote root. A CLOUD_LOG_PATH with
// a colon is a full rclone reference (legacy) and returns false, like the remote form.
func LocalCloudLogDir(cloudLogPath, cloudRemote string) (dir string, ok bool) {
	logPath := strings.TrimSpace(cloudLogPath)
	if logPath == "" || strings.Contains(logPath, ":") {
		return "", false
	}
	base, ok := LocalCloudRemote(cloudRemote)
	if !ok {
		return "", false
	}
	return filepath.Join(base, logPath), true
}

// validateLocalCloudRemote refuses, before it reaches rclone's argv, what cannot be a
// local directory: a path that is not absolute or that starts with "-". Everything
// else is normalized by LocalCloudRemote. The remote form keeps
// safeexec.ValidateRcloneRemoteName.
func validateLocalCloudRemote(dir string) error {
	switch {
	case strings.HasPrefix(dir, "-"):
		return fmt.Errorf("local directory must not start with '-'")
	case !filepath.IsAbs(dir):
		return fmt.Errorf("local directory must be an absolute path")
	}
	return nil
}

// isLocalRcloneRef reports whether an rclone reference is a local path: it starts with
// "/". A remote reference starts with the remote name, which holds no separator.
func isLocalRcloneRef(ref string) bool {
	return filepath.IsAbs(strings.TrimSpace(ref))
}

// LocalCloudLogOutside reports whether a CLOUD_LOG_PATH in the local form resolves
// outside the CLOUD_REMOTE directory, and returns that directory, cleaned. The
// CLOUD_REMOTE directory itself counts as inside. The remote form, and a CLOUD_LOG_PATH
// with a colon, are not checked and are never outside.
func LocalCloudLogOutside(cloudLogPath, cloudRemote string) (root string, outside bool) {
	dir, ok := LocalCloudLogDir(cloudLogPath, cloudRemote)
	if !ok {
		return "", false
	}
	root, _ = LocalCloudRemote(cloudRemote)
	return root, !pathWithin(dir, root)
}

// pathWithin reports whether the cleaned path p is root or lies under it.
func pathWithin(p, root string) bool {
	if p == root || root == "/" {
		return true
	}
	return strings.HasPrefix(p, root+"/")
}
