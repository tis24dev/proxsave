// Package whatsnew implements Screen 0: the one-shot "what's new" screen shown
// once before the dashboard menu on a bare interactive launch when the gate
// (plan 01-01) reports the current version's notes as unseen. It is a thin
// presentational flow only: it renders a caller-supplied body through the
// existing components.Pager and returns the resolution error unchanged. It
// decides nothing about the seen flag; the caller (plan 01-03) gates the
// flag-write on the screen having been shown to a person, so an idle-timeout
// context never counts as "seen".
package whatsnew

import (
	"context"

	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Run presents body as a scrollable "What's new" pager and blocks until the
// user resolves it. It returns the resolution error unchanged: Enter resolves
// nil (continue) and a cancelled context surfaces its own error. Ctrl+C is the
// emergency exit the router owns above every screen, and it surfaces as
// shell.ErrClosed. The body is rendered verbatim by the Pager (which sanitizes
// and wraps it); this flow adds no styling.
//
// WithPagerNoAbort takes esc and q away, so ENTER IS THE ONLY WAY OUT. The
// screen exists to be read, and a second exit key said nothing the first did
// not: since the caller marks the notes seen however a person closed the screen,
// esc and enter had become the same action wearing two names, one of them
// labelled "cancel" over a screen with nothing to cancel.
func Run(ctx context.Context, session *shell.Session, body string) error {
	_, err := shell.Ask(ctx, session, components.NewPager(
		"What's new", body,
		components.WithPagerConfirmLabel("continue"),
		components.WithPagerNoAbort(),
	))
	return err
}
