// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/logging"
)

func ensurePBSDatastoreLockFile(datastorePath string, logger *logging.Logger) (bool, error) {
	lockPath := datastoreLockPath(datastorePath)
	info, err := os.Lstat(lockPath)
	if err != nil {
		return ensureMissingPBSDatastoreLock(lockPath, err, logger)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s is a symlink; refusing to manage lock file", lockPath)
	}
	if info.IsDir() {
		return replacePBSDatastoreLockDirectory(lockPath, logger)
	}
	return chownExistingPBSDatastoreLock(lockPath, logger)
}

func datastoreLockPath(datastorePath string) string {
	return filepath.Join(datastorePath, ".lock")
}

func ensureMissingPBSDatastoreLock(lockPath string, statErr error, logger *logging.Logger) (bool, error) {
	if !os.IsNotExist(statErr) {
		return false, fmt.Errorf("stat %s: %w", lockPath, statErr)
	}

	logger.Debug("PBS datastore lock: creating %s", lockPath)
	if err := createPBSDatastoreLockFile(lockPath); err != nil {
		return false, err
	}
	if err := setDatastoreOwnership(lockPath, logger); err != nil {
		return true, fmt.Errorf("chown %s: %w", lockPath, err)
	}
	return true, nil
}

func replacePBSDatastoreLockDirectory(lockPath string, logger *logging.Logger) (bool, error) {
	changed, err := removeOrRenamePBSDatastoreLockDirectory(lockPath, logger)
	if err != nil {
		return false, err
	}
	if err := createPBSDatastoreLockFile(lockPath); err != nil {
		return changed, err
	}
	if err := setDatastoreOwnership(lockPath, logger); err != nil {
		return true, fmt.Errorf("chown %s: %w", lockPath, err)
	}
	return true, nil
}

func removeOrRenamePBSDatastoreLockDirectory(lockPath string, logger *logging.Logger) (bool, error) {
	entries, err := os.ReadDir(lockPath)
	if err != nil {
		return false, fmt.Errorf("lock path %s is a directory and cannot be read: %w", lockPath, err)
	}
	if len(entries) == 0 {
		logger.Warning("PBS datastore lock: %s is a directory (invalid); removing and recreating as file", lockPath)
		return true, os.Remove(lockPath)
	}

	backupPath := fmt.Sprintf("%s.proxsave-dir.%s", lockPath, nowRestore().Format("20060102-150405"))
	logger.Warning("PBS datastore lock: %s is a non-empty directory (invalid); renaming to %s and creating lock file", lockPath, backupPath)
	if err := os.Rename(lockPath, backupPath); err != nil {
		return false, fmt.Errorf("rename invalid lock dir %s -> %s: %w", lockPath, backupPath, err)
	}
	return true, nil
}

func createPBSDatastoreLockFile(lockPath string) error {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("create %s: %w", lockPath, err)
	}
	_ = file.Close()
	return nil
}

func chownExistingPBSDatastoreLock(lockPath string, logger *logging.Logger) (bool, error) {
	if err := setDatastoreOwnership(lockPath, logger); err != nil {
		return false, fmt.Errorf("chown %s: %w", lockPath, err)
	}
	return false, nil
}
