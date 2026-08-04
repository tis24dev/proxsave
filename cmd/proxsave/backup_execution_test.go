package main

import (
	"bytes"
	"os"
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
		logBackupStatistics(stats, false)
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

// TestLogBackupStatisticsSkippedWhenTheOutcomeRendersIt: the dashboard screen renders
// the recap itself in its outcome block, so the engine must stay silent there — at
// EVERY level. c0cc02a suppressed only the Debug arm's predecessor and let the compact
// rows into the run viewport, where they landed underneath the same four rows the
// outcome block was already showing. Debug was worse and older: capturing the console
// swaps the writer, not the level, so the full block reached the viewport too.
func TestLogBackupStatisticsSkippedWhenTheOutcomeRendersIt(t *testing.T) {
	prevLogger := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })

	stats := &orchestrator.BackupStats{
		FilesCollected: 42,
		DirsCreated:    7,
		ArchivePath:    "/var/backup/proxsave.tar.zst",
	}

	for _, level := range []types.LogLevel{types.LogLevelInfo, types.LogLevelDebug} {
		buf := &bytes.Buffer{}
		logger := logging.New(level, false)
		logger.SetOutput(buf)
		logging.SetDefaultLogger(logger)

		logBackupStatistics(stats, true)

		if out := buf.String(); strings.TrimSpace(out) != "" {
			t.Errorf("level %v: the engine must log nothing when the outcome renders the recap, got %q", level, out)
		}
	}
}

// TestStreamedBackupSuppressesTheLoggedRecapOnlyOnTheViewportPath is the trap this
// repair had to avoid. runBackupStreamed has TWO ways to run the same steps: with a
// live session (viewport + outcome block, so the logged recap would be a duplicate) and
// without one, when the dashboard handoff vanished and the run continues plain. The
// second shows no outcome at all, so it must keep the logged recap — putting the flag
// on the shared options instead of on the viewport clone would have silently stripped
// the statistics from exactly the run that has no other copy of them.
func TestStreamedBackupSuppressesTheLoggedRecapOnlyOnTheViewportPath(t *testing.T) {
	src, err := os.ReadFile("backup_stream.go")
	if err != nil {
		t.Fatalf("read backup_stream.go: %v", err)
	}
	body := string(src)

	// Count ASSIGNMENTS, not mentions: the field is named in comments here too, and a
	// test that counted the bare name would fail on documentation.
	const set = "stepOpts.outcomeRendersRecap = true"
	if n := strings.Count(body, "outcomeRendersRecap = "); n != 1 {
		t.Fatalf("the flag must be assigned exactly once in this file, found %d assignments", n)
	}
	setIdx := strings.Index(body, set)
	if setIdx < 0 {
		t.Fatalf("the flag must be set on the stepOpts CLONE, not on opts")
	}
	// The fallback runs the steps with the un-cloned opts and returns before the
	// viewport exists; the clone is created after it. If the set ever moves above the
	// fallback it would start applying to a run that renders no outcome.
	fallbackIdx := strings.Index(body, "res := backupStreamSteps(opts)")
	if fallbackIdx < 0 {
		t.Fatal("the no-session fallback call site moved; re-check which options it passes")
	}
	if setIdx < fallbackIdx {
		t.Fatalf("the flag is set before the no-session fallback (set=%d fallback=%d): that run has no outcome block and must keep the logged recap", setIdx, fallbackIdx)
	}
}
