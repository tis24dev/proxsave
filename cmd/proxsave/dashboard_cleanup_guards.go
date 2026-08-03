package main

import (
	"context"
	"io"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// cleanupGuardsReport is the seam for orchestrator.CleanupMountGuardsReport so the
// dashboard flow can be tested without touching real mounts / requiring root.
var cleanupGuardsReport = orchestrator.CleanupMountGuardsReport

// runDashboardCleanupGuards runs the guard cleanup from the dashboard using the shared
// two-step check/apply brick: a read-only CHECK classifies Clean (green, nothing to
// unlock) vs Found (yellow, guards present), and Apply runs the real cleanup.
func runDashboardCleanupGuards(ctx context.Context, session *shell.Session) {
	runDashboardCheckApply(ctx, session, "Cleanup guards",
		func() (dashboardCheckResult, error) {
			report, err := cleanupGuardsReport(ctx, discardLogger(), true) // read-only check
			if err != nil {
				return dashboardCheckResult{}, err
			}
			if !report.HasGuards() {
				return dashboardCheckResult{Found: false, Level: orchestrator.HealthcheckSetupLevelOk, Keyword: "CLEAN", Explanation: describeGuardCheck(report)}, nil
			}
			return dashboardCheckResult{Found: true, Level: orchestrator.HealthcheckSetupLevelWarn, Keyword: "FOUND", Explanation: describeGuardCheck(report)}, nil
		},
		func() (dashboardApplyResult, error) {
			report, err := cleanupGuardsReport(ctx, discardLogger(), false) // run for real
			if err != nil {
				return dashboardApplyResult{}, err
			}
			level, keyword := classifyGuardApply(report)
			return dashboardApplyResult{Level: level, Keyword: keyword, Explanation: describeGuardApply(report)}, nil
		},
		"Apply", "remove the guards now to unlock the storage")
}

// discardLogger is a quiet logger for the in-session cleanup: the outcome is taken from
// the structured report and shown on screen, so the cleanup's own log lines are dropped
// (writing them to stdout would corrupt the live TUI).
func discardLogger() *logging.Logger {
	lg := logging.New(types.LogLevelInfo, false)
	lg.SetOutput(io.Discard)
	return lg
}

// classifyGuardApply maps a real cleanup report to the styled result (level, keyword):
// fully removed -> Ok/DONE; anything left behind (or unconfirmed) -> Warn/PENDING.
func classifyGuardApply(r orchestrator.GuardCleanupReport) (orchestrator.HealthcheckSetupLevel, string) {
	if guardApplyClean(r) {
		return orchestrator.HealthcheckSetupLevelOk, "DONE"
	}
	return orchestrator.HealthcheckSetupLevelWarn, "PENDING"
}

// describeGuardCheck renders the CHECK explanation (no "dry run" wording): the shared
// facts, plus this front-end's call to action, which names the on-screen button.
func describeGuardCheck(r orchestrator.GuardCleanupReport) string {
	if !r.HasGuards() {
		return guardCheckFacts(r)
	}
	return guardCheckFacts(r) + " Apply removes them to unlock it."
}

// describeGuardApply renders the real-run outcome: shared facts plus the retry
// instruction phrased as the menu entry the operator would pick again.
func describeGuardApply(r orchestrator.GuardCleanupReport) string {
	if guardApplyClean(r) {
		return guardApplyFacts(r)
	}
	return guardApplyFacts(r) + " Unmount the datastore and run Cleanup guards again once it is offline."
}
