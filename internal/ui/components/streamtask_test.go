package components

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// pumpEnterUntilDone repeatedly sends Enter until RunStreamTask returns. Enter
// on a not-yet-done screen or an empty stack is a no-op, so this is safe to
// spam (idempotent: the first Enter that lands post-done resolves).
func pumpEnterUntilDone(s *shell.Session, done <-chan error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
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

func TestStreamScreenAppendsAndIgnoresForeignToken(t *testing.T) {
	scr := newStreamScreen("Finalizing", 1, func() {})
	scr.Update(StreamLineMsg{Token: 1, Line: "line-one"})   //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: "line-two"})   //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: "line-three"}) //nolint:errcheck
	view := scr.View(80, 40)
	for _, want := range []string{"line-one", "line-two", "line-three"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q\n%s", want, view)
		}
	}
	// Foreign-token lines are ignored.
	scr.Update(StreamLineMsg{Token: 2, Line: "intruder"}) //nolint:errcheck
	if strings.Contains(scr.View(80, 40), "intruder") {
		t.Error("foreign-token line must be ignored")
	}
}

func TestStreamScreenDoneShowsOutcomeAndHint(t *testing.T) {
	scr := newStreamScreen("Finalizing", 1, func() {})
	scr.Update(StreamLineMsg{Token: 1, Line: "working"}) //nolint:errcheck

	// A foreign-token done must not complete the screen.
	scr.Update(StreamDoneMsg{Token: 2, Outcome: "nope", Err: nil}) //nolint:errcheck
	if scr.done {
		t.Fatal("foreign-token done must not mark the screen done")
	}

	scr.Update(StreamDoneMsg{Token: 1, Outcome: "OUTCOME-XYZ", Err: nil}) //nolint:errcheck
	view := scr.View(80, 40)
	if !strings.Contains(view, "OUTCOME-XYZ") {
		t.Errorf("view missing outcome\n%s", view)
	}
	if !strings.Contains(view, "enter continue") {
		t.Errorf("view missing continue hint\n%s", view)
	}
	if !strings.Contains(view, theme.SymbolSuccess) {
		t.Errorf("view missing success symbol\n%s", view)
	}
}

func TestStreamScreenDoneErrorShowsErrorSymbol(t *testing.T) {
	scr := newStreamScreen("Finalizing", 1, func() {})
	scr.Update(StreamDoneMsg{Token: 1, Outcome: "bad", Err: errors.New("boom")}) //nolint:errcheck
	view := scr.View(80, 40)
	if !strings.Contains(view, theme.SymbolError) {
		t.Errorf("view missing error symbol\n%s", view)
	}
}

func TestStreamScreenEnterResolvesOnlyAfterDone(t *testing.T) {
	scr := newStreamScreen("Finalizing", 1, func() {})
	var got StreamResult
	resolved := false
	scr.Bind(func(v StreamResult, err error) {
		resolved = true
		got = v
	})

	// Enter before done must NOT resolve.
	scr.Update(shell.KeyMsg("enter")) //nolint:errcheck
	if resolved {
		t.Fatal("enter before done must not resolve")
	}

	boom := errors.New("finalize failed")
	scr.Update(StreamDoneMsg{Token: 1, Outcome: "done", Err: boom}) //nolint:errcheck
	if resolved {
		t.Fatal("done must not auto-resolve")
	}

	// Enter after done resolves, carrying the stored error.
	scr.Update(shell.KeyMsg("enter")) //nolint:errcheck
	if !resolved {
		t.Fatal("enter after done must resolve")
	}
	if !errors.Is(got.Err, boom) {
		t.Fatalf("expected resolution with stored error, got %+v", got)
	}
}

func TestStreamScreenBoundedRingDropsOldest(t *testing.T) {
	scr := newStreamScreen("Finalizing", 1, func() {})
	total := streamLineCap + 50
	for i := 0; i < total; i++ {
		scr.Update(StreamLineMsg{Token: 1, Line: fmt.Sprintf("line-%d", i)}) //nolint:errcheck
	}
	if len(scr.lines) != streamLineCap {
		t.Fatalf("ring not bounded: got %d lines, want %d", len(scr.lines), streamLineCap)
	}
	// Oldest line dropped, newest retained.
	if scr.lines[0] != fmt.Sprintf("line-%d", total-streamLineCap) {
		t.Errorf("oldest line not dropped: front is %q", scr.lines[0])
	}
	if scr.lines[len(scr.lines)-1] != fmt.Sprintf("line-%d", total-1) {
		t.Errorf("newest line not retained: back is %q", scr.lines[len(scr.lines)-1])
	}
}

func TestStreamScreenSkipsBlankLines(t *testing.T) {
	scr := newStreamScreen("Finalizing", 1, func() {})
	scr.Update(StreamLineMsg{Token: 1, Line: "real-line"}) //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: ""})          //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: "   "})       //nolint:errcheck
	scr.Update(StreamLineMsg{Token: 1, Line: "\t"})        //nolint:errcheck
	if len(scr.lines) != 1 {
		t.Fatalf("blank lines were appended: got %d lines, want 1", len(scr.lines))
	}
	if !strings.Contains(scr.View(80, 40), "real-line") {
		t.Fatalf("real line missing from view")
	}
}

func TestStreamScreenEscShowsCancelling(t *testing.T) {
	cancelled := false
	scr := newStreamScreen("Finalizing", 1, func() { cancelled = true })
	scr.Update(shell.KeyMsg("esc")) //nolint:errcheck
	if !cancelled {
		t.Fatal("esc must cancel the task context")
	}
	if !strings.Contains(scr.View(80, 40), "cancelling") {
		t.Fatalf("esc must show a cancelling indicator:\n%s", scr.View(80, 40))
	}
}

func TestRunStreamTaskDriverSuccess(t *testing.T) {
	s := testSession(t)
	ran := false
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		done <- RunStreamTask(context.Background(), s, "Finalizing", func(ctx context.Context, emit func(string)) (string, error) {
			emit("step 1")
			emit("step 2")
			ran = true
			close(started)
			return "OUTCOME", nil
		})
	}()
	<-started
	// Drive Enter until the screen (now done) resolves and RunStreamTask
	// returns. Enter on a not-yet-done or empty stack is a no-op.
	if err := pumpEnterUntilDone(s, done); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("run function did not execute")
	}
}

func TestRunStreamTaskDriverPropagatesError(t *testing.T) {
	s := testSession(t)
	boom := errors.New("finalize failed")
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		done <- RunStreamTask(context.Background(), s, "Finalizing", func(ctx context.Context, emit func(string)) (string, error) {
			close(started)
			return "OUTCOME", boom
		})
	}()
	<-started
	err := pumpEnterUntilDone(s, done)
	if !errors.Is(err, boom) {
		t.Fatalf("expected task error, got %v", err)
	}
}
