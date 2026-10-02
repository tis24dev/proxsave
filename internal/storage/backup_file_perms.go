package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
)

// backupSetGlobs are the base-name patterns the local and secondary List glob for:
// an archive or bundle in either naming, plus its sidecars.
var backupSetGlobs = []string{
	"proxmox-backup-*.tar.*", // Legacy Bash naming
	"*-backup-*.tar*",        // Go pipeline naming (bundle and sidecars included)
}

// IsBackupSetFile reports whether name (a base name) is a file of a backup set: an
// archive or bundle, or one of its .sha256/.metadata/.manifest.json sidecars, matched
// with the same patterns List uses. In-flight temp and .partial files are not.
func IsBackupSetFile(name string) bool {
	if isBackupTempArtifact(name) {
		return false
	}
	for _, pattern := range backupSetGlobs {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

// ResolveBackupOwner returns the uid and gid of BACKUP_USER and BACKUP_GROUP. It is a
// copy of resolveUserGroupIDs in package main, which the permissions pass at startup
// uses; the storage backends need the same answer for the files of the current run.
// A test in package main pins the two to the same results.
func ResolveBackupOwner(userName, groupName string) (int, int, error) {
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

// backupFileOwnership returns the owner and mode the files of a backup set get on a
// local or secondary destination. Default: root:root and backup.ArtifactFilePerm.
// With SET_BACKUP_PERMISSIONS=true and both BACKUP_USER and BACKUP_GROUP set and
// resolvable: that user and group and backup.SharedArtifactFilePerm. Anything else
// keeps the default; the permissions pass at startup already warns about an empty or
// unresolvable name.
func backupFileOwnership(cfg *config.Config, logger *logging.Logger) (int, int, os.FileMode) {
	if cfg == nil || !cfg.SetBackupPermissions {
		return 0, 0, backup.ArtifactFilePerm
	}
	backupUser := strings.TrimSpace(cfg.BackupUser)
	backupGroup := strings.TrimSpace(cfg.BackupGroup)
	if backupUser == "" || backupGroup == "" {
		logger.Debug("Backup files: SET_BACKUP_PERMISSIONS=true but BACKUP_USER/BACKUP_GROUP is empty; keeping root:root %o", backup.ArtifactFilePerm)
		return 0, 0, backup.ArtifactFilePerm
	}
	uid, gid, err := ResolveBackupOwner(backupUser, backupGroup)
	if err != nil {
		logger.Debug("Backup files: %v; keeping root:root %o", err, backup.ArtifactFilePerm)
		return 0, 0, backup.ArtifactFilePerm
	}
	return uid, gid, backup.SharedArtifactFilePerm
}

// setBackupSetPermissions gives every file of the backup set backupFile belongs to
// (the bundle, or the archive and its sidecars) that exists next to it the owner and
// mode backupFileOwnership returns. warn reports a file whose mode could not be set.
func setBackupSetPermissions(ctx context.Context, cfg *config.Config, logger *logging.Logger, d *FilesystemDetector, fsInfo *FilesystemInfo, backupFile string, warn func(path string, err error)) {
	uid, gid, mode := backupFileOwnership(cfg, logger)
	logger.Debug("Backup files: owner %d:%d, mode %o for the files of %s", uid, gid, mode, filepath.Base(backupFile))
	timeout := fsIoTimeout(cfg)
	for _, path := range buildBackupCandidatePaths(backupFile, true) {
		if _, err := safefs.Stat(ctx, path, timeout); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.Debug("Backup files: skipping %s: %v", filepath.Base(path), err)
			}
			continue
		}
		if err := d.SetPermissions(ctx, path, uid, gid, mode, fsInfo); err != nil {
			warn(path, err)
		}
	}
}
