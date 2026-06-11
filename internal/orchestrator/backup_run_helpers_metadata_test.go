package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestWriteBackupCollectionMetadata_FailuresEscalateToWarnings locks in the
// PS-BH-003 fix: a failure to write the backup metadata or the collection
// manifest is logged at WARNING (so the log parser counts it and it drives
// WarningCount / the exit code / notifications), not silently at DEBUG. A
// successful write emits no warning.
func TestWriteBackupCollectionMetadata_FailuresEscalateToWarnings(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	o := &Orchestrator{logger: logger}

	// Failure path: a regular file used as the "tempDir" makes both writes fail
	// (writeBackupMetadata cannot MkdirAll under it and the collector cannot write
	// manifest.json under it).
	badTemp := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badTemp, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	badCollector := backup.NewCollector(logger, backup.GetDefaultCollectorConfig(), badTemp, types.ProxmoxUnknown, false)

	before := logger.WarningCount()
	o.writeBackupCollectionMetadata(badTemp, "test-host", &BackupStats{}, badCollector)
	if got := logger.WarningCount() - before; got != 2 {
		t.Fatalf("expected 2 warnings (metadata + manifest write failures), got %d", got)
	}

	// Success path: a writable tempDir produces no warnings.
	okTemp := t.TempDir()
	okCollector := backup.NewCollector(logger, backup.GetDefaultCollectorConfig(), okTemp, types.ProxmoxUnknown, false)

	before = logger.WarningCount()
	o.writeBackupCollectionMetadata(okTemp, "test-host", &BackupStats{}, okCollector)
	if got := logger.WarningCount() - before; got != 0 {
		t.Fatalf("expected no warnings on a writable tempDir, got %d", got)
	}
}

// TestWriteBackupCollectionMetadata_FailureReflectedInReSnapshot locks in the
// PS-BH-003 secondary fix: re-applying the collector stats after the metadata
// writes reflects a manifest write failure in FilesFailed (the first snapshot,
// taken before the writes, does not).
func TestWriteBackupCollectionMetadata_FailureReflectedInReSnapshot(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	o := &Orchestrator{logger: logger}

	badTemp := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badTemp, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	collector := backup.NewCollector(logger, backup.GetDefaultCollectorConfig(), badTemp, types.ProxmoxUnknown, false)

	var stats BackupStats

	// First snapshot, taken before the metadata writes, sees no failures.
	o.applyBackupCollectionStats(&stats, collector.GetStats(), collector)
	if stats.FilesFailed != 0 {
		t.Fatalf("pre-write FilesFailed = %d, want 0", stats.FilesFailed)
	}

	o.writeBackupCollectionMetadata(badTemp, "test-host", &stats, collector)

	// Re-snapshot after the writes: the failed manifest write is now reflected.
	o.applyBackupCollectionStats(&stats, collector.GetStats(), collector)
	if stats.FilesFailed == 0 {
		t.Fatalf("post-write FilesFailed = 0, want >0 (manifest write failure not reflected)")
	}
}
