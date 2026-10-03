package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// Live test case 6: a backup retention could not delete keeps its associated log; the
// log goes only with the backup's data file.
func TestRetentionKeepsTheLogOfABackupNotDeleted(t *testing.T) {
	const name = "node-backup-20261003-100000.tar.zst"
	const logName = "backup-node-20261003-100000.log"
	for _, deletable := range []bool{true, false} {
		backupDir := t.TempDir()
		localLogs := t.TempDir()
		secondaryLogs := t.TempDir()
		archive := filepath.Join(backupDir, name)
		if deletable {
			writeTestFile(t, archive, "x")
		} else if err := os.MkdirAll(filepath.Join(archive, "blocker"), 0o755); err != nil { // a non-empty directory cannot be removed
			t.Fatalf("mkdir: %v", err)
		}
		writeTestFile(t, filepath.Join(localLogs, logName), "log")
		writeTestFile(t, filepath.Join(secondaryLogs, logName), "log")

		local, err := NewLocalStorage(&config.Config{BackupPath: backupDir, LogPath: localLogs}, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewLocalStorage: %v", err)
		}
		logDeleted, err := local.deleteBackupInternal(context.Background(), archive)
		checkLogKept(t, "local", deletable, logDeleted, err, filepath.Join(localLogs, logName))

		secondary, err := NewSecondaryStorage(&config.Config{SecondaryPath: backupDir, SecondaryLogPath: secondaryLogs}, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		if deletable {
			writeTestFile(t, archive, "x")
		}
		logDeleted, err = secondary.deleteBackupInternal(context.Background(), archive)
		checkLogKept(t, "secondary", deletable, logDeleted, err, filepath.Join(secondaryLogs, logName))
	}
}

func checkLogKept(t *testing.T, label string, deletable, logDeleted bool, err error, logFile string) {
	t.Helper()
	_, statErr := os.Stat(logFile)
	if deletable {
		if err != nil || !logDeleted || !os.IsNotExist(statErr) {
			t.Fatalf("%s: a deleted backup takes its log: err=%v logDeleted=%v stat=%v", label, err, logDeleted, statErr)
		}
		return
	}
	if err == nil || logDeleted || statErr != nil {
		t.Fatalf("%s: a backup not deleted keeps its log: err=%v logDeleted=%v stat=%v", label, err, logDeleted, statErr)
	}
}

// The cloud: when the archive's deletefile fails, no deletefile reaches the log.
func TestCloudRetentionKeepsTheLogOfABackupNotDeleted(t *testing.T) {
	const name = "node-backup-20261003-100000.tar.zst"
	for _, deletable := range []bool{true, false} {
		rec := &argvRecorder{respond: func(args []string) ([]byte, error) {
			if !deletable && args[0] == "deletefile" && strings.HasSuffix(args[1], "/"+name) {
				return []byte("2026/10/03 10:00:00 ERROR : x: Failed to delete: permission denied\n"), errors.New("exit status 1")
			}
			return nil, nil
		}}
		cs := newFormCloud(t, cloudFormsFor(t)[2], nil, rec)
		cs.setRemoteSnapshot(map[string]struct{}{name: {}})
		logDeleted, err := cs.deleteBackupInternal(context.Background(), name)
		logCalls := 0
		for _, line := range rec.argv() {
			if strings.Contains(line, "backup-node-20261003-100000.log") {
				logCalls++
			}
		}
		if deletable && (err != nil || !logDeleted || logCalls != 1) {
			t.Fatalf("a deleted backup takes its log: err=%v logDeleted=%v log calls=%d", err, logDeleted, logCalls)
		}
		if !deletable && (err == nil || logDeleted || logCalls != 0) {
			t.Fatalf("a backup not deleted keeps its log: err=%v logDeleted=%v log calls=%d (%v)", err, logDeleted, logCalls, rec.argv())
		}
	}
}
