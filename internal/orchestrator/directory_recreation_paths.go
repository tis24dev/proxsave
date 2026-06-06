// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/logging"
)

func computeMissingDirs(target string) ([]string, error) {
	path := filepath.Clean(target)
	if isTerminalFilesystemPath(path) {
		return nil, nil
	}

	missing, err := collectMissingDirs(path)
	if err != nil {
		return nil, err
	}
	reverseStrings(missing)
	return missing, nil
}

func collectMissingDirs(path string) ([]string, error) {
	var missing []string
	for !isTerminalFilesystemPath(path) {
		exists, err := pathExistsForMissingDirs(path)
		if err != nil || exists {
			return missing, err
		}
		missing = append(missing, path)

		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return missing, nil
}

func pathExistsForMissingDirs(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func reverseStrings(values []string) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func isTerminalFilesystemPath(path string) bool {
	return path == "" || path == "." || path == "/"
}

func isConfirmableDatastoreMountRoot(path string) bool {
	path = filepath.Clean(path)
	switch {
	case strings.HasPrefix(path, "/mnt/"):
		return true
	case strings.HasPrefix(path, "/media/"):
		return true
	case strings.HasPrefix(path, "/run/media/"):
		return true
	default:
		return false
	}
}

func isSuspiciousDatastoreMountLocation(path string) bool {
	return isConfirmableDatastoreMountRoot(path)
}

func isPathOnRootFilesystem(path string) (bool, string, error) {
	rootDev, err := deviceID("/")
	if err != nil {
		return false, "/", err
	}

	existing, err := nearestExistingPath(path)
	if err != nil {
		return false, "", err
	}
	targetDev, err := deviceID(existing)
	if err != nil {
		return false, existing, err
	}
	return rootDev == targetDev, existing, nil
}

func nearestExistingPath(target string) (string, error) {
	path := filepath.Clean(target)
	if path == "" || path == "." {
		return "", fmt.Errorf("invalid path")
	}

	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(path)
		if parent == path {
			return path, nil
		}
		path = parent
	}
}

func deviceID(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, fmt.Errorf("unsupported stat type for %s", path)
	}
	return uint64(stat.Dev), nil
}

// isLikelyZFSMountPoint checks if a path is likely a ZFS mount point
func isLikelyZFSMountPoint(path string, logger *logging.Logger) bool {
	if _, err := os.Stat(zpoolCachePath); err != nil {
		return false
	}

	pathLower := strings.ToLower(path)
	if isCommonZFSMountPath(pathLower) {
		logger.Debug("Path %s matches ZFS mount point pattern", path)
		return true
	}
	return false
}

func isCommonZFSMountPath(pathLower string) bool {
	pathLower = filepath.Clean(strings.ToLower(pathLower))
	if !strings.HasPrefix(pathLower, "/") {
		return false
	}
	return strings.HasPrefix(pathLower, "/mnt/") ||
		hasPathSegment(pathLower, "backup") ||
		hasPathSegment(pathLower, "datastore")
}

func hasPathSegment(path, segment string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == segment {
			return true
		}
	}
	return false
}

// rootFilesystemMountShouldSkip reports whether directory creation should be
// skipped because the target resolves to the root filesystem while looking like a
// dedicated/ZFS mount that is not yet mounted and holds no data we would risk.
// Shared by PVE storage and PBS datastore recreation so both use one decision.
func rootFilesystemMountShouldSkip(onRootFS, suspicious, hasData, dataUnknown bool) bool {
	if !onRootFS || !suspicious {
		return false
	}
	return dataUnknown || !hasData
}

// shouldSkipUnmountedStorageMount decides, from the path alone, whether to skip
// recreating directories because basePath looks like a dedicated/ZFS mount that
// is not yet mounted and currently resolves to the root filesystem. Creating
// directories there would shadow the real storage once it is mounted. hasData
// signals the caller already found real content at basePath (then we do not skip).
// It composes the same shared primitives the PBS datastore guard uses.
func shouldSkipUnmountedStorageMount(basePath string, hasData bool, logger *logging.Logger) bool {
	zfsLikely := isLikelyZFSMountPoint(basePath, logger)
	if shouldSkipMissingZFSMountPoint(basePath, zfsLikely, logger) {
		return true
	}
	onRootFS, _, err := isPathOnRootFilesystem(basePath)
	if err != nil {
		return false
	}
	suspicious := isSuspiciousDatastoreMountLocation(basePath) || zfsLikely
	if rootFilesystemMountShouldSkip(onRootFS, suspicious, hasData, false) {
		logger.Warning("Storage mount preflight: %s resolves to the root filesystem (mount missing?) — skipping directory creation to avoid writing to the wrong disk; mount the storage disk / import the pool first", basePath)
		return true
	}
	return false
}

func isIgnorableOwnershipError(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EROFS)
}
