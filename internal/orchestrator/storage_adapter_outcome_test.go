package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// outcomeReportingBackend is a fake backend that reports what its last Store left
// undone and what its last retention pass could not do, as the real backends do.
type outcomeReportingBackend struct {
	*fakeStorageBackend
	issues      []storage.StoreIssue
	summary     storage.RetentionSummary
	noOwnership bool
}

func (b *outcomeReportingBackend) SetsBackupPermissions() bool { return !b.noOwnership }

func (b *outcomeReportingBackend) LastStoreIssues() []storage.StoreIssue { return b.issues }

func (b *outcomeReportingBackend) LastRetentionSummary() storage.RetentionSummary {
	return b.summary
}

func newOutcomeBackend(location storage.BackupLocation, critical bool) *outcomeReportingBackend {
	return &outcomeReportingBackend{fakeStorageBackend: &fakeStorageBackend{
		name:     string(location),
		location: location,
		enabled:  true,
		critical: critical,
		detectFilesystemFn: func(context.Context) (*storage.FilesystemInfo, error) {
			return &storage.FilesystemInfo{Type: storage.FilesystemExt4}, nil
		},
		getStatsFn: func(context.Context) (*storage.StorageStats, error) {
			return &storage.StorageStats{TotalBackups: 1}, nil
		},
	}}
}

func syncOutcome(t *testing.T, backend storage.Storage) (string, *BackupStats, error) {
	t.Helper()
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	cfg := &config.Config{LocalRetentionDays: 2, SecondaryRetentionDays: 2, CloudRetentionDays: 2}
	stats := sampleAdapterStats()
	err := NewStorageAdapter(backend, logger, cfg).Sync(context.Background(), stats)
	return buf.String(), stats, err
}

func requireLines(t *testing.T, out string, want ...string) {
	t.Helper()
	last := -1
	for _, w := range want {
		idx := strings.Index(out, w+"\n")
		if idx < 0 {
			t.Fatalf("missing line %q in:\n%s", w, out)
		}
		if idx < last {
			t.Fatalf("line %q is out of order in:\n%s", w, out)
		}
		last = idx
	}
}

func TestStoreIssuesCloseTheBlockWithTheirOwnOutcome(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationCloud, false)
	backend.issues = []storage.StoreIssue{storage.StoreIssueBundleNotSent, storage.StoreIssueSidecarNotSaved, storage.StoreIssueChecksumNotVerified}

	out, stats, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// ONE outcome lists every problem, in the order they happened.
	requireLines(t, out,
		"STEP     cloud",
		"INFO     Storing backup...",
		"WARNING  ⚠ cloud: backup saved, bundle not sent, sidecar file not saved, checksum not verified",
		"INFO     Applying retention policy...",
	)
	if strings.Contains(out, "✓ cloud: backup saved") || strings.Count(out, "WARNING") != 1 {
		t.Fatalf("a store with issues must close on exactly one warning line:\n%s", out)
	}
	// The backup is there with a sidecar file missing: warning.
	if stats.CloudStatus != "warning" {
		t.Fatalf("CloudStatus = %q, want warning (sidecar file not saved)", stats.CloudStatus)
	}

	// Without the sidecar the issues are WARNING lines around a saved backup and the
	// status does not move.
	backend = newOutcomeBackend(storage.LocationCloud, false)
	backend.issues = []storage.StoreIssue{storage.StoreIssueBundleNotSent, storage.StoreIssueChecksumNotVerified, storage.StoreIssuePermissionsNotSet}
	out, stats, err = syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "WARNING  ⚠ cloud: backup saved, bundle not sent, checksum not verified, permissions not set")
	if stats.CloudStatus != "ok" {
		t.Fatalf("CloudStatus = %q, want ok", stats.CloudStatus)
	}
}

// A failed sidecar upload (the store error) and the issues recorded before it close
// on the same single line; a copy's failed permissions join it too.
func TestStoreIssuesAndSidecarErrorShareOneOutcome(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationCloud, false)
	backend.issues = []storage.StoreIssue{storage.StoreIssueBundleNotSent}
	backend.storeFn = func(context.Context, string, *types.BackupMetadata) error {
		return &storage.StorageError{Location: storage.LocationCloud, Operation: "upload_associated", Err: errors.New("sidecar"), PrimarySaved: true}
	}
	out, stats, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "WARNING  ⚠ cloud: backup saved, bundle not sent, sidecar file not saved")
	if stats.CloudStatus != "warning" {
		t.Fatalf("CloudStatus = %q, want warning (the backup is saved, a sidecar upload failed)", stats.CloudStatus)
	}

	secondary := newOutcomeBackend(storage.LocationSecondary, false)
	secondary.issues = []storage.StoreIssue{storage.StoreIssuePermissionsNotSet}
	out, stats, err = syncOutcome(t, secondary)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "INFO     Storing backup...", "WARNING  ⚠ secondary: backup saved, permissions not set")
	if stats.SecondaryStatus != "ok" {
		t.Fatalf("SecondaryStatus = %q, want ok (permissions only)", stats.SecondaryStatus)
	}

	// The Secondary copy of a sidecar fails: Store returns nil, the sidecar is in the
	// issues, the status is warning.
	secondary = newOutcomeBackend(storage.LocationSecondary, false)
	secondary.issues = []storage.StoreIssue{storage.StoreIssueSidecarNotSaved}
	out, stats, err = syncOutcome(t, secondary)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "WARNING  ⚠ secondary: backup saved, sidecar file not saved")
	if stats.SecondaryStatus != "warning" {
		t.Fatalf("SecondaryStatus = %q, want warning (sidecar file not saved)", stats.SecondaryStatus)
	}

	// The backup itself not saved stays an error.
	failed := newOutcomeBackend(storage.LocationCloud, false)
	failed.storeFn = func(context.Context, string, *types.BackupMetadata) error {
		return &storage.StorageError{Location: storage.LocationCloud, Operation: "upload", Err: errors.New("archive")}
	}
	out, stats, err = syncOutcome(t, failed)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "WARNING  ✗ cloud: backup not saved")
	if stats.CloudStatus != "error" {
		t.Fatalf("CloudStatus = %q, want error (backup not saved)", stats.CloudStatus)
	}
}

func TestPrimaryWithoutOwnershipSkipsPermissions(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationPrimary, true)
	backend.noOwnership = true
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	adapter := NewStorageAdapter(backend, logger, &config.Config{LocalRetentionDays: 2})
	adapter.SetFilesystemInfo(&storage.FilesystemInfo{Type: storage.FilesystemFAT32})
	if err := adapter.Sync(context.Background(), sampleAdapterStats()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	out := buf.String()
	requireLines(t, out, "STEP     primary", "SKIP     Permissions: vfat does not support ownership", "INFO     Applying retention policy...")
	if strings.Contains(out, "Setting backup permissions") || strings.Contains(out, "permissions set") {
		t.Fatalf("no permissions are set on a filesystem without ownership:\n%s", out)
	}
}

// Retention is never critical, on the Primary either: a backup it could not delete is
// one backup too many, never a backup lost. The run goes on.
func TestPrimaryRetentionFailureDoesNotStopTheRun(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationPrimary, true)
	backend.applyRetentionFn = func(context.Context, storage.RetentionConfig) (int, error) {
		return 0, &storage.StorageError{Location: storage.LocationPrimary, Operation: "apply_retention", Err: errors.New("list failed")}
	}
	out, stats, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync returned %v; a retention failure must not stop the run", err)
	}
	requireLines(t, out, "INFO     Applying retention policy...", "WARNING  ⚠ Retention not applied", "INFO     primary statistics:")
	if stats.LocalStatus != "warning" {
		t.Fatalf("LocalStatus = %q, want warning", stats.LocalStatus)
	}
}

func TestUnreadableStatisticsAreAFactAndAWarning(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationSecondary, false)
	backend.getStatsFn = func(context.Context) (*storage.StorageStats, error) {
		return nil, &fs.PathError{Op: "stat", Path: "/mnt/secondary", Err: syscall.EIO}
	}
	out, stats, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "INFO     secondary statistics:", "INFO       Read failed: input/output error", "WARNING  ⚠ Statistics unavailable")
	if stats.SecondaryStatus != "ok" {
		t.Fatalf("SecondaryStatus = %q, want ok: only the figures are missing", stats.SecondaryStatus)
	}
}

func TestFilesystemDetectionFailureInSyncMeansNotSaved(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationSecondary, false)
	backend.detectFilesystemFn = func(context.Context) (*storage.FilesystemInfo, error) {
		return nil, &fs.PathError{Op: "statfs", Path: "/mnt/secondary", Err: syscall.ENOENT}
	}
	out, stats, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "STEP     secondary", "INFO       Filesystem detection failed: no such file or directory", "WARNING  ✗ secondary: backup not saved")
	if strings.Contains(out, "Storing backup") || strings.Contains(out, "operations will be skipped") {
		t.Fatalf("nothing else runs at this destination:\n%s", out)
	}
	if stats.SecondaryStatus != "error" {
		t.Fatalf("SecondaryStatus = %q, want error", stats.SecondaryStatus)
	}
}

func TestPrimaryBlockSetsPermissions(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationPrimary, true)
	out, _, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out,
		"STEP     primary",
		"INFO     Setting backup permissions...",
		"INFO     ✓ primary: permissions set",
		"INFO     Applying retention policy...",
	)
	if strings.Contains(out, "Storing backup...") || strings.Contains(out, "backup saved") {
		t.Fatalf("the Primary stores nothing at step [6]:\n%s", out)
	}

	backend.issues = []storage.StoreIssue{storage.StoreIssuePermissionsNotSet}
	out, stats, err := syncOutcome(t, backend)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	requireLines(t, out, "INFO     Setting backup permissions...", "WARNING  ⚠ primary: permissions not set")
	if strings.Contains(out, "permissions set\n") {
		t.Fatalf("failed permissions must not also close green:\n%s", out)
	}
	if stats.LocalStatus != "ok" {
		t.Fatalf("LocalStatus = %q, want ok: the run continues", stats.LocalStatus)
	}
}

func TestPrimaryBackupNotAccessibleIsTheOnlyErrorLine(t *testing.T) {
	backend := newOutcomeBackend(storage.LocationPrimary, true)
	backend.storeFn = func(context.Context, string, *types.BackupMetadata) error {
		return &storage.StorageError{Location: storage.LocationPrimary, Operation: "store", Path: "/tmp/archive.tar",
			Err: errors.New("backup file could not be read: permission denied"), IsCritical: true}
	}
	out, _, err := syncOutcome(t, backend)
	if err == nil {
		t.Fatalf("a critical store failure must stop the run")
	}
	if !OutcomeLogged(err) {
		t.Fatalf("the error must say its outcome line is already on screen: %v", err)
	}
	requireLines(t, out, "INFO     Setting backup permissions...", "ERROR    ✗ primary: backup not accessible")
	if strings.Count(out, "ERROR") != 1 {
		t.Fatalf("want exactly one ERROR line:\n%s", out)
	}

	// Any other critical failure keeps today's report by the caller.
	backend.storeFn = func(context.Context, string, *types.BackupMetadata) error { return context.DeadlineExceeded }
	out, _, err = syncOutcome(t, backend)
	if err == nil || OutcomeLogged(err) {
		t.Fatalf("a non-store failure must not claim a logged outcome: %v", err)
	}
	if strings.Contains(out, "backup not accessible") {
		t.Fatalf("a non-store failure must not print the not-accessible outcome:\n%s", out)
	}
}

func TestRetentionOutcomesAreAutonomous(t *testing.T) {
	cases := []struct {
		name    string
		summary storage.RetentionSummary
		want    []string
		absent  []string
	}{
		{
			name:    "nothing to delete",
			summary: storage.RetentionSummary{PassCompleted: true},
			want:    []string{"INFO     ✓ Nothing to delete"},
			absent:  []string{"Backups deleted", "Logs deleted"},
		},
		{
			name:    "deleted with logs",
			summary: storage.RetentionSummary{Planned: 2, BackupsDeleted: 2, LogsDeleted: 2},
			want:    []string{"INFO     ✓ Backups deleted: 2", "INFO     ✓ Logs deleted: 2"},
			absent:  []string{"Nothing to delete"},
		},
		{
			name:    "a backup not deleted",
			summary: storage.RetentionSummary{Planned: 3, BackupsDeleted: 2, NotDeleted: 1, LogsDeleted: 2},
			want:    []string{"WARNING  ⚠ Backups deleted: 2 of 3", "INFO     ✓ Logs deleted: 2"},
		},
		{
			name:    "files left behind",
			summary: storage.RetentionSummary{Planned: 2, BackupsDeleted: 2, LeftBehind: 1},
			want:    []string{"WARNING  ⚠ Backups deleted: 2, files left behind"},
			absent:  []string{"Logs deleted"},
		},
		{
			name:    "skipped backups",
			summary: storage.RetentionSummary{Planned: 1, BackupsDeleted: 1, Skipped: 2, LogsDeleted: 1},
			want:    []string{"WARNING  ⚠ Backups deleted: 1, 2 skipped", "INFO     ✓ Logs deleted: 1"},
		},
		{
			name:    "skipped with nothing to delete",
			summary: storage.RetentionSummary{Skipped: 1},
			want:    []string{"WARNING  ⚠ Backups deleted: 0, 1 skipped"},
			absent:  []string{"Nothing to delete"},
		},
		{
			name:    "backups not listed",
			summary: storage.RetentionSummary{Planned: 1, BackupsDeleted: 1, NotListed: 4, Skipped: 1},
			want:    []string{"WARNING  ⚠ Backups deleted: 1, 4 not listed"},
		},
		{
			name:    "backups without metadata",
			summary: storage.RetentionSummary{Planned: 1, BackupsDeleted: 1, NoMetadata: 2},
			want:    []string{"WARNING  ⚠ Backups deleted: 1, 2 without metadata"},
		},
		{
			name:    "backups named under another spelling, not rotated",
			summary: storage.RetentionSummary{Planned: 1, BackupsDeleted: 1, NotRotated: 3, NotRotatedNames: "pve.home.arpa", LogsDeleted: 1},
			want:    []string{"WARNING  ⚠ Backups deleted: 1, 3 named pve.home.arpa not rotated", "INFO     ✓ Logs deleted: 1"},
		},
		{
			name:    "not rotated with nothing to delete",
			summary: storage.RetentionSummary{NotRotated: 2, NotRotatedNames: "pve.home.arpa and pve.lan"},
			want:    []string{"WARNING  ⚠ Backups deleted: 0, 2 named pve.home.arpa and pve.lan not rotated"},
			absent:  []string{"Nothing to delete"},
		},
		{
			// Last in the precedence: another cause on the same pass takes the line.
			name:    "not rotated beside backups without metadata",
			summary: storage.RetentionSummary{Planned: 1, BackupsDeleted: 1, NoMetadata: 2, NotRotated: 3, NotRotatedNames: "pve.home.arpa"},
			want:    []string{"WARNING  ⚠ Backups deleted: 1, 2 without metadata"},
			absent:  []string{"not rotated"},
		},
		{
			name:    "server identity shared",
			summary: storage.RetentionSummary{Planned: 2, BackupsDeleted: 2, SharedIdentityNames: "clone1", LogsDeleted: 2},
			want:    []string{"INFO     ✓ Backups deleted: 2", "WARNING  ⚠ Server identity shared with clone1", "INFO     ✓ Logs deleted: 2"},
			absent:  []string{"Nothing to delete"},
		},
		{
			name:    "server identity shared with nothing to delete",
			summary: storage.RetentionSummary{SharedIdentityNames: "clone1 and clone2"},
			want:    []string{"INFO     ✓ Nothing to delete", "WARNING  ⚠ Server identity shared with clone1 and clone2"},
			absent:  []string{"Backups deleted"},
		},
		{
			// The backups line keeps its own precedence; the shared identity is a
			// line of its own after it, before the logs line.
			name:    "server identity shared beside a backup not deleted",
			summary: storage.RetentionSummary{Planned: 3, BackupsDeleted: 2, NotDeleted: 1, SharedIdentityNames: "clone1", LogsDeleted: 1, LogsNotDeleted: 1},
			want:    []string{"WARNING  ⚠ Backups deleted: 2 of 3", "WARNING  ⚠ Server identity shared with clone1", "WARNING  ⚠ Logs deleted: 1 of 2"},
		},
		{
			name:    "server identity shared beside backups not rotated",
			summary: storage.RetentionSummary{NotRotated: 1, NotRotatedNames: "pve.lan", SharedIdentityNames: "clone1"},
			want:    []string{"WARNING  ⚠ Backups deleted: 0, 1 named pve.lan not rotated", "WARNING  ⚠ Server identity shared with clone1"},
		},
		{
			name:    "a log not deleted",
			summary: storage.RetentionSummary{Planned: 2, BackupsDeleted: 2, LogsDeleted: 1, LogsNotDeleted: 1},
			want:    []string{"INFO     ✓ Backups deleted: 2", "WARNING  ⚠ Logs deleted: 1 of 2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newOutcomeBackend(storage.LocationSecondary, false)
			backend.summary = tc.summary
			backend.applyRetentionFn = func(context.Context, storage.RetentionConfig) (int, error) {
				return tc.summary.BackupsDeleted, nil
			}
			out, stats, err := syncOutcome(t, backend)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			requireLines(t, out, append([]string{"INFO     Applying retention policy..."}, tc.want...)...)
			if strings.Contains(out, ", server identity shared") {
				t.Fatalf("the shared identity rode on the backups line instead of its own:\n%s", out)
			}
			for _, a := range tc.absent {
				if strings.Contains(out, a) {
					t.Fatalf("unexpected %q in:\n%s", a, out)
				}
			}
			// The outcomes are lines, not a status: a retention pass that ran keeps "ok".
			if stats.SecondaryStatus != "ok" {
				t.Fatalf("SecondaryStatus = %q, want ok", stats.SecondaryStatus)
			}
		})
	}
}

func TestStepSixCountsOnlyTheCopies(t *testing.T) {
	o := &Orchestrator{}
	for _, loc := range []storage.BackupLocation{storage.LocationPrimary, storage.LocationSecondary, storage.LocationCloud} {
		o.storageTargets = append(o.storageTargets, NewStorageAdapter(newOutcomeBackend(loc, loc == storage.LocationPrimary), nil, &config.Config{}))
	}
	if got := o.storageCopyTargetCount(); got != 2 {
		t.Fatalf("copies = %d, want 2: the Primary is not a copy", got)
	}
	o.storageTargets = o.storageTargets[:1]
	if got := o.storageCopyTargetCount(); got != 0 {
		t.Fatalf("copies with the Primary only = %d, want 0", got)
	}
}
