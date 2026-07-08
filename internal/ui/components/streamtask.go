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

// inlineStreamTask is the INLINE (non-altscreen) streaming screen: rather than
// retaining a bounded tail of lines and painting them in the body, it forwards
// each streamed line straight to tea.Println so the line lands in the terminal's
// NATIVE scrollback (colors, native selection preserved). The live View is
// therefore just a compact status line (spinner + title, then the outcome). It
// uses the StreamLineMsg/StreamDoneMsg/StreamResult message types and resolves
// only after done (never on input while running, never automatically).
type inlineStreamTask struct {
	shell.Resolver[StreamResult]
	token      uint64
	title      string
	spin       spinner.Model
	done       bool
	outcome    string
	err        error
	cancel     context.CancelFunc
	cancelling bool
}

func newInlineStreamScreen(title string, token uint64, cancel context.CancelFunc) *inlineStreamTask {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &inlineStreamTask{
		token:  token,
		title:  sanitizeLine(title),
		spin:   sp,
		cancel: cancel,
	}
}

// ReceivesBackgroundMessages keeps lines and completion flowing while buried
// under another screen.
func (t *inlineStreamTask) ReceivesBackgroundMessages() bool { return true }

func (t *inlineStreamTask) Init() tea.Cmd { return t.spin.Tick }

func (t *inlineStreamTask) Title() string { return t.title }

func (t *inlineStreamTask) Help() string {
	if t.done {
		return "enter continue"
	}
	return "esc cancel"
}

func (t *inlineStreamTask) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case StreamLineMsg:
		if msg.Token == t.token && !t.done {
			// Streamed lines carry untrusted runtime data: sanitize at the
			// boundary. Skip blank lines so no empty rows scroll past.
			// NOTHING is stored: the line goes to the native scrollback via
			// tea.Println, which the router batches to the program. Do NOT
			// hard-truncate the line -- that would drop selectable data (see
			// bubbletea issue #959 on long-line wrapping). emit must be
			// called from a SINGLE goroutine so tea.Println ordering is FIFO.
			if line := sanitizeLine(msg.Line); strings.TrimSpace(line) != "" {
				return t, tea.Println(line)
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

func (t *inlineStreamTask) View(width, height int) string {
	// ONLY the compact status line: no log tail (the lines live in the native
	// scrollback), no separator. While running: spinner + title (+ cancelling
	// note); once done: the caller's pre-styled outcome verbatim.
	if t.done {
		return t.outcome
	}
	var b strings.Builder
	b.WriteString(theme.Title.Render(t.spin.View()))
	b.WriteString(" " + theme.Emphasis.Render(t.title))
	if t.cancelling {
		b.WriteString(" " + theme.WarningText.Render("(cancelling...)"))
	}
	return b.String()
}

// RunStreamTaskInline drives a streaming operation behind an INLINE stream
// screen: a goroutine runs run and emits lines, which the screen forwards to
// tea.Println (native scrollback). run emits lines via emit, which MUST be
// called from a single goroutine so the tea.Println ordering stays FIFO.
// RunStreamTaskInline always waits for run to return before returning (drain
// semantics), even when the user aborts or the UI dies, so callers can rely on
// run's resources being released.
func RunStreamTaskInline(ctx context.Context, s *shell.Session, title string, run func(ctx context.Context, emit func(line string)) (outcome string, err error)) error {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	token := streamToken.Add(1)
	scr := newInlineStreamScreen(title, token, cancel)

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
