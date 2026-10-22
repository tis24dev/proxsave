package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// Every run that does not end in a restore opens backup-<host>-<ts>.log before it
// knows whether it will produce an archive (cmd/proxsave/main_runtime.go). Retention
// deletes a log together with the archive carrying the same timestamp, so the log of a
// run that left no archive (a dry run, a failed run, a run stopped by the preflight)
// had nothing to be deleted with and stayed forever (discussion #336). The orphan-log
// pass closes that gap: once the archives are rotated, a log no archive answers to is
// deleted when it is older than the oldest backup kept at that destination, so a log
// lives as long as the backups around it.

// runLogTimestampLayout is the timestamp a run puts in its log name and in the name of
// the archive it produces.
const runLogTimestampLayout = "20060102-150405"

// orphanLogTarget is one destination's log folder, as the orphan-log pass sees it.
type orphanLogTarget struct {
	// op is the DebugStep operation of the evidence lines ("local orphan logs").
	op string
	// where is the folder, as the evidence names it.
	where string
	// hostname and aliases are the names this host answers to: only the logs carrying
	// one of them are considered, so a folder shared by several hosts is pruned by
	// each host for its own logs.
	hostname string
	aliases  []string
	// list returns the file names in the folder; a folder that does not exist is an
	// empty list, not an error.
	list func(ctx context.Context) ([]string, error)
	// remove deletes one log by name. An error matching fs.ErrNotExist means the log
	// was already gone; any other error is a failed removal, whose ErrorCause is the
	// fact line.
	remove func(ctx context.Context, name string) error
	// own is the name of the log this run is writing when it sits in this folder, ""
	// otherwise. It is never deleted, whatever its name says: with the clock set back
	// it can read as older than the oldest backup kept.
	own string
	// heldOpen, when set, names the logs in the folder some process still holds open
	// (safefs.HeldOpenIn). None of them is deleted; a folder whose open logs cannot be
	// read has none deleted. Nil for a folder no process writes into (the cloud).
	heldOpen func(ctx context.Context) (map[string]bool, error)
}

// orphanLogResult is what one orphan-log pass did.
type orphanLogResult struct {
	deleted    int
	notDeleted int
	// notListed is the listing error when the folder could not be read: the orphans
	// were not checked.
	notListed error
	// stopped is the context's error when the pass was cancelled before it finished:
	// the caller returns it, as the archive deletion does, so the pass is reported as
	// not applied rather than as complete.
	stopped error
}

// runLogKey splits a run log name, backup-<host>-<YYYYMMDD-HHMMSS>.log, into the host
// and the timestamp it carries: the inverse of the name the run gives its log.
func runLogKey(name string) (host, ts string, ok bool) {
	const prefix, suffix = "backup-", ".log"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	tsLen := len(runLogTimestampLayout)
	if len(rest) < tsLen+2 || rest[len(rest)-tsLen-1] != '-' {
		return "", "", false
	}
	ts = rest[len(rest)-tsLen:]
	if _, err := time.Parse(runLogTimestampLayout, ts); err != nil {
		return "", "", false
	}
	return rest[:len(rest)-tsLen-1], ts, true
}

// pruneOrphanLogs deletes the logs of this host that no archive at the destination
// answers to and that are older than the oldest archive kept there. backups is the
// listing this host's retention pass worked on, removed the archives that pass deleted:
// their logs went with them (deleteAssociatedLog) and are not looked at again, so a log
// whose removal already failed is not counted twice. With no archive kept there is no
// window to measure against, and nothing is deleted.
func pruneOrphanLogs(ctx context.Context, logger *logging.Logger, t orphanLogTarget, backups []*types.BackupMetadata, removed []string) orphanLogResult {
	var res orphanLogResult

	gone := make(map[string]bool, len(removed))
	for _, file := range removed {
		gone[file] = true
	}
	withArchive := make(map[string]bool, len(backups))
	handled := make(map[string]bool, len(removed))
	oldestKept := ""
	for _, b := range backups {
		if b == nil {
			continue
		}
		_, ts, ok := extractLogKeyFromBackup(b.BackupFile)
		if !ok {
			continue
		}
		if gone[b.BackupFile] {
			handled[ts] = true
			continue
		}
		withArchive[ts] = true
		if oldestKept == "" || ts < oldestKept {
			oldestKept = ts
		}
	}
	if oldestKept == "" {
		logging.DebugStep(logger, t.op, "no backup kept, skipped")
		return res
	}

	logging.DebugStep(logger, t.op, "scan path=%s pattern=backup-*.log", t.where)
	names, err := t.list(ctx)
	if err != nil {
		logging.DebugStep(logger, t.op, "failed stage=list path=%s error=%v", t.where, err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			res.stopped = ctxErr
			return res
		}
		res.notListed = err
		return res
	}
	slices.Sort(names)

	listed, matched, orphans := 0, 0, 0
	var keep, expired []string
	for _, name := range names {
		host, ts, ok := runLogKey(name)
		if !ok || !hostOwnsName(host, t.hostname, t.aliases...) {
			continue
		}
		listed++
		switch {
		case withArchive[ts]:
			matched++
		case handled[ts]:
		case name == t.own:
			orphans++
			keep = append(keep, name)
			logging.DebugStep(logger, t.op, "kept %s reason=own_log", name)
		case ts >= oldestKept:
			orphans++
			keep = append(keep, name)
		default:
			orphans++
			expired = append(expired, name)
		}
	}
	logging.DebugStep(logger, t.op, "listed=%d with_archive=%d orphans=%d oldest_kept=%s", listed, matched, orphans, oldestKept)
	for _, name := range keep {
		if name != t.own {
			logging.DebugStep(logger, t.op, "kept %s reason=newer_than_oldest_kept", name)
		}
	}

	var open map[string]bool
	if t.heldOpen != nil && len(expired) > 0 {
		open, err = t.heldOpen(ctx)
		if err != nil {
			logging.DebugStep(logger, t.op, "skipped stage=open_check path=%s error=%v", t.where, err)
			if ctxErr := ctx.Err(); ctxErr != nil {
				res.stopped = ctxErr
			}
			return res
		}
	}

	for _, name := range expired {
		if err := ctx.Err(); err != nil {
			logging.DebugStep(logger, t.op, "stopped error=%v", err)
			res.stopped = err
			break
		}
		if open[name] {
			logging.DebugStep(logger, t.op, "kept %s reason=held_open", name)
			continue
		}
		err := t.remove(ctx, name)
		switch {
		case err == nil:
			res.deleted++
			logging.DebugStep(logger, t.op, "deleted %s", name)
		case errors.Is(err, fs.ErrNotExist):
			logging.DebugStep(logger, t.op, "%s already removed", name)
		default:
			res.notDeleted++
			logging.DebugStep(logger, t.op, "failed %s error=%v", name, err)
			logger.Info("  Log not deleted: %s: %s", name, ErrorCause(err))
		}
	}
	logging.DebugStep(logger, t.op, "result deleted=%d not_deleted=%d kept=%d", res.deleted, res.notDeleted, len(keep))
	return res
}

// ownLogIn is the name of the log logger is writing when it sits in dir, "" otherwise:
// the one log in that folder this process knows to be open without asking.
func ownLogIn(logger *logging.Logger, dir string) string {
	if logger == nil {
		return ""
	}
	current := logger.GetLogFilePath()
	if current == "" || filepath.Clean(filepath.Dir(current)) != filepath.Clean(dir) {
		return ""
	}
	return filepath.Base(current)
}

// keptOpenLog reports whether the log name in dir must stay although its archive is
// being deleted: it is the log this run is writing, some process still holds it open,
// or the open logs could not be read, in which case none is deleted. The reason is
// DEBUG evidence; nothing is asked of the operator.
func keptOpenLog(ctx context.Context, logger *logging.Logger, op, dir, name string, timeout time.Duration) bool {
	if name == ownLogIn(logger, dir) {
		logging.DebugStep(logger, op, "kept %s reason=own_log", name)
		return true
	}
	open, err := safefs.HeldOpenIn(ctx, dir, timeout)
	if err != nil {
		logging.DebugStep(logger, op, "kept %s reason=open_check_failed error=%v", name, err)
		return true
	}
	if open[name] {
		logging.DebugStep(logger, op, "kept %s reason=held_open", name)
		return true
	}
	return false
}

// listLogFolder returns the file names in a local log folder, bounded against a dead
// mount. A folder that does not exist holds no log: an empty list, not an error.
func listLogFolder(ctx context.Context, op, dir string, timeout time.Duration) ([]string, error) {
	return safefs.Run(ctx, op, dir, timeout, func() ([]string, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				names = append(names, entry.Name())
			}
		}
		return names, nil
	})
}

// countOwnRunLogs counts the run logs of this host among the names of a log folder,
// with the filter of the orphan-log pass: a backup-<host>-<timestamp>.log whose host
// this machine answers to. A shared log folder also holds other hosts' logs, which
// are not this destination's logs remaining.
func countOwnRunLogs(names []string, hostname string, aliases []string) int {
	count := 0
	for _, name := range names {
		if host, _, ok := runLogKey(name); ok && hostOwnsName(host, hostname, aliases...) {
			count++
		}
	}
	return count
}

// mergeOrphanLogs folds an orphan-log pass into the pass's summary and tally: the
// orphans it deleted are logs deleted, the ones it could not delete are logs not
// deleted.
func mergeOrphanLogs(ret *RetentionSummary, tally *retentionTally, res orphanLogResult) {
	ret.LogsDeleted += res.deleted
	if ret.HasLogInfo {
		ret.LogsRemaining -= res.deleted
		if ret.LogsRemaining < 0 {
			ret.LogsRemaining = 0
		}
	}
	tally.logsNotDeleted += res.notDeleted
	tally.logsNotListed = res.notListed
}
