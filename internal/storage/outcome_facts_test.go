package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

func newCapturedLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	return logger, buf
}

// The simple retention pass of the Local backend prints its facts under "Applying
// retention policy..." and leaves the outcomes to the caller: the scale of the pass,
// the archive it could not delete with the system error and no path, and the counts
// the caller needs to print "⚠ Backups deleted: K' of K".
func TestLocalRetentionPrintsFactsAndTallies(t *testing.T) {
	dir := t.TempDir()
	logger, buf := newCapturedLogger()
	local, err := NewLocalStorage(&config.Config{BackupPath: dir}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	now := time.Now()
	newest := filepath.Join(dir, "newest.tar.zst")
	good := filepath.Join(dir, "good.tar.zst")
	bad := filepath.Join(dir, "bad.tar.zst")
	for _, f := range []string{newest, good} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	// The bad archive is a non-empty directory, so its removal fails.
	if err := os.MkdirAll(filepath.Join(bad, "blocker"), 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	backups := []*types.BackupMetadata{
		{BackupFile: newest, Timestamp: now, Verified: true},
		{BackupFile: good, Timestamp: now.Add(-24 * time.Hour), Verified: true},
		{BackupFile: bad, Timestamp: now.Add(-48 * time.Hour), Verified: true},
		{BackupFile: filepath.Join(dir, "nomanifest.tar.zst"), Timestamp: now, Verified: false},
	}

	local.retTally = retentionTally{}
	if _, err := local.applySimpleRetention(context.Background(), backups, 1); err != nil {
		t.Fatalf("applySimpleRetention: %v", err)
	}

	out := buf.String()
	skipped := strings.Index(out, "INFO       Skipped, no manifest: nomanifest.tar.zst\n")
	scale := strings.Index(out, "INFO       Backups: 3, limit: 1\n")
	notDeleted := strings.Index(out, "INFO       Not deleted: bad.tar.zst: directory not empty\n")
	if skipped < 0 || scale < 0 || notDeleted < 0 || skipped > scale || scale > notDeleted {
		t.Fatalf("want the skipped fact, then the scale, then the failed delete, got:\n%s", out)
	}
	if strings.Contains(out, "WARNING") || strings.Contains(out, "Applying simple retention policy") {
		t.Fatalf("the backend prints facts only; the outcome is the caller's:\n%s", out)
	}

	summary := local.LastRetentionSummary()
	if summary.Planned != 2 || summary.NotDeleted != 1 || summary.Skipped != 1 || summary.LeftBehind != 0 {
		t.Fatalf("summary = %+v, want Planned=2 NotDeleted=1 Skipped=1 LeftBehind=0", summary)
	}
}

func TestDeleteFailureFactNamesArchiveOrSidecar(t *testing.T) {
	logger, buf := newCapturedLogger()
	logDeleteFailure(logger, "/backups/host-backup-20260101-000000.tar.zst", "permission denied")
	logDeleteFailure(logger, "/backups/host-backup-20260101-000000.tar.zst.sha256", "permission denied")
	want := "INFO       Not deleted: host-backup-20260101-000000.tar.zst: permission denied\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
	want = "INFO       Left behind: host-backup-20260101-000000.tar.zst.sha256: permission denied\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
}

// The short cause of a failed rclone command is its last line without the timestamp,
// except when the process was killed: then the output is whatever rclone printed
// before the kill and the kill is the cause.
func TestRcloneCause(t *testing.T) {
	out := "2026/10/02 23:57:06 ERROR : r3-archive.bin: Failed to copy: read-only file system\n" +
		"2026/10/02 23:57:06 Failed to copyto: open /x/r3-archive.bin: read-only file system\n\n"
	if got := rcloneCause(out, errors.New("exit status 1")); got != "Failed to copyto: open /x/r3-archive.bin: read-only file system" {
		t.Fatalf("rcloneCause(exit status) = %q", got)
	}
	if got := rcloneCause("2026/09/02 01:00:00 NOTICE: Waiting for transfers to finish", errors.New("signal: killed")); got != "signal: killed" {
		t.Fatalf("rcloneCause(killed) = %q, want the kill", got)
	}
	if got := rcloneCause("", errors.New("exit status 3")); got != "exit status 3" {
		t.Fatalf("rcloneCause(no output) = %q", got)
	}

	cmdErr := &StorageError{Location: LocationCloud, Operation: "upload", Err: &uploadTaskError{file: "a.tar", err: &rcloneCommandError{
		msg: "rclone copy failed: exit status 1: ...", short: rcloneCause(out, errors.New("exit status 1")), err: errors.New("exit status 1"),
	}}}
	if got := ErrorCause(cmdErr); got != "Failed to copyto: open /x/r3-archive.bin: read-only file system" {
		t.Fatalf("ErrorCause(upload) = %q", got)
	}
	if got := uploadFailureFact(cmdErr.Err); got != "a.tar: Failed to copyto: open /x/r3-archive.bin: read-only file system" {
		t.Fatalf("uploadFailureFact = %q", got)
	}
	if got := ErrorCause(&StorageError{Err: &os.PathError{Op: "stat", Path: "/x", Err: os.ErrPermission}}); got != "permission denied" {
		t.Fatalf("ErrorCause(path error) = %q, want the system error without the path", got)
	}
	if got := ErrorCause(&StorageError{Err: &safefs.TimeoutError{Op: "stat", Path: "/x", Timeout: 5 * time.Second}}); got != "timed out after 5s" {
		t.Fatalf("ErrorCause(timeout) = %q", got)
	}
}

// The Primary's Store prints the permissions it applies: 0600 root:root unless
// SET_BACKUP_PERMISSIONS opens them.
func TestLocalStorePrintsAppliedPermissions(t *testing.T) {
	dir := t.TempDir()
	logger, buf := newCapturedLogger()
	local, err := NewLocalStorage(&config.Config{BackupPath: dir}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	archive := filepath.Join(dir, "host-backup-20260101-000000.tar.zst")
	if err := os.WriteFile(archive, []byte("x"), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := local.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !strings.Contains(buf.String(), "INFO       Permissions: 0600 root:root\n") {
		t.Fatalf("missing the permissions fact:\n%s", buf.String())
	}

	buf.Reset()
	missing := filepath.Join(dir, "missing.tar.zst")
	if err := local.Store(context.Background(), missing, &types.BackupMetadata{BackupFile: missing}); err == nil {
		t.Fatalf("Store of a missing archive must fail")
	}
	if !strings.Contains(buf.String(), "INFO       Backup file not accessible: no such file or directory\n") {
		t.Fatalf("missing the not-accessible fact:\n%s", buf.String())
	}
}
