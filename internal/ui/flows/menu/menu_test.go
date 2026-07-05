package menu

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

type driver struct {
	t       *testing.T
	buf     *shell.SyncBuffer
	pushes  chan string
	session *shell.Session
	cancel  context.CancelFunc
}

func newDriver(t *testing.T) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &driver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan string, 16), cancel: cancel}
	d.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		d.buf, func(title string) { d.pushes <- title })
	t.Cleanup(func() {
		_ = d.session.Close()
		cancel()
	})
	return d
}

func (d *driver) waitScreen(title string) {
	d.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-d.pushes:
			if got == title {
				return
			}
		case <-deadline:
			d.t.Fatalf("timed out waiting for screen %q", title)
		}
	}
}

func (d *driver) keys(script string) {
	d.t.Helper()
	for _, msg := range shell.Keys(script) {
		d.session.Send(msg)
	}
}

func run(d *driver, ctx context.Context) <-chan struct {
	action Action
	err    error
} {
	ch := make(chan struct {
		action Action
		err    error
	}, 1)
	go func() {
		action, err := Run(ctx, d.session, DaemonStateOnCron)
		ch <- struct {
			action Action
			err    error
		}{action, err}
	}()
	return ch
}

// TestMenuRowOrder pins the row order the dashboard dispatch tests (and the
// docs) rely on: backup first, exit last, with the diagnostics group after
// Reconfigure. Down-navigation must skip the separator, so N downs lands on the
// N-th SELECTABLE row (the divider is invisible to the cursor).
func TestMenuRowOrder(t *testing.T) {
	expected := []Action{
		ActionBackup, ActionRestore, ActionDecrypt, ActionNewKey, ActionReconfigure,
		ActionCheckTelegram, ActionCheckHealthcheck, ActionPostInstallCheck,
		ActionDaemonSetup, ActionDaemonStatus, ActionExit,
	}
	for i, want := range expected {
		d := newDriver(t)
		ch := run(d, context.Background())
		d.waitScreen("Dashboard")
		for j := 0; j < i; j++ {
			d.keys("down")
		}
		d.keys("enter")
		res := <-ch
		if res.err != nil || res.action != want {
			t.Fatalf("row %d: action=%v err=%v, want %v", i, res.action, res.err, want)
		}
	}
}

// TestMenuNeverFallsThroughToBackup: esc, ctrl+c, and a dying UI must all
// resolve ActionExit with no error; a failed menu can never trigger a run.
func TestMenuNeverFallsThroughToBackup(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drive func(d *driver)
	}{
		{"esc", func(d *driver) { d.keys("esc") }},
		{"ctrl+c", func(d *driver) { d.keys("ctrl+c") }},
		{"ui death", func(d *driver) { d.cancel() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDriver(t)
			ch := run(d, context.Background())
			d.waitScreen("Dashboard")
			tc.drive(d)
			res := <-ch
			if res.err != nil || res.action != ActionExit {
				t.Fatalf("expected clean ActionExit, got action=%v err=%v", res.action, res.err)
			}
		})
	}
}

// TestMenuCtxTimeoutSurfacesError: the idle-timeout context must surface as
// an error (the caller prints guidance and exits without action).
func TestMenuCtxTimeoutSurfacesError(t *testing.T) {
	d := newDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ch := run(d, ctx)
	d.waitScreen("Dashboard")
	res := <-ch
	if res.err == nil {
		t.Fatal("expected a context error from the idle timeout")
	}
	if res.action != ActionExit {
		t.Fatalf("timeout must resolve ActionExit, got %v", res.action)
	}
}
