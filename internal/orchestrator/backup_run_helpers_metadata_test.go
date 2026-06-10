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
