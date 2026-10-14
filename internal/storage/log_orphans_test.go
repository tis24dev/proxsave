package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestRunLogKey(t *testing.T) {
	cases := []struct {
		name     string
		host, ts string
		ok       bool
	}{
		{"backup-node-20261003-100000.log", "node", "20261003-100000", true},
		{"backup-pbs.sci1.com-20261011-212710.log", "pbs.sci1.com", "20261011-212710", true},
		{"backup-my-host-20261003-100000.log", "my-host", "20261003-100000", true},
		{"backup-stats-20261003-100000.json", "", "", false},
		{"daemon-node-20261003-100000.log", "", "", false},
		{"backup-node-2026100-100000.log", "", "", false},
		{"backup-node-20261399-100000.log", "", "", false},
		{"backup--20261003-100000.log", "", "", false},
	}
	for _, tc := range cases {
		host, ts, ok := runLogKey(tc.name)
		if host != tc.host || ts != tc.ts || ok != tc.ok {
			t.Errorf("runLogKey(%q) = %q, %q, %v; want %q, %q, %v", tc.name, host, ts, ok, tc.host, tc.ts, tc.ok)
		}
	}
}

func orphanBackups(names ...string) []*types.BackupMetadata {
	out := make([]*types.BackupMetadata, 0, len(names))
	for _, name := range names {
		out = append(out, &types.BackupMetadata{BackupFile: "/backups/" + name})
	}
	return out
}

// fakeOrphanTarget lists a fixed folder and records what it was asked to remove.
type fakeOrphanTarget struct {
	names     []string
	listErr   error
	removeErr map[string]error
	removed   []string
	listed    int
}

func (f *fakeOrphanTarget) target() orphanLogTarget {
	return orphanLogTarget{
		op:       "local orphan logs",
		where:    "/logs",
		hostname: "node",
		aliases:  []string{"node.lan"},
		list: func(context.Context) ([]string, error) {
			f.listed++
			return f.names, f.listErr
		},
		remove: func(_ context.Context, name string) error {
			f.removed = append(f.removed, name)
			return f.removeErr[name]
		},
	}
}

func TestPruneOrphanLogsDeletesOnlyExpiredOrphansOfThisHost(t *testing.T) {
	backups := orphanBackups(
		"node-backup-20261003-000000.tar.zst", // deleted by this pass
		"node-backup-20261005-000000.tar.zst",
		"node-backup-20261007-000000.tar.zst",
	)
	removed := []string{"/backups/node-backup-20261003-000000.tar.zst"}
	f := &fakeOrphanTarget{
		names: []string{
			"backup-node-20261001-000000.log",     // orphan, older than the oldest kept: deleted
			"backup-node-20261003-000000.log",     // log of the archive this pass deleted: not looked at again
			"backup-node.lan-20261004-000000.log", // orphan under an alias: deleted
			"backup-node-20261005-000000.log",     // has its archive
			"backup-node-20261006-000000.log",     // orphan, newer than the oldest kept: kept
			"backup-other-20261001-000000.log",    // another host's
			"backup-stats-20261001-000000.json",
			"daemon-node-20261001-000000.log",
		},
	}
	logger, buf := newRecordingRetentionLogger()
	res := pruneOrphanLogs(context.Background(), logger, f.target(), backups, removed)

	want := []string{"backup-node-20261001-000000.log", "backup-node.lan-20261004-000000.log"}
	if !reflect.DeepEqual(f.removed, want) {
		t.Fatalf("removed %v, want %v", f.removed, want)
	}
	if res.deleted != 2 || res.notDeleted != 0 || res.notListed != nil {
		t.Fatalf("result = %+v, want 2 deleted", res)
	}
	out := buf.String()
	for _, line := range []string{
		"local orphan logs: scan path=/logs pattern=backup-*.log",
		"local orphan logs: listed=5 with_archive=1 orphans=3 oldest_kept=20261005-000000",
		"local orphan logs: kept backup-node-20261006-000000.log reason=newer_than_oldest_kept",
		"local orphan logs: deleted backup-node-20261001-000000.log",
		"local orphan logs: result deleted=2 not_deleted=0 kept=1",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
	if strings.Contains(out, "INFO") {
		t.Errorf("a clean pass prints no INFO line:\n%s", out)
	}
}

func TestPruneOrphanLogsFailedAndMissingRemovals(t *testing.T) {
	backups := orphanBackups("node-backup-20261005-000000.tar.zst")
	f := &fakeOrphanTarget{
		names: []string{"backup-node-20261001-000000.log", "backup-node-20261002-000000.log"},
		removeErr: map[string]error{
			"backup-node-20261001-000000.log": &fs.PathError{Op: "remove", Path: "/logs/backup-node-20261001-000000.log", Err: fs.ErrPermission},
			"backup-node-20261002-000000.log": fs.ErrNotExist,
		},
	}
	logger, buf := newRecordingRetentionLogger()
	res := pruneOrphanLogs(context.Background(), logger, f.target(), backups, nil)
	if res.deleted != 0 || res.notDeleted != 1 {
		t.Fatalf("result = %+v, want 1 not deleted and the missing one not counted", res)
	}
	if out := buf.String(); !strings.Contains(out, "INFO       Log not deleted: backup-node-20261001-000000.log: permission denied") {
		t.Fatalf("missing the fact line of the failed removal:\n%s", out)
	}
}

func TestPruneOrphanLogsFolderNotListed(t *testing.T) {
	f := &fakeOrphanTarget{listErr: errors.New("read failed")}
	res := pruneOrphanLogs(context.Background(), newTestLogger(), f.target(), orphanBackups("node-backup-20261005-000000.tar.zst"), nil)
	if res.notListed == nil || res.deleted != 0 || len(f.removed) != 0 {
		t.Fatalf("result = %+v removed=%v, want the listing error and nothing removed", res, f.removed)
	}
}

func TestPruneOrphanLogsWithoutAKeptBackupDeletesNothing(t *testing.T) {
	f := &fakeOrphanTarget{names: []string{"backup-node-20261001-000000.log"}}
	backups := orphanBackups("node-backup-20261003-000000.tar.zst")
	res := pruneOrphanLogs(context.Background(), newTestLogger(), f.target(), backups, []string{"/backups/node-backup-20261003-000000.tar.zst"})
	if f.listed != 0 || len(f.removed) != 0 || res != (orphanLogResult{}) {
		t.Fatalf("listed=%d removed=%v result=%+v, want no listing with no backup kept", f.listed, f.removed, res)
	}
}

func TestListLogFolderMissingFolderIsEmpty(t *testing.T) {
	names, err := listLogFolder(context.Background(), "test-log-list", filepath.Join(t.TempDir(), "absent"), time.Second)
	if err != nil || len(names) != 0 {
		t.Fatalf("listLogFolder = %v, %v; want an empty list", names, err)
	}
}

// writeSidecarBackup writes a node-backup-<ts>.tar.zst with its sidecars, dated ts.
func writeSidecarBackup(t *testing.T, dir, ts string) string {
	t.Helper()
	when, err := time.ParseInLocation(runLogTimestampLayout, ts, time.Local)
	if err != nil {
		t.Fatalf("parse %s: %v", ts, err)
	}
	name := filepath.Join(dir, "node-backup-"+ts+".tar.zst")
	writeTestFile(t, name, "x")
	for _, suffix := range []string{".metadata", ".metadata.sha256", ".sha256"} {
		writeTestFile(t, name+suffix, "aux")
	}
	for _, path := range []string{name, name + ".metadata", name + ".metadata.sha256", name + ".sha256"} {
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	return name
}

// Discussion #336 end to end on the primary: the run logs no archive answers to go
// once they are older than the oldest backup kept, counted with the logs deleted.
func TestLocalRetentionDeletesExpiredOrphanLogs(t *testing.T) {
	for _, steady := range []bool{false, true} {
		backupDir, logDir := t.TempDir(), t.TempDir()
		writeSidecarBackup(t, backupDir, "20261003-020000")
		writeSidecarBackup(t, backupDir, "20261004-020000")
		writeSidecarBackup(t, backupDir, "20261005-020000")
		logs := []string{
			"backup-node-20261001-090000.log", // a daemon start, before every backup: expired
			"backup-node-20261003-020000.log", // with its archive
			"backup-node-20261003-120000.log", // a dry run: expired only once 20261003 is rotated
			"backup-node-20261004-020000.log", // with its archive
			"backup-node-20261005-020000.log", // with its archive
			"backup-node-20261005-150000.log", // a failed run, after the oldest kept: kept
		}
		for _, name := range logs {
			writeTestFile(t, filepath.Join(logDir, name), "log")
		}

		local, err := NewLocalStorage(&config.Config{BackupPath: backupDir, LogPath: logDir}, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewLocalStorage: %v", err)
		}
		local.hostname = "node"
		max := 2
		if steady {
			max = 3 // within the limit: no archive goes, the orphan pass still runs
		}
		deleted, err := local.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: max})
		if err != nil {
			t.Fatalf("ApplyRetention: %v", err)
		}

		gone := map[string]bool{"backup-node-20261001-090000.log": true}
		wantDeleted, wantLogs := 0, 1
		if !steady {
			gone["backup-node-20261003-020000.log"] = true
			gone["backup-node-20261003-120000.log"] = true
			wantDeleted, wantLogs = 1, 3
		}
		for _, name := range logs {
			_, statErr := os.Stat(filepath.Join(logDir, name))
			if gone[name] != os.IsNotExist(statErr) {
				t.Errorf("steady=%v %s: gone=%v, stat=%v", steady, name, gone[name], statErr)
			}
		}
		summary := local.LastRetentionSummary()
		if deleted != wantDeleted || summary.LogsDeleted != wantLogs || !summary.HasLogPath || summary.LogsNotListed || summary.LogsNotDeleted != 0 {
			t.Fatalf("steady=%v: deleted=%d summary=%+v; want %d deleted, %d logs deleted", steady, deleted, summary, wantDeleted, wantLogs)
		}
	}
}

// The same pass on the secondary, against SECONDARY_LOG_PATH; with no log folder the
// destination reports no logs at all.
func TestSecondaryRetentionDeletesExpiredOrphanLogs(t *testing.T) {
	backupDir, logDir := t.TempDir(), t.TempDir()
	writeSidecarBackup(t, backupDir, "20261003-020000")
	writeSidecarBackup(t, backupDir, "20261004-020000")
	writeTestFile(t, filepath.Join(logDir, "backup-node-20261002-090000.log"), "log")
	writeTestFile(t, filepath.Join(logDir, "backup-node-20261004-020000.log"), "log")

	secondary, err := NewSecondaryStorage(&config.Config{SecondaryPath: backupDir, SecondaryLogPath: logDir}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	secondary.hostname = "node"
	if _, err := secondary.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "backup-node-20261002-090000.log")); !os.IsNotExist(err) {
		t.Fatalf("the expired orphan is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "backup-node-20261004-020000.log")); err != nil {
		t.Fatalf("a log with its archive went: %v", err)
	}
	if s := secondary.LastRetentionSummary(); s.LogsDeleted != 1 || !s.HasLogPath {
		t.Fatalf("summary = %+v, want 1 log deleted at a destination keeping logs", s)
	}

	noLogs, err := NewSecondaryStorage(&config.Config{SecondaryPath: backupDir}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	noLogs.hostname = "node"
	if _, err := noLogs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if s := noLogs.LastRetentionSummary(); s.HasLogPath {
		t.Fatalf("summary = %+v, want no log path without SECONDARY_LOG_PATH", s)
	}
}

// The cloud lists CLOUD_LOG_PATH with lsf and removes an expired orphan with
// deletefile, in every form of CLOUD_REMOTE; an lsf that fails leaves the logs
// unchecked and says so.
func TestCloudRetentionDeletesExpiredOrphanLogs(t *testing.T) {
	newest := "node-backup-20241112-100000.tar.zst"
	oldest := "node-backup-20241110-100000.tar.zst"
	listing := strings.Join([]string{
		"100 2024-11-12 10:00:00 " + newest,
		"100 2024-11-10 10:00:00 " + oldest,
	}, "\n")
	for _, form := range cloudFormsFor(t) {
		for _, lsfFails := range []bool{false, true} {
			rec := &argvRecorder{respond: func(args []string) ([]byte, error) {
				switch args[0] {
				case "lsl":
					return []byte(listing), nil
				case "cat":
					return []byte(`{"hostname":"node"}`), nil
				case "lsf":
					if lsfFails {
						return []byte("2024/11/12 10:00:00 ERROR : x: permission denied\n"), errors.New("exit status 1")
					}
					return []byte("backup-node-20241101-090000.log\nbackup-node-20241110-100000.log\nbackup-node-20241111-090000.log\n"), nil
				}
				return nil, nil
			}}
			cs := newFormCloud(t, form, nil, rec)
			cs.hostname = "node"
			if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2}); err != nil {
				t.Fatalf("%s: ApplyRetention: %v", form.name, err)
			}
			var deletes []string
			for _, line := range rec.argv() {
				if strings.HasPrefix(line, "deletefile ") {
					deletes = append(deletes, line)
				}
			}
			s := cs.LastRetentionSummary()
			if lsfFails {
				if len(deletes) != 0 || !s.LogsNotListed || s.LogsNotListedCause == "" {
					t.Fatalf("%s: deletes=%v summary=%+v; want nothing deleted and the folder not listed", form.name, deletes, s)
				}
				continue
			}
			want := []string{"deletefile " + form.logDir + "/backup-node-20241101-090000.log"}
			if !reflect.DeepEqual(deletes, want) || s.LogsDeleted != 1 || !s.HasLogPath {
				t.Fatalf("%s: deletes=%v summary=%+v; want %v and 1 log deleted", form.name, deletes, s, want)
			}
		}
	}
}
