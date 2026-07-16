package components

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// streamLineCap bounds the ring of retained streamed lines. A long backup can
// emit thousands of lines; keeping a generous history lets the user scroll back
// through the whole run inside the panel, while the cap still guards against an
// unbounded pathological producer (oldest lines drop first, a note is shown).
//
// The cap bounds the RAW lines (the copy source). Each raw line can soft-wrap
// into several display lines; the wrapped mirror is kept consistent with the
// ring (oldest wrapped rows drop together with their raw parent).
const streamLineCap = 5000

// streamLineWidthCap bounds the display width of a SINGLE line before wrapLine.
// wrapLine is O(L^2/width) in one line's width, so a pathological multi-100k-cell
// line (no newline) would block the event-loop goroutine on append and on every
// resize. 8192 cells is about 100 rows at width 80, far beyond any readable line;
// anything wider is truncated with a marker (unreadable in a TUI regardless).
const streamLineWidthCap = 8192

// streamFlushInterval is the coalescing window: emitted lines are buffered and
// flushed as one StreamLinesMsg at most this often, so a debug run's firehose of
// thousands of lines lands as a handful of batches instead of one s.Send (and
// one Update) per line. ~60ms stays below the flicker threshold (smooth) yet
// coarse enough to collapse the storm.
const streamFlushInterval = 60 * time.Millisecond

// streamFlushCount forces an early flush once this many lines are buffered even
// before the interval elapses, so a very fast producer cannot grow the buffer
// without bound between ticks.
const streamFlushCount = 256

// streamPendingCap bounds the producer-side coalescing buffer. The display ring
// keeps only the newest streamLineCap lines, so if the flusher's Send stalls on
// UI backpressure the producer never needs more than that; the oldest overflow is
// dropped so a wedged UI cannot grow this buffer without bound (262-9).
const streamPendingCap = streamLineCap

// StreamLineMsg appends one line to the running stream screen. Retained for
// direct/legacy senders and unit tests; the production emit path coalesces into
// StreamLinesMsg batches. Both are handled identically in Update.
type StreamLineMsg struct {
	Token uint64
	Line  string
}

// StreamLinesMsg appends a BATCH of lines to the running stream screen in one
// Update. The coalescing streamEmitBuffer flushes these so the UI pays one
// message + one incremental append per batch instead of per line.
type StreamLinesMsg struct {
	Token uint64
	Lines []string
}

// StreamDoneMsg marks the stream complete. It stores the outcome and error
// but does NOT resolve the screen; the user must Continue.
type StreamDoneMsg struct {
	Token   uint64
	Outcome string
	Err     error
}

// StreamResult is the resolve payload of a StreamTask screen.
type StreamResult struct {
	Err error
}

var streamToken atomic.Uint64

// StreamTask renders a streaming operation inside a CONTAINED, scrollable,
// colored viewport panel on the altscreen. The header (spinner + title) and,
// once complete, a pre-styled outcome block stay pinned; every streamed log
// line lands in a bounded ring rendered through a bubbles viewport, so
// up/down/pgup/pgdown/home/end and the mouse wheel scroll WITHIN the panel's
// box - the run is fully inside the graphics frame, never in the terminal's raw
// scrollback. Lines carry ANSI (the caller streams via logging.NewLineWriterRaw
// paired with CaptureConsoleWithColor), so the panel shows real colors.
//
// PERFORMANCE (soft-wrap done here, not by the viewport). The bubbles viewport's
// own SoftWrap re-measures EVERY retained line on every render: calculateLine
// (called by View, TotalLineCount and GotoBottom) iterates all lines when
// SoftWrap is on, so a run of N retained lines costs O(N) PER FRAME - and the
// spinner ticks ~10x/s, so it never stops. Instead we keep SoftWrap OFF and
// wrap the content OURSELVES, incrementally: we retain the RAW lines (the copy
// source, ring-bounded) AND a derived slice of already-wrapped display lines.
// New lines are wrapped ONCE on arrival; the whole buffer is re-wrapped ONLY on
// a width change. With SoftWrap off calculateLine is O(1), so each render is
// O(visible rows). wrapLine reproduces the viewport's EXACT fixed-column wrap
// (ansi.Cut over [idx, idx+width) chunks) so colored lines wrap byte-identically
// to the old path.
//
// It resolves only on Enter/Space AFTER completion, never on input while
// running and never automatically. While running Esc requests cancellation.
type StreamTask struct {
	shell.Resolver[StreamResult]
	token           uint64
	title           string
	spin            spinner.Model
	lines           []string // bounded ring of RAW retained log lines (with ANSI) - copy source
	wrapped         []string // pre-soft-wrapped DISPLAY lines derived from lines at wrapWidth
	wrapWidth       int      // the width wrapped was computed against (0 = not yet wrapped)
	dirty           bool     // wrapped content changed since the last SetContentLines (View gate)
	dropped         bool     // true once the ring has shed its oldest lines
	pendingDropRows int      // wrapped rows shed by ring-drops since the last View (261-6)
	done            bool
	outcome         string
	err             error
	cancel          context.CancelFunc
	cancelling      bool
	copied          bool // transient "log copied to clipboard" confirmation
	vp              viewport.Model
	// follow keeps the viewport pinned to the newest line (auto-scroll). It
	// turns off the moment the user scrolls up, so a manual review is not
	// yanked back to the bottom by the next streamed line; End/GotoBottom
	// re-enables it.
	follow bool
}

func newStreamScreen(title string, token uint64, cancel context.CancelFunc) *StreamTask {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	vp := viewport.New()
	// We soft-wrap the content OURSELVES (incrementally) and feed the viewport
	// pre-wrapped display lines, so the viewport must NOT wrap again: SoftWrap
	// OFF means calculateLine is O(1) and each retained display line is exactly
	// one row. (With SoftWrap ON the viewport re-measures the whole buffer every
	// render - the O(N)-per-frame cost we are removing.)
	vp.SoftWrap = false
	return &StreamTask{
		token:  token,
		title:  sanitizeLine(title),
		spin:   sp,
		cancel: cancel,
		vp:     vp,
		follow: true,
	}
}

// ReceivesBackgroundMessages keeps lines and completion flowing while buried
// under another screen.
func (t *StreamTask) ReceivesBackgroundMessages() bool { return true }

func (t *StreamTask) Init() tea.Cmd { return t.spin.Tick }

func (t *StreamTask) Title() string { return t.title }

func (t *StreamTask) Help() string {
	if t.done {
		return "↑/↓ scroll · c copy log · enter continue"
	}
	return "↑/↓ scroll · c copy log · esc cancel"
}

// appendRaw appends one already-sanitized raw line to the ring, enforces the
// cap, and keeps the wrapped mirror consistent - the O(new) hot path:
//   - wrap ONLY the newcomer (at the current wrapWidth) and append its rows;
//   - when the cap sheds the oldest RAW lines, shed exactly THEIR wrapped rows
//     off the front (row counts are deterministic at the fixed width), so the
//     mirror stays == wrapLines(lines, wrapWidth) WITHOUT re-wrapping the whole
//     buffer (that would be O(cap) on every post-cap append = quadratic again).
//
// Before the first View sets a width (wrapWidth==0) wrapping is deferred: the
// first View re-wraps everything once via rewrapAll. Marks the content dirty so
// the View gate re-feeds the viewport.
func (t *StreamTask) appendRaw(raw string) {
	t.lines = append(t.lines, raw)
	if t.wrapWidth > 0 {
		t.wrapped = append(t.wrapped, wrapLine(raw, t.wrapWidth)...)
	}
	if len(t.lines) > streamLineCap {
		drop := len(t.lines) - streamLineCap
		if t.wrapWidth > 0 {
			// Shed exactly the wrapped rows of the oldest `drop` raw lines
			// (computed BEFORE slicing lines), O(drop) not O(cap).
			dropRows := 0
			for i := 0; i < drop; i++ {
				dropRows += len(wrapLine(t.lines[i], t.wrapWidth))
			}
			t.wrapped = t.wrapped[dropRows:]
			t.pendingDropRows += dropRows
		}
		t.lines = t.lines[drop:]
		t.dropped = true
	}
	t.dirty = true
}

func (t *StreamTask) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamLineMsg:
		if msg.Token == t.token && !t.done {
			// Streamed lines carry untrusted runtime data: sanitize at the
			// boundary while PRESERVING ANSI SGR so the colors survive into the
			// panel. BLANK lines are KEPT: they are the section spacers the run
			// prints (fmt.Println), so the panel spaces sections like the CLI.
			t.appendRaw(sanitizeStreamLine(msg.Line))
		}
		return t, nil
	case StreamLinesMsg:
		if msg.Token == t.token && !t.done {
			// A coalesced batch: sanitize + append each with the same policy as
			// the single-line path (ANSI SGR preserved, blank spacers kept, ring
			// cap enforced per line).
			for _, line := range msg.Lines {
				t.appendRaw(sanitizeStreamLine(line))
			}
		}
		return t, nil
	case StreamDoneMsg:
		if msg.Token == t.token {
			t.outcome = msg.Outcome
			t.err = msg.Err
			t.done = true
			// Keep follow so the final lines are shown; the user can still
			// scroll up to review the whole run afterwards.
		}
		return t, nil
	case spinner.TickMsg:
		if t.done {
			return t, nil
		}
		var cmd tea.Cmd
		t.spin, cmd = t.spin.Update(msg)
		return t, cmd
	case tea.KeyPressMsg:
		// Any keypress clears the transient "copied" confirmation; the copy key
		// below re-sets it.
		t.copied = false
		switch msg.String() {
		case "c":
			// Copy the WHOLE log to the system clipboard via OSC52, ANSI stripped
			// so the paste is clean plain text for a support request. Copy the RAW
			// lines (not the wrapped display rows): the paste must reflect the
			// original logical lines, not the panel's cosmetic wrap boundaries.
			t.copied = true
			return t, tea.SetClipboard(ansi.Strip(strings.Join(t.lines, "\n")))
		case "enter", "space":
			if t.done {
				return t, t.Resolve(StreamResult{Err: t.err}, nil)
			}
			return t, nil
		case "esc":
			if !t.done && !t.cancelling && t.cancel != nil {
				t.cancelling = true
				t.cancel()
			}
			return t, nil
		case "up", "pgup", "home", "k":
			// User is reviewing history: stop auto-following so the next
			// streamed line does not yank the view back to the bottom.
			t.follow = false
			var cmd tea.Cmd
			t.vp, cmd = t.vp.Update(msg)
			return t, cmd
		case "end", "G":
			// Jump to the newest line and resume auto-follow.
			t.follow = true
			t.vp.GotoBottom()
			return t, nil
		case "down", "pgdown", "j":
			var cmd tea.Cmd
			t.vp, cmd = t.vp.Update(msg)
			// If the user scrolled down to the very bottom, resume follow.
			if t.vp.AtBottom() {
				t.follow = true
			}
			return t, cmd
		}
		return t, nil
	}
	// Everything else (mouse wheel, other tea messages) goes to the viewport
	// so wheel-scroll works inside the box.
	if _, ok := msg.(tea.MouseWheelMsg); ok {
		var cmd tea.Cmd
		t.vp, cmd = t.vp.Update(msg)
		// A wheel-up leaves the bottom -> stop following; a wheel-down that
		// reaches the bottom resumes it.
		t.follow = t.vp.AtBottom()
		return t, cmd
	}
	var cmd tea.Cmd
	t.vp, cmd = t.vp.Update(msg)
	return t, cmd
}

func (t *StreamTask) View(width, height int) string {
	// Header (spinner + title, or the done outcome) is built FIRST so its height
	// carves out the space left for the CONTAINED scrollable panel below it.
	var header strings.Builder
	if t.done {
		header.WriteString(theme.Emphasis.Render("Completed"))
	} else {
		header.WriteString(theme.Title.Render(t.spin.View()))
		header.WriteString(" " + theme.Emphasis.Render(t.title))
		if t.cancelling {
			header.WriteString(" " + theme.WarningText.Render("(cancelling...)"))
		}
	}
	if t.dropped {
		header.WriteString(" " + theme.Subtle.Render(fmt.Sprintf("(showing last %d lines)", streamLineCap)))
	}
	if t.copied {
		header.WriteString(" " + theme.SuccessText.Render(theme.SymbolSuccess+" log copied"))
	}
	headerStr := header.String()

	// The outcome block (verbatim, pre-styled by the caller) sits BELOW the
	// panel once the run completes, with the streamed log scrolling above it.
	outcome := ""
	if t.done && t.outcome != "" {
		outcome = t.outcome
	}

	// Layout: header (1 row) + separator (1) + panel + separator? We reserve
	// the header, a rule, a scroll indicator row, and the outcome height; the
	// REST goes to the viewport (the bounded, scrollable, colored box).
	rule := theme.Subtle.Render(strings.Repeat("─", max(width, 1)))
	reserved := lipglossCount(headerStr) + 1 /*rule*/ + 1 /*scroll row*/
	if outcome != "" {
		reserved += lipglossCount(outcome) + 1 /*rule below panel*/
	}
	bodyH := height - reserved
	if bodyH < 1 {
		bodyH = 1
	}

	// Size FIRST (garble fix 80704d7 / memory proxsave-charm-viewport-gotcha):
	// SetWidth/SetHeight then SetContentLines, both here in View, so the content
	// is always measured against the CURRENT width and re-pinned while following.
	t.vp.SetWidth(width)
	t.vp.SetHeight(bodyH)

	// Incremental-wrap maintenance. We wrap OURSELVES (SoftWrap off), so a width
	// change re-wraps the WHOLE retained buffer once (rare); otherwise wrapped is
	// already current (built incrementally on append). wrapWidth<=0 (first View)
	// also forces the initial wrap.
	wrapW := max(width, 1)
	if wrapW != t.wrapWidth {
		t.rewrapAll(wrapW)
	}

	// GATE: only push content into the viewport when the wrapped content actually
	// changed (new lines / re-wrap) - not on every spinner tick or key.
	// SetContentLines (pre-wrapped, \n-free rows) stays HERE, after sizing, per
	// the garble fix, and avoids a Join+Split round-trip vs SetContent.
	if t.dirty {
		t.vp.SetContentLines(t.wrapped)
		t.dirty = false
	}
	if t.follow {
		t.vp.GotoBottom()
	} else if t.pendingDropRows > 0 {
		// A ring-drop shed rows from the front while the user was scrolled up; shift
		// the offset by the same amount so the reviewed content stays pinned to the
		// same logical lines instead of drifting (the viewport clamps to range).
		t.vp.SetYOffset(t.vp.YOffset() - t.pendingDropRows)
	}
	t.pendingDropRows = 0

	scroll := ""
	if t.vp.TotalLineCount() > bodyH {
		scroll = theme.Subtle.Render(fmt.Sprintf("(%d%%)", int(t.vp.ScrollPercent()*100)))
		if !t.follow {
			scroll += theme.Subtle.Render("  end↵ to follow")
		}
	}

	var b strings.Builder
	b.WriteString(headerStr)
	b.WriteString("\n")
	b.WriteString(rule)
	b.WriteString("\n")
	b.WriteString(t.vp.View())
	b.WriteString("\n")
	b.WriteString(scroll)
	if outcome != "" {
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
		b.WriteString(outcome)
	}
	return b.String()
}

// rewrapAll recomputes the whole wrapped mirror for a new width. This is the
// ONLY O(N) wrap path and it runs only on a width change (resize) or the first
// View - never per-line and never per-frame. It marks the content dirty so the
// next View re-feeds the viewport (which re-clamps its offset to the new
// geometry); the follow flag is untouched (View re-pins via GotoBottom).
func (t *StreamTask) rewrapAll(width int) {
	t.wrapWidth = width
	t.wrapped = wrapLines(t.lines, width)
	t.dirty = true
}

// wrapLines soft-wraps a slice of raw lines into display rows at the given
// width, concatenating each line's wrap chunks in order.
func wrapLines(lines []string, width int) []string {
	if width < 1 {
		width = 1
	}
	// Pre-size generously: most lines are one row; long ones add a few.
	out := make([]string, 0, len(lines)+len(lines)/8)
	for _, line := range lines {
		out = append(out, wrapLine(line, width)...)
	}
	return out
}

// wrapLine soft-wraps ONE raw line into display rows the way the bubbles
// viewport's softWrap does (viewport.go softWrap): byte-identical for normal
// content, and grapheme-safe (never over-width, never drops a glyph) for the
// wide-rune edge the viewport itself mishandles.
//
//   - a single line wider than streamLineWidthCap cells is first truncated with a
//     marker, so the O(L^2/width) loop below cannot block the event loop on a
//     pathological line (F04-03); normal lines are far under the cap and untouched.
//   - a line whose display width is <= width is returned unchanged (one row);
//     this includes blank/empty lines (width 0), which stay one empty row - the
//     section spacers survive 1:1.
//   - a wider line is cut into <= width chunks via ansi.Cut over [idx, idx+width)
//     windows. ansi.Cut is ANSI- and grapheme-aware: it never splits an escape,
//     and it re-emits the active SGR at each chunk start, so a colored line keeps
//     its color across every wrap boundary. For pure ASCII / boundary-aligned
//     content this is exactly the viewport's fixed-column wrap.
//   - a WIDE grapheme (CJK/emoji) straddling the right boundary makes ansi.Cut
//     over-INCLUDE it, so the chunk would exceed width; we clamp it with
//     ansi.Truncate and advance idx by the chunk's ACTUAL width, so the
//     straddling grapheme carries to the next row. Otherwise the SoftWrap-off
//     viewport would horizontally truncate the over-width chunk and DROP that
//     glyph (the viewport's own softWrap instead overflows it - both wrong; this
//     is correct). Fidelity for normal content is asserted against a live
//     SoftWrap viewport (TestWrapFidelityAgainstViewport); if bubbles changes
//     its wrap it fails loudly.
//
// The returned chunks never contain '\n' (the input is a single sanitized line),
// so feeding them to viewport.SetContent keeps one display row per chunk. This
// mirrors the viewport used with no left gutter and no Style frame (maxWidth ==
// Width()).
func wrapLine(line string, width int) []string {
	if width < 1 {
		width = 1
	}
	if ansi.StringWidth(line) > streamLineWidthCap {
		// Bound a pathological single line so the loop below stays O(cap^2/width);
		// the marker is honest and no readable TUI line is anywhere near this wide.
		line = ansi.Truncate(line, streamLineWidthCap, "...(truncated)")
	}
	lineWidth := ansi.StringWidth(line)
	if lineWidth <= width {
		// One row - unchanged. (Mirrors viewport.softWrap's <= maxWidth path.)
		return []string{line}
	}
	rows := make([]string, 0, (lineWidth+width-1)/width)
	for idx := 0; idx < lineWidth; {
		chunk := ansi.Cut(line, idx, idx+width)
		if ansi.StringWidth(chunk) > width {
			// A wide grapheme straddling the boundary got over-included; clamp so
			// the chunk fits and the straddling grapheme starts the next row.
			chunk = ansi.Truncate(chunk, width, "")
		}
		cw := ansi.StringWidth(chunk)
		if cw < 1 {
			// The next grapheme is WIDER than the whole window (only at a
			// degenerate width < 2, unreachable in the real layout): emit it
			// whole rather than dropping it, and step past it. This keeps wrapLine
			// total (no silent drop) and the loop always progressing.
			chunk = ansi.Cut(line, idx, idx+2)
			cw = ansi.StringWidth(chunk)
			if cw < 1 {
				cw = 1
			}
		}
		rows = append(rows, chunk)
		idx += cw
	}
	return rows
}

// lipglossCount returns the number of display rows a (possibly multi-line)
// rendered block occupies.
func lipglossCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// streamEmitBuffer is the goroutine-safe, NON-BLOCKING seam between the run's
// emit(line) calls (from the producer / io.Copy goroutine) and the UI. emit
// appends into a mutex-guarded slice and returns immediately - the producer
// NEVER blocks on UI backpressure (the old s.Send-per-line path could stall the
// io.Copy pump and slow the backup itself). A dedicated flusher goroutine drains
// the slice into StreamLinesMsg batches, flushing on WHICHEVER-COMES-FIRST: the
// pending count crossing streamFlushCount (signalled via a coalescing 1-deep
// channel) or a streamFlushInterval tick. Close() flushes the remainder, stops
// the goroutine, and blocks until it has exited, so the caller can send
// StreamDoneMsg AFTER with the guarantee that no line is in flight or lost.
type streamEmitBuffer struct {
	s     *shell.Session
	token uint64

	mu      sync.Mutex
	pending []string
	closed  bool

	wake chan struct{}  // 1-deep, coalescing: "count threshold reached"
	quit chan struct{}  // closed by Close to stop the flusher
	wg   sync.WaitGroup // waits for the flusher goroutine to exit
}

func newStreamEmitBuffer(s *shell.Session, token uint64) *streamEmitBuffer {
	b := &streamEmitBuffer{
		s:     s,
		token: token,
		wake:  make(chan struct{}, 1),
		quit:  make(chan struct{}),
	}
	b.wg.Add(1)
	go b.loop()
	return b
}

// emit queues one line for the UI. Safe from any goroutine and NEVER blocks
// (the producer/io.Copy pump must not stall on UI backpressure). After Close it
// is a no-op, so a late line from a straggling producer cannot send-after-done.
func (b *streamEmitBuffer) emit(line string) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.pending = append(b.pending, line)
	if len(b.pending) > streamPendingCap {
		// Drop the oldest overflow (a wedged UI can only ever review the newest
		// streamLineCap lines anyway). Copy into a fresh slice so the large backing
		// array is released rather than retained by a reslice.
		drop := len(b.pending) - streamPendingCap
		b.pending = append([]string(nil), b.pending[drop:]...)
	}
	over := len(b.pending) >= streamFlushCount
	b.mu.Unlock()
	if over {
		b.signal()
	}
}

// signal nudges the flusher without ever blocking: the wake channel is 1-deep,
// so a nudge that finds it full is simply coalesced (the flusher drains all
// pending on its next pass anyway).
func (b *streamEmitBuffer) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// take atomically swaps out the pending slice (nil if empty).
func (b *streamEmitBuffer) take() []string {
	b.mu.Lock()
	batch := b.pending
	b.pending = nil
	b.mu.Unlock()
	return batch
}

// flush drains the pending lines and sends them as one StreamLinesMsg. The
// s.Send happens OUTSIDE the lock (never hold the mutex across a channel op).
func (b *streamEmitBuffer) flush() {
	batch := b.take()
	if len(batch) == 0 {
		return
	}
	b.s.Send(StreamLinesMsg{Token: b.token, Lines: batch})
}

// loop is the flusher goroutine: flush on a count nudge or the interval tick; on
// quit do a final drain and return.
func (b *streamEmitBuffer) loop() {
	defer b.wg.Done()
	ticker := time.NewTicker(streamFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.quit:
			// Final drain: whatever queued between the last flush and Close is
			// sent before the goroutine exits, so no line is lost.
			b.flush()
			return
		case <-b.wake:
			b.flush()
		case <-ticker.C:
			b.flush()
		}
	}
}

// Close stops accepting new lines, flushes everything remaining, stops the
// flusher goroutine, and blocks until it has exited. After Close returns the
// caller may send StreamDoneMsg with the guarantee that every emitted line was
// already delivered (ordered before StreamDoneMsg) and no goroutine leaks.
// Idempotent.
func (b *streamEmitBuffer) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.mu.Unlock()

	close(b.quit) // loop() does a final flush() then returns
	b.wg.Wait()   // block until the flush is delivered and the goroutine is gone
}

// RunStreamTask drives a streaming operation behind a CONTAINED viewport stream
// screen. run emits lines via emit (safe to call from any goroutine) and returns
// a pre-styled outcome string plus an error. RunStreamTask always waits for run
// to return before returning (drain semantics), even when the user aborts or the
// UI dies, so callers can rely on run's resources being released.
//
// Emitted lines flow through a coalescing, non-blocking streamEmitBuffer: emit
// never blocks the producer on UI backpressure, and the buffer batches lines
// into StreamLinesMsg. On run return the buffer is Closed (final flush + flusher
// goroutine stopped) BEFORE StreamDoneMsg, so no line is lost, no line arrives
// after done, and no goroutine leaks.
func RunStreamTask(ctx context.Context, s *shell.Session, title string, run func(ctx context.Context, emit func(line string)) (outcome string, err error)) error {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	token := streamToken.Add(1)
	scr := newStreamScreen(title, token, cancel)
	buf := newStreamEmitBuffer(s, token)

	done := make(chan error, 1)
	go func() {
		outcome, rerr := run(taskCtx, buf.emit)
		// Flush the remaining lines and stop the flusher BEFORE the completion
		// message, so the final line is delivered (ordered) ahead of
		// StreamDoneMsg and nothing races in after "done". buf.emit is a no-op
		// after Close, so a straggling producer goroutine can never send-after-done.
		buf.Close()
		done <- rerr
		s.Send(StreamDoneMsg{Token: token, Outcome: outcome, Err: rerr})
	}()

	res, askErr := shell.Ask(ctx, s, scr)
	cancel()
	runErr := <-done

	if askErr != nil {
		if shell.IsAbort(askErr) {
			// The user aborted the screen; the task context was cancelled and
			// run has drained. Surface the task's own outcome.
			return runErr
		}
		return askErr
	}
	return res.Err
}
