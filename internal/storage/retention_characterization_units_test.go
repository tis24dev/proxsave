package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// The direct-call half of the retention characterization lock (see
// retention_characterization_test.go): the branches of the six twin methods that
// ApplyRetention cannot reach with files on disk, called on both backends.

// charPrivate binds the private methods under test to one backend.
type charPrivate struct {
	gfs       func(context.Context, []*types.BackupMetadata, RetentionConfig) (int, error)
	simple    func(context.Context, []*types.BackupMetadata, int) (int, error)
	assocLog  func(context.Context, string) bool
	countLogs func(context.Context) int
	prune     func(context.Context, []*types.BackupMetadata) error
	tally     *retentionTally
	lastRet   *RetentionSummary
}

func charPrivates(t *testing.T, b any) charPrivate {
	t.Helper()
	// A typed nil receiver binds the methods but has no fields to point at.
	switch s := b.(type) {
	case *LocalStorage:
		p := charPrivate{gfs: s.applyGFSRetention, simple: s.applySimpleRetention, assocLog: s.deleteAssociatedLog, countLogs: s.countLogFiles, prune: s.pruneOrphanLogs}
		if s != nil {
			p.tally, p.lastRet = &s.retTally, &s.lastRet
		}
		return p
	case *SecondaryStorage:
		p := charPrivate{gfs: s.applyGFSRetention, simple: s.applySimpleRetention, assocLog: s.deleteAssociatedLog, countLogs: s.countLogFiles, prune: s.pruneOrphanLogs}
		if s != nil {
			p.tally, p.lastRet = &s.retTally, &s.lastRet
		}
		return p
	}
	t.Fatalf("unexpected backend %T", b)
	return charPrivate{}
}

// charBare builds a backend the way no constructor does: the bare struct with only the
// fields the twin methods read. logDir is the destination's log folder variable, decoy
// the other destination's; a nil-config backend is built with withConfig false.
func charBare(flavor string, logger *logging.Logger, basePath, logDir, decoy string, withConfig bool) any {
	switch flavor {
	case "local":
		l := &LocalStorage{logger: logger, basePath: basePath}
		if withConfig {
			l.config = &config.Config{BackupPath: basePath, LogPath: logDir, SecondaryLogPath: decoy}
		}
		return l
	default:
		s := &SecondaryStorage{logger: logger, basePath: basePath}
		if withConfig {
			s.config = &config.Config{SecondaryEnabled: true, SecondaryPath: basePath, SecondaryLogPath: logDir, LogPath: decoy}
		}
		return s
	}
}

// charNil is the typed nil receiver of a flavor.
func charNil(flavor string) any {
	if flavor == "local" {
		return (*LocalStorage)(nil)
	}
	return (*SecondaryStorage)(nil)
}

// Both retention methods leave an unverified entry and a verified entry with no date
// alone, each with its own fact line, and count them as skipped; a nil entry is
// dropped silently. Only a direct call can hand a verified entry with a zero date: the
// listing always stats one in.
func TestRetentionCharacterizationInertEntries(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			logger, sink := newCharLogger()
			backend := fl.build(t, e, logger)
			p := charPrivates(t, backend)
			before := e.snapshot()
			sink.reset()
			entries := func() []*types.BackupMetadata {
				return []*types.BackupMetadata{
					{BackupFile: filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")), Timestamp: charWhen(t, "2024-06-20"), Verified: true},
					{BackupFile: filepath.Join(e.backups, charArchiveName(charHost, "2024-06-10")), Verified: true},
					{BackupFile: filepath.Join(e.backups, charArchiveName(charHost, "2024-06-01")), Timestamp: charWhen(t, "2024-06-01"), Verified: false},
					nil,
				}
			}

			sink.mark("applySimpleRetention max=1")
			*p.tally = retentionTally{}
			if n, err := p.simple(context.Background(), entries(), 1); n != 0 || err != nil {
				t.Errorf("applySimpleRetention = %d, %v; want 0, nil", n, err)
			}
			if p.tally.skipped != 2 || p.tally.planned != 0 {
				t.Errorf("simple tally = %+v, want skipped 2 planned 0", *p.tally)
			}

			sink.mark("applyGFSRetention daily=1 weekly=0 monthly=0 yearly=1")
			*p.tally = retentionTally{}
			if n, err := p.gfs(context.Background(), entries(), charGFS); n != 0 || err != nil {
				t.Errorf("applyGFSRetention = %d, %v; want 0, nil", n, err)
			}
			want := RetentionSummary{BackupsRemaining: 1, HasLogInfo: true, Skipped: 2}
			if got := backend.LastRetentionSummary(); got != want {
				t.Errorf("LastRetentionSummary\n got %+v\nwant %+v", got, want)
			}

			e.checkFiles(before, nil)
			assertRetentionGolden(t, "inert_entries."+fl.name, charNormalize(sink.text(), e.root))
		})
	}
}

// deleteAssociatedLog in every state the log of a deleted backup can be in. Only the
// present log goes; only the failed removal is counted.
func TestRetentionCharacterizationDeleteAssociatedLog(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			e.addLog(charHost, "2024-06-20", charLogFile)    // the log this run writes
			e.addLog(charHost, "2024-06-10", charLogFile)    // deleted
			e.addLog(charHost, "2024-06-01", charLogBlocked) // its removal fails
			e.addLog(charHost, "2024-05-01", charLogFile)    // held open, then unreadable open logs
			logger, sink := newCharLogger()
			backend := fl.build(t, e, logger)
			p := charPrivates(t, backend)
			before := e.snapshot()
			sink.reset()
			ctx := context.Background()
			archive := func(day string) string { return filepath.Join(e.backups, charArchiveName(charHost, day)) }
			check := func(label string, got, want bool) {
				t.Helper()
				if got != want {
					t.Errorf("%s: deleteAssociatedLog = %v, want %v", label, got, want)
				}
			}

			sink.mark("nil receiver")
			check("nil receiver", charPrivates(t, charNil(fl.name)).assocLog(ctx, archive("2024-06-10")), false)
			sink.mark("no config")
			check("no config", charPrivates(t, charBare(fl.name, logger, e.backups, e.logs, e.decoy, false)).assocLog(ctx, archive("2024-06-10")), false)
			sink.mark("log folder variable not set")
			check("unset", charPrivates(t, charBare(fl.name, logger, e.backups, "", e.decoy, true)).assocLog(ctx, archive("2024-06-10")), false)
			sink.mark("name without a run key")
			check("no key", p.assocLog(ctx, filepath.Join(e.backups, "manual-archive.tar.zst")), false)

			sink.mark("the log this run writes")
			if err := logger.OpenLogFile(filepath.Join(e.logs, charLogName(charHost, "2024-06-20"))); err != nil {
				t.Fatalf("open own log: %v", err)
			}
			check("own log", p.assocLog(ctx, archive("2024-06-20")), false)
			if err := logger.CloseLogFile(); err != nil {
				t.Fatalf("close own log: %v", err)
			}

			sink.mark("absent")
			check("absent", p.assocLog(ctx, archive("2024-04-01")), false)
			sink.mark("present")
			check("present", p.assocLog(ctx, archive("2024-06-10")), true)
			sink.mark("removal fails")
			check("removal fails", p.assocLog(ctx, archive("2024-06-01")), false)
			if p.tally.logsNotDeleted != 1 {
				t.Errorf("logsNotDeleted = %d, want 1", p.tally.logsNotDeleted)
			}

			sink.mark("held open by another process")
			holdOpenForTest(t, filepath.Join(e.logs, charLogName(charHost, "2024-05-01")))
			check("held open", p.assocLog(ctx, archive("2024-05-01")), false)
			sink.mark("open logs cannot be read")
			t.Cleanup(safefs.SetProcRootForTest(filepath.Join(e.root, "proc-missing")))
			check("open check failed", p.assocLog(ctx, archive("2024-05-01")), false)

			if p.tally.logsNotDeleted != 1 {
				t.Errorf("logsNotDeleted = %d at the end, want 1", p.tally.logsNotDeleted)
			}
			e.checkFiles(before, []string{charLogRel("logs", "2024-06-10")})
			assertRetentionGolden(t, "delete_associated_log."+fl.name, charNormalize(sink.text(), e.root))
		})
	}
}

// countLogFiles counts what the "backup-*.log" glob matches, directories and other
// hosts' logs included, and answers -1 when it cannot count.
func TestRetentionCharacterizationCountLogFiles(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			e.addLog(charHost, "2024-06-20", charLogFile)
			e.addLog(charHost, "2024-06-10", charLogBlocked)
			e.addLog("other", "2024-06-01", charLogFile)
			e.addLogFolderFile("notes.txt")
			e.addLogFolderFile("backup-pve-latest.txt")
			badDir := filepath.Join(e.root, "logs[")
			if err := os.MkdirAll(badDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			writeTestFile(t, filepath.Join(badDir, charLogName(charHost, "2024-06-20")), "log")
			logger, sink := newCharLogger()
			backend := fl.build(t, e, logger)
			before := e.snapshot()
			sink.reset()
			ctx := context.Background()
			bare := func(logDir string) charPrivate {
				return charPrivates(t, charBare(fl.name, logger, e.backups, logDir, e.decoy, true))
			}
			cases := []struct {
				label string
				count func() int
				want  int
			}{
				{"nil receiver", func() int { return charPrivates(t, charNil(fl.name)).countLogs(ctx) }, -1},
				{"no config", func() int {
					return charPrivates(t, charBare(fl.name, logger, e.backups, e.logs, e.decoy, false)).countLogs(ctx)
				}, -1},
				{"log folder variable not set", func() int { return bare("").countLogs(ctx) }, 0},
				{"log folder missing", func() int { return bare(filepath.Join(e.root, "missing")).countLogs(ctx) }, 0},
				{"log folder", func() int { return charPrivates(t, backend).countLogs(ctx) }, 3},
				{"log folder the glob cannot parse", func() int { return bare(badDir).countLogs(ctx) }, -1},
			}
			for _, c := range cases {
				sink.mark(c.label)
				if got := c.count(); got != c.want {
					t.Errorf("%s: countLogFiles = %d, want %d", c.label, got, c.want)
				}
			}
			e.checkFiles(before, nil)
			assertRetentionGolden(t, "count_log_files."+fl.name, charNormalize(sink.text(), e.root))
		})
	}
}

// The pruneOrphanLogs method around the shared pass: nothing without a log folder,
// nothing when no backup carries a run key, a listing failure recorded in the summary,
// and a cancelled context returned as is.
func TestRetentionCharacterizationPruneOrphanLogs(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			e.addLog(charHost, "2024-01-01", charLogFile)
			notAFolder := filepath.Join(e.root, "logs-file")
			writeTestFile(t, notAFolder, "a file where the log folder should be")
			logger, sink := newCharLogger()
			backend := fl.build(t, e, logger)
			before := e.snapshot()
			sink.reset()
			kept := []*types.BackupMetadata{{BackupFile: filepath.Join(e.backups, charArchiveName(charHost, "2024-06-20")), Timestamp: charWhen(t, "2024-06-20"), Verified: true}}
			unkeyed := []*types.BackupMetadata{{BackupFile: filepath.Join(e.backups, "manual-archive.tar.zst"), Verified: true}}

			sink.mark("log folder variable not set")
			unset := charPrivates(t, charBare(fl.name, logger, e.backups, "", e.decoy, true))
			if err := unset.prune(context.Background(), kept); err != nil || unset.tally.hasLogPath {
				t.Errorf("unset: err=%v hasLogPath=%v; want nil, false", err, unset.tally.hasLogPath)
			}

			sink.mark("no backup carries a run key")
			p := charPrivates(t, backend)
			if err := p.prune(context.Background(), unkeyed); err != nil || !p.tally.hasLogPath {
				t.Errorf("unkeyed: err=%v hasLogPath=%v; want nil, true", err, p.tally.hasLogPath)
			}

			sink.mark("log folder cannot be listed")
			listFails := charPrivates(t, charBare(fl.name, logger, e.backups, notAFolder, e.decoy, true))
			if err := listFails.prune(context.Background(), kept); err != nil {
				t.Errorf("list fails: err=%v, want nil", err)
			}
			s := RetentionSummary{}
			listFails.tally.apply(&s)
			if !s.HasLogPath || !s.LogsNotListed || s.LogsNotListedCause != "not a directory" {
				t.Errorf("list fails: summary %+v, want HasLogPath, LogsNotListed, cause %q", s, "not a directory")
			}

			sink.mark("context cancelled")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := p.prune(ctx, kept); err != context.Canceled {
				t.Errorf("cancelled: err=%v, want context.Canceled", err)
			}
			if *p.lastRet != (RetentionSummary{}) {
				t.Errorf("lastRet = %+v, want zero", *p.lastRet)
			}

			e.checkFiles(before, nil)
			assertRetentionGolden(t, "prune_orphan_logs."+fl.name, charNormalize(sink.text(), e.root))
		})
	}
}
