package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// Storage init, a local directory that cannot be created: one attempt, no retry and
// no backoff (like the Secondary), one fact.
func TestCloudLocalDirectoryNotCreatedIsNotRetried(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	writeTestFile(t, blocker, "x")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: filepath.Join(blocker, "cloud"), RcloneTimeoutConnection: 30}
	rec := &argvRecorder{}
	cs, buf := newCloudWithCapturedLog(t, cfg, rec.exec)
	waits := 0
	cs.waitForRetry = func(context.Context, time.Duration) error { waits++; return nil }
	_, err := cs.DetectFilesystem(context.Background())
	var dirErr *DirectoryError
	if !errors.As(err, &dirErr) {
		t.Fatalf("DetectFilesystem = %v, want a DirectoryError", err)
	}
	if waits != 0 || len(rec.argv()) != 0 {
		t.Fatalf("no retry and no rclone: waits=%d argv=%v", waits, rec.argv())
	}
	if got := strings.Count(buf.String(), "Directory not created:"); got != 1 {
		t.Fatalf("the fact must be printed once, got %d:\n%s", got, buf.String())
	}
}

// Step [6], local directory: (re)created 0700 before the copy, like the Secondary; a
// directory that cannot be created is "  Directory not created: <system error>" and
// the copy does not run.
func TestCloudLocalStoreRecreatesTheDirectory(t *testing.T) {
	cs, _, archive, root := newLocalFormStore(t, "", false)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := cs.Store(context.Background(), archive, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if st, err := os.Stat(root); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the directory must be re-created 0700: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.Base(archive))); err != nil {
		t.Fatalf("the copy must have run: %v", err)
	}

	cs, buf, archive, root := newLocalFormStore(t, "", false)
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil { // a file where the directory goes
		t.Fatalf("write blocker: %v", err)
	}
	rec := &argvRecorder{}
	cs.execCommand = rec.exec
	err := cs.Store(context.Background(), archive, nil)
	var se *StorageError
	if !errors.As(err, &se) || se.PrimarySaved {
		t.Fatalf("Store = %v, want a backup not saved", err)
	}
	if got, want := strings.Join(visibleOf(buf.String()), "\n"), "INFO       Directory not created: not a directory"; got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}
	if len(rec.argv()) != 0 {
		t.Fatalf("no copy without the directory: %v", rec.argv())
	}
}

// The log copied into a local directory gets 0640 only on a filesystem that takes
// ownership (as for the backups), judged on the detection made at storage init: no
// second detection, no ownership probe in the log directory.
func TestCloudSetLocalLogModeFollowsOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cloud")
	logDir := filepath.Join(dir, "proxsave", "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logFile := filepath.Join(logDir, "backup-node-20261003-100000.log")
	for _, tc := range []struct {
		name    string
		fsInfo  *FilesystemInfo
		applied bool
		mode    os.FileMode
	}{
		{"ownership", &FilesystemInfo{Type: FilesystemExt4, SupportsOwnership: true}, true, 0o640},
		{"no ownership", &FilesystemInfo{Type: FilesystemFAT32}, false, 0o644},
		{"no detection", nil, false, 0o644},
	} {
		if err := os.WriteFile(logFile, []byte("log"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(logFile, 0o644); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: dir}, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}
		detections := 0
		cs.fsDetector.mountPointLookup = func(string) (string, error) { detections++; return dir, nil }
		cs.fsDetector.ownershipSupportTest = func(context.Context, string) bool { detections++; return true }
		applied, err := cs.SetLocalLogMode(context.Background(), logFile, tc.fsInfo)
		st, _ := os.Stat(logFile)
		if err != nil || applied != tc.applied || st.Mode().Perm() != tc.mode {
			t.Fatalf("%s: applied=%v err=%v mode=%v, want applied=%v mode=%v", tc.name, applied, err, st.Mode().Perm(), tc.applied, tc.mode)
		}
		if detections != 0 {
			t.Fatalf("%s: the init detection is reused, got %d new detection calls", tc.name, detections)
		}
		entries, _ := os.ReadDir(logDir)
		if len(entries) != 1 {
			t.Fatalf("%s: only the log in its directory, got %v", tc.name, entries)
		}
	}

	cs, _ := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: dir}, newTestLogger(), "")
	ext4 := &FilesystemInfo{Type: FilesystemExt4, SupportsOwnership: true}
	if _, err := cs.SetLocalLogMode(context.Background(), filepath.Join(logDir, "missing.log"), ext4); err == nil {
		t.Fatalf("a mode that cannot be set must be returned")
	}
	remote, _ := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: "remote:x"}, newTestLogger(), "")
	if applied, err := remote.SetLocalLogMode(context.Background(), logFile, ext4); applied || err != nil {
		t.Fatalf("the remote form sets nothing: %v %v", applied, err)
	}
}
