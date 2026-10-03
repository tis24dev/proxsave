package storage

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
)

// StoreIssue is something that went wrong AROUND a backup that Store did save: the
// archive reached the destination, a piece beside it did not. Store reports these
// without an error, because the backup is there; the caller prints one "⚠ <Name>:
// ..." outcome per issue, after the fact lines the backend printed while storing.
type StoreIssue int

const (
	// StoreIssueSidecarNotSaved: an associated file did not reach the destination.
	StoreIssueSidecarNotSaved StoreIssue = iota + 1
	// StoreIssueBundleNotSent: the bundle was unreadable, the standalone archive went instead.
	StoreIssueBundleNotSent
	// StoreIssueChecksumNotVerified: the upload was verified by size only.
	StoreIssueChecksumNotVerified
	// StoreIssuePermissionsNotSet: owner and mode could not be applied to the backup set.
	StoreIssuePermissionsNotSet
)

// StoreReporter is implemented by backends that can say what the last Store left
// undone around a saved backup. The slice is in the order the issues happened, holds
// each issue once, and is empty after a clean Store or a Store that returned an error.
type StoreReporter interface {
	LastStoreIssues() []StoreIssue
}

// storeIssueRecorder collects the issues of ONE Store call. It is safe for the
// parallel upload workers that verify sidecars concurrently.
type storeIssueRecorder struct {
	mu     sync.Mutex
	issues []StoreIssue
}

func (r *storeIssueRecorder) add(issue StoreIssue) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.issues {
		if existing == issue {
			return
		}
	}
	r.issues = append(r.issues, issue)
}

func (r *storeIssueRecorder) list() []StoreIssue {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StoreIssue(nil), r.issues...)
}

// ErrorCause is the short cause a fact line prints for a storage error: the last line
// rclone wrote, without its timestamp, when the error carries rclone output, and the
// code's own wording plus the system error without the path otherwise
// (safefs.SystemErrorText). The StorageError frame is dropped first: it repeats the
// location and the path the block already shows.
func ErrorCause(err error) string {
	if err == nil {
		return ""
	}
	for {
		se, ok := err.(*StorageError)
		if !ok || se == nil || se.Err == nil {
			break
		}
		err = se.Err
	}
	var rcErr *remoteCheckError
	if errors.As(err, &rcErr) && rcErr != nil && rcErr.short != "" {
		return rcErr.short
	}
	var cmdErr *rcloneCommandError
	if errors.As(err, &cmdErr) && cmdErr != nil && cmdErr.short != "" {
		return cmdErr.short
	}
	return safefs.SystemErrorText(err)
}

// capitalizeFirst upper-cases the first letter of a cause that opens its own fact
// line ("  Timed out after 30s").
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

// rcloneCause is the short cause of a failed rclone command: its last line, unless the
// exec error says more than "exit status N". That "more" is the SIGKILL shape
// (defaultExecCommand sets cmd.WaitDelay): the output is then whatever rclone printed
// before the kill, typically a NOTICE, and presenting it as the cause would hide the
// kill. The exec error alone is the cause there; the full output stays in DEBUG.
func rcloneCause(output string, err error) string {
	if err != nil && !strings.HasPrefix(err.Error(), "exit status ") {
		return safefs.SystemErrorText(err)
	}
	if short := rcloneShortCause(output); short != "" {
		return short
	}
	if err != nil {
		return safefs.SystemErrorText(err)
	}
	return ""
}

// rcloneShortCause returns the last non-empty line of rclone's output without the
// "YYYY/MM/DD HH:MM:SS " prefix: rclone closes every failed command with a summary
// line that carries the cause ("Failed to copyto: ...: read-only file system").
func rcloneShortCause(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(rcloneTimestampPrefix.ReplaceAllString(lines[i], ""))
		if line != "" {
			return line
		}
	}
	return ""
}

// rcloneCommandError is a failed rclone command: the message the callers have always
// returned, plus the short cause a fact line shows.
type rcloneCommandError struct {
	msg   string
	short string
	err   error
}

func (e *rcloneCommandError) Error() string { return e.msg }

func (e *rcloneCommandError) Unwrap() error { return e.err }

// retentionTally counts what one retention pass could not do, for the outcome lines
// the caller prints under "Applying retention policy...". It is reset with lastRet at
// the start of every pass and copied into the RetentionSummary.
type retentionTally struct {
	planned        int
	notDeleted     int
	leftBehind     int
	skipped        int
	notListed      int
	noMetadata     int
	logsNotDeleted int
}

func (t retentionTally) apply(s *RetentionSummary) {
	s.Planned = t.planned
	s.NotDeleted = t.notDeleted
	s.LeftBehind = t.leftBehind
	s.Skipped = t.skipped
	s.NotListed = t.notListed
	s.NoMetadata = t.noMetadata
	s.LogsNotDeleted = t.logsNotDeleted
}

// listingGap is what the last listing could not see: how many archives were left out
// and the cause they share. Retention prints it as a fact; the other readers of the
// listing (statistics, the post-copy count) only log it at DEBUG.
type listingGap struct {
	count int
	cause string
}

// logListingGap prints the fact for the archives the listing left out, before the
// scale of the pass, and returns how many there were.
func logListingGap(logger *logging.Logger, gap listingGap) int {
	if gap.count <= 0 {
		return 0
	}
	logger.Info("  Not listed: %d backups, %s", gap.count, gap.cause)
	return gap.count
}

// logListingNoMetadata prints one fact per archive the listing could only describe
// from its file name, and returns how many there were.
func logListingNoMetadata(logger *logging.Logger, names []string) int {
	for _, name := range names {
		logger.Info("  No metadata, name used: %s", name)
	}
	return len(names)
}

// DirectoryError is a destination directory that could not be created. The storage
// initialization shows it as "  Directory not created: <cause>".
type DirectoryError struct {
	Err error
}

func (e *DirectoryError) Error() string { return "failed to create directory: " + e.Err.Error() }

func (e *DirectoryError) Unwrap() error { return e.Err }

// logRetentionSkipped prints one fact line per archive retention could not date and
// left alone, and returns how many there were.
func logRetentionSkipped(logger *logging.Logger, location string, inert []retentionInert) int {
	for _, in := range inert {
		if in.Backup == nil {
			continue
		}
		name := filepath.Base(in.Backup.BackupFile)
		logger.Debug("%s: retention - ignored %s (%s)", location, in.Backup.BackupFile, in.Reason)
		switch in.Reason {
		case retentionInertNoManifest:
			logger.Info("  Skipped, no manifest: %s", name)
		default:
			logger.Info("  Skipped, no reliable date: %s", name)
		}
	}
	return len(inert)
}

// logRetentionScale prints the fact that opens a simple-retention deletion.
func logRetentionScale(logger *logging.Logger, total, limit int) {
	logger.Info("  Backups: %d, limit: %d", total, limit)
}

// logDeleteFailure prints the fact for one file a retention delete could not remove:
// the archive itself is "Not deleted", a file beside it is "Left behind".
func logDeleteFailure(logger *logging.Logger, file, cause string) {
	if isBackupSidecar(file) {
		logger.Info("  Left behind: %s: %s", filepath.Base(file), cause)
		return
	}
	logger.Info("  Not deleted: %s: %s", filepath.Base(file), cause)
}
