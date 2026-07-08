package components

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

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
const streamLineCap = 5000

// StreamLineMsg appends one line to the running stream screen.
type StreamLineMsg struct {
	Token uint64
	Line  string
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
// It resolves only on Enter/Space AFTER completion, never on input while
// running and never automatically. While running Esc requests cancellation.
type StreamTask struct {
	shell.Resolver[StreamResult]
	token      uint64
	title      string
	spin       spinner.Model
	lines      []string // bounded ring of retained log lines (with ANSI)
	dropped    bool     // true once the ring has shed its oldest lines
	done       bool
	outcome    string
	err        error
	cancel     context.CancelFunc
	cancelling bool
	copied     bool // transient "log copied to clipboard" confirmation
	vp         viewport.Model
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
	// Wrap long lines INSIDE the box so the whole line stays visible and there is
	// no horizontal-scroll offset (which renders unpredictably in the frame).
	vp.SoftWrap = true
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

func (t *StreamTask) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamLineMsg:
		if msg.Token == t.token && !t.done {
			// Streamed lines carry untrusted runtime data: sanitize at the
			// boundary while PRESERVING ANSI SGR so the colors survive into
			// the panel. Skip blank lines so no empty rows scroll past.
			if line := sanitizeStreamLine(msg.Line); strings.TrimSpace(line) != "" {
				t.lines = append(t.lines, line)
				if len(t.lines) > streamLineCap {
					// Drop the oldest lines beyond the cap (bounded ring) and
					// remember it so the panel can note the truncation.
					t.lines = t.lines[len(t.lines)-streamLineCap:]
					t.dropped = true
				}
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
			// so the paste is clean plain text for a support request.
			t.copied = true
			return t, tea.SetClipboard(ansi.Strip(strings.Join(t.lines, "\n")))
		case "enter", "space", "return":
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
	t.vp.SetWidth(width)
	t.vp.SetHeight(bodyH)
	// Set the content HERE, after sizing, so it is always measured/soft-wrapped
	// against the CURRENT width (never a stale or zero width from an earlier
	// Update), and re-pin to the bottom while following.
	t.vp.SetContent(strings.Join(t.lines, "\n"))
	if t.follow {
		t.vp.GotoBottom()
	}

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

// lipglossCount returns the number of display rows a (possibly multi-line)
// rendered block occupies.
func lipglossCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// RunStreamTask drives a streaming operation behind a CONTAINED viewport stream
// screen. run emits lines via emit (safe to call from any goroutine) and returns
// a pre-styled outcome string plus an error. RunStreamTask always waits for run
// to return before returning (drain semantics), even when the user aborts or the
// UI dies, so callers can rely on run's resources being released.
func RunStreamTask(ctx context.Context, s *shell.Session, title string, run func(ctx context.Context, emit func(line string)) (outcome string, err error)) error {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	token := streamToken.Add(1)
	scr := newStreamScreen(title, token, cancel)

	done := make(chan error, 1)
	go func() {
		outcome, rerr := run(taskCtx, func(line string) {
			s.Send(StreamLineMsg{Token: token, Line: line})
		})
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
