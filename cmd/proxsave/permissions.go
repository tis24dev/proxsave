package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// applyBackupPermissions applies ownership and basic directory permissions to
// backup and log paths when SET_BACKUP_PERMISSIONS=true is configured.
//
// This is a best-effort, Bash-compatible helper:
//   - It never creates users or groups (unlike the legacy Bash scripts).
//   - It only touches backup/log paths (not binaries/config files).
//   - Failures are logged as warnings but do not abort the backup.
func applyBackupPermissions(cfg *config.Config, logger *logging.Logger) error {
	backupUser := strings.TrimSpace(cfg.BackupUser)
	backupGroup := strings.TrimSpace(cfg.BackupGroup)
	if backupUser == "" || backupGroup == "" {
		logger.Warning("SET_BACKUP_PERMISSIONS=true but BACKUP_USER/BACKUP_GROUP are empty; skipping permission adjustments")
		return nil
	}

	uid, gid, err := resolveUserGroupIDs(backupUser, backupGroup)
	if err != nil {
		// Log and skip rather than aborting
		logger.Warning("Failed to resolve BACKUP_USER/BACKUP_GROUP (%s:%s): %v", backupUser, backupGroup, err)
		return nil
	}

	logger.Info("Applying backup permissions (SET_BACKUP_PERMISSIONS=true) for user:group %s:%s", backupUser, backupGroup)

	dirs := []string{
		strings.TrimSpace(cfg.BackupPath),
		strings.TrimSpace(cfg.LogPath),
		strings.TrimSpace(cfg.SecondaryPath),
		strings.TrimSpace(cfg.SecondaryLogPath),
	}

	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}

		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				logger.Skip("Permissions: directory does not exist: %s", dir)
				continue
			}
			logger.Warning("Permissions: failed to stat %s (skipping): %v", dir, err)
			continue
		}
		if !info.IsDir() {
			logger.Skip("Permissions: path is not a directory, skipping: %s", dir)
			continue
		}

		logger.Debug("Applying permissions on path: %s (uid=%d,gid=%d)", dir, uid, gid)
		if err := applyDirOwnershipRecursive(dir, uid, gid, logger); err != nil {
			logger.Warning("Failed to apply permissions on %s: %v", dir, err)
		}
	}
	return nil
}

func resolveUserGroupIDs(userName, groupName string) (int, int, error) {
	u, err := user.Lookup(userName)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot lookup user %s: %w", userName, err)
	}
	g, err := user.LookupGroup(groupName)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot lookup group %s: %w", groupName, err)
	}

	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid uid for user %s: %w", userName, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid gid for group %s: %w", groupName, err)
	}
	return uid, gid, nil
}

// applyDirOwnershipRecursive walks a directory tree and applies chown to all
// entries, and a conservative chmod (0750) on directories only. This matches
// the intent of the Bash version but avoids touching unrelated system paths.
func applyDirOwnershipRecursive(root string, uid, gid int, logger *logging.Logger) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("cannot stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil
	}

	logger.Debug("Walking directory tree for permissions: %s (uid=%d,gid=%d)", root, uid, gid)

	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if err := os.Chown(path, uid, gid); err != nil {
			// Do not stop on chown errors; just log at debug level.
			logger.Debug("chown failed on %s: %v", path, err)
		}

		if d.IsDir() {
			if err := os.Chmod(path, 0o750); err != nil {
				logger.Debug("chmod failed on %s: %v", path, err)
			}
		}
		return nil
	})
}
