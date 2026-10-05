package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestStartupOwnedBackupsCountsThisHostOnly runs the startup count over a real local
// location two hosts share, through the same two reads initializeBackupStorage makes
// (GetStats and fetchBackupList): two archives are this host's, one is another's.
func TestStartupOwnedBackupsCountsThisHostOnly(t *testing.T) {
	dir := t.TempDir()
	seeds := []struct{ host, stamp string }{
		{"f1-owned-host", "20260930-100000"},
		{"f1-owned-host", "20261001-100000"},
		{"f1-other-host", "20261001-110000"},
	}
	for _, s := range seeds {
		name := filepath.Join(dir, fmt.Sprintf("%s-backup-%s.tar.xz", s.host, s.stamp))
		when, _ := time.Parse("20060102-150405", s.stamp)
		writeOwnedTestFile(t, name, "archive")
		writeOwnedTestFile(t, name+".sha256", "h  archive\n")
		writeOwnedTestFile(t, name+".metadata", fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, s.host, when.Format(time.RFC3339)))
	}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	backend, err := storage.NewLocalStorage(&config.Config{BackupPath: dir}, logger, "f1-owned-host")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	ctx := context.Background()
	stats := fetchStorageStats(ctx, backend, logger, "Local storage")
	backups := fetchBackupList(ctx, backend)

	owned, known := startupOwnedBackups(ctx, backend, stats, backups)
	if !known || owned != 2 {
		t.Fatalf("startupOwnedBackups = (%d, %v), want (2, true); the location holds %d archives", owned, known, stats.ListedBackups)
	}
}

// TestStorageInitCountsOnlyThisHostsBackups pins the storage-init facts on the same
// shared location: "  Backups: <N>" and the GFS tiers count this host's two archives,
// not the other host's third.
func TestStorageInitCountsOnlyThisHostsBackups(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	seeds := []struct {
		host string
		when time.Time
	}{
		{"f1-owned-host", now.Add(-1 * time.Hour)},
		{"f1-owned-host", now.Add(-8 * 24 * time.Hour)},
		{"f1-other-host", now.Add(-2 * time.Hour)},
	}
	for _, s := range seeds {
		name := filepath.Join(dir, fmt.Sprintf("%s-backup-%s.tar.xz", s.host, s.when.Format("20060102-150405")))
		writeOwnedTestFile(t, name, "archive")
		writeOwnedTestFile(t, name+".sha256", "h  archive\n")
		writeOwnedTestFile(t, name+".metadata", fmt.Sprintf(`{"hostname":%q,"created_at":%q}`, s.host, s.when.Format(time.RFC3339)))
	}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	cfg := &config.Config{BackupPath: dir, RetentionPolicy: "gfs", RetentionDaily: 1, RetentionWeekly: 1, RetentionMonthly: 0, RetentionYearly: -1}
	backend, err := storage.NewLocalStorage(cfg, logger, "f1-owned-host")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	ctx := context.Background()
	stats := fetchStorageStats(ctx, backend, logger, "Local storage")
	backups := fetchBackupList(ctx, backend)
	if stats == nil || stats.TotalBackups != 2 || stats.ListedBackups != 3 {
		t.Fatalf("stats = %+v, want 2 of 3 listed: only this host's backups are counted", stats)
	}
	owned := startupOwnedListing(ctx, backend, backups)
	if len(owned) != 2 {
		t.Fatalf("startupOwnedListing kept %d of %d, want 2", len(owned), len(backups))
	}

	summary, _ := formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, stats, owned)
	if !strings.HasPrefix(summary, "  Backups: 2\n  Daily: 1/1\n  Weekly: 1/1\n") {
		t.Fatalf("init summary = %q, want this host's two backups in the count and the tiers", summary)
	}
	if !strings.Contains(summary, "Kept (est.): 2, To delete (est.): 0") {
		t.Fatalf("init summary = %q, want the estimate over this host's two backups", summary)
	}
}

// TestStartupOwnedBackupsRefusesDisagreeingReads pins the guard: fetchBackupList
// returns nil on a failed listing, which is also what an empty location returns, so
// a nil listing beside a GetStats that counted archives must not become "0 owned".
func TestStartupOwnedBackupsRefusesDisagreeingReads(t *testing.T) {
	backend, err := storage.NewLocalStorage(&config.Config{BackupPath: t.TempDir()}, logging.New(types.LogLevelError, false), "f1-owned-host")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	ctx := context.Background()
	if _, known := startupOwnedBackups(ctx, backend, nil, nil); known {
		t.Fatal("no startup stats: want known=false")
	}
	if _, known := startupOwnedBackups(ctx, backend, &storage.StorageStats{ListedBackups: 3}, nil); known {
		t.Fatal("GetStats listed 3, the listing is nil: want known=false, not 0 owned")
	}
	if owned, known := startupOwnedBackups(ctx, backend, &storage.StorageStats{TotalBackups: 0}, nil); !known || owned != 0 {
		t.Fatalf("empty location: got (%d, %v), want (0, true)", owned, known)
	}
}

func writeOwnedTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
