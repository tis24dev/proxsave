package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// fsRetention is the retention pass of one folder-backed destination, LocalStorage or
// SecondaryStorage: the two used to carry six copies of the same methods each. A
// destination builds one for every call (retention() on its type) and keeps its own
// state: the pass writes the tally and the summary through the two pointers, and
// nothing else.
//
// The receivers are values: an fsRetention is built for one call and never changed by
// it, and the two pointers are the only way it reaches anything it changes.
type fsRetention struct {
	logger *logging.Logger
	// config is the destination's config. It may be nil only where the destination's
	// methods allowed it before: deleteAssociatedLog and countLogFiles answer for it.
	config *config.Config
	// basePath is the backup folder; the log count names it as the path of its bound.
	basePath string
	// logPath is the log folder variable of this destination (LOG_PATH,
	// SECONDARY_LOG_PATH) as configured, "" when config is nil. Every use trims it.
	logPath string
	// hostname and aliases are the names this host answers to, for the orphan-log pass.
	hostname string
	aliases  []string
	tally    *retentionTally
	lastRet  *RetentionSummary
	text     fsRetentionText
}

// fsRetentionText is how one destination words its retention lines, and the names of
// its bounded operations. The two destinations used to write these sentences in their
// own copies of the code; the differences are kept here exactly as they were.
type fsRetentionText struct {
	// name is the destination in the middle of a sentence: "local", "secondary".
	name string
	// storage opens the sentences both destinations word alike: "Local storage".
	storage string
	// storageTitle labels the retention facts and the failure lines: "Local Storage".
	storageTitle string
	// logs opens the sentences about the log folder, and is the open-log check's
	// operation: "Local logs".
	logs string
	// orphanOp is the DebugStep operation of the orphan-log pass.
	orphanOp string
	// logGlobOp and logListOp are the safefs operations of the log count and of the
	// orphan-log listing.
	logGlobOp string
	logListOp string
	// removing (one file of the backup set, by path) and deleted (the backup set, by
	// archive base name) take the same argument on both destinations, worded
	// differently.
	removing string
	deleted  string
	// The sentences whose arguments differ between the two destinations.
	deleting     func(logger *logging.Logger, backupFile string)
	removeFailed func(logger *logging.Logger, file string, err error)
	notDeleted   func(logger *logging.Logger, backupFile string, err error)
	leftBehind   func(logger *logging.Logger, backupFile string, err error)
}

var localRetentionText = fsRetentionText{
	name:         "local",
	storage:      "Local storage",
	storageTitle: "Local Storage",
	logs:         "Local logs",
	orphanOp:     "local orphan logs",
	logGlobOp:    "local-log-glob",
	logListOp:    "local-log-list",
	removing:     "Local storage: removing file %s",
	deleted:      "Local storage: deleted backup and associated files: %s",
	deleting: func(logger *logging.Logger, backupFile string) {
		logger.Debug("Local storage: deleting backup %s", filepath.Base(backupFile))
	},
	removeFailed: func(logger *logging.Logger, file string, err error) {
		logger.Debug("Local Storage: retention - failed to remove %s: %v", file, err)
	},
	notDeleted: func(logger *logging.Logger, backupFile string, err error) {
		logger.Debug("Local Storage: retention - failed to delete %s: %v", backupFile, err)
	},
	leftBehind: func(logger *logging.Logger, backupFile string, err error) {
		logger.Debug("Local Storage: retention - %s removed but the sidecar cleanup failed: %v", backupFile, err)
	},
}

var secondaryRetentionText = fsRetentionText{
	name:         "secondary",
	storage:      "Secondary storage",
	storageTitle: "Secondary Storage",
	logs:         "Secondary logs",
	orphanOp:     "secondary orphan logs",
	logGlobOp:    "secondary-log-glob",
	logListOp:    "secondary-log-list",
	removing:     "Removing file: %s",
	deleted:      "Deleted secondary backup: %s",
	deleting: func(logger *logging.Logger, backupFile string) {
		logger.Debug("Deleting secondary backup: %s", backupFile)
	},
	removeFailed: func(logger *logging.Logger, _ string, err error) {
		logger.Debug("Secondary Storage: retention - %v", err)
	},
	notDeleted: func(logger *logging.Logger, backupFile string, err error) {
		logger.Debug("Secondary Storage: retention - left %s in place: %v", filepath.Base(backupFile), err)
	},
	leftBehind: func(logger *logging.Logger, backupFile string, err error) {
		logger.Debug("Secondary Storage: retention - left files behind from %s: %v", filepath.Base(backupFile), err)
	},
}

// deleteBackup removes a backup file and its associated files.
func (r fsRetention) deleteBackup(ctx context.Context, backupFile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	r.text.deleting(r.logger, backupFile)

	basePath, _ := trimBundleSuffix(backupFile)
	// Always include the bundle path so an orphan .bundle.tar (created while
	// bundling was enabled, then disabled) is cleaned up. Remove of an absent
	// bundle is a no-op, so this is behavior-preserving when no bundle exists.
	filesToDelete := buildBackupCandidatePaths(basePath, true)

	// Delete all files; collect real removal failures (not "already gone") and
	// track whether the data archive itself (not just a sidecar) failed, so the
	// caller never counts a backup whose archive remains on disk as deleted
	// (PS-BH-001), while a sidecar-only failure still counts (the archive IS gone).
	timeout := fsIoTimeout(r.config)
	var failedFiles []string
	dataFailed := false
	for _, f := range filesToDelete {
		if f == "" {
			continue
		}
		r.logger.Debug(r.text.removing, f)
		if err := safefs.Remove(ctx, f, timeout); err != nil {
			if os.IsNotExist(err) {
				r.logger.Debug("%s: file already removed %s", r.text.storage, f)
				continue
			}
			r.text.removeFailed(r.logger, f, err)
			logDeleteFailure(r.logger, f, safefs.SystemErrorText(err))
			failedFiles = append(failedFiles, f)
			if !isBackupSidecar(f) {
				dataFailed = true
			}
		}
	}

	// Best-effort: delete the associated log, but only once the backup's data file is
	// gone. A backup that could not be deleted keeps its log.
	logDeleted := false
	if !dataFailed {
		logDeleted = r.deleteAssociatedLog(ctx, backupFile)
	} else {
		r.logger.Debug("%s: keeping the log of %s, the backup was not deleted", r.text.storage, filepath.Base(backupFile))
	}

	if len(failedFiles) > 0 {
		if !dataFailed {
			return logDeleted, fmt.Errorf("%w: %v", errBackupSidecarDeleteOnly, failedFiles)
		}
		return logDeleted, fmt.Errorf("failed to remove %d file(s): %v", len(failedFiles), failedFiles)
	}

	r.logger.Debug(r.text.deleted, filepath.Base(backupFile))
	return logDeleted, nil
}

// deleteAssociatedLog attempts to remove the log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (r fsRetention) deleteAssociatedLog(ctx context.Context, backupFile string) bool {
	if r.config == nil {
		return false
	}
	logPath := strings.TrimSpace(r.logPath)
	if logPath == "" {
		return false
	}

	host, ts, ok := extractLogKeyFromBackup(backupFile)
	if !ok {
		return false
	}

	logName := fmt.Sprintf("backup-%s-%s.log", host, ts)
	fullPath := filepath.Join(logPath, logName)
	if keptOpenLog(ctx, r.logger, r.text.logs, logPath, logName, fsIoTimeout(r.config)) {
		return false
	}

	// Bounded against a dead/stale mount: on a cancelled/expired ctx safefs
	// returns immediately without running the remove (best-effort cleanup).
	if err := safefs.Remove(ctx, fullPath, fsIoTimeout(r.config)); err != nil {
		if os.IsNotExist(err) {
			r.logger.Debug("%s: log not found %s", r.text.logs, logName)
			return false
		}
		r.logger.Debug("%s: failed to delete %s: %v", r.text.logs, logName, err)
		r.logger.Info("  Log not deleted: %s: %s", logName, safefs.SystemErrorText(err))
		r.tally.logsNotDeleted++
		return false
	}

	r.logger.Debug("%s: deleted log file %s", r.text.logs, logName)
	return true
}

func (r fsRetention) countLogFiles(ctx context.Context) int {
	if r.config == nil {
		return -1
	}
	logPath := strings.TrimSpace(r.logPath)
	if logPath == "" {
		return 0
	}
	pattern := filepath.Join(logPath, "backup-*.log")
	// Bounded against a dead/stale mount, consistent with the main backup glob.
	matches, err := safefs.Run(ctx, r.text.logGlobOp, r.basePath, fsIoTimeout(r.config), func() ([]string, error) {
		return filepath.Glob(pattern)
	})
	if err != nil {
		r.logger.Debug("%s: failed to count log files: %v", r.text.logs, err)
		return -1
	}
	return len(matches)
}

// pruneOrphanLogs runs the orphan-log pass (log_orphans.go) on the destination's log
// folder once the archives are rotated, and folds what it did into this pass's summary.
func (r fsRetention) pruneOrphanLogs(ctx context.Context, backups []*types.BackupMetadata) error {
	logPath := strings.TrimSpace(r.logPath)
	if logPath == "" {
		return nil
	}
	r.tally.hasLogPath = true
	timeout := fsIoTimeout(r.config)
	res := pruneOrphanLogs(ctx, r.logger, orphanLogTarget{
		own: ownLogIn(r.logger, logPath),
		heldOpen: func(ctx context.Context) (map[string]bool, error) {
			return safefs.HeldOpenIn(ctx, logPath, timeout)
		},
		op:       r.text.orphanOp,
		where:    logPath,
		hostname: r.hostname,
		aliases:  r.aliases,
		list: func(ctx context.Context) ([]string, error) {
			return listLogFolder(ctx, r.text.logListOp, logPath, timeout)
		},
		remove: func(ctx context.Context, name string) error {
			return safefs.Remove(ctx, filepath.Join(logPath, name), timeout)
		},
	}, backups, r.tally.removed)
	mergeOrphanLogs(r.lastRet, r.tally, res)
	return res.stopped
}

// applyGFS applies GFS (Grandfather-Father-Son) retention policy
func (r fsRetention) applyGFS(ctx context.Context, backups []*types.BackupMetadata, config RetentionConfig) (int, error) {
	eligible, inert := partitionRetentionEligible(backups)
	r.tally.skipped = logRetentionSkipped(r.logger, r.text.storageTitle, inert)
	backups = eligible

	config = EffectiveGFSRetentionConfig(config)
	r.logger.Debug("Applying GFS retention policy (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
		config.Daily, config.Weekly, config.Monthly, config.Yearly)

	initialLogs := r.countLogFiles(ctx)

	// Classify backups according to GFS scheme
	classification := ClassifyBackupsGFS(backups, config)

	// Get statistics
	stats := GetRetentionStats(classification)
	kept := len(backups) - stats[CategoryDelete]
	r.logger.Debug("GFS classification -> daily: %d/%d, weekly: %d/%d, monthly: %d/%d, yearly: %d/%d, kept: %d, to_delete: %d",
		stats[CategoryDaily], config.Daily,
		stats[CategoryWeekly], config.Weekly,
		stats[CategoryMonthly], config.Monthly,
		stats[CategoryYearly], config.Yearly,
		kept,
		stats[CategoryDelete])
	r.tally.planned = stats[CategoryDelete]

	// Delete backups marked for deletion
	var toDelete []*types.BackupMetadata
	for backup, category := range classification {
		if category != CategoryDelete {
			continue
		}
		toDelete = append(toDelete, backup)
	}
	// Oldest first, as the simple policy deletes: a pass stopped halfway leaves the
	// newest of them. The map hands them out in random order; the same timestamp is
	// settled by file name.
	sort.Slice(toDelete, func(i, j int) bool {
		a, b := toDelete[i], toDelete[j]
		if !a.Timestamp.Equal(b.Timestamp) {
			return a.Timestamp.Before(b.Timestamp)
		}
		return a.BackupFile < b.BackupFile
	})

	return r.deleteBackups(ctx, toDelete, len(backups), initialLogs)
}

// applySimple applies simple count-based retention policy
func (r fsRetention) applySimple(ctx context.Context, backups []*types.BackupMetadata, maxBackups int) (int, error) {
	if maxBackups <= 0 {
		r.logger.Debug("Retention disabled for %s storage (maxBackups = %d)", r.text.name, maxBackups)
		return 0, nil
	}

	eligible, inert := partitionRetentionEligible(backups)
	r.tally.skipped = logRetentionSkipped(r.logger, r.text.storageTitle, inert)
	backups = eligible

	totalBackups := len(backups)
	if totalBackups <= maxBackups {
		r.logger.Debug("%s: %d backups (within retention limit of %d)", r.text.storage, totalBackups, maxBackups)
		return 0, nil
	}

	// Calculate how many to delete
	toDelete := totalBackups - maxBackups
	r.logger.Debug("Simple retention -> current: %d, limit: %d, to_delete: %d",
		totalBackups, maxBackups, toDelete)
	logRetentionScale(r.logger, totalBackups, maxBackups)
	r.tally.planned = toDelete

	// Delete oldest backups (already sorted newest first)
	initialLogs := r.countLogFiles(ctx)
	oldest := make([]*types.BackupMetadata, 0, toDelete)
	for i := totalBackups - 1; i >= maxBackups; i-- {
		oldest = append(oldest, backups[i])
	}

	return r.deleteBackups(ctx, oldest, totalBackups, initialLogs)
}

// deleteBackups deletes toDelete in the order given, out of the total backups the
// policy looked at, and writes the pass's summary. initialLogs is the log count taken
// before the policy ran (-1: unknown). A cancelled context stops it before the next
// deletion, without a summary.
func (r fsRetention) deleteBackups(ctx context.Context, toDelete []*types.BackupMetadata, total int, initialLogs int) (int, error) {
	logsDeleted := 0
	deleted := 0
	for _, backup := range toDelete {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		r.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := r.deleteBackup(ctx, backup.BackupFile)
		if err != nil {
			if !errors.Is(err, errBackupSidecarDeleteOnly) {
				r.text.notDeleted(r.logger, backup.BackupFile, err)
				r.tally.notDeleted++
				continue
			}
			// Archive removed, only sidecar(s) failed: count as deleted; the fact
			// line naming the leftover was printed by deleteBackup.
			r.text.leftBehind(r.logger, backup.BackupFile, err)
			r.tally.leftBehind++
		}

		deleted++
		r.tally.removed = append(r.tally.removed, backup.BackupFile)
		if logDeleted {
			logsDeleted++
		}
	}

	remaining := total - deleted
	if remaining < 0 {
		remaining = 0
	}

	if logsRemaining, ok := computeRemaining(initialLogs, logsDeleted); ok {
		r.logger.Debug("%s retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			r.text.storage, deleted, logsDeleted, remaining, logsRemaining)
		*r.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		r.logger.Debug("%s retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			r.text.storage, deleted, logsDeleted, remaining)
		*r.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}
