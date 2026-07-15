package components

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// pumpEnterUntilDone repeatedly sends Enter until RunStreamTask returns. Enter
// on a not-yet-done screen or an empty stack is a no-op, so this is safe to spam
// (idempotent: the first Enter that lands post-done resolves).
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
			return errors.New("RunStreamTask did not return")
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

// TestStreamScreenRetainsAllLines drives newStreamScreen with N StreamLineMsg
// and asserts every line is present in the rendered viewport panel (the ring
// holds the whole run; the panel is tall enough to show them all here).
func TestStreamScreenRetainsAllLines(t *testing.T) {
	scr := newStreamScreen("Running backup", 5, func() {})
	const n = 20
	for i := 0; i < n; i++ {
		updated, _ := scr.Update(StreamLineMsg{Token: 5, Line: fmt.Sprintf("line %d", i)})
		scr = updated.(*StreamTask)
	}
	if len(scr.lines) != n {
		t.Fatalf("ring should hold %d lines, got %d", n, len(scr.lines))
	}
	out := scr.View(80, 40)
	for i := 0; i < n; i++ {
		if !strings.Contains(out, fmt.Sprintf("line %d", i)) {
			t.Fatalf("view missing %q\n%s", fmt.Sprintf("line %d", i), out)
		}
	}
}

// TestStreamScreenKeepsANSIInRing proves a colored line keeps its ANSI SGR in
// the ring (the panel renders real colors), while dangerous control chars are
// stripped.
func TestStreamScreenKeepsANSIInRing(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	colored := "\x1b[32mgreen\x1b[0m\x07 tail" // SGR kept, BEL (0x07) dropped
	updated, _ := scr.Update(StreamLineMsg{Token: 1, Line: colored})
	scr = updated.(*StreamTask)
	if len(scr.lines) != 1 {
		t.Fatalf("expected 1 retained line, got %d", len(scr.lines))
	}
	got := scr.lines[0]
	if !strings.Contains(got, "\x1b[32m") || !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("ANSI SGR must survive in the ring, got %q", got)
	}
	if strings.Contains(got, "\x07") {
		t.Fatalf("control char must be stripped, got %q", got)
	}
	if !strings.Contains(got, "green") || !strings.Contains(got, "tail") {
		t.Fatalf("text must survive, got %q", got)
	}
}

// TestStreamScreenIgnoresForeignToken: a line for a different token must not be
// retained or complete the screen.
func TestStreamScreenIgnoresForeignToken(t *testing.T) {
	scr := newStreamScreen("Running backup", 7, func() {})
	updated, _ := scr.Update(StreamLineMsg{Token: 99, Line: "foreign"})
	scr = updated.(*StreamTask)
	if len(scr.lines) != 0 {
		t.Fatalf("foreign-token line must not be retained, got %v", scr.lines)
	}
	if scr.done {
		t.Fatal("foreign-token line should not complete the screen")
	}
	// A matching-token, non-blank line is retained.
	updated, _ = scr.Update(StreamLineMsg{Token: 7, Line: "real line"})
	scr = updated.(*StreamTask)
	if len(scr.lines) != 1 {
		t.Fatalf("matching-token line must be retained, got %v", scr.lines)
	}
}

// TestStreamScreenRingCap: the ring is bounded and drops the oldest lines,
// flagging the truncation.
func TestStreamScreenRingCap(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	for i := 0; i < streamLineCap+50; i++ {
		updated, _ := scr.Update(StreamLineMsg{Token: 1, Line: fmt.Sprintf("l%d", i)})
		scr = updated.(*StreamTask)
	}
	if len(scr.lines) != streamLineCap {
		t.Fatalf("ring must be capped at %d, got %d", streamLineCap, len(scr.lines))
	}
	if !scr.dropped {
		t.Fatal("dropped flag must be set once the ring sheds lines")
	}
	if scr.lines[0] == "l0" {
		t.Fatal("oldest line must have been dropped")
	}
}

// TestStreamScreenDoneAndResolve: Enter pre-done does not resolve; Done stores
// the outcome + error and shows the hint; Enter post-done resolves carrying
// t.err.
func TestStreamScreenDoneAndResolve(t *testing.T) {
	boom := errors.New("boom")
	scr := newStreamScreen("Running backup", 3, func() {})

	// Enter before done does not resolve.
	if _, cmd := scr.Update(shell.KeyMsg("enter")); cmd != nil {
		t.Fatal("enter before done should not resolve")
	}

	// Done sets outcome + error + done.
	updated, _ := scr.Update(StreamDoneMsg{Token: 3, Outcome: "BACKUP DONE", Err: boom})
	scr = updated.(*StreamTask)
	if !scr.done || scr.outcome != "BACKUP DONE" || !errors.Is(scr.err, boom) {
		t.Fatalf("done not applied: done=%v outcome=%q err=%v", scr.done, scr.outcome, scr.err)
	}

	// The view shows the outcome and the Continue hint.
	out := scr.View(80, 20)
	if !strings.Contains(out, "BACKUP DONE") {
		t.Fatalf("view must show the outcome, got:\n%s", out)
	}
	if !strings.Contains(scr.Help(), "enter continue") {
		t.Fatalf("done help must offer continue, got %q", scr.Help())
	}

	// Bind a resolver, then Enter post-done resolves carrying t.err.
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

// TestStreamScreenEscCancels: Esc while running sets cancelling and calls cancel.
func TestStreamScreenEscCancels(t *testing.T) {
	cancelled := false
	scr := newStreamScreen("Running backup", 1, func() { cancelled = true })
	scr.Update(shell.KeyMsg("esc"))
	if !scr.cancelling {
		t.Fatal("esc should set cancelling")
	}
	if !cancelled {
		t.Fatal("esc should call cancel")
	}
}

func TestStreamScreenCopyLog(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	scr.Update(StreamLineMsg{Token: 1, Line: "\x1b[32m[a] INFO\x1b[0m first line"}) //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: "[b] STEP second line"})               //nolint:errcheck

	_, cmd := scr.Update(shell.KeyMsg("c"))
	if !scr.copied {
		t.Fatal("'c' should set the copied confirmation")
	}
	if cmd == nil {
		t.Fatal("'c' should return a SetClipboard command")
	}
	// tea.SetClipboard's message is a string type; %s yields the copied text.
	got := fmt.Sprintf("%s", cmd())
	if !strings.Contains(got, "[a] INFO first line") || !strings.Contains(got, "[b] STEP second line") {
		t.Fatalf("clipboard missing log lines: %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("clipboard must be ANSI-stripped plain text: %q", got)
	}
	if !strings.Contains(scr.View(80, 20), "log copied") {
		t.Fatal("view should show the 'log copied' confirmation")
	}

	// Any other key clears the transient confirmation.
	scr.Update(shell.KeyMsg("down")) //nolint:errcheck
	if scr.copied {
		t.Fatal("copied confirmation should clear on the next keypress")
	}
}

func TestStreamScreenKeepsBlankLines(t *testing.T) {
	scr := newStreamScreen("Running", 1, func() {})
	scr.Update(StreamLineMsg{Token: 1, Line: "section one"}) //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: ""})            //nolint:errcheck // section spacer
	scr.Update(StreamLineMsg{Token: 1, Line: "section two"}) //nolint:errcheck
	if len(scr.lines) != 3 {
		t.Fatalf("blank spacer dropped: got %d lines, want 3 (%q)", len(scr.lines), scr.lines)
	}
	if scr.lines[1] != "" {
		t.Fatalf("middle line should be the blank spacer, got %q", scr.lines[1])
	}
}

// TestStreamScreenScrollUpStopsFollow: scrolling up stops auto-follow so a
// manual review is not yanked back to the bottom by the next line.
func TestStreamScreenScrollUpStopsFollow(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	// Fill enough lines to make the panel scrollable, then size the viewport.
	for i := 0; i < 100; i++ {
		updated, _ := scr.Update(StreamLineMsg{Token: 1, Line: fmt.Sprintf("l%d", i)})
		scr = updated.(*StreamTask)
	}
	scr.View(80, 10) // sets the viewport height so scrolling is meaningful
	if !scr.follow {
		t.Fatal("should start following")
	}
	updated, _ := scr.Update(shell.KeyMsg("up"))
	scr = updated.(*StreamTask)
	if scr.follow {
		t.Fatal("scrolling up must stop auto-follow")
	}
	// End re-enables follow.
	updated, _ = scr.Update(shell.KeyMsg("end"))
	scr = updated.(*StreamTask)
	if !scr.follow {
		t.Fatal("end must re-enable auto-follow")
	}
}

// TestStreamScreenWheelTogglesFollow covers the mouse-wheel branch (distinct
// from the keyboard scroll above): a wheel-up leaves the bottom and stops
// auto-follow; a wheel-down back to the bottom resumes it.
func TestStreamScreenWheelTogglesFollow(t *testing.T) {
	scr := newStreamScreen("Running backup", 1, func() {})
	for i := 0; i < 100; i++ {
		updated, _ := scr.Update(StreamLineMsg{Token: 1, Line: fmt.Sprintf("l%d", i)})
		scr = updated.(*StreamTask)
	}
	scr.View(80, 10)
	if !scr.follow {
		t.Fatal("should start following")
	}
	updated, _ := scr.Update(wheel(false)) // wheel up
	scr = updated.(*StreamTask)
	if scr.follow {
		t.Fatal("wheel up must stop auto-follow")
	}
	for i := 0; i < 20; i++ { // wheel down back to the bottom
		updated, _ = scr.Update(wheel(true))
		scr = updated.(*StreamTask)
	}
	if !scr.follow {
		t.Fatal("wheel down to the bottom must resume auto-follow")
	}
}

// runDriver runs RunStreamTask in an output session, waits for the emitted
// lines and outcome to reach the buffer (proving the contained panel renders
// them), then pumps Enter until it returns.
func runDriver(t *testing.T, outcome string, taskErr error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	buf := &shell.SyncBuffer{}
	s := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, buf)
	t.Cleanup(func() {
		_ = s.Close()
		cancel()
	})

	done := make(chan error, 1)
	go func() {
		done <- RunStreamTask(ctx, s, "Running backup", func(ctx context.Context, emit func(string)) (string, error) {
			emit("step 1")
			emit("step 2")
			return outcome, taskErr
		})
	}()

	// The lines render inside the contained panel (altscreen), so they reach
	// the output buffer.
	waitForBuffer(t, buf, "step 1")
	waitForBuffer(t, buf, "step 2")
	// Outcome appears once the task completes.
	waitForBuffer(t, buf, outcome)
	waitForBuffer(t, buf, "enter continue")

	return pumpEnterUntilDone(s, done)
}

func TestRunStreamTaskDriverSuccess(t *testing.T) {
	if err := runDriver(t, "BACKUP OK", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunStreamTaskDriverPropagatesError(t *testing.T) {
	boom := errors.New("backup failed")
	if err := runDriver(t, "BACKUP FAILED", boom); !errors.Is(err, boom) {
		t.Fatalf("expected task error, got %v", err)
	}
}
