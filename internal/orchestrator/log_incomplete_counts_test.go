package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The exit code and the healthchecks ping come from the counts parsed back from the run
// log; when lines never reached it (here a full disk), the parse read a clean run while
// the logger had counted the issues. An incomplete file now yields the higher counts.
func TestRefreshLogIssuesUsesTheLoggerCountsWhenTheFileIsIncomplete(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available")
	}
	var out bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&out)
	if err := logger.OpenLogFile("/dev/full"); err != nil {
		t.Skipf("cannot open /dev/full as a log: %v", err)
	}
	logger.Warning("lost warning")
	logger.Error("lost error")

	parsed := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(parsed, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{logger: logger}
	stats := &BackupStats{LogFilePath: parsed}
	o.refreshLogIssuesFromFile(stats, false)
	if stats.ErrorCount < 1 || stats.WarningCount < 2 {
		t.Fatalf("errors=%d warnings=%d, want at least the logger's 1 and 2 (lost warning + the write-failure WARNING)", stats.ErrorCount, stats.WarningCount)
	}
}
