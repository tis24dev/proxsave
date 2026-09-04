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

// The tier closing line is the log's last word on a storage backend, and until now
// nothing pinned it: the branch read only hasWarnings, so a run whose Store FAILED
// (status "error" in the notification, "Backup was not saved" two lines above)
// still closed green with "✓ ... operations completed" at INFO whenever retention
// then ran clean - exactly the shape of a secondary/cloud mount that dies mid-copy.
// The closing line must follow the worst thing the adapter saw, not the best.
func adapterClosingLine(t *testing.T, storeErr, retentionErr error) string {
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
	if err := adapter.Sync(context.Background(), sampleAdapterStats()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}

	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if strings.Contains(line, "operations completed") {
			return line
		}
	}
	t.Fatalf("no closing line rendered:\n%s", buf.String())
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

func TestClosingLineAfterFailedStoreSaysErrorsAtWarning(t *testing.T) {
	line := adapterClosingLine(t, errors.New("store fail"), nil)
	if !strings.Contains(line, "✗ secondary operations completed with errors") {
		t.Fatalf("a failed store still closes green; the log's last word contradicts the notification's \"error\":\n%s", line)
	}
	if got := closeLevelOf(t, line); got != "WARNING" {
		t.Fatalf("closing line level = %s, want WARNING (store failure stays warning-weight, exit contract unchanged):\n%s", got, line)
	}
}

func TestClosingLineErrorsOutrankWarnings(t *testing.T) {
	line := adapterClosingLine(t, errors.New("store fail"), errors.New("retention fail"))
	if !strings.Contains(line, "completed with errors") {
		t.Fatalf("with both flags set the closing line must report the worse one:\n%s", line)
	}
}

func TestClosingLineArmsStillRender(t *testing.T) {
	warn := adapterClosingLine(t, nil, errors.New("retention fail"))
	if !strings.Contains(warn, "✗ secondary operations completed with warnings") || closeLevelOf(t, warn) != "WARNING" {
		t.Fatalf("warnings arm changed:\n%s", warn)
	}
	ok := adapterClosingLine(t, nil, nil)
	if !strings.Contains(ok, "✓ secondary operations completed") || closeLevelOf(t, ok) != "INFO" {
		t.Fatalf("clean arm changed:\n%s", ok)
	}
}
