package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestLogBackupStatisticsLevelSplit asserts what each log level gets. This test used
// to be TestLogBackupStatisticsDebugGating and asserted that Info emits NOTHING — the
// behaviour a cron run inherited, which left an unattended backup with no statistics
// anywhere. Info now gets the compact rows; only the "=== Backup Statistics ===" header
// and the full detail stay behind Debug.
func TestLogBackupStatisticsLevelSplit(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	stats := &orchestrator.BackupStats{
		FilesCollected: 42,
		DirsCreated:    7,
		ArchivePath:    "/var/backup/proxsave.tar.zst",
	}

	capture := func(level types.LogLevel) string {
		buf := &bytes.Buffer{}
		logger := logging.New(level, false)
		logger.SetOutput(buf)
		logging.SetDefaultLogger(logger)
		logBackupStatistics(stats)
		return buf.String()
	}

	// Info: the compact rows, no section header. This is what a cron log gets.
	info := capture(types.LogLevelInfo)
	if strings.Contains(info, "=== Backup Statistics ===") {
		t.Fatalf("the section header belongs to the full block:\n%s", info)
	}
	for _, want := range []string{"Files: 42 collected", "/var/backup/proxsave.tar.zst"} {
		if !strings.Contains(info, want) {
			t.Fatalf("an unattended run must still log %q:\n%s", want, info)
		}
	}
	if strings.Contains(info, "Directories created") {
		t.Fatalf("the compact rows must stay compact:\n%s", info)
	}

	// Debug: the full block, header included.
	debug := capture(types.LogLevelDebug)
	for _, want := range []string{"=== Backup Statistics ===", "Files: 42 collected", "Directories created: 7"} {
		if !strings.Contains(debug, want) {
			t.Fatalf("the full block must contain %q:\n%s", want, debug)
		}
	}
}
