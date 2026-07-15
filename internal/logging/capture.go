package logging

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// lineWriter is a thread-safe io.Writer that splits its input into complete
// lines (on '\n'), strips ANSI escapes, right-trims trailing whitespace, and
// hands each finished line to a forward callback. A trailing partial write with
// no newline stays buffered until a later write completes it. It is the reusable
// bridge from a logger's io.Writer sink to a line-oriented UI consumer.
type lineWriter struct {
	mu      sync.Mutex
	buf     []byte
	forward func(line string)
}

// NewLineWriter returns a thread-safe io.Writer that buffers partial writes,
// splits on '\n', strips ANSI escapes (charmbracelet/x/ansi), right-trims
// trailing whitespace, and calls forward(line) for EACH complete line, in
// order. The "[ts] LEVEL msg" prefix is preserved verbatim (the line is not
// reparsed). A trailing partial (no newline) is retained until the next write
// completes it. A nil forward is tolerated (lines are dropped).
func NewLineWriter(forward func(line string)) io.Writer {
	return &lineWriter{forward: forward}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if w.forward != nil {
			w.forward(strings.TrimRight(ansi.Strip(line), " \t\r"))
		}
	}
	return len(p), nil
}

// CaptureConsole is the SINGLE place that wires the loggers to w so a full-screen
// UI (or any phase that wants its own log lines) can stream them. It:
//
//   - redirects the DEFAULT logger's console output to w (log files unaffected);
//   - if bootstrap is non-nil, attaches a color-free mirror Logger (clean
//     "[ts] LEVEL msg" prefix) writing to w so bootstrap lines flow to w too, and
//     forces the bootstrap console-quiet so its own raw prints stay off the
//     terminal.
//
// It returns restore(), which undoes ALL of the above: it restores the default
// logger's previous output, detaches the bootstrap mirror (SetMirrorLogger(nil)),
// and restores the bootstrap's prior console-quiet state. A nil bootstrap
// captures only the default logger. restore() is idempotent-safe to call once;
// call it via defer for panic safety.
func CaptureConsole(bootstrap *BootstrapLogger, w io.Writer) (restore func()) {
	def := GetDefaultLogger()
	var prevDefault io.Writer
	if def != nil {
		prevDefault = def.SwapOutput(w)
	}

	var (
		prevQuiet    bool
		wiredMirror  bool
		wiredQuietOK bool
	)
	if bootstrap != nil {
		prevQuiet = bootstrap.consoleQuietEnabled()
		// Mirror at the bootstrap's OWN level (INFO by default), NOT hardcoded Debug:
		// otherwise every bootstrap.Debug line (e.g. DebugStepBootstrap) would leak into
		// the UI stream even on a standard run. A debug run (bootstrap SetLevel Debug)
		// still streams debug - see the plan's "run finalization in debug" design task.
		mirror := New(bootstrap.levelValue(), false) // useColor=false -> clean prefix
		mirror.SetOutput(w)
		bootstrap.SetMirrorLogger(mirror)
		bootstrap.SetConsoleQuiet(true)
		wiredMirror = true
		wiredQuietOK = true
	}

	return func() {
		if def != nil {
			def.SwapOutput(prevDefault)
		}
		if bootstrap != nil {
			if wiredMirror {
				bootstrap.SetMirrorLogger(nil)
			}
			if wiredQuietOK {
				bootstrap.SetConsoleQuiet(prevQuiet)
			}
		}
	}
}
