package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// A failed Store used to close green: the branch read only hasWarnings, so a run whose
// Store FAILED (status "error" in the notification) still closed with "✓ ... operations
// completed" at INFO whenever retention then ran clean - exactly the shape of a
// secondary/cloud mount that dies mid-copy. The closing line is gone now: each outcome
// is printed where it happened, the store's under "Storing backup..." and retention's
// under "Applying retention policy...". These tests pin that every outcome follows
// what the adapter saw, that the status still follows the worst of them, and that no
// summary line comes back to contradict them.
func adapterSyncLog(t *testing.T, storeErr, retentionErr error) (string, *BackupStats) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	backend := &fakeStorageBackend{
		name:     "secondary",
		location: storage.LocationSecondary,
		enabled:  true,
		critical: false,
		detectFilesystemFn: func(context.Context) (*storage.FilesystemInfo, error) {
			return &storage.FilesystemInfo{Type: storage.FilesystemExt4}, nil
		},
		storeFn: func(context.Context, string, *types.BackupMetadata) error { return storeErr },
		applyRetentionFn: func(context.Context, storage.RetentionConfig) (int, error) {
			return 0, retentionErr
		},
		getStatsFn: func(context.Context) (*storage.StorageStats, error) {
			return &storage.StorageStats{TotalBackups: 1}, nil
		},
	}
	adapter := NewStorageAdapter(backend, logger, &config.Config{SecondaryRetentionDays: 2})
	stats := sampleAdapterStats()
	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	out := buf.String()
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, "operations completed") && closeLevelOf(t, line) != "DEBUG" {
			t.Fatalf("a closing summary line came back above DEBUG:\n%s", line)
		}
	}
	return out, stats
}

// lineWith returns the first rendered line containing want, or fails.
func lineWith(t *testing.T, out, want string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line with %q rendered:\n%s", want, out)
	return ""
}

func closeLevelOf(t *testing.T, line string) string {
	t.Helper()
	_, rest, found := strings.Cut(line, "] ")
	if !found {
		t.Fatalf("unparseable line: %q", line)
	}
	return strings.Fields(rest)[0]
}

func TestFailedStoreOutcomeSaysNotSavedAtWarning(t *testing.T) {
	out, stats := adapterSyncLog(t, errors.New("store fail"), nil)
	line := lineWith(t, out, "✗ secondary: backup not saved")
	if got := closeLevelOf(t, line); got != "WARNING" {
		t.Fatalf("store outcome level = %s, want WARNING (store failure stays warning-weight, exit contract unchanged):\n%s", got, line)
	}
	if strings.Contains(out, "✓ secondary: backup saved") {
		t.Fatalf("a failed store still reports a saved backup:\n%s", out)
	}
	if stats.SecondaryStatus != "error" {
		t.Fatalf("SecondaryStatus = %q, want error", stats.SecondaryStatus)
	}
}

func TestStoreErrorsOutrankRetentionWarningsInTheStatus(t *testing.T) {
	out, stats := adapterSyncLog(t, errors.New("store fail"), errors.New("retention fail"))
	lineWith(t, out, "✗ secondary: backup not saved")
	if got := closeLevelOf(t, lineWith(t, out, "⚠ Retention not applied")); got != "WARNING" {
		t.Fatalf("retention outcome level = %s, want WARNING", got)
	}
	if stats.SecondaryStatus != "error" {
		t.Fatalf("with both failures the status must report the worse one, got %q", stats.SecondaryStatus)
	}
}

func TestStoreAndRetentionOutcomeArmsStillRender(t *testing.T) {
	warnOut, warnStats := adapterSyncLog(t, nil, errors.New("retention fail"))
	if closeLevelOf(t, lineWith(t, warnOut, "✓ secondary: backup saved")) != "INFO" ||
		closeLevelOf(t, lineWith(t, warnOut, "⚠ Retention not applied")) != "WARNING" ||
		warnStats.SecondaryStatus != "warning" {
		t.Fatalf("warnings arm changed (status %q):\n%s", warnStats.SecondaryStatus, warnOut)
	}
	okOut, okStats := adapterSyncLog(t, nil, nil)
	if closeLevelOf(t, lineWith(t, okOut, "✓ secondary: backup saved")) != "INFO" ||
		closeLevelOf(t, lineWith(t, okOut, "✓ Nothing to delete")) != "INFO" ||
		okStats.SecondaryStatus != "ok" {
		t.Fatalf("clean arm changed (status %q):\n%s", okStats.SecondaryStatus, okOut)
	}
}
