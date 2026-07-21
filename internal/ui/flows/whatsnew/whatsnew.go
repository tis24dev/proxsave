// Package whatsnew implements Screen 0: the one-shot "what's new" screen shown
// once before the dashboard menu on a bare interactive launch when the gate
// (plan 01-01) reports the current version's notes as unseen. It is a thin
// presentational flow only: it renders a caller-supplied body through the
// existing components.Pager and returns the resolution error unchanged. It
// decides nothing about the seen flag; the caller (plan 01-03) gates the
// flag-write on Run returning nil, so a reflex Esc or an idle-timeout context
// never counts as "seen".
package whatsnew

import (
	"context"

	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Run presents body as a scrollable "What's new" pager and blocks until the
// user resolves it. It returns the resolution error unchanged: Enter resolves
// nil (continue), Esc or q resolves shell.ErrAborted, and a cancelled context
// surfaces its own error. These three outcomes stay type-distinct so the caller
// can write the seen flag only when err == nil. The body is rendered verbatim
// by the Pager (which sanitizes and wraps it); this flow adds no styling and
// keeps the Pager's default abortErr so Esc is a distinct non-nil outcome.
func Run(ctx context.Context, session *shell.Session, body string) error {
	_, err := shell.Ask(ctx, session, components.NewPager(
		"What's new", body,
		components.WithPagerConfirmLabel("continue"),
	))
	return err
}
