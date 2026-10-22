package logging

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// silenceStd sends the bootstrap's direct console prints to a pipe for the test.
func silenceStd(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, r); close(done) }()
	t.Cleanup(func() {
		os.Stdout, os.Stderr = origOut, origErr
		_ = w.Close()
		<-done
	})
}

func flushInto(t *testing.T, b *BootstrapLogger) (console string, logger *Logger) {
	t.Helper()
	var buf bytes.Buffer
	logger = New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	b.Flush(logger)
	return buf.String(), logger
}

// A line the bootstrap printed live is not printed again by Flush, but it still
// counts and still reaches the issue summary.
func TestBootstrapFlushDoesNotRepeatPrintedLines(t *testing.T) {
	silenceStd(t)
	b := NewBootstrapLogger()
	b.Info("Configuration integrity check:")
	b.Warning("X is not a known variable and is ignored; did you mean Y?")
	console, logger := flushInto(t, b)
	if console != "" {
		t.Fatalf("printed lines repeated on the console:\n%s", console)
	}
	if logger.WarningCount() != 1 {
		t.Fatalf("WarningCount = %d; the flushed warning must count", logger.WarningCount())
	}
	if issues := logger.IssueLines(); len(issues) != 1 || !strings.Contains(issues[0], "did you mean Y?") {
		t.Fatalf("issue lines = %v; the flushed warning must be in the summary", issues)
	}
}

// A line recorded while the console was quiet (a full-screen UI owned it) was
// never shown: Flush still prints it.
func TestBootstrapFlushPrintsLinesRecordedWhileQuiet(t *testing.T) {
	silenceStd(t)
	b := NewBootstrapLogger()
	b.SetConsoleQuiet(true)
	b.Info("recorded while quiet")
	b.SetConsoleQuiet(false)
	console, _ := flushInto(t, b)
	if !strings.Contains(console, "recorded while quiet") {
		t.Fatalf("a line never shown must be printed by Flush, console:\n%s", console)
	}
}

// A quiet warning replayed by ReplayConsoleSince is on the console: Flush does
// not print it a second time.
func TestBootstrapFlushDoesNotRepeatReplayedLines(t *testing.T) {
	silenceStd(t)
	b := NewBootstrapLogger()
	mark := b.EntryCount()
	b.SetConsoleQuiet(true)
	b.Warning("handoff failed")
	b.SetConsoleQuiet(false)
	b.ReplayConsoleSince(mark)
	console, logger := flushInto(t, b)
	if strings.Contains(console, "handoff failed") {
		t.Fatalf("a replayed line must not be printed again:\n%s", console)
	}
	if logger.WarningCount() != 1 {
		t.Fatalf("WarningCount = %d; want 1", logger.WarningCount())
	}
}
