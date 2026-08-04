package main

import (
	"context"
	"errors"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// optionalInstallStepAborts is the SINGLE rule deciding whether a failed OPTIONAL install
// step (post-install audit, Telegram pairing, healthcheck setup, healthcheck self params)
// stops the install or is merely skipped. Both drivers ask it; neither re-derives it.
//
// The rule needs TWO discriminators, and each driver used to know only its own:
//
//   - ctx.Err() != nil -- the run context is WithCancel and is cancelled only by the
//     signal handler (setupRunContext), so it is non-nil after a SIGINT/SIGTERM. This is
//     what the stdin CLI sees, where Ctrl+C at a cooked-mode prompt raises a real signal.
//   - shell.ErrClosed -- the Charm program terminated. This is what the TUI sees: in the
//     alternate screen the terminal is in raw mode, so Ctrl+C arrives as a KEY that ends
//     the program rather than as a signal, and Ask resolves to ErrClosed (pinned by
//     TestAskReturnsErrClosedOnCtrlC). It is a DIFFERENT error from context.Canceled.
//
// Knowing only one of the two is what made the TUI continue past a user's Ctrl+C: three
// of its optional steps logged "failed (non-blocking)" and carried on to a finalization
// that then ran on a context RunStreamTask had already cancelled, installing no scheduler
// -- and the run still ended on the "Installation completed" banner. That false green is
// precisely what the CLI's own rule was written to prevent.
//
// Anything else -- a plain EOF at a prompt, an unreachable relay, a failed dry-run -- is
// benign: the step is accessory, the config is already written, so the install continues.
// A nil error is never an abort.
func optionalInstallStepAborts(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return errors.Is(err, shell.ErrClosed)
}

// abortInstallOnOptionalStep is the TUI driver's action on that verdict: return the
// canonical aborted-install error so the deferred footer shows the honest banner, or nil
// to carry on. It does NOT close the session -- the driver's deferred Close already runs
// before the deferred footer (defers are LIFO), so the terminal is released in time.
//
// The benign case is deliberately silent here: each step already logs its own outcome
// (logTelegramSetupOutcome, logHealthcheckSetupOutcome, the audit block), and duplicating
// that would put two lines in the install log for one event.
func abortInstallOnOptionalStep(ctx context.Context, bootstrap *logging.BootstrapLogger, step string, err error) error {
	if !optionalInstallStepAborts(ctx, err) {
		return nil
	}
	// Worded for the surface: on the TUI the cause is usually a key, not a signal.
	logBootstrapWarning(bootstrap, "%s aborted (interrupted); stopping the install before finalization", step)
	return wrapInstallError(errInteractiveAborted)
}
