package components

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// TaskProgressMsg updates the progress line of the running task screen.
type TaskProgressMsg struct {
	Token   uint64
	Message string
}

// TaskDoneMsg completes the task screen.
type TaskDoneMsg struct {
	Token uint64
	Err   error
}

// TaskResult is the resolve payload of a Task screen.
type TaskResult struct {
	Err error
}

var taskToken atomic.Uint64

// Task renders a long-running operation: spinner, title, latest progress
// message, and an Esc-to-cancel affordance. It resolves only when the
// operation reports completion (TaskDoneMsg), never on user input alone.
type Task struct {
	shell.Resolver[TaskResult]
	token      uint64
	title      string
	message    string
	spin       spinner.Model
	cancel     context.CancelFunc
	cancelling bool
	// notifyPushed, set by RunTask, is fired once from Init (i.e. once the screen
	// is on the stack) so the worker can order its completion send after the push.
	notifyPushed func()
	pushOnce     sync.Once
}

func newTaskScreen(title, initialMessage string, token uint64, cancel context.CancelFunc) *Task {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return &Task{
		token:   token,
		title:   sanitizeLine(title),
		message: sanitizeLine(initialMessage),
		spin:    sp,
		cancel:  cancel,
	}
}

// ReceivesBackgroundMessages keeps progress and completion flowing while
// buried under another screen.
func (t *Task) ReceivesBackgroundMessages() bool { return true }

func (t *Task) Init() tea.Cmd {
	if t.notifyPushed != nil {
		t.pushOnce.Do(t.notifyPushed)
	}
	return t.spin.Tick
}

func (t *Task) Title() string { return t.title }

func (t *Task) Help() string { return "esc cancel" }

func (t *Task) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case TaskProgressMsg:
		if msg.Token == t.token && !t.cancelling {
			// Progress messages carry untrusted runtime data (scanned
			// backup filenames): sanitize at the boundary.
			t.message = sanitizeLine(msg.Message)
		}
		return t, nil
	case TaskDoneMsg:
		if msg.Token != t.token {
			return t, nil
		}
		return t, t.Resolve(TaskResult{Err: msg.Err}, nil)
	case spinner.TickMsg:
		var cmd tea.Cmd
		t.spin, cmd = t.spin.Update(msg)
		return t, cmd
	case tea.KeyPressMsg:
		if msg.String() == "esc" && !t.cancelling {
			t.cancelling = true
			t.message = "Cancelling..."
			t.cancel()
		}
		return t, nil
	}
	return t, nil
}

func (t *Task) View(width, height int) string {
	var b strings.Builder
	b.WriteString(theme.Title.Render(t.spin.View()) + " " + theme.Emphasis.Render(t.title))
	b.WriteString("\n\n")
	style := theme.Text
	if t.cancelling {
		style = theme.WarningText
	}
	b.WriteString(style.Width(width).Render(t.message))
	return b.String()
}

// RunTask drives a long-running engine operation with a progress screen.
// The report callback is safe to call from any goroutine. RunTask always
// waits for run to return before returning (drain semantics), even when the
// user aborts or the UI dies, so callers can rely on run's resources being
// released.
func RunTask(ctx context.Context, s *shell.Session, title, initialMessage string, run func(ctx context.Context, report func(message string)) error) error {
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	token := taskToken.Add(1)
	scr := newTaskScreen(title, initialMessage, token, cancel)

	// The task screen must be on the stack before its TaskDoneMsg is processed:
	// the router matches the done message by token, so a done that lands before
	// the screen is pushed finds no match, is dropped, and Ask then blocks forever
	// (the deadlock a fast/instant run can hit under the race detector). Init fires
	// notifyPushed once the screen is pushed, so gate the completion send on it.
	// An early progress message is harmless to drop (cosmetic). If the program is
	// gone before the push, bail without sending so the worker never leaks.
	pushed := make(chan struct{})
	scr.notifyPushed = func() { close(pushed) }

	done := make(chan error, 1)
	go func() {
		err := run(taskCtx, func(message string) {
			s.Send(TaskProgressMsg{Token: token, Message: message})
		})
		done <- err
		select {
		case <-pushed:
			s.Send(TaskDoneMsg{Token: token, Err: err})
		case <-s.Done():
		}
	}()

	res, askErr := shell.Ask(ctx, s, scr)
	cancel()
	runErr := <-done

	if askErr != nil {
		if shell.IsAbort(askErr) {
			// The user aborted the screen; the task context was cancelled
			// and run has drained. Surface the task's own outcome.
			return runErr
		}
		return askErr
	}
	return res.Err
}
