package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// f1Startup is what the three locations held when the run started: five archives
// at each, three of them this host's, and a filesystem that is far from empty.
var f1Startup = storage.StorageStats{TotalBackups: 5, AvailableSpace: 4000, UsedSpace: 6000, TotalSpace: 10000}

const f1StartupOwned = 3

func f1Config() *config.Config {
	return &config.Config{
		BackupPath:             "/var/backups",
		SecondaryEnabled:       true,
		SecondaryPath:          "/mnt/secondary",
		CloudEnabled:           true,
		CloudRemote:            "remote:/cloud",
		LocalRetentionDays:     7,
		SecondaryRetentionDays: 14,
		CloudRetentionDays:     30,
		MaxLocalBackups:        7,
		MaxSecondaryBackups:    14,
		MaxCloudBackups:        30,
	}
}

func f1Stats(cfg *config.Config) *BackupStats {
	return InitializeBackupStats("node1", &environment.EnvironmentInfo{Type: types.ProxmoxVE, Version: "8"},
		"1.0.0", time.Unix(1700000000, 0), cfg, types.CompressionZstd, "standard", 3, 1, "", "", "")
}

// f1Adapters registers one adapter per location, each carrying the startup figures
// the way initializeBackupStorage hands them over, and returns the backends so a
// test can assert which of them were ever asked to store.
func f1Adapters(o *Orchestrator, cfg *config.Config, ownedKnown bool) map[storage.BackupLocation]*fakeStorageBackend {
	backends := map[storage.BackupLocation]*fakeStorageBackend{}
	for _, loc := range []storage.BackupLocation{storage.LocationPrimary, storage.LocationSecondary, storage.LocationCloud} {
		backend := &fakeStorageBackend{name: string(loc), location: loc, enabled: true, critical: loc == storage.LocationPrimary}
		adapter := NewStorageAdapter(backend, newStorageAdapterTestLogger(), cfg)
		startup := f1Startup
		adapter.SetInitialStats(&startup)
		adapter.SetInitialOwnedBackups(f1StartupOwned, ownedKnown)
		o.RegisterStorageTarget(adapter)
		backends[loc] = backend
	}
	return backends
}

func f1NotificationData(stats *BackupStats) *notify.NotificationData {
	return (&NotificationAdapter{logger: newStorageAdapterTestLogger()}).convertBackupStatsToNotificationData(stats)
}

// TestNeverStartedCopiesAreSkippedOnEveryFailurePath is F1. A run that fails before
// its copies (compression 11, verification 8, archive 10) never reaches Sync for any
// location, and a run whose critical local store fails (5) stops before Sync starts
// for the secondary and the cloud. Those copies never started, and every channel
// used to render them with the "ok" InitializeBackupStats started from.
func TestNeverStartedCopiesAreSkippedOnEveryFailurePath(t *testing.T) {
	beforeCopies := []struct {
		name string
		code types.ExitCode
	}{
		{"compression", types.ExitCompressionError},
		{"verification", types.ExitVerificationError},
		{"archive", types.ExitArchiveError},
	}
	for _, tc := range beforeCopies {
		t.Run(tc.name, func(t *testing.T) {
			cfg := f1Config()
			o := New(newTestLogger(), false)
			backends := f1Adapters(o, cfg, true)
			run := &backupRunContext{stats: f1Stats(cfg)}

			o.finalizeFailedBackupStats(run, &BackupError{Phase: tc.name, Err: errors.New("failed"), Code: tc.code})

			assertF1Skipped(t, run.stats)
			for loc, backend := range backends {
				if len(backend.calls) != 0 && containsCall(backend.calls, "Store") {
					t.Fatalf("%s: Store was called on a run that failed before the copies", loc)
				}
			}
		})
	}

	t.Run("local store", func(t *testing.T) {
		cfg := f1Config()
		o := New(newTestLogger(), false)
		backends := f1Adapters(o, cfg, true)
		backends[storage.LocationPrimary].storeFn = func(context.Context, string, *types.BackupMetadata) error {
			return errors.New("bundle vanished")
		}
		run := &backupRunContext{stats: f1Stats(cfg)}

		err := o.syncStorageTargets(context.Background(), run.stats)
		if err == nil {
			t.Fatal("a critical local store failure must abort the dispatch")
		}
		o.finalizeFailedBackupStats(run, err)

		if run.stats.LocalStatus != "error" {
			t.Fatalf("LocalStatus = %q, want error: the local store did start and fail", run.stats.LocalStatus)
		}
		assertF1Skipped(t, run.stats)
		for _, loc := range []storage.BackupLocation{storage.LocationSecondary, storage.LocationCloud} {
			if containsCall(backends[loc].calls, "Store") {
				t.Fatalf("%s: Store was called after the critical local store failed", loc)
			}
		}
	})
}

func assertF1Skipped(t *testing.T, stats *BackupStats) {
	t.Helper()
	if stats.SecondaryStatus != "skipped" || stats.CloudStatus != "skipped" {
		t.Fatalf("secondary=%q cloud=%q, want skipped/skipped for copies that never started", stats.SecondaryStatus, stats.CloudStatus)
	}
	data := f1NotificationData(stats)
	if got := notify.GetStorageEmoji(data.SecondaryStatus); got != "➖" {
		t.Fatalf("secondary emoji = %s, want ➖", got)
	}
	if got := notify.GetStorageEmoji(data.CloudStatus); got != "➖" {
		t.Fatalf("cloud emoji = %s, want ➖", got)
	}
}

func containsCall(calls []string, name string) bool {
	for _, c := range calls {
		if c == name {
			return true
		}
	}
	return false
}

// TestSyncStillWritesTheRealOutcome pins the other half: "skipped" is only where a
// location STARTS, and a copy that does run still ends at ok, warning or error.
func TestSyncStillWritesTheRealOutcome(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeStorageBackend)
		want  string
	}{
		{"ok", func(*fakeStorageBackend) {}, "ok"},
		{"store fails", func(b *fakeStorageBackend) {
			b.storeFn = func(context.Context, string, *types.BackupMetadata) error { return errors.New("nfs gone") }
		}, "error"},
		{"retention fails", func(b *fakeStorageBackend) {
			b.applyRetentionFn = func(context.Context, storage.RetentionConfig) (int, error) { return 0, errors.New("list failed") }
		}, "warning"},
	}
	for _, tc := range cases {
		for _, loc := range []storage.BackupLocation{storage.LocationSecondary, storage.LocationCloud} {
			t.Run(fmt.Sprintf("%s/%s", loc, tc.name), func(t *testing.T) {
				cfg := f1Config()
				stats := f1Stats(cfg)
				backend := &fakeStorageBackend{name: string(loc), location: loc, enabled: true}
				tc.setup(backend)
				if err := NewStorageAdapter(backend, newStorageAdapterTestLogger(), cfg).Sync(context.Background(), stats); err != nil {
					t.Fatalf("Sync: %v", err)
				}
				got := stats.SecondaryStatus
				if loc == storage.LocationCloud {
					got = stats.CloudStatus
				}
				if got != tc.want {
					t.Fatalf("status = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

// TestFailurePathCountsAreTheStartupFigures pins the counts and the space. A failed
// run used to report "0/7 backups" and "0 B free" for every location, because only
// Sync fills those fields; the locations it never reached hold what they held at
// startup, so that is what is reported.
func TestFailurePathCountsAreTheStartupFigures(t *testing.T) {
	cfg := f1Config()
	o := New(newTestLogger(), false)
	f1Adapters(o, cfg, true)
	run := &backupRunContext{stats: f1Stats(cfg)}

	o.finalizeFailedBackupStats(run, &BackupError{Phase: "compression", Err: errors.New("xz"), Code: types.ExitCompressionError})

	s := run.stats
	if s.LocalBackups != f1StartupOwned || s.SecondaryBackups != f1StartupOwned || s.CloudBackups != f1StartupOwned {
		t.Fatalf("counts local=%d secondary=%d cloud=%d, want %d each: the owned count read at startup", s.LocalBackups, s.SecondaryBackups, s.CloudBackups, f1StartupOwned)
	}
	if s.LocalFreeSpace != 4000 || s.LocalUsedSpace != 6000 || s.LocalTotalSpace != 10000 {
		t.Fatalf("local space free=%d used=%d total=%d, want the startup figures", s.LocalFreeSpace, s.LocalUsedSpace, s.LocalTotalSpace)
	}
	if s.SecondaryFreeSpace != 4000 || s.SecondaryUsedSpace != 6000 || s.SecondaryTotalSpace != 10000 {
		t.Fatalf("secondary space free=%d used=%d total=%d, want the startup figures", s.SecondaryFreeSpace, s.SecondaryUsedSpace, s.SecondaryTotalSpace)
	}
	data := f1NotificationData(s)
	if data.LocalStatusSummary != "3/7" || data.SecondaryStatusSummary != "3/14" || data.CloudStatusSummary != "3/30" {
		t.Fatalf("summaries %q %q %q, want 3/7 3/14 3/30", data.LocalStatusSummary, data.SecondaryStatusSummary, data.CloudStatusSummary)
	}
	if data.LocalFree == formatBytesHR(0) || data.SecondaryFree == formatBytesHR(0) {
		t.Fatalf("free space still reads as empty: local=%q secondary=%q", data.LocalFree, data.SecondaryFree)
	}
}

// TestFailurePathCountKeepsTheSuccessRule pins that the startup count is chosen by
// the rule Sync uses, so it means what a successful run's count means: the owned
// count only where retention would have produced one (a limit is configured and
// the host could name itself), the unscoped listing otherwise.
func TestFailurePathCountKeepsTheSuccessRule(t *testing.T) {
	cases := []struct {
		name       string
		ownedKnown bool
		mutate     func(*config.Config)
		want       int
		summary    string
	}{
		{"owned known", true, func(*config.Config) {}, f1StartupOwned, "3/7"},
		{"owned unknown", false, func(*config.Config) {}, f1Startup.TotalBackups, "5/7"},
		{"no retention limit", true, func(c *config.Config) { c.LocalRetentionDays, c.MaxLocalBackups = 0, 0 }, f1Startup.TotalBackups, "5/?"},
		{"gfs", true, func(c *config.Config) { c.RetentionPolicy, c.RetentionWeekly = "gfs", 4 }, f1StartupOwned, "3/-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := f1Config()
			tc.mutate(cfg)
			o := New(newTestLogger(), false)
			f1Adapters(o, cfg, tc.ownedKnown)
			run := &backupRunContext{stats: f1Stats(cfg)}
			o.finalizeFailedBackupStats(run, errors.New("failed"))
			if run.stats.LocalBackups != tc.want {
				t.Fatalf("LocalBackups = %d, want %d", run.stats.LocalBackups, tc.want)
			}
			if got := f1NotificationData(run.stats).LocalStatusSummary; got != tc.summary {
				t.Fatalf("LocalStatusSummary = %q, want %q", got, tc.summary)
			}
			if tc.name == "gfs" && (run.stats.LocalGFSDaily != 1 || run.stats.LocalGFSWeekly != 4) {
				t.Fatalf("GFS tiers daily=%d weekly=%d, want 1 (enforced minimum) and 4", run.stats.LocalGFSDaily, run.stats.LocalGFSWeekly)
			}
		})
	}
}

// TestFailurePathKeepsWhatSyncWrote pins the boundary: a location Sync already
// described keeps Sync's figures, and a run that did not fail gets nothing from the
// startup figures at all (a successful run is byte for byte what it was).
func TestFailurePathKeepsWhatSyncWrote(t *testing.T) {
	cfg := f1Config()
	o := New(newTestLogger(), false)
	backends := f1Adapters(o, cfg, true)
	backends[storage.LocationPrimary].getStatsFn = func(context.Context) (*storage.StorageStats, error) {
		return &storage.StorageStats{TotalBackups: 9, AvailableSpace: 1, UsedSpace: 2, TotalSpace: 3}, nil
	}
	backends[storage.LocationSecondary].storeFn = func(context.Context, string, *types.BackupMetadata) error {
		return context.Canceled
	}
	backends[storage.LocationSecondary].getStatsFn = func(context.Context) (*storage.StorageStats, error) {
		return nil, context.Canceled
	}
	stats := f1Stats(cfg)
	if err := o.storageTargets[0].Sync(context.Background(), stats); err != nil {
		t.Fatalf("local Sync: %v", err)
	}
	if err := o.storageTargets[1].Sync(context.Background(), stats); err != nil {
		t.Fatalf("secondary Sync: %v", err)
	}

	success := *stats
	o.finalizeFailedBackupStats(&backupRunContext{stats: &success}, nil)
	if !reflect.DeepEqual(success, *stats) {
		t.Fatalf("a run that did not fail was changed:\n got %+v\nwant %+v", success, *stats)
	}

	o.finalizeFailedBackupStats(&backupRunContext{stats: stats}, errors.New("cancelled"))
	if stats.LocalBackups != 9 || stats.LocalFreeSpace != 1 {
		t.Fatalf("local = %d backups, %d free; want what Sync wrote (9, 1)", stats.LocalBackups, stats.LocalFreeSpace)
	}
	if stats.SecondaryBackups != f1StartupOwned || stats.SecondaryStatus != "error" {
		t.Fatalf("secondary = %d backups, status %q; want the startup count beside the error Sync recorded", stats.SecondaryBackups, stats.SecondaryStatus)
	}
	if stats.CloudBackups != f1StartupOwned || stats.CloudStatus != "skipped" {
		t.Fatalf("cloud = %d backups, status %q; want the startup count and skipped", stats.CloudBackups, stats.CloudStatus)
	}
}

// TestStartupFiguresWriteWhatSyncWrites is the "same meaning" pin. Given the same
// listing figures and the same owned count, the failure-path fallback must write
// exactly the storage fields a successful Sync writes, for every location and both
// retention policies; only the status differs, because only Sync knows the outcome.
func TestStartupFiguresWriteWhatSyncWrites(t *testing.T) {
	for _, policy := range []string{"simple", "gfs"} {
		for _, loc := range []storage.BackupLocation{storage.LocationPrimary, storage.LocationSecondary, storage.LocationCloud} {
			t.Run(fmt.Sprintf("%s/%s", policy, loc), func(t *testing.T) {
				cfg := f1Config()
				if policy == "gfs" {
					cfg.RetentionPolicy, cfg.RetentionWeekly, cfg.RetentionMonthly = "gfs", 4, 6
				}
				list := make([]*types.BackupMetadata, 5)
				for i := range list {
					list[i] = &types.BackupMetadata{BackupFile: fmt.Sprintf("b%d", i)}
				}
				backend := &scopeReportingStub{
					stubStorage: stubStorage{loc: loc, list: list},
					summary:     storage.RetentionSummary{ScopeValid: true, Owned: f1StartupOwned},
				}

				synced := f1Stats(cfg)
				if err := NewStorageAdapter(backend, newStorageAdapterTestLogger(), cfg).Sync(context.Background(), synced); err != nil {
					t.Fatalf("Sync: %v", err)
				}

				fallback := f1Stats(cfg)
				startup, _ := backend.GetStats(context.Background())
				adapter := NewStorageAdapter(backend, newStorageAdapterTestLogger(), cfg)
				adapter.SetInitialStats(startup)
				adapter.SetInitialOwnedBackups(f1StartupOwned, true)
				adapter.applyInitialStats(fallback)

				synced.LocalStatus, synced.SecondaryStatus, synced.CloudStatus = "", "", ""
				fallback.LocalStatus, fallback.SecondaryStatus, fallback.CloudStatus = "", "", ""
				if !reflect.DeepEqual(synced, fallback) {
					t.Fatalf("fallback differs from Sync:\nsync     %+v\nfallback %+v", *synced, *fallback)
				}
			})
		}
	}
}
