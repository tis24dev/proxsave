package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// scopeReportingStub is a stubStorage that also answers LastRetentionSummary, which
// is how a real backend hands back the number retention arrived at. Its
// ApplyRetention deletes nothing, which is the steady state: the common healthy run
// and the one where the owned count used to be discarded (discussion #292).
type scopeReportingStub struct {
	stubStorage
	summary storage.RetentionSummary
}

func (s *scopeReportingStub) LastRetentionSummary() storage.RetentionSummary { return s.summary }

// TestApplyStorageStatsPrefersTheRetentionScopeOverTheListing pins the defect
// itself. GetStats counts every archive at the location, so on a directory or
// remote prefix shared with another ProxSave host it counts that host's too, and
// rendering that against this host's own limit reads as a breach that is not
// happening.
func TestApplyStorageStatsPrefersTheRetentionScopeOverTheListing(t *testing.T) {
	adapter := &StorageAdapter{
		backend: &stubStorage{loc: storage.LocationPrimary},
		logger:  logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}

	adapter.applyStorageStats(
		&storage.StorageStats{TotalBackups: 40},
		storage.RetentionConfig{Policy: "simple", MaxBackups: 7},
		5,
		stats,
	)

	if stats.LocalBackups != 5 {
		t.Fatalf("LocalBackups = %d, want 5: the summary must report what retention manages, not the 40 archives the shared location happens to hold (discussion #292)", stats.LocalBackups)
	}
}

// TestApplyStorageStatsFallsBackToTheListingWhenRetentionDidNotRun pins the other
// half. Retention is skipped entirely when no limit is configured, and there is
// then no owned count to report. Printing 0 on a location holding forty archives
// would be a worse lie than the one being fixed, so the unscoped total stands.
func TestApplyStorageStatsFallsBackToTheListingWhenRetentionDidNotRun(t *testing.T) {
	adapter := &StorageAdapter{
		backend: &stubStorage{loc: storage.LocationPrimary},
		logger:  logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}

	adapter.applyStorageStats(
		&storage.StorageStats{TotalBackups: 40},
		storage.RetentionConfig{Policy: "simple", MaxBackups: 7},
		-1,
		stats,
	)

	if stats.LocalBackups != 40 {
		t.Fatalf("LocalBackups = %d, want 40: with no ownership scope available the listing is the only number there is, and reporting 0 or -1 beside the limit would be worse than reporting too many", stats.LocalBackups)
	}
}

// TestSyncReportsTheScopedCountWhenRetentionDeletedNothing is the wiring half, and
// it is the one that matters. The helper above can be correct while Sync never
// reaches it: reading the summary only when something was deleted is exactly how
// the right number came to be computed and then thrown away on every healthy run.
func TestSyncReportsTheScopedCountWhenRetentionDeletedNothing(t *testing.T) {
	backend := &scopeReportingStub{
		stubStorage: stubStorage{loc: storage.LocationPrimary},
		summary:     storage.RetentionSummary{ScopeValid: true, Owned: 5},
	}
	adapter := &StorageAdapter{
		backend: backend,
		// LocalRetentionDays drives MaxBackups for the primary location, so this is
		// what makes Sync call ApplyRetention at all.
		config: &config.Config{LocalRetentionDays: 7},
		logger: logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stats.LocalBackups != 5 {
		t.Fatalf("LocalBackups = %d, want 5: retention deleted nothing, which is the healthy steady state, and Sync must still read back the count retention scoped rather than fall through to the unscoped listing (discussion #292)", stats.LocalBackups)
	}
}

// TestSyncFallsBackWhenTheBackendReportsNoScope pins that an invalid scope is not
// mistaken for an owned count of zero. A backend that could not name itself
// publishes exactly this, and GetStats is then the only source left.
func TestSyncFallsBackWhenTheBackendReportsNoScope(t *testing.T) {
	backend := &scopeReportingStub{
		stubStorage: stubStorage{
			loc:  storage.LocationPrimary,
			list: []*types.BackupMetadata{{}, {}, {}},
		},
		summary: storage.RetentionSummary{ScopeValid: false, Owned: 0},
	}
	adapter := &StorageAdapter{
		backend: backend,
		config:  &config.Config{LocalRetentionDays: 7},
		logger:  logging.New(types.LogLevelError, false),
	}
	stats := &BackupStats{}

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if stats.LocalBackups != 3 {
		t.Fatalf("LocalBackups = %d, want 3: the backend published no ownership scope, so the listing stands. Reporting 0 here would tell an operator their backups had vanished", stats.LocalBackups)
	}
}

// deletingScopeReportingStub is a scopeReportingStub whose retention pass actually
// removed something. Every other test in this file runs a pass that deletes nothing,
// so the reporting branch that only runs when deleted > 0 stayed unexercised.
type deletingScopeReportingStub struct {
	scopeReportingStub
	deleted int
}

func (s *deletingScopeReportingStub) ApplyRetention(context.Context, storage.RetentionConfig) (int, error) {
	return s.deleted, nil
}

// TestSyncReportsRetentionsOwnCountsWhenItDeleted covers the two decisions Sync makes
// once a pass has deleted something, neither of which the steady-state tests reach.
//
// The first is the fallback: a summary that reports ScopeValid but leaves
// BackupsDeleted at zero is a backend that scoped the pass without counting its own
// removals, and the raw return value is then the only number there is. Reporting the
// zero instead would tell an operator nothing was removed on the very run that removed
// something. The second is the log suffix, which is the only place deleted LOGS are
// ever surfaced: without it a retention pass that pruned logs reports as if it had not.
func TestSyncReportsRetentionsOwnCountsWhenItDeleted(t *testing.T) {
	backend := &deletingScopeReportingStub{
		scopeReportingStub: scopeReportingStub{
			stubStorage: stubStorage{loc: storage.LocationPrimary},
			summary:     storage.RetentionSummary{ScopeValid: true, Owned: 5, BackupsDeleted: 0, LogsDeleted: 2},
		},
		deleted: 3,
	}

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	adapter := &StorageAdapter{
		backend: backend,
		config:  &config.Config{LocalRetentionDays: 7},
		logger:  logger,
	}
	stats := &BackupStats{}

	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if stats.LocalBackups != 5 {
		t.Fatalf("LocalBackups = %d, want 5: deleting something must not cost the scoped count", stats.LocalBackups)
	}
	line := buf.String()
	if !strings.Contains(line, "Deleted 3 old backups") {
		t.Fatalf("log = %q, want the raw deleted count 3: the summary left BackupsDeleted at zero, and reporting that would say nothing was removed on a run that removed three", line)
	}
	if !strings.Contains(line, "(logs deleted: 2)") {
		t.Fatalf("log = %q, want the logs-deleted suffix: it is the only place a pruned log is ever reported", line)
	}
}
