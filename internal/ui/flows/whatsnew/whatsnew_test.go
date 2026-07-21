package whatsnew

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// testBody is a fixed, multi-line note body. The flow must render it verbatim
// through the Pager without hardcoding or styling note content itself.
const testBody = "ProxSave changed since your last version.\n\n- one thing\n- another thing"

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
	d.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "What's new"},
		d.buf, func(title string) { d.pushes <- title })
	t.Cleanup(func() {
		_ = d.session.Close()
		cancel()
	})
	return d
}

// waitScreen blocks until a screen with the given title is pushed, proving the
// flow presented exactly that screen (Screen 0 identity).
func (d *driver) waitScreen(title string) {
	d.t.Helper()
	deadline := time.After(uitest.Deadline(60 * time.Second))
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

// waitBuffer blocks until the rendered output contains substr, so a test can
// assert on-screen text (e.g. the footer confirm label) deterministically.
func (d *driver) waitBuffer(substr string) {
	d.t.Helper()
	deadline := time.After(uitest.Deadline(60 * time.Second))
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(d.buf.String(), substr) {
			return
		}
		select {
		case <-deadline:
			d.t.Fatalf("timed out waiting for buffer to contain %q; got:\n%s", substr, d.buf.String())
		case <-tick.C:
		}
	}
}

func (d *driver) keys(script string) {
	d.t.Helper()
	for _, msg := range shell.Keys(script) {
		d.session.Send(msg)
	}
}

func runFlow(d *driver, ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- Run(ctx, d.session, testBody) }()
	return ch
}

// TestWhatsNewContinueResolvesNil pins the continue contract: pressing Enter on
// the Screen 0 pager resolves the flow with err == nil, the one outcome plan
// 01-03 gates the flag-write on. The observed screen title must be "What's new"
// and the footer must offer the "continue" confirm label.
func TestWhatsNewContinueResolvesNil(t *testing.T) {
	d := newDriver(t)
	ch := runFlow(d, context.Background())
	d.waitScreen("What's new")
	d.waitBuffer("continue")
	d.keys("enter")
	if err := <-ch; err != nil {
		t.Fatalf("continue (enter) must resolve nil, got %v", err)
	}
}

// TestWhatsNewCancelResolvesError pins the abort contract: Esc and q both
// resolve with a non-nil shell.ErrAborted, type-distinct from continue's nil so
// a reflex dismiss never counts as "seen".
func TestWhatsNewCancelResolvesError(t *testing.T) {
	for _, key := range []string{"esc", "q"} {
		t.Run(key, func(t *testing.T) {
			d := newDriver(t)
			ch := runFlow(d, context.Background())
			d.waitScreen("What's new")
			d.keys(key)
			err := <-ch
			if err == nil {
				t.Fatalf("%s must resolve a non-nil error, got nil", key)
			}
			if !shell.IsAbort(err) {
				t.Fatalf("%s must resolve shell.ErrAborted, got %v", key, err)
			}
		})
	}
}

// TestWhatsNewContextCancelSurfacesError proves the idle-timeout path (wired by
// plan 01-03 via withDashboardIdle) surfaces as a non-nil error while the
// screen is open: a cancelled context makes Run return the context error, so
// the caller will not clear the seen flag.
func TestWhatsNewContextCancelSurfacesError(t *testing.T) {
	d := newDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := runFlow(d, ctx)
	d.waitScreen("What's new")
	cancel()
	err := <-ch
	if err == nil {
		t.Fatal("a cancelled context must surface as a non-nil error")
	}
	if shell.IsAbort(err) {
		t.Fatalf("context cancel must be distinct from the Esc abort error, got %v", err)
	}
}
