// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/logging"
)

var pbsDatastoreSubdirs = []string{".chunks", ".index"}

type pbsDatastorePreflight struct {
	basePath        string
	datastoreName   string
	existingPath    string
	zfsLikely       bool
	onRootFS        bool
	hasData         bool
	dataUnknown     bool
	suspiciousMount bool
}

// createPBSDatastoreStructure creates the directory structure for a PBS datastore.
// It returns true when ProxSave made filesystem changes for this datastore path.
func createPBSDatastoreStructure(basePath, datastoreName string, logger *logging.Logger) (bool, error) {
	done := logging.DebugStart(logger, "pbs datastore directory recreation", "datastore=%s path=%s", datastoreName, basePath)
	var err error
	defer func() { done(err) }()

	zfsLikely := isLikelyZFSMountPoint(basePath, logger)
	if shouldSkipMissingZFSMountPoint(basePath, zfsLikely, logger) {
		return false, nil
	}

	preflight := inspectPBSDatastore(basePath, datastoreName, zfsLikely, logger)
	if shouldSkipUnsafePBSDatastore(preflight, logger) {
		return false, nil
	}

	changed, err := initializePBSDatastore(basePath, datastoreName, logger)
	if err != nil {
		return false, err
	}
	return changed, nil
}

func shouldSkipMissingZFSMountPoint(basePath string, zfsLikely bool, logger *logging.Logger) bool {
	if !zfsLikely {
		return false
	}
	_, statErr := os.Stat(basePath)
	if statErr == nil {
		return false
	}
	if os.IsNotExist(statErr) {
		logger.Warning("PBS datastore preflight: %s looks like a ZFS mountpoint and does not exist yet; skipping directory creation to avoid shadowing a not-yet-imported pool", basePath)
		return true
	}
	logger.Warning("PBS datastore preflight: unable to stat potential ZFS mountpoint %s: %v; skipping any datastore filesystem changes", basePath, statErr)
	return true
}

func inspectPBSDatastore(basePath, datastoreName string, zfsLikely bool, logger *logging.Logger) pbsDatastorePreflight {
	preflight := pbsDatastorePreflight{
		basePath:        basePath,
		datastoreName:   datastoreName,
		zfsLikely:       zfsLikely,
		suspiciousMount: isSuspiciousDatastoreMountLocation(basePath) || zfsLikely,
	}

	preflight.hasData, preflight.dataUnknown = inspectPBSDatastoreData(basePath, logger)
	preflight.onRootFS, preflight.existingPath = inspectPBSDatastoreDevice(basePath, logger)
	logPBSDatastorePreflight(preflight, logger)
	return preflight
}

func inspectPBSDatastoreData(basePath string, logger *logging.Logger) (bool, bool) {
	hasData, err := pbsDatastoreHasData(basePath)
	if err == nil {
		return hasData, false
	}
	logger.Warning("PBS datastore preflight: unable to determine whether %s contains datastore data: %v", basePath, err)
	return false, true
}

func inspectPBSDatastoreDevice(basePath string, logger *logging.Logger) (bool, string) {
	onRootFS, existingPath, err := isPathOnRootFilesystem(basePath)
	if err == nil {
		return onRootFS, existingPath
	}
	logger.Warning("PBS datastore preflight: unable to determine filesystem device for %s: %v", basePath, err)
	return false, existingPath
}

func logPBSDatastorePreflight(preflight pbsDatastorePreflight, logger *logging.Logger) {
	logging.DebugStep(
		logger,
		"pbs datastore preflight",
		"path=%s existing=%s on_rootfs=%t has_data=%t data_unknown=%t",
		preflight.basePath,
		preflight.existingPath,
		preflight.onRootFS,
		preflight.hasData,
		preflight.dataUnknown,
	)
}

func shouldSkipUnsafePBSDatastore(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if shouldSkipRootFilesystemDatastore(preflight, logger) {
		return true
	}
	if shouldSkipUnknownDatastoreData(preflight, logger) {
		return true
	}
	if shouldSkipExistingDatastoreData(preflight, logger) {
		return true
	}
	return shouldSkipUnexpectedDatastoreEntries(preflight.basePath, logger)
}

func shouldSkipRootFilesystemDatastore(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if !preflight.onRootFS || !preflight.suspiciousMount || (!preflight.dataUnknown && preflight.hasData) {
		return false
	}

	logger.Warning("PBS datastore preflight: %s resolves to the root filesystem (mount missing?) — skipping datastore directory initialization to avoid writing to the wrong disk", preflight.basePath)
	logger.Info("Mount/import the datastore disk/pool first, then restart PBS services.")
	if _, err := os.Stat(zpoolCachePath); err == nil {
		logger.Info("ZFS detected: if this datastore was on ZFS, you may need to import the pool first (e.g. `zpool import` then `zpool import <pool-name>`).")
	}
	return true
}

func shouldSkipUnknownDatastoreData(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if !preflight.dataUnknown {
		return false
	}
	logger.Warning("PBS datastore preflight: datastore path inspection failed — skipping any datastore filesystem changes to avoid risking existing data")
	return true
}

func shouldSkipExistingDatastoreData(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if !preflight.hasData {
		return false
	}
	if warn := validatePBSDatastoreReadOnly(preflight.basePath); warn != "" {
		logger.Warning("PBS datastore preflight: %s", warn)
	}
	logger.Info("PBS datastore preflight: datastore %s appears to contain data; skipping directory/permission changes to avoid risking datastore contents", preflight.datastoreName)
	return true
}

func shouldSkipUnexpectedDatastoreEntries(basePath string, logger *logging.Logger) bool {
	unexpected, err := pbsDatastoreHasUnexpectedEntries(basePath)
	if err != nil {
		logger.Warning("PBS datastore preflight: unable to inspect %s contents: %v; skipping any datastore filesystem changes to avoid risking unrelated data", basePath, err)
		return true
	}
	if unexpected {
		logger.Warning("PBS datastore preflight: %s is not empty (unexpected entries present); skipping any datastore filesystem changes to avoid risking unrelated data", basePath)
		return true
	}
	return false
}

func initializePBSDatastore(basePath, datastoreName string, logger *logging.Logger) (bool, error) {
	dirsToFix, err := computeMissingDirs(basePath)
	if err != nil {
		return false, fmt.Errorf("compute missing dirs: %w", err)
	}

	if err := os.MkdirAll(basePath, 0750); err != nil {
		return false, fmt.Errorf("create base directory: %w", err)
	}
	changed := len(dirsToFix) > 0

	subdirChanged, dirsToFix, err := ensurePBSDatastoreSubdirs(basePath, dirsToFix)
	if err != nil {
		return false, fmt.Errorf("create datastore subdirectories: %w", err)
	}
	applyPBSDatastoreOwnership(basePath, datastoreName, dirsToFix, logger)

	lockChanged, lockErr := ensurePBSDatastoreLockFile(basePath, logger)
	if lockErr != nil {
		logger.Warning("PBS datastore lock file: %v", lockErr)
	}

	return changed || subdirChanged || lockChanged, nil
}

func ensurePBSDatastoreSubdirs(basePath string, dirsToFix []string) (bool, []string, error) {
	changed := false
	for _, subdir := range pbsDatastoreSubdirs {
		path := filepath.Join(basePath, subdir)
		if isMissingPath(path) {
			changed = true
			dirsToFix = append(dirsToFix, path)
		}
		if err := os.MkdirAll(path, 0750); err != nil {
			return changed, dirsToFix, fmt.Errorf("create %s: %w", path, err)
		}
	}
	return changed, dirsToFix, nil
}

func applyPBSDatastoreOwnership(basePath, datastoreName string, dirsToFix []string, logger *logging.Logger) {
	if len(dirsToFix) > 0 {
		logger.Debug("PBS datastore permissions: applying ownership to %d created path(s) (datastore=%s path=%s)", len(dirsToFix), datastoreName, basePath)
	}
	baseProcessed := false
	cleanBasePath := filepath.Clean(basePath)
	for _, dir := range dirsToFix {
		if err := setDatastoreOwnership(dir, logger); err != nil {
			logger.Warning("Could not set datastore ownership for %s: %v", dir, err)
		}
		if filepath.Clean(dir) == cleanBasePath {
			baseProcessed = true
		}
	}
	if baseProcessed {
		return
	}
	if err := setDatastoreOwnership(basePath, logger); err != nil {
		logger.Warning("Could not set datastore ownership for %s: %v", basePath, err)
	}
}

func isMissingPath(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}
