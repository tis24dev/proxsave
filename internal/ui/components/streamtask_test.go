package components

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// pumpEnterUntilDone repeatedly sends Enter until RunStreamTaskInline returns.
// Enter on a not-yet-done screen or an empty stack is a no-op, so this is safe
// to spam (idempotent: the first Enter that lands post-done resolves).
func pumpEnterUntilDone(s *shell.Session, done <-chan error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(uitest.Deadline(5 * time.Second))
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			s.Send(shell.KeyMsg("enter"))
		case <-deadline:
			return errors.New("RunStreamTaskInline did not return")
		}
	}
}

// waitForBuffer polls buf until it contains want or the deadline expires.
func waitForBuffer(t *testing.T, buf *shell.SyncBuffer, want string) {
	t.Helper()
	deadline := time.After(uitest.Deadline(5 * time.Second))
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(buf.String(), want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("buffer never contained %q; got:\n%s", want, buf.String())
		case <-tick.C:
		}
	}
}

// runInlineDriver runs RunStreamTaskInline in an inline output session, waits
// for both emitted lines and the outcome to reach the buffer (proving
// tea.Println), then pumps Enter until it returns.
func runInlineDriver(t *testing.T, outcome string, taskErr error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	buf := &shell.SyncBuffer{}
	s := shell.StartInlineForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, buf)
	t.Cleanup(func() {
		_ = s.Close()
		cancel()
	})

	done := make(chan error, 1)
	go func() {
		done <- RunStreamTaskInline(ctx, s, "Running backup", func(ctx context.Context, emit func(string)) (string, error) {
			emit("step 1")
			emit("step 2")
			return outcome, taskErr
		})
	}()

	// tea.Println lands the emitted lines in the native scrollback, i.e. the
	// output buffer (a no-op in altscreen): assert both are there.
	waitForBuffer(t, buf, "step 1")
	waitForBuffer(t, buf, "step 2")
	// Outcome + the Continue hint appear once the task completes.
	waitForBuffer(t, buf, outcome)
	waitForBuffer(t, buf, "enter continue")

	return pumpEnterUntilDone(s, done)
}

func TestRunStreamTaskInlineDriverSuccess(t *testing.T) {
	if err := runInlineDriver(t, "BACKUP OK", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunStreamTaskInlineDriverPropagatesError(t *testing.T) {
	boom := errors.New("backup failed")
	if err := runInlineDriver(t, "BACKUP FAILED", boom); !errors.Is(err, boom) {
		t.Fatalf("expected task error, got %v", err)
	}
}

func TestInlineStreamScreenIgnoresForeignToken(t *testing.T) {
	scr := newInlineStreamScreen("Running backup", 7, func() {})
	// A line for a different token must not resolve or complete the screen and
	// must not emit a Println command.
	updated, cmd := scr.Update(StreamLineMsg{Token: 99, Line: "foreign"})
	scr = updated.(*inlineStreamTask)
	if cmd != nil {
		t.Fatal("foreign-token line should not emit a command")
	}
	if scr.done {
		t.Fatal("foreign-token line should not complete the screen")
	}
	// A matching-token, non-blank line emits a (Println) command.
	if _, cmd := scr.Update(StreamLineMsg{Token: 7, Line: "real line"}); cmd == nil {
		t.Fatal("matching-token line should emit a Println command")
	}
}

func TestInlineStreamScreenDoneAndResolve(t *testing.T) {
	boom := errors.New("boom")
	scr := newInlineStreamScreen("Running backup", 3, func() {})

	// Enter before done does not resolve.
	if _, cmd := scr.Update(shell.KeyMsg("enter")); cmd != nil {
		t.Fatal("enter before done should not resolve")
	}

	// Done sets outcome + error + done.
	updated, _ := scr.Update(StreamDoneMsg{Token: 3, Outcome: "DONE", Err: boom})
	scr = updated.(*inlineStreamTask)
	if !scr.done || scr.outcome != "DONE" || !errors.Is(scr.err, boom) {
		t.Fatalf("done not applied: done=%v outcome=%q err=%v", scr.done, scr.outcome, scr.err)
	}

	// Bind a resolver so Resolve() has somewhere to deliver, then Enter
	// post-done resolves carrying t.err.
	var got StreamResult
	var gotErr error
	scr.Bind(func(v StreamResult, err error) { got, gotErr = v, err })
	_, cmd := scr.Update(shell.KeyMsg("enter"))
	if cmd == nil {
		t.Fatal("enter after done should resolve (non-nil command)")
	}
	cmd() // fire the resolve
	if gotErr != nil {
		t.Fatalf("resolve returned unexpected error: %v", gotErr)
	}
	if !errors.Is(got.Err, boom) {
		t.Fatalf("resolve should carry t.err, got %v", got.Err)
	}
}

func TestInlineStreamScreenEscCancels(t *testing.T) {
	cancelled := false
	scr := newInlineStreamScreen("Running backup", 1, func() { cancelled = true })
	scr.Update(shell.KeyMsg("esc"))
	if !scr.cancelling {
		t.Fatal("esc should set cancelling")
	}
	if !cancelled {
		t.Fatal("esc should call cancel")
	}
}
