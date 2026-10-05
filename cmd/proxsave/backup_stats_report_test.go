package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// A dry run writes no statistics report, so "Statistics report saved" must not
// follow "[DRY RUN] Would write stats report"; a real run still prints it.
func TestPersistBackupStatsSavedLineOnlyWhenWritten(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	for _, dryRun := range []bool{true, false} {
		buf := &bytes.Buffer{}
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(buf)
		logging.SetDefaultLogger(logger)

		logDir := t.TempDir()
		orch := orchestrator.New(logger, dryRun)
		orch.SetBackupConfig(t.TempDir(), logDir, types.CompressionNone, 0, 0, "standard", nil)
		stats := &orchestrator.BackupStats{Timestamp: time.Date(2026, 10, 2, 11, 33, 6, 0, time.UTC)}
		reportPath := filepath.Join(logDir, "backup-stats-20261002-113306.json")

		persistBackupStats(orch, stats)

		out := buf.String()
		saved := strings.Contains(out, "✓ Statistics report saved to "+reportPath)
		wouldWrite := strings.Contains(out, "[DRY RUN] Would write stats report: "+reportPath)
		if dryRun && (saved || !wouldWrite) {
			t.Fatalf("dry run: want only the would-write line, got:\n%s", out)
		}
		if !dryRun && (!saved || wouldWrite) {
			t.Fatalf("real run: want the saved line, got:\n%s", out)
		}
	}
}
