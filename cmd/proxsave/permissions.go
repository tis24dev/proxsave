package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/security"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// fsIoTimeoutDuration converts the configured FS_IO_TIMEOUT into a per-operation
// duration for safefs. A non-positive value (the explicit FS_IO_TIMEOUT=0 opt-out)
// yields 0, which safefs treats as unbounded; the config default is 30s.
func fsIoTimeoutDuration(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.FsIoTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.FsIoTimeoutSeconds) * time.Second
}

// applyBackupPermissions applies ownership and basic directory permissions to
// backup and log paths when SET_BACKUP_PERMISSIONS=true is configured.
//
// This is a best-effort, Bash-compatible helper:
//   - It never creates users or groups (unlike the legacy Bash scripts).
//   - It only touches backup/log paths (not binaries/config files).
//   - Failures are logged as warnings but do not abort the backup.
func applyBackupPermissions(ctx context.Context, cfg *config.Config, logger *logging.Logger, dryRun bool) error {
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

	timeout := fsIoTimeoutDuration(cfg)

	dirs := []string{
		strings.TrimSpace(cfg.BackupPath),
		strings.TrimSpace(cfg.LogPath),
		strings.TrimSpace(cfg.SecondaryPath),
		strings.TrimSpace(cfg.SecondaryLogPath),
	}

	// Bounded + dry-run-aware detector (parity with the security preflight): a
	// dead/stale mount must not wedge detection, and a dry run must not write the
	// network-FS ownership probe file.
	fsDetector := storage.NewFilesystemDetector(logger,
		storage.WithIOTimeout(timeout),
		storage.WithDryRun(dryRun),
	)

	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if strings.Contains(dir, ":") && !isLocalPath(dir) {
			logger.Info("Permissions: skipping remote cloud path (ownership unsupported): %s", dir)
			continue
		}

		info, err := safefs.Stat(ctx, dir, timeout)
		if err != nil {
			switch {
			case errors.Is(err, safefs.ErrTimeout):
				logger.Warning("Permissions: stat of %s timed out after %s; skipping (dead/stale mount?)", dir, timeout)
			case os.IsNotExist(err):
				logger.Skip("Permissions: directory does not exist: %s", dir)
			default:
				logger.Warning("Permissions: failed to stat %s (skipping): %v", dir, err)
			}
			continue
		}
		if !info.IsDir() {
			logger.Skip("Permissions: path is not a directory, skipping: %s", dir)
			continue
		}

		fsInfo, err := fsDetector.DetectFilesystem(ctx, dir)
		if err != nil {
			if errors.Is(err, safefs.ErrTimeout) {
				logger.Warning("Permissions: filesystem detection on %s timed out after %s; skipping chown/chmod (dead/stale mount?)", dir, timeout)
			} else {
				logger.Warning("Permissions: failed to detect filesystem for %s; skipping chown/chmod: %v", dir, err)
			}
			continue
		}
		if fsInfo.Type.ShouldAutoExclude() || !fsInfo.SupportsOwnership {
			logger.Info("Permissions: skipping chown/chmod for %s (filesystem %s does not support Unix ownership)", dir, fsInfo.Type)
			continue
		}

		logger.Debug("Applying permissions on path: %s (uid=%d,gid=%d)", dir, uid, gid)
		if err := applyDirOwnershipRecursive(ctx, dir, uid, gid, timeout, dryRun, logger); err != nil {
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
//
// The walk uses safefs.WalkBounded so the directory reads themselves are bounded
// by FS_IO_TIMEOUT (filepath.WalkDir's internal readdir/lstat would otherwise
// block forever in an uninterruptible syscall on a dead/stale mount). In dry-run
// it mutates nothing, matching the read-only dry-run security preflight.
func applyDirOwnershipRecursive(ctx context.Context, root string, uid, gid int, timeout time.Duration, dryRun bool, logger *logging.Logger) error {
	if dryRun {
		logger.Info("DRY RUN: would recursively set ownership %d:%d and 0750 directory perms on %s", uid, gid, root)
		return nil
	}

	logger.Debug("Walking directory tree for permissions: %s (uid=%d,gid=%d)", root, uid, gid)

	return safefs.WalkBounded(ctx, root, timeout, func(path string, d fs.DirEntry, walkErr error) error {
		return applyOwnershipWalkEntry(ctx, path, d, walkErr, uid, gid, timeout, logger)
	})
}

// applyOwnershipWalkEntry applies bounded ownership/permissions to a single walked
// entry. A traversal error (walkErr, including a per-directory ErrTimeout from the
// bounded walk) is logged and SKIPPED (return nil) rather than propagated:
// returning it would abort the walk, leaving every not-yet-visited entry with its
// old ownership. One unreadable/stale subtree must not block fixing the rest of
// the backup tree.
func applyOwnershipWalkEntry(ctx context.Context, path string, d fs.DirEntry, walkErr error, uid, gid int, timeout time.Duration, logger *logging.Logger) error {
	if walkErr != nil {
		logger.Debug("permission walk: skipping %s: %v", path, walkErr)
		return nil
	}

	// Lchown (not Chown): bounded, and never follows a symlink to chown a target
	// outside the backup tree (the walk itself never descends symlinks). For
	// regular files and directories it is identical to chown.
	if err := safefs.Lchown(ctx, path, uid, gid, timeout); err != nil {
		// Do not stop on chown errors (including timeouts); just log at debug level.
		logger.Debug("chown failed on %s: %v", path, err)
	}

	if d != nil && d.IsDir() {
		// Directories must keep the execute bit to stay traversable, so gosec's G302
		// default ceiling (<=0600, which is appropriate for files) does not fit here.
		// 0750 = rwxr-x--- grants access to the owner and the backup group - both set
		// by the Lchown above to backupUser:backupGroup, which is the whole point of
		// SET_BACKUP_PERMISSIONS - while denying "others" entirely. Tightening to
		// <=0700 would silently remove backup-group access; <=0600 would make the
		// directory non-traversable. Routing the chmod through safefs.Lchmod (variable
		// mode) both bounds the call and structurally avoids the G302 finding.
		if err := safefs.Lchmod(ctx, path, 0o750, timeout); err != nil {
			logger.Debug("chmod failed on %s: %v", path, err)
		}
	}
	return nil
}

// fixPermissionsAfterInstall runs a best-effort permission and ownership
// normalization after a successful --install / --install --cli run so that
// the environment starts in a consistent state.
//
// It reuses the existing security checks (with AutoFix enabled and
// ContinueOnSecurityIssues=true) and, if configured, applies backup
// permissions managed by SET_BACKUP_PERMISSIONS.
//
// It returns a status code (ok, warning, error, skipped) and a short
// human-readable message suitable for the install footer. Errors are
// always non-blocking for the install flow.
//
// logSink, when non-nil, receives the temporary logger's console output so a
// UI stream can display the security/backup-permission lines instead of raw
// stdout (used by the TUI finalization). The CLI passes nil (unchanged).
func fixPermissionsAfterInstall(ctx context.Context, configPath, baseDir string, bootstrap *logging.BootstrapLogger, logSink io.Writer) (string, string) {
	configPath = strings.TrimSpace(configPath)
	baseDir = strings.TrimSpace(baseDir)
	if configPath == "" {
		return "skipped", "permissions normalization skipped (configuration path unavailable)"
	}

	if baseDir == "" {
		baseDir, _ = detectedBaseDirOrFallback()
	}
	cfg, err := config.LoadConfigWithBaseDir(configPath, baseDir)
	if err != nil {
		if bootstrap != nil {
			bootstrap.Warning("Post-install: skipping permission fix, failed to load configuration: %v", err)
		}
		return "error", "unable to normalize permissions: failed to load configuration (see log)"
	}

	logger := logging.New(types.LogLevelInfo, cfg.UseColor)
	if logSink != nil {
		// Route this local logger's console output into the UI stream so its
		// lines appear in-graphics instead of corrupting the alternate screen.
		logger.SetOutput(logSink)
	}

	// Force-enable security checks in a safe, non-blocking way for install.
	cfg.SecurityCheckEnabled = true
	cfg.AutoFixPermissions = true
	cfg.ContinueOnSecurityIssues = true

	// Avoid running network/port/process-heavy checks during install.
	cfg.CheckNetworkSecurity = false
	cfg.CheckFirewall = false
	cfg.CheckOpenPorts = false

	if bootstrap != nil {
		bootstrap.Info("Finalizing installation: normalizing permissions and ownership")
	}

	execInfo := getExecInfo()
	execPath := execInfo.ExecPath

	status := "ok"
	message := "permissions and ownership normalized correctly"

	if _, secErr := security.Run(ctx, logger, cfg, configPath, execPath, nil); secErr != nil {
		if bootstrap != nil {
			bootstrap.Warning("Post-install: security permission checks reported errors (ignored): %v", secErr)
		}
		status = "error"
		message = "errors during security permission checks (non-blocking, see log)"
	}

	if cfg.SetBackupPermissions {
		if bootstrap != nil {
			bootstrap.Info("Finalizing installation: applying backup directory permissions")
		}
		// Install/upgrade finalization always mutates (dryRun=false); it exists to
		// leave the environment consistent and has no dry-run mode of its own.
		if err := applyBackupPermissions(ctx, cfg, logger, false); err != nil {
			if bootstrap != nil {
				bootstrap.Warning("Post-install: backup permission adjustment failed (ignored): %v", err)
			}
			if status != "error" {
				status = "warning"
				message = "permissions normalized with warnings for backup paths (non-blocking, see log)"
			}
		} else if status == "ok" {
			message = "permissions and ownership normalized correctly (including backup paths)"
		}
	}

	return status, message
}
