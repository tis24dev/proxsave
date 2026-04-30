// Package main contains the proxsave command entrypoint.
package main

import (
	"context"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/types"
)

func handleSupportIntro(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, state *appRunState) (int, bool) {
	if !args.Support {
		return types.ExitSuccess.Int(), false
	}

	logging.DebugStepBootstrap(bootstrap, "main run", "mode=support")
	meta, continueRun, interrupted := support.RunIntro(ctx, bootstrap)
	if continueRun {
		args.SupportGitHubUser = meta.GitHubUser
		args.SupportIssueID = meta.IssueID
		return types.ExitSuccess.Int(), false
	}

	if interrupted {
		state.finalize(exitCodeInterrupted)
		printFinalSummary(state.finalExitCode)
		return state.finalExitCode, true
	}
	state.finalize(types.ExitGenericError.Int())
	printFinalSummary(state.finalExitCode)
	return state.finalExitCode, true
}
