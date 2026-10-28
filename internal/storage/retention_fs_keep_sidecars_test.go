package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// TestRetentionKeepsSidecarsWhenArchiveRemovalFails pins that a backup whose archive
// cannot be removed loses none of the files beside it, so the next pass still lists
// it as verified and retries it, instead of skipping it for "no manifest" forever.
func TestRetentionKeepsSidecarsWhenArchiveRemovalFails(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
			e.addBackup(charBackup{day: "2024-06-01", archiveBlocked: true, log: charLogFile})
			blocked := filepath.Join(e.backups, charArchiveName(charHost, "2024-06-01"))
			writeTestFile(t, blocked+".manifest.json", "{}")
			sidecars := []string{blocked + ".sha256", blocked + ".manifest.json", blocked + ".metadata"}

			for pass := 1; pass <= 2; pass++ {
				logger, _ := newCharLogger()
				backend := fl.build(t, e, logger)
				deleted, err := backend.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
				if err != nil || deleted != 0 {
					t.Fatalf("pass %d: ApplyRetention = %d, %v; want 0, nil", pass, deleted, err)
				}
				tally := charTally(t, backend)
				if tally.skipped != 0 || tally.planned != 1 || tally.notDeleted != 1 {
					t.Fatalf("pass %d: skipped=%d planned=%d notDeleted=%d; want 0, 1, 1 (the backup is retried)",
						pass, tally.skipped, tally.planned, tally.notDeleted)
				}
				for _, s := range sidecars {
					if _, err := os.Stat(s); err != nil {
						t.Fatalf("pass %d: %s was touched: %v", pass, filepath.Base(s), err)
					}
				}
			}
		})
	}
}

// TestRetentionRemovesSidecarsWhenArchiveRemovalTimesOut pins that a backup whose
// archive remove timed out loses its sidecars as well: the abandoned unlink can
// still complete (safefs), and nothing lists or removes a sidecar whose archive is
// gone. The backup is still reported as not deleted.
func TestRetentionRemovesSidecarsWhenArchiveRemovalTimesOut(t *testing.T) {
	for _, fl := range charFlavors {
		t.Run(fl.name, func(t *testing.T) {
			e := newCharEnv(t, "logs")
			e.addBackup(charBackup{day: "2024-06-20", log: charLogFile})
			e.addBackup(charBackup{day: "2024-06-01", log: charLogFile})
			expired := filepath.Join(e.backups, charArchiveName(charHost, "2024-06-01"))
			writeTestFile(t, expired+".manifest.json", "{}")
			sidecars := []string{expired + ".sha256", expired + ".manifest.json", expired + ".metadata"}

			orig := retentionRemove
			t.Cleanup(func() { retentionRemove = orig })
			retentionRemove = func(ctx context.Context, path string, timeout time.Duration) error {
				if path != expired {
					return orig(ctx, path, timeout)
				}
				// The unlink completes after the caller stopped waiting for it.
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove %s: %v", path, err)
				}
				return &safefs.TimeoutError{Op: "remove", Path: path, Timeout: time.Second}
			}

			logger, _ := newCharLogger()
			backend := fl.build(t, e, logger)
			if _, err := backend.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
				t.Fatalf("ApplyRetention: %v", err)
			}
			if tally := charTally(t, backend); tally.notDeleted != 1 {
				t.Fatalf("notDeleted=%d; want 1, the timed-out remove is not a deletion", tally.notDeleted)
			}
			for _, s := range sidecars {
				if _, err := os.Stat(s); !os.IsNotExist(err) {
					t.Fatalf("%s left behind: %v", filepath.Base(s), err)
				}
			}
		})
	}
}
