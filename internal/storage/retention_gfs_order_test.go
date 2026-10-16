package storage

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

// charGFSExpired are the backups charGFS expires next to 2024-06-20 and 2024-06-10,
// oldest first.
var charGFSExpired = []string{"2024-04-01", "2024-05-01", "2024-06-01"}

// newGFSOrderEnv is the five-backup fixture of the GFS order tests: charGFS keeps
// 2024-06-20 and 2024-06-10 and expires charGFSExpired.
func newGFSOrderEnv(t *testing.T) *charEnv {
	t.Helper()
	e := newCharEnv(t, "logs")
	for _, day := range append([]string{"2024-06-20", "2024-06-10"}, charGFSExpired...) {
		e.addBackup(charBackup{day: day, log: charLogFile})
	}
	e.seal()
	return e
}

// charDeletionOrder is the days of charGFSExpired in the order their deletion started.
func charDeletionOrder(lines []string) []string {
	var order []string
	for _, l := range lines {
		for _, day := range charGFSExpired {
			if strings.Contains(l, charDeletingLine+charArchiveName(charHost, day)) {
				order = append(order, day)
			}
		}
	}
	return order
}

// TestRetentionGFSDeletesOldestFirst pins the order of the GFS delete loop: from the
// oldest expired backup to the newest, as the simple policy deletes.
func TestRetentionGFSDeletesOldestFirst(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newGFSOrderEnv(t)
			logger, sink := newCharLogger()
			backend := fl.build(t, e, logger)
			sink.reset()

			deleted, err := backend.ApplyRetention(context.Background(), charGFS)

			if deleted != 3 || err != nil {
				t.Fatalf("ApplyRetention = %d, %v; want 3, nil", deleted, err)
			}
			if got := charDeletionOrder(charNormalize(sink.text(), e.root)); !slices.Equal(got, charGFSExpired) {
				t.Errorf("deletion order = %v, want %v", got, charGFSExpired)
			}
		})
	}
}

// TestRetentionGFSCancelledAfterFirstDeletion stops the GFS pass once its first
// deletion is complete: the oldest expired backup is gone with its log, the two newer
// ones are never reached and stay whole.
func TestRetentionGFSCancelledAfterFirstDeletion(t *testing.T) {
	doneLine := map[string]string{
		"local":     strings.TrimSuffix(localRetentionText.deleted, "%s"),
		"secondary": strings.TrimSuffix(secondaryRetentionText.deleted, "%s"),
	}
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newGFSOrderEnv(t)
			logger, sink := newCharLogger()
			before := e.snapshot()
			backend := fl.build(t, e, logger)
			sink.reset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink.arm(doneLine[fl.name], 1, cancel)

			deleted, err := backend.ApplyRetention(ctx, charGFS)

			if deleted != 1 || err != context.Canceled {
				t.Errorf("ApplyRetention = %d, %v; want 1, context.Canceled", deleted, err)
			}
			oldest := charGFSExpired[0]
			if got := charDeletionOrder(charNormalize(sink.text(), e.root)); !slices.Equal(got, []string{oldest}) {
				t.Errorf("deletions started = %v, want only %s", got, oldest)
			}
			e.checkFiles(before, charJoin(charSet(oldest), []string{charLogRel("logs", oldest)}))
			checkRemoved(t, backend, []string{charArchiveName(charHost, oldest)})
			if got := backend.LastRetentionSummary().NotDeleted; got != 0 {
				t.Errorf("NotDeleted = %d, want 0: the pass stops before the next deletion", got)
			}
		})
	}
}

// TestRetentionGFSOrderTieBreak gives two expired backups the same timestamp: the file
// name settles their order, whatever order the classification map hands them out in.
func TestRetentionGFSOrderTieBreak(t *testing.T) {
	dir := t.TempDir()
	at := func(day string) time.Time {
		ts, err := time.Parse("2006-01-02 15:04", day+" 12:00")
		if err != nil {
			t.Fatalf("parse %s: %v", day, err)
		}
		return ts
	}
	backup := func(name, day string) *types.BackupMetadata {
		return &types.BackupMetadata{BackupFile: filepath.Join(dir, name), Timestamp: at(day), Verified: true}
	}
	want := []string{
		filepath.Join(dir, "pve-backup-20240401-120000-a.tar.zst"),
		filepath.Join(dir, "pve-backup-20240401-120000-b.tar.zst"),
		filepath.Join(dir, "pve-backup-20240601-120000.tar.zst"),
	}
	// The map's order is random on every pass: twenty passes leave a missing tie-break
	// one chance in a million to go unseen.
	for range 20 {
		backups := []*types.BackupMetadata{
			backup("pve-backup-20240620-120000.tar.zst", "2024-06-20"),
			backup("pve-backup-20240610-120000.tar.zst", "2024-06-10"),
			backup("pve-backup-20240601-120000.tar.zst", "2024-06-01"),
			backup("pve-backup-20240401-120000-b.tar.zst", "2024-04-01"),
			backup("pve-backup-20240401-120000-a.tar.zst", "2024-04-01"),
		}
		logger, _ := newCharLogger()
		r := fsRetention{logger: logger, text: localRetentionText, tally: &retentionTally{}, lastRet: &RetentionSummary{}}

		deleted, err := r.applyGFS(context.Background(), backups, charGFS)

		if deleted != 3 || err != nil {
			t.Fatalf("applyGFS = %d, %v; want 3, nil", deleted, err)
		}
		if !slices.Equal(r.tally.removed, want) {
			t.Fatalf("deletion order = %v, want %v", r.tally.removed, want)
		}
	}
}
