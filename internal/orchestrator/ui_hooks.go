package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// uiSessionHandoff, when set (by cmd), offers an already-running session
// (the dashboard's) to adopt instead of starting a new program: the frame
// never leaves the screen between the menu and the chosen flow. The hook
// must consume the offer (return nil on the second call).
var uiSessionHandoff func(cfg shell.Config) *shell.Session

// SetUISessionHandoff installs the dashboard session handoff hook.
func SetUISessionHandoff(f func(cfg shell.Config) *shell.Session) {
	uiSessionHandoff = f
}

// newUISession is an injection point for tests (renderless sessions with
// scripted input). Production adopts a handed-off dashboard session when one
// is pending, otherwise starts a fresh program.
var newUISession = func(ctx context.Context, cfg shell.Config) *shell.Session {
	if uiSessionHandoff != nil {
		if s := uiSessionHandoff(cfg); s != nil {
			return s
		}
	}
	return shell.Start(ctx, cfg)
}
