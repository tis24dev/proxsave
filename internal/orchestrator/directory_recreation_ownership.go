// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/tis24dev/proxsave/internal/logging"
)

// setDatastoreOwnership sets ownership to backup:backup for PBS datastores
func setDatastoreOwnership(path string, logger *logging.Logger) error {
	if os.Geteuid() != 0 {
		logger.Debug("PBS datastore ownership: running as non-root (euid=%d); skipping chown/chmod for %s", os.Geteuid(), path)
		return nil
	}

	uid, gid, found, err := lookupBackupOwnership(path, logger)
	if err != nil || !found {
		return err
	}
	if err := chownDatastorePath(path, uid, gid, logger); err != nil {
		return err
	}
	return ensureDatastoreDirectoryMode(path, logger)
}

func lookupBackupOwnership(path string, logger *logging.Logger) (int, int, bool, error) {
	backupUser, err := user.Lookup("backup")
	if err != nil {
		logger.Debug("PBS datastore ownership: user 'backup' not found; skipping chown for %s", path)
		return 0, 0, false, nil
	}

	uid, err := parseBackupUserID("uid", backupUser.Uid)
	if err != nil {
		return 0, 0, false, err
	}
	gid, err := parseBackupUserID("gid", backupUser.Gid)
	if err != nil {
		return 0, 0, false, err
	}
	return uid, gid, true, nil
}

func parseBackupUserID(label, value string) (int, error) {
	id, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse backup %s: %w", label, err)
	}
	return id, nil
}

func chownDatastorePath(path string, uid, gid int, logger *logging.Logger) error {
	logger.Debug("PBS datastore ownership: chown %s to backup:backup (uid=%d gid=%d)", path, uid, gid)
	if err := os.Chown(path, uid, gid); err != nil {
		return handleDatastoreOwnershipError("ownership", path, uid, gid, err, logger)
	}
	return nil
}

func handleDatastoreOwnershipError(action, path string, uid, gid int, err error, logger *logging.Logger) error {
	if isIgnorableOwnershipError(err) {
		logger.Warning("PBS datastore %s: unable to chown %s to backup:backup (uid=%d gid=%d): %v (continuing)", action, path, uid, gid, err)
		return nil
	}
	return fmt.Errorf("chown %s: %w", path, err)
}

func ensureDatastoreDirectoryMode(path string, logger *logging.Logger) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}

	current := info.Mode().Perm()
	desired := current | os.FileMode(0o750)
	if desired == current {
		return nil
	}

	logger.Debug("PBS datastore permissions: chmod %s from %o to %o", path, current, desired)
	if err := os.Chmod(path, desired); err != nil {
		return handleDatastoreModeError(path, current, desired, err, logger)
	}
	return nil
}

func handleDatastoreModeError(path string, current, desired os.FileMode, err error, logger *logging.Logger) error {
	if isIgnorableOwnershipError(err) {
		logger.Warning("PBS datastore permissions: unable to chmod %s from %o to %o: %v (continuing)", path, current, desired, err)
		return nil
	}
	return fmt.Errorf("chmod %s: %w", path, err)
}
