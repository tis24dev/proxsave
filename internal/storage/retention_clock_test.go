package storage

// An archive dated in the future (a clock that was ahead on an earlier run) took the
// daily slot and the archive this run just stored was deleted in the same pass.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// writeRetentionClockArchive writes an archive of host "node" with its .sha256 and its
// .metadata manifest (CreatedAt = when), as a completed run leaves it.
func writeRetentionClockArchive(t *testing.T, dir string, when time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "node-backup-"+when.Format("20060102-150405")+".tar.zst")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sha256", []byte("x  "+filepath.Base(path)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := backup.Manifest{ArchivePath: path, ArchiveSize: 4, SHA256: "x", CreatedAt: when, Hostname: "node", CompressionType: "zstd"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".metadata", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGFSRetentionKeepsThisRunsArchiveBesideAFutureDatedOne(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	// An earlier run made while the clock was ahead by three months.
	future := writeRetentionClockArchive(t, dir, now.AddDate(0, 3, 0))
	// An ordinary earlier backup, last week.
	older := writeRetentionClockArchive(t, dir, now.AddDate(0, 0, -8))
	// The archive this run just produced.
	current := writeRetentionClockArchive(t, dir, now.Add(-time.Minute))

	// RETENTION_POLICY=gfs, RETENTION_DAILY unset (forced to 1), weekly/monthly/yearly set.
	cfg := &config.Config{BackupPath: dir, RetentionPolicy: "gfs", RetentionWeekly: 4, RetentionMonthly: 6, RetentionYearly: 1}
	logger := logging.New(types.LogLevelDebug, false)
	ls, err := NewLocalStorage(cfg, logger, "")
	if err != nil {
		t.Fatal(err)
	}
	ls.hostname = "node"
	ls.fsInfo = &FilesystemInfo{Type: FilesystemExt4}

	ctx := context.Background()
	// Production order (StorageAdapter.Sync): Store, then the retention pass.
	if err := ls.Store(ctx, current, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	rc := NormalizeGFSRetentionConfig(logger, "Local storage", NewRetentionConfigFromConfig(cfg, LocationPrimary))
	deleted, err := ls.ApplyRetention(ctx, rc)
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	_, curErr := os.Stat(current)
	_, futErr := os.Stat(future)
	_, oldErr := os.Stat(older)
	t.Logf("deleted=%d current exists=%v future exists=%v older exists=%v summary=%+v",
		deleted, curErr == nil, futErr == nil, oldErr == nil, ls.LastRetentionSummary())
	if os.IsNotExist(curErr) {
		t.Fatalf("the archive this run just stored (%s) was deleted by its own retention pass; the future-dated one survives=%v", filepath.Base(current), futErr == nil)
	}
}

func TestSimpleRetentionKeepsThisRunsArchiveBesideAFutureDatedOne(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	future := writeRetentionClockArchive(t, dir, now.AddDate(0, 3, 0))
	current := writeRetentionClockArchive(t, dir, now.Add(-time.Minute))

	cfg := &config.Config{BackupPath: dir, LocalRetentionDays: 1}
	logger := logging.New(types.LogLevelDebug, false)
	ls, err := NewLocalStorage(cfg, logger, "")
	if err != nil {
		t.Fatal(err)
	}
	ls.hostname = "node"
	ls.fsInfo = &FilesystemInfo{Type: FilesystemExt4}
	ctx := context.Background()
	if err := ls.Store(ctx, current, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	deleted, err := ls.ApplyRetention(ctx, NewRetentionConfigFromConfig(cfg, LocationPrimary))
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	_, curErr := os.Stat(current)
	_, futErr := os.Stat(future)
	t.Logf("simple: deleted=%d current exists=%v future exists=%v", deleted, curErr == nil, futErr == nil)
	if os.IsNotExist(curErr) {
		t.Fatalf("simple policy deleted this run's archive %s", filepath.Base(current))
	}
}

// Clock-behind variant: this run's clock is behind the dates of every earlier archive.

// With the clock behind in this run, every earlier archive is dated in the future:
// GFS kept the single newest and deleted the weekly, monthly and yearly history.
// Archives dated after now are no longer deleted.
func TestGFSRetentionKeepsHistoryWhenTheClockIsBehind(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var history []string
	for _, d := range []int{1, 9, 40, 400} {
		history = append(history, writeRetentionClockArchive(t, dir, now.AddDate(0, 0, d)))
	}
	cfg := &config.Config{BackupPath: dir, RetentionPolicy: "gfs", RetentionDaily: 1, RetentionWeekly: 1, RetentionMonthly: 1, RetentionYearly: 1}
	ls, err := NewLocalStorage(cfg, logging.New(types.LogLevelError, false), "")
	if err != nil {
		t.Fatal(err)
	}
	ls.hostname = "node"
	current := writeRetentionClockArchive(t, dir, now.Add(-time.Minute))
	if err := ls.Store(context.Background(), current, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := ls.ApplyRetention(context.Background(), RetentionConfig{Policy: "gfs", Daily: 1, Weekly: 1, Monthly: 1, Yearly: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	for _, p := range append(history, current) {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s deleted with the clock behind: %v", filepath.Base(p), err)
		}
	}
}

// The cloud deletes through the same filter.
func TestWithoutRetentionProtected(t *testing.T) {
	now := time.Now()
	in := []*types.BackupMetadata{
		{BackupFile: "node-backup-a.tar.zst.bundle.tar", Timestamp: now.Add(-48 * time.Hour)},
		{BackupFile: "node-backup-run.tar.zst.bundle.tar", Timestamp: now.Add(-time.Minute)},
		{BackupFile: "node-backup-future.tar.zst.bundle.tar", Timestamp: now.Add(24 * time.Hour)},
	}
	got := withoutRetentionProtected(logging.New(types.LogLevelError, false), "Cloud storage", in, "node-backup-run.tar.zst", now)
	if len(got) != 1 || got[0].BackupFile != "node-backup-a.tar.zst.bundle.tar" {
		t.Fatalf("kept for deletion: %v, want only the past archive of another run", got)
	}
}
