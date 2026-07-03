package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// newUISession is an injection point for tests (renderless sessions with
// scripted input). Production uses shell.Start.
var newUISession = func(ctx context.Context, cfg shell.Config) *shell.Session {
	return shell.Start(ctx, cfg)
}
