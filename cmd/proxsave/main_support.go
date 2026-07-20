// Package main contains the proxsave command entrypoint.
package main

import (
	"context"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/types"
)

// emitSupportEmail is the single "send the support email with the attached log"
// action, shared by the streamed dashboard run (which calls it inside the
// viewport capture so the outcome is visible) and the deferred CLI sender. The
// leading Step announces it in both places; SendEmail's own Info/Warning lines
// report the actual hand-off or the skip.
var emitSupportEmail = func(ctx context.Context, cfg *config.Config, logger *logging.Logger, proxmoxType types.ProxmoxType, stats *orchestrator.BackupStats, meta support.Meta) {
	logging.Step("Support mode - sending support email with attached log")
	support.SendEmail(ctx, cfg, logger, proxmoxType, stats, meta, buildSignature())
}

func handleSupportIntro(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, state *appRunState) (int, bool) {
	if !args.Support {
		return types.ExitSuccess.Int(), false
	}

	if args.SupportMetaProvided {
		// The dashboard already collected consent + GitHub metadata graphically; skip the
		// stdin RunIntro (it would prompt over the in-graphics run). args.Support* are set.
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=support (meta provided)")
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
