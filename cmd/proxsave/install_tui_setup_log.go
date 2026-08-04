package main

import (
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// The install log trail for the two optional setup steps, TUI side.
//
// The CLI emits these lines inline as it walks the step (runTelegramSetupCLI,
// runHealthcheckSetupCLI); the TUI cannot, because the step runs inside a flow that
// returns a result struct. Doing it from the driver is fine — what was NOT fine is
// that the driver open-coded the trail twice and got the two halves out of step: the
// Telegram half logged the bootstrap eligibility diagnosis, the healthcheck half
// silently dropped it. Keeping both here, in one shape, is what makes the omission
// visible the next time a step is added.

// logTelegramSetupOutcome writes the install-log trail for one Telegram setup step:
// the non-blocking failure warning, the bootstrap eligibility diagnosis, and the
// verification verdict — the same three the CLI emits.
//
// The verdict lines are emitted even when err is non-nil: RunTelegramSetup returns
// what it collected before failing, and that partial verdict is worth recording. The
// eligibility diagnosis is not, because a run that failed may never have produced one.
func logTelegramSetupOutcome(bootstrap *logging.BootstrapLogger, res installer.TelegramSetupResult, err error) {
	if bootstrap == nil {
		return
	}
	if err != nil {
		bootstrap.Warning("Telegram setup failed (non-blocking): %v", err)
	} else {
		logTelegramSetupBootstrapOutcome(bootstrap, res.TelegramSetupBootstrap)
	}
	if !res.Shown {
		return
	}
	switch {
	case res.Verified:
		bootstrap.Info("Telegram setup: verified (code=%d)", res.LastStatusCode)
	case res.SkippedVerification:
		bootstrap.Info("Telegram setup: verification skipped by user")
	case res.CheckAttempts > 0:
		// The relay's own words, scrubbed and with the shared stand-in — the exact
		// treatment runTelegramSetupCLI gives them. The field is already scrubbed at
		// its write site; this call is what supplies the stand-in when the relay sent
		// nothing, and it keeps the line identical to the CLI's if that ever changes.
		bootstrap.Info("Telegram setup: not verified (attempts=%d last=%d %s)",
			res.CheckAttempts, res.LastStatusCode,
			orchestrator.TelegramSetupStatusMessageForLog(res.LastStatusMessage))
	default:
		bootstrap.Info("Telegram setup: not verified (no check performed)")
	}
}

// logHealthcheckSetupOutcome is the healthcheck twin. It calls
// logHealthcheckSetupBootstrapOutcome, which the TUI install path did not: a skip the
// CLI explains in the log ("no alive URL configured yet", "unable to load config", the
// identity/secret verdict, "self mode") left no trace at all on a TUI install, so the
// step read as if it had never run and the reason it was skipped was unrecoverable
// from the log afterwards.
//
// Unlike the Telegram twin the verdict lines are suppressed on error. That is the rule
// the driver already applied here (`hcErr == nil &&` guarded the whole block), kept
// rather than harmonised because harmonising would be a behaviour change nobody asked
// for. It makes no observable difference today — RunHealthcheckSetup returns a ZERO
// result on every error path, and a zero result carries Shown=false and stops here
// anyway — so the rule is stated in code and pinned by a test with a populated result,
// which is the only way it can be pinned at all.
func logHealthcheckSetupOutcome(bootstrap *logging.BootstrapLogger, res installer.HealthcheckSetupResult, err error) {
	if bootstrap == nil {
		return
	}
	if err != nil {
		bootstrap.Warning("Healthcheck setup failed (non-blocking): %v", err)
		return
	}
	logHealthcheckSetupBootstrapOutcome(bootstrap, res.HealthcheckSetupBootstrap)
	if !res.Shown {
		return
	}
	switch {
	case res.Verified:
		bootstrap.Info("Healthcheck setup: verified")
	case res.SkippedVerification:
		bootstrap.Info("Healthcheck setup: check skipped by user")
	case res.CheckAttempts > 0:
		bootstrap.Info("Healthcheck setup: not verified (attempts=%d)", res.CheckAttempts)
	default:
		bootstrap.Info("Healthcheck setup: not verified (no check performed)")
	}
}
