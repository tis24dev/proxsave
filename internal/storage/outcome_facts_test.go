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

func newCloudWithCapturedLog(t *testing.T, cfg *config.Config, exec func(context.Context, string, ...string) ([]byte, error)) (*CloudStorage, *bytes.Buffer) {
	t.Helper()
	logger, buf := newCapturedLogger()
	cs, err := NewCloudStorage(cfg, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
	cs.execCommand = exec
	cs.waitForRetry = func(context.Context, time.Duration) error { return nil }
	return cs, buf
}

// The accessibility check opens its own line and puts what it found under it.
func TestCloudCheckPrintsWhatItFound(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneTimeoutConnection: 30}

	cs, buf := newCloudWithCapturedLog(t, cfg, func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
	if _, err := cs.DetectFilesystem(context.Background()); err != nil {
		t.Fatalf("DetectFilesystem: %v", err)
	}
	out := buf.String()
	check := strings.Index(out, "INFO     Checking cloud remote accessibility...\n")
	found := strings.Index(out, "INFO       Accessible\n")
	if check < 0 || found < check || strings.Contains(out, "is accessible") || strings.Contains(out, "timeout: max") {
		t.Fatalf("want the check line, then \"  Accessible\", and no remote/timeout on screen:\n%s", out)
	}

	cs, buf = newCloudWithCapturedLog(t, cfg, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("2026/10/02 23:57:36 Failed to create file system for \"remote:\": didn't find section in config file\n"), errors.New("exit status 1")
	})
	if _, err := cs.DetectFilesystem(context.Background()); err == nil {
		t.Fatalf("DetectFilesystem must fail on a remote missing from the config")
	}
	if want := "INFO       Failed to create file system for \"remote:\": didn't find section in config file\n"; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("the check writes facts; the outcome is the caller's:\n%s", buf.String())
	}

	cs, buf = newCloudWithCapturedLog(t, cfg, nil)
	cs.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	if _, err := cs.DetectFilesystem(context.Background()); err == nil {
		t.Fatalf("DetectFilesystem must fail without rclone")
	}
	if !strings.Contains(stripTimes(buf.String()), "INFO     Checking cloud remote accessibility...\nINFO       rclone: not found in PATH\n") ||
		strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("want the check line, then the rclone fact:\n%s", buf.String())
	}
}

func stripTimes(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if _, rest, ok := strings.Cut(line, "] "); ok {
			lines[i] = rest
		}
	}
	return strings.Join(lines, "\n")
}

// A failed attempt followed by a successful one is a fact, never a WARNING: the
// backup is saved, and only the outcome line speaks of it.
func TestCloudUploadAttemptsAreFacts(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 2}
	calls := 0
	cs, buf := newCloudWithCapturedLog(t, cfg, func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("2026/10/02 23:57:06 Failed to copyto: open /x: read-only file system\n"), errors.New("exit status 1")
		}
		return nil, nil
	})
	if err := cs.uploadWithRetry(context.Background(), "/tmp/a.tar", "remote:backup/a.tar"); err != nil {
		t.Fatalf("uploadWithRetry: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "INFO       Attempt 1/2 failed: Failed to copyto: open /x: read-only file system\n") {
		t.Fatalf("missing the attempt fact:\n%s", out)
	}
	if strings.Contains(out, "WARNING") || strings.Contains(out, "Upload retry attempt") {
		t.Fatalf("a retried attempt must not warn:\n%s", out)
	}
}

// A missing log folder is a fact under the retention header, and every log the pass
// then cannot delete counts toward "Logs deleted: 0 of <K>".
func TestCloudMissingLogFolderIsAFactAndCounts(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", CloudLogPath: "remote:logs"}
	cs, buf := newCloudWithCapturedLog(t, cfg, nil)
	cs.markCloudLogPathMissing("remote:logs", "directory not found")
	if !strings.Contains(buf.String(), "INFO       Log folder not found: remote:logs\n") || strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("want the fact and no WARNING:\n%s", buf.String())
	}
	if cs.deleteAssociatedLog(context.Background(), "host-backup-20260101-000000.tar.zst") {
		t.Fatalf("a log under a missing folder cannot be deleted")
	}
	if got := cs.LastRetentionSummary().LogsNotDeleted; got != 1 {
		t.Fatalf("LogsNotDeleted = %d, want 1", got)
	}
}

// A Local archive without its .metadata is listed from its file name; List records it
// for the retention fact and writes no WARNING of its own.
func TestLocalListRecordsArchivesWithoutMetadata(t *testing.T) {
	dir := t.TempDir()
	logger, buf := newCapturedLogger()
	local, err := NewLocalStorage(&config.Config{BackupPath: dir}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	name := "host-backup-20260101-000000.tar.zst"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if _, err := local.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("List must not warn:\n%s", buf.String())
	}
	if len(local.lastNoMetadata) != 1 || local.lastNoMetadata[0] != name {
		t.Fatalf("lastNoMetadata = %v, want [%s]", local.lastNoMetadata, name)
	}
	if n := logListingNoMetadata(logger, local.lastNoMetadata); n != 1 {
		t.Fatalf("logListingNoMetadata = %d, want 1", n)
	}
	if !strings.Contains(buf.String(), "INFO       No metadata, name used: "+name+"\n") {
		t.Fatalf("missing the retention fact:\n%s", buf.String())
	}
}

// A secondary directory that cannot be created is a DirectoryError the storage
// initialization turns into "  Directory not created"; the backend writes no WARNING.
func TestSecondaryDirectoryNotCreatedIsTyped(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	logger, buf := newCapturedLogger()
	s, err := NewSecondaryStorage(&config.Config{SecondaryPath: filepath.Join(blocker, "sub")}, logger, "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	_, err = s.DetectFilesystem(context.Background())
	var dirErr *DirectoryError
	if !errors.As(err, &dirErr) {
		t.Fatalf("DetectFilesystem error = %v, want a DirectoryError", err)
	}
	if got := safefs.SystemErrorText(dirErr.Err); got != "not a directory" {
		t.Fatalf("cause = %q, want not a directory", got)
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("the backend must not warn:\n%s", buf.String())
	}
}

func TestCloudCheckTimeoutIsAFact(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneTimeoutConnection: 1}
	cs, buf := newCloudWithCapturedLog(t, cfg, func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, errors.New("signal: killed")
	})
	if _, err := cs.DetectFilesystem(context.Background()); err == nil {
		t.Fatalf("DetectFilesystem must fail on a remote that never answers")
	}
	if !strings.Contains(buf.String(), "INFO       Timed out after 1s\n") {
		t.Fatalf("missing the timeout fact:\n%s", buf.String())
	}
}

// With RCLONE_RETRIES > 1, every failed attempt is a fact with its cause, and when
// all of them fail no "  Upload failed" line repeats the cause after them: the caller's
// outcome follows the attempt lines directly. With a single attempt there is no
// attempt line, and "  Upload failed" carries the cause.
func TestCloudUploadAttemptLinesReplaceTheUploadFailedFact(t *testing.T) {
	const cause = "Failed to copyto: open /x: read-only file system"
	failingCopy := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "copyto" {
			return []byte("2026/10/03 10:00:00 " + cause + "\n"), errors.New("exit status 1")
		}
		return nil, nil
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "host-backup-20261003-100000.tar.zst")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 3}
	cs, buf := newCloudWithCapturedLog(t, cfg, failingCopy)
	err := cs.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive})
	if err == nil {
		t.Fatalf("Store must fail when every attempt fails")
	}
	if !AttemptsReported(err) {
		t.Fatalf("AttemptsReported(%v) = false, want true after 3 failed attempts", err)
	}
	out := stripTimes(buf.String())
	want := "INFO       Attempt 1/3 failed: " + cause + "\n" +
		"INFO       Attempt 2/3 failed: " + cause + "\n" +
		"INFO       Attempt 3/3 failed: " + cause + "\n"
	if !strings.Contains(out, want) || strings.Contains(out, "Upload failed") || strings.Contains(out, "WARNING") {
		t.Fatalf("want the three attempt facts and no \"Upload failed\" line:\n%s", out)
	}

	cfg = &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 1}
	cs, buf = newCloudWithCapturedLog(t, cfg, failingCopy)
	err = cs.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive})
	if err == nil || AttemptsReported(err) {
		t.Fatalf("a single failed attempt is not reported by attempt lines: err=%v", err)
	}
	out = stripTimes(buf.String())
	if !strings.Contains(out, "INFO       Upload failed: "+cause+"\n") || strings.Contains(out, "Attempt") {
		t.Fatalf("want \"Upload failed\" with the cause and no attempt line:\n%s", out)
	}
}

// What a failing SIDECAR upload prints with RCLONE_RETRIES=3, pinned as it is today
// (round 3 leaves it unchanged for the maintainer to decide): the attempt lines of the
// sidecar, without its name, then "  Sidecar failed: <file>: <cause>". The caller then
// closes the block with "⚠ Cloud Storage (rclone): backup saved, sidecar file not saved".
func TestCloudSidecarUploadFailureWithRetries(t *testing.T) {
	const cause = "Failed to copyto: permission denied"
	dir := t.TempDir()
	archive := filepath.Join(dir, "host-backup-20261003-100000.tar.zst")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := os.WriteFile(archive+".sha256", []byte("sum"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 3}
	cs, buf := newCloudWithCapturedLog(t, cfg, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "copyto":
			if strings.HasSuffix(args[len(args)-2], ".sha256") {
				return []byte("2026/10/03 10:00:00 " + cause + "\n"), errors.New("exit status 1")
			}
		case "lsl":
			if strings.HasSuffix(args[len(args)-1], filepath.Base(archive)) {
				return []byte("        7 2026-10-03 10:00:00.000000000 " + filepath.Base(archive) + "\n"), nil
			}
		}
		return nil, nil
	})
	err := cs.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive})
	var se *StorageError
	if !errors.As(err, &se) || !se.PrimarySaved {
		t.Fatalf("Store = %v, want a StorageError with the primary saved", err)
	}
	var visible []string
	for _, line := range strings.Split(stripTimes(buf.String()), "\n") {
		if strings.HasPrefix(line, "INFO") || strings.HasPrefix(line, "WARNING") {
			visible = append(visible, line)
		}
	}
	want := []string{
		"INFO       Attempt 1/3 failed: " + cause,
		"INFO       Attempt 2/3 failed: " + cause,
		"INFO       Attempt 3/3 failed: " + cause,
		"INFO       Sidecar failed: " + filepath.Base(archive) + ".sha256: " + cause,
	}
	if strings.Join(visible, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible lines =\n%s\nwant\n%s", strings.Join(visible, "\n"), strings.Join(want, "\n"))
	}
}

// A file whose owner or mode could not be set gets one fact per failed operation, in
// the order they run: the chown ("Owner failed"), then the chmod ("Permissions failed").
func TestPermissionsFailureFactsFollowTheOperations(t *testing.T) {
	ownerErr := &os.PathError{Op: "chown", Path: "/b/a.tar.zst", Err: os.ErrPermission}
	modeErr := &os.PathError{Op: "chmod", Path: "/b/a.tar.zst", Err: errors.New("read-only file system")}

	logger, buf := newCapturedLogger()
	logPermissionsFailure(logger, "/b/a.tar.zst", &PermissionsError{Owner: ownerErr})
	if got := stripTimes(buf.String()); got != "INFO       Owner failed: a.tar.zst: permission denied\n" {
		t.Fatalf("chown only = %q", got)
	}

	logger, buf = newCapturedLogger()
	logPermissionsFailure(logger, "/b/a.tar.zst", &PermissionsError{Owner: ownerErr, Mode: modeErr})
	want := "INFO       Owner failed: a.tar.zst: permission denied\n" +
		"INFO       Permissions failed: a.tar.zst: read-only file system\n"
	if got := stripTimes(buf.String()); got != want {
		t.Fatalf("chown and chmod = %q, want %q", got, want)
	}

	logger, buf = newCapturedLogger()
	logPermissionsFailure(logger, "/b/a.tar.zst", &PermissionsError{Mode: modeErr})
	if got := stripTimes(buf.String()); got != "INFO       Permissions failed: a.tar.zst: read-only file system\n" {
		t.Fatalf("chmod only = %q", got)
	}
}

// The Primary's Store reports a chown that failed: the fact, and one
// StoreIssuePermissionsNotSet for the caller's "⚠ Local Storage: permissions not set".
func TestLocalStoreReportsAFailedChown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can chown to root; the failure needs an unprivileged run")
	}
	dir := t.TempDir()
	logger, buf := newCapturedLogger()
	local, err := NewLocalStorage(&config.Config{BackupPath: dir}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	name := "host-backup-20260101-000000.tar.zst"
	archive := filepath.Join(dir, name)
	for _, f := range []string{archive, archive + ".sha256"} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if err := local.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	out := stripTimes(buf.String())
	for _, want := range []string{
		"INFO       Owner failed: " + name + ": operation not permitted\n",
		"INFO       Owner failed: " + name + ".sha256: operation not permitted\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Permissions failed") || strings.Contains(out, "WARNING") {
		t.Fatalf("the chmod succeeded and the backend writes no outcome:\n%s", out)
	}
	if issues := local.LastStoreIssues(); len(issues) != 1 || issues[0] != StoreIssuePermissionsNotSet {
		t.Fatalf("LastStoreIssues = %v, want one StoreIssuePermissionsNotSet", issues)
	}
}

// A secondary destination directory that cannot be created at step [6] is the same
// fact the storage initialization and the step [8] log copy print: "  Directory not
// created: <cause>". The caller closes the block with "✗ Secondary Storage: backup not saved".
func TestSecondaryStoreDirectoryNotCreated(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "host-backup-20260101-000000.tar.zst")
	if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	logger, buf := newCapturedLogger()
	s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: filepath.Join(blocker, "sub")}, logger, "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	if err := s.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive}); err == nil {
		t.Fatalf("Store must fail when the destination directory cannot be created")
	}
	out := stripTimes(buf.String())
	if out != "INFO       Directory not created: not a directory\n" {
		t.Fatalf("visible lines = %q, want only the directory fact", out)
	}
}

// Live test case 2: a destination the copy cannot write into. The fact carries the
// system error without the path the wrapper names ("failed to create temporary file in
// <dir>").
func TestSecondaryCopyFailedFactCarriesNoPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory; the failure needs an unprivileged run")
	}
	dest := t.TempDir()
	if err := os.Chmod(dest, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dest, 0o700) })
	archive := filepath.Join(t.TempDir(), "host-backup-20260101-000000.tar.zst")
	if err := os.WriteFile(archive, []byte("x"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	logger, buf := newCapturedLogger()
	s, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: dest}, logger, "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	if err := s.Store(context.Background(), archive, &types.BackupMetadata{BackupFile: archive}); err == nil {
		t.Fatalf("Store must fail when the destination cannot be written")
	}
	if got := stripTimes(buf.String()); got != "INFO       Copy failed: permission denied\n" {
		t.Fatalf("visible lines = %q, want the system error alone", got)
	}
}
