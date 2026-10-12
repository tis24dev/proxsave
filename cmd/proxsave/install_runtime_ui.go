package main

import (
	"context"

	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// runInstallFinalizationTask keeps a backend failure independently of the stream
// presentation result, which can be nil when the user dismisses a failed task.
func runInstallFinalizationTask(ctx context.Context, session *shell.Session, p *installRuntimePreparation, finish func(context.Context) (string, string)) (streamErr, backendErr error) {
	streamErr = components.RunStreamTask(ctx, session, "Finalizing installation", func(taskCtx context.Context, emit func(string)) (string, error) {
		select {
		case <-session.Done():
			backendErr = shell.ErrClosed
			return "", backendErr
		default:
		}
		p.Replay(emit)
		defer captureRunOutput(p.bootstrap, emit)()
		backendErr = p.Finalize(taskCtx)
		if backendErr != nil {
			return "", backendErr
		}
		permStatus, permMessage := finish(taskCtx)
		return buildInstallOutcomePrompt(p.result.Verification, p.result.Mode == "daemon", permStatus, permMessage, p.result), nil
	})
	return streamErr, backendErr
}
