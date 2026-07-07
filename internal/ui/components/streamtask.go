package components

import (
	"context"
	"strings"
	"sync/atomic"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// streamLineCap bounds the ring of retained streamed lines; the oldest are
// dropped once the cap is exceeded, guarding against pathological scans.
const streamLineCap = 200

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

// StreamTask renders a streaming operation: spinner, title, a growing list of
// lines (bounded tail), and once complete a pre-rendered outcome block plus a
// Continue hint. It resolves only on Enter/Space AFTER completion, never on
// input while running and never automatically.
type StreamTask struct {
	shell.Resolver[StreamResult]
	token      uint64
	title      string
	spin       spinner.Model
	lines      []string
	done       bool
	outcome    string
	err        error
	cancel     context.CancelFunc
	cancelling bool
}

func newStreamScreen(title string, token uint64, cancel context.CancelFunc) *StreamTask {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &StreamTask{
		token:  token,
		title:  sanitizeLine(title),
		spin:   sp,
		cancel: cancel,
	}
}

// ReceivesBackgroundMessages keeps lines and completion flowing while buried
// under another screen.
func (t *StreamTask) ReceivesBackgroundMessages() bool { return true }

func (t *StreamTask) Init() tea.Cmd { return t.spin.Tick }

func (t *StreamTask) Title() string { return t.title }

func (t *StreamTask) Help() string {
	if t.done {
		return "enter continue"
	}
	return ""
}

func (t *StreamTask) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamLineMsg:
		if msg.Token == t.token && !t.done {
			// Streamed lines carry untrusted runtime data: sanitize at the
			// boundary. Skip blank lines so the stream shows no empty rows.
			if line := sanitizeLine(msg.Line); strings.TrimSpace(line) != "" {
				t.lines = append(t.lines, line)
				if len(t.lines) > streamLineCap {
					// Drop the oldest lines beyond the cap (bounded ring).
					t.lines = t.lines[len(t.lines)-streamLineCap:]
				}
			}
		}
		return t, nil
	case StreamDoneMsg:
		if msg.Token == t.token {
			t.outcome = msg.Outcome
			t.err = msg.Err
			t.done = true
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
		switch msg.String() {
		case "enter", "space", "return":
			if t.done {
				return t, t.Resolve(StreamResult{Err: t.err}, nil)
			}
		case "esc":
			if !t.done && !t.cancelling && t.cancel != nil {
				t.cancelling = true
				t.cancel()
			}
		}
		return t, nil
	}
	return t, nil
}

func (t *StreamTask) View(width, height int) string {
	// Bottom summary: spinner (running) or a colored symbol (done) + title, and
	// once done the pre-styled outcome + Continue hint. Built FIRST so its height
	// sizes the line tail, and rendered LAST so it sits at the BOTTOM with the
	// streamed log lines scrolling ABOVE it (recaps/outcomes go below the log).
	var summary strings.Builder
	if t.done {
		if t.err != nil {
			summary.WriteString(theme.ErrorText.Render(theme.SymbolError))
		} else {
			summary.WriteString(theme.SuccessText.Render(theme.SymbolSuccess))
		}
	} else {
		summary.WriteString(theme.Title.Render(t.spin.View()))
	}
	summary.WriteString(" " + theme.Emphasis.Render(t.title))
	if t.cancelling && !t.done {
		summary.WriteString(" " + theme.WarningText.Render("(cancelling...)"))
	}
	if t.done {
		summary.WriteString("\n")
		// Outcome is pre-styled by the caller; render it verbatim.
		summary.WriteString(t.outcome)
		summary.WriteString("\n")
		summary.WriteString(theme.Subtle.Render(t.Help()))
	}
	summaryStr := summary.String()

	// The log tail fills the space above the summary (separator + summary block).
	reserved := strings.Count(summaryStr, "\n") + 2
	visible := height - reserved
	if visible < 1 {
		visible = 1
	}
	lines := t.lines
	if len(lines) > visible {
		lines = lines[len(lines)-visible:]
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString(theme.Text.Width(width).Render(line))
		b.WriteString("\n")
	}
	b.WriteString(theme.Subtle.Render(strings.Repeat("─", 3)))
	b.WriteString("\n")
	b.WriteString(summaryStr)
	return b.String()
}

// RunStreamTask drives a streaming operation behind a stream screen. run emits
// lines via emit (safe to call from any goroutine) and returns a pre-styled
// outcome string plus an error. RunStreamTask always waits for run to return
// before returning (drain semantics), even when the user aborts or the UI
// dies, so callers can rely on run's resources being released.
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
