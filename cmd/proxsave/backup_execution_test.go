package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestLogBackupStatisticsDebugGating asserts the "=== Backup Statistics ==="
// block is debug-only: at LogLevelInfo logBackupStatistics emits nothing, and at
// LogLevelDebug it emits the full block. The block moved to the graphical outcome
// recap (buildBackupOutcomePrompt) for standard runs.
func TestLogBackupStatisticsDebugGating(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	stats := &orchestrator.BackupStats{
		FilesCollected: 42,
		DirsCreated:    7,
		ArchivePath:    "/var/backup/proxsave.tar.zst",
	}

	// At Info level the block (and its blank spacers) is skipped entirely.
	infoBuf := &bytes.Buffer{}
	infoLogger := logging.New(types.LogLevelInfo, false)
	infoLogger.SetOutput(infoBuf)
	logging.SetDefaultLogger(infoLogger)
	logBackupStatistics(stats)
	if strings.Contains(infoBuf.String(), "=== Backup Statistics ===") {
		t.Fatalf("stats block must be absent at Info level:\n%s", infoBuf.String())
	}

	// At Debug level the full block is emitted.
	debugBuf := &bytes.Buffer{}
	debugLogger := logging.New(types.LogLevelDebug, false)
	debugLogger.SetOutput(debugBuf)
	logging.SetDefaultLogger(debugLogger)
	logBackupStatistics(stats)
	if !strings.Contains(debugBuf.String(), "=== Backup Statistics ===") {
		t.Fatalf("stats block must be present at Debug level:\n%s", debugBuf.String())
	}
	if !strings.Contains(debugBuf.String(), "Files collected: 42") {
		t.Fatalf("stats block content missing at Debug level:\n%s", debugBuf.String())
	}
}
