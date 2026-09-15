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

// TestWhatsNewIgnoresEveryExitKeyButEnter pins the one-way-out contract. Esc and q
// close every other pager in the product; here they must do NOTHING, and the proof
// is that enter still resolves after them - a screen that had already resolved
// could not.
//
// The reason they are gone is not that dismissing was dangerous: the caller marks
// the notes seen however the screen is closed, so esc and enter had the same
// effect under two names, and the footer called one of them "cancel" over a screen
// with nothing to cancel. Ctrl+C is untouched and stays the emergency exit; it is
// the router's, above every screen, and never reaches this flow.
func TestWhatsNewIgnoresEveryExitKeyButEnter(t *testing.T) {
	for _, key := range []string{"esc", "q"} {
		t.Run(key, func(t *testing.T) {
			d := newDriver(t)
			ch := runFlow(d, context.Background())
			d.waitScreen("What's new")
			d.waitBuffer("continue")

			d.keys(key)
			select {
			case err := <-ch:
				t.Fatalf("%s must not resolve Screen 0, got %v", key, err)
			case <-time.After(200 * time.Millisecond):
			}

			d.keys("enter")
			if err := <-ch; err != nil {
				t.Fatalf("enter must still resolve nil after %s was ignored, got %v", key, err)
			}
		})
	}
}

// TestWhatsNewFooterOffersOnlyEnter is the other half: the key is gone AND the
// screen stops advertising it. A footer that still read "esc cancel" would send a
// person pressing a key that does nothing, which is the failure this change was
// made to remove, only reversed.
func TestWhatsNewFooterOffersOnlyEnter(t *testing.T) {
	d := newDriver(t)
	ch := runFlow(d, context.Background())
	d.waitScreen("What's new")
	d.waitBuffer("continue")

	if got := d.buf.String(); strings.Contains(got, "esc") {
		t.Fatalf("Screen 0 must not offer esc in its footer; got %q", got)
	}

	d.keys("enter")
	if err := <-ch; err != nil {
		t.Fatalf("continue (enter) must resolve nil, got %v", err)
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
		t.Fatalf("context cancel must be distinct from a pager abort, got %v", err)
	}
}
