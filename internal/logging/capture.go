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
	strip   bool
	forward func(line string)
}

// NewLineWriter returns a thread-safe io.Writer that buffers partial writes,
// splits on '\n', strips ANSI escapes (charmbracelet/x/ansi), right-trims
// trailing whitespace, and calls forward(line) for EACH complete line, in
// order. The "[ts] LEVEL msg" prefix is preserved verbatim (the line is not
// reparsed). A trailing partial (no newline) is retained until the next write
// completes it. A nil forward is tolerated (lines are dropped).
func NewLineWriter(forward func(line string)) io.Writer {
	return &lineWriter{forward: forward, strip: true}
}

// NewLineWriterRaw is the color-preserving sibling of NewLineWriter: identical
// line-splitting and trailing-whitespace trimming, but it KEEPS ANSI escapes
// (no ansi.Strip) so colored "[ts] LEVEL msg" lines survive into the forward
// callback. This is the sink used for the streamed viewport panel (StreamTask),
// where the scrollable box renders the colored lines.
func NewLineWriterRaw(forward func(line string)) io.Writer {
	return &lineWriter{forward: forward, strip: false}
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
		if w.forward == nil {
			continue
		}
		if w.strip {
			line = ansi.Strip(line)
		}
		w.forward(strings.TrimRight(line, " \t\r"))
	}
	return len(p), nil
}

// LineBacklog is a bounded, thread-safe io.Writer that retains the most recent
// complete lines written to it (ANSI escapes PRESERVED, like NewLineWriterRaw), so
// a late-attaching consumer can replay everything produced before it existed. It
// is the buffer behind Logger.SetMirror: the run logger tees its colored stream
// here while the graphical viewport does not yet exist, then the viewport drains
// Lines() once it is on the stack. Retention is bounded: the newest max lines are
// always kept and the total never exceeds 2*max (older lines are dropped in an
// amortized compaction, since a wedged/absent consumer can only ever review the
// newest anyway).
type LineBacklog struct {
	mu    sync.Mutex
	lines []string
	max   int
	w     io.Writer // NewLineWriterRaw(b.append): splits on '\n', keeps ANSI
}

// NewLineBacklog returns a LineBacklog that always retains the newest max complete
// lines (and at most 2*max at any instant). A non-positive max is clamped to 1.
func NewLineBacklog(max int) *LineBacklog {
	if max <= 0 {
		max = 1
	}
	b := &LineBacklog{max: max}
	b.w = NewLineWriterRaw(b.append)
	return b
}

// Write feeds bytes through the line splitter; complete lines are retained.
func (b *LineBacklog) Write(p []byte) (int, error) { return b.w.Write(p) }

func (b *LineBacklog) append(line string) {
	b.mu.Lock()
	b.lines = append(b.lines, line)
	// Amortized trim: let the slice grow to 2*max, then compact to the newest max
	// in one copy. This keeps the steady-state cost O(1) per line (instead of an
	// O(max) copy on every line past the cap) while never retaining more than 2*max
	// lines. The compaction copies into a fresh slice so the large backing array is
	// released rather than pinned by a reslice.
	if len(b.lines) > 2*b.max {
		b.lines = append([]string(nil), b.lines[len(b.lines)-b.max:]...)
	}
	b.mu.Unlock()
}

// Lines returns a snapshot copy of the retained lines, oldest first.
func (b *LineBacklog) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.lines...)
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
	return captureConsole(bootstrap, w, false)
}

// CaptureConsoleWithColor is the color-preserving sibling of CaptureConsole: it
// wires the same default-logger + bootstrap-mirror plumbing, but the bootstrap
// mirror is COLORED (useColor=true) so its "[ts] LEVEL msg" lines carry ANSI
// like the default logger already does. Pair it with NewLineWriterRaw so the
// colors reach the streamed viewport panel (StreamTask). The mirror is still created at the
// bootstrap's OWN level, so the standard-run debug-suppression contract holds.
func CaptureConsoleWithColor(bootstrap *BootstrapLogger, w io.Writer) (restore func()) {
	return captureConsole(bootstrap, w, true)
}

// captureConsole is the shared body of CaptureConsole / CaptureConsoleWithColor.
// mirrorColor selects whether the bootstrap mirror logger is colored; every
// other behavior (default-logger SwapOutput, bootstrap console-quiet, the
// idempotent-safe restore) is identical between the two.
func captureConsole(bootstrap *BootstrapLogger, w io.Writer, mirrorColor bool) (restore func()) {
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
		mirror := New(bootstrap.levelValue(), mirrorColor) // useColor per caller
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
