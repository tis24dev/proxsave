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

func testSession(t *testing.T) *shell.Session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := shell.StartForTest(ctx, shell.Config{AppName: "ProxSave", UseColor: true})
	t.Cleanup(func() {
		_ = s.Close()
		cancel()
	})
	return s
}

func TestRunTaskSuccess(t *testing.T) {
	s := testSession(t)
	var reported []string
	err := RunTask(context.Background(), s, "Scanning", "Starting...", func(ctx context.Context, report func(string)) error {
		report("step 1")
		report("step 2")
		reported = append(reported, "ran")
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reported) != 1 {
		t.Fatal("run function did not execute")
	}
}

func TestRunTaskPropagatesError(t *testing.T) {
	s := testSession(t)
	boom := errors.New("scan failed")
	err := RunTask(context.Background(), s, "Scanning", "Starting...", func(ctx context.Context, report func(string)) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected task error, got %v", err)
	}
}

func TestRunTaskContextCancelDrains(t *testing.T) {
	s := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	drained := false
	done := make(chan error, 1)
	go func() {
		done <- RunTask(ctx, s, "Scanning", "Starting...", func(taskCtx context.Context, report func(string)) error {
			close(started)
			<-taskCtx.Done()
			// Simulate cleanup work after cancellation: RunTask must wait
			// for this to finish (drain semantics).
			time.Sleep(50 * time.Millisecond)
			drained = true
			return taskCtx.Err()
		})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if !drained {
			t.Fatal("RunTask returned before the task drained")
		}
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("RunTask did not return")
	}
}

func TestRunTaskUserCancelViaEsc(t *testing.T) {
	s := testSession(t)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RunTask(context.Background(), s, "Scanning", "Starting...", func(taskCtx context.Context, report func(string)) error {
			close(started)
			<-taskCtx.Done()
			return taskCtx.Err()
		})
	}()
	<-started
	// The push may not have been processed yet; esc on an empty stack is
	// dropped, so keep sending it until the task reacts (idempotent: the
	// first esc that lands cancels, later ones are no-ops).
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(uitest.Deadline(5 * time.Second))
	for {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled after user cancel, got %v", err)
			}
			return
		case <-ticker.C:
			s.Send(shell.KeyMsg("esc"))
		case <-deadline:
			t.Fatal("RunTask did not return after user cancel")
		}
	}
}

func TestTaskScreenProgressAndDone(t *testing.T) {
	scr := newTaskScreen("Scanning", "Starting...", 1, func() {})
	var got TaskResult
	resolved := false
	scr.Bind(func(v TaskResult, err error) {
		resolved = true
		got = v
	})
	scr.Update(TaskProgressMsg{Token: 1, Message: "50% done"}) //nolint:errcheck
	if !strings.Contains(scr.View(80, 20), "50% done") {
		t.Error("progress message not rendered")
	}
	// Messages for other tokens are ignored.
	scr.Update(TaskProgressMsg{Token: 2, Message: "other"}) //nolint:errcheck
	if strings.Contains(scr.View(80, 20), "other") {
		t.Error("foreign progress message must be ignored")
	}
	scr.Update(TaskDoneMsg{Token: 2, Err: nil}) //nolint:errcheck
	if resolved {
		t.Fatal("foreign done message must not resolve")
	}
	boom := errors.New("x")
	scr.Update(TaskDoneMsg{Token: 1, Err: boom}) //nolint:errcheck
	if !resolved || !errors.Is(got.Err, boom) {
		t.Fatalf("expected resolution with task error, got %+v", got)
	}
}
