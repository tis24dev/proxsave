// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/logging"
)

// pbsDatastoreSubdirs are the directories PBS creates in a datastore
// (ChunkStore::create): only the chunk store. Index files live in the snapshot
// directories, there is no .index directory.
var pbsDatastoreSubdirs = []string{".chunks"}

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

	if err = validateRecreationPath(basePath); err != nil {
		err = fmt.Errorf("unsafe datastore path %q from datastore.cfg: %w", basePath, err)
		return false, err
	}

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
		logger.Warning("Storage mount preflight: %s looks like a ZFS mountpoint and does not exist yet; skipping directory creation to avoid shadowing a not-yet-imported pool", basePath)
		return true
	}
	logger.Warning("Storage mount preflight: unable to stat potential ZFS mountpoint %s: %v; skipping any filesystem changes", basePath, statErr)
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
	if preflight.hasData {
		// A chunk store left incomplete, which initializePBSDatastore completes.
		return false
	}
	return shouldSkipUnexpectedDatastoreEntries(preflight.basePath, logger)
}

func shouldSkipRootFilesystemDatastore(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if !rootFilesystemMountShouldSkip(preflight.onRootFS, preflight.suspiciousMount, preflight.hasData, preflight.dataUnknown) {
		return false
	}

	logger.Warning("PBS datastore preflight: %s resolves to the root filesystem (mount missing?), skipping datastore directory initialization to avoid writing to the wrong disk", preflight.basePath)
	logger.Info("PBS datastore - left uninitialized until its disk or pool is mounted at %s", preflight.basePath)
	if _, err := os.Stat(zpoolCachePath); err == nil {
		logger.Info("ZFS detected: a datastore on a ZFS pool is available only once that pool is imported")
	}
	return true
}

func shouldSkipUnknownDatastoreData(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if !preflight.dataUnknown {
		return false
	}
	logger.Warning("PBS datastore preflight: datastore path inspection failed; skipping any datastore filesystem changes to avoid risking existing data")
	return true
}

func shouldSkipExistingDatastoreData(preflight pbsDatastorePreflight, logger *logging.Logger) bool {
	if !preflight.hasData {
		return false
	}
	// PBS writes .lock when it creates a chunk store, and ProxSave last, after the
	// hash directories: entries in .chunks without .lock are a creation that
	// stopped half-way (disk or inodes exhausted, the run killed). The missing
	// directories and .lock are created; nothing there is removed.
	if _, err := os.Lstat(filepath.Join(preflight.basePath, ".lock")); os.IsNotExist(err) {
		logger.Debug("PBS datastore preflight: datastore=%s .chunks has entries, .lock missing action=complete", preflight.datastoreName)
		logger.Info("Datastore %s initialization is incomplete, its missing directories are created", preflight.datastoreName)
		return false
	}
	if warn := validatePBSDatastoreReadOnly(preflight.basePath); warn != "" {
		logger.Warning("PBS datastore preflight: %s", warn)
	}
	// has_data only means .chunks has entries, which every datastore PBS created
	// has (its 65536 hash directories), with or without backups.
	logger.Info("Datastore %s is already initialized, its directory is left as it is", preflight.datastoreName)
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
	chunkDirsChanged, err := ensurePBSChunkStoreDirs(basePath, logger)
	if err != nil {
		return false, fmt.Errorf("create chunk store directories: %w", err)
	}

	lockChanged, lockErr := ensurePBSDatastoreLockFile(basePath, logger)
	if lockErr != nil {
		logger.Warning("PBS datastore lock file: %v", lockErr)
	}
	modesChanged := setPBSChunkStoreModes(basePath, logger)

	return changed || subdirChanged || chunkDirsChanged || lockChanged || modesChanged, nil
}

// pbsChunkStoreModes are the modes PBS creates a chunk store with, and the only
// ones it reopens an existing one with: "datastore create --reuse-datastore"
// refuses any other, "permissions or owner not correct" (measured on PBS 4.2.8).
// The hash directories are created 0750 by ensurePBSChunkStoreDirs.
var pbsChunkStoreModes = []struct {
	rel  string
	mode os.FileMode
}{
	{".", 0o755},
	{".chunks", 0o750},
	{".lock", 0o644},
}

// setPBSChunkStoreModes gives the datastore directory, .chunks and .lock the modes
// of pbsChunkStoreModes. They are changed through an os.Root of the datastore
// directory, so a symlink put in place of .chunks or .lock (the datastore belongs
// to the backup user) cannot lead the chmod outside it. A chmod that fails is
// reported like the other datastore permission changes and does not stop the
// recreation.
func setPBSChunkStoreModes(basePath string, logger *logging.Logger) bool {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		logger.Debug("PBS datastore permissions: cannot open %s: %v", basePath, err)
		return false
	}
	defer func() { _ = root.Close() }()
	changed := false
	for _, m := range pbsChunkStoreModes {
		path := filepath.Join(basePath, m.rel)
		info, err := root.Lstat(m.rel)
		if err != nil {
			// A missing entry has no mode to set; any other failure is traced.
			if !errors.Is(err, os.ErrNotExist) {
				logger.Debug("PBS datastore permissions: cannot stat %s: %v", path, err)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		current := info.Mode().Perm()
		if current == m.mode {
			continue
		}
		logger.Debug("PBS datastore permissions: chmod %s from %o to %o", path, current, m.mode)
		if err := root.Chmod(m.rel, m.mode); err != nil {
			if err := handleDatastoreModeError(path, current, m.mode, err, logger); err != nil {
				logger.Warning("Could not set datastore ownership for %s: %v", path, err)
			}
			continue
		}
		changed = true
	}
	return changed
}

// pbsChunkStoreDirTotal is the number of hash directories PBS creates in .chunks,
// 0000 to ffff (ChunkStore::create): a chunk is written into the directory named
// after the first 4 hex digits of its digest, and fails when that directory is
// missing. pbsChunkStoreDirCount is how many ensurePBSChunkStoreDirs creates; only
// tests lower it.
const pbsChunkStoreDirTotal = 64 * 1024

var pbsChunkStoreDirCount = pbsChunkStoreDirTotal

func pbsChunkStoreDirName(i int) string {
	return fmt.Sprintf("%04x", i)
}

// ensurePBSChunkStoreDirs creates the missing hash directories of .chunks, 0750
// and owned by backup:backup like the ones PBS creates. Without them the
// datastore is listed but every backup and garbage collection fails.
func ensurePBSChunkStoreDirs(basePath string, logger *logging.Logger) (bool, error) {
	chunkDir := filepath.Join(basePath, ".chunks")
	uid, gid, chown := -1, -1, false
	if os.Geteuid() == 0 {
		var err error
		if uid, gid, chown, err = lookupBackupOwnership(chunkDir, logger); err != nil {
			return false, err
		}
	}
	created := 0
	for i := range pbsChunkStoreDirCount {
		dir := filepath.Join(chunkDir, pbsChunkStoreDirName(i))
		if err := os.Mkdir(dir, 0o750); err != nil {
			if os.IsExist(err) {
				continue
			}
			return created > 0, fmt.Errorf("create %s: %w", dir, err)
		}
		created++
		if !chown {
			continue
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			// One report for the whole chunk store, not one per directory.
			chown = false
			if err := handleDatastoreOwnershipError("ownership", dir, uid, gid, err, logger); err != nil {
				return true, err
			}
		}
	}
	logger.Debug("PBS datastore chunk store: created %d of %d hash directories in %s (chown=%v)", created, pbsChunkStoreDirCount, chunkDir, chown)
	return created > 0, nil
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
