package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// `--log-level error` used to make warnings vanish from EVERY channel at once:
// the threshold check sat before the counters, the issue capture and the file
// sink, so HasWarnings()==false painted the footer green, the re-parse found
// nothing in the file, and the run exited 0 - for a run that DID warn. The
// maintainer call (2026-09-02): the threshold is a CONSOLE filter. Warning-weight
// lines and above are always counted and always reach the log file (which is the
// artifact notifications ship); only their display is muted. Below warning the
// filter keeps its old full meaning.

func muteLevelLogger(t *testing.T, level types.LogLevel) (*Logger, *bytes.Buffer, string) {
	t.Helper()
	logger := New(level, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.CloseLogFile() })
	return logger, buf, logPath
}

func TestWarningsBelowConsoleLevelStillCountAndReachTheFile(t *testing.T) {
	logger, console, logPath := muteLevelLogger(t, types.LogLevelError)

	logger.Warning("secondary copy failed: %s", "io timeout")

	if got := logger.WarningCount(); got != 1 {
		t.Fatalf("WarningCount = %d, want 1: a muted console must not blind the counters (footer/exit)", got)
	}
	if s := console.String(); strings.Contains(s, "secondary copy failed") {
		t.Fatalf("--log-level error must still mute the console display:\n%s", s)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "WARNING") || !strings.Contains(string(data), "secondary copy failed") {
		t.Fatalf("the shipped log file lost the warning line, the evidence for the exit code:\n%s", data)
	}
}

func TestErrorsBelowConsoleLevelStillCountAndReachTheFile(t *testing.T) {
	logger, console, logPath := muteLevelLogger(t, types.LogLevelCritical)

	logger.Error("store blew up")

	if got := logger.ErrorCount(); got != 1 {
		t.Fatalf("ErrorCount = %d, want 1", got)
	}
	if strings.Contains(console.String(), "store blew up") {
		t.Fatalf("console must stay muted at level critical:\n%s", console.String())
	}
	if data, _ := os.ReadFile(logPath); !strings.Contains(string(data), "store blew up") {
		t.Fatalf("error line missing from the file:\n%s", data)
	}
}

func TestBelowWarningTheThresholdKeepsItsFullMeaning(t *testing.T) {
	logger, console, logPath := muteLevelLogger(t, types.LogLevelError)

	logger.Info("chatty")
	logger.Debug("chattier")

	if s := console.String(); strings.Contains(s, "chatt") {
		t.Fatalf("info/debug leaked to console:\n%s", s)
	}
	if data, _ := os.ReadFile(logPath); strings.Contains(string(data), "chatt") {
		t.Fatalf("info/debug below the threshold must not reach the file either:\n%s", data)
	}
}
