package main

import (
	"context"
	"fmt"
	"io"
	"strings"

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
				return dashboardCheckResult{Found: false, Level: orchestrator.HealthcheckSetupLevelOk, Keyword: "Clean", Explanation: describeGuardCheck(report)}, nil
			}
			return dashboardCheckResult{Found: true, Level: orchestrator.HealthcheckSetupLevelWarn, Keyword: "Found", Explanation: describeGuardCheck(report)}, nil
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

// guardApplyClean reports whether a real run left nothing behind. GuardsRemaining == -1
// is the fail-closed "unknown" sentinel, which counts as not-clean.
func guardApplyClean(r orchestrator.GuardCleanupReport) bool {
	return r.GuardsRemaining == 0 && r.ImmutablePending == 0
}

// describeGuardCheck renders the CHECK explanation (no "dry run" wording): either that
// there is nothing to unlock, or what was found locking the storage.
func describeGuardCheck(r orchestrator.GuardCleanupReport) string {
	if !r.HasGuards() {
		return "No restore mount guards are present — nothing to unlock."
	}
	var parts []string
	if r.BindGuards > 0 {
		parts = append(parts, countLabel(r.BindGuards, "bind mount guard"))
	}
	if r.ImmutableGuards > 0 {
		parts = append(parts, countLabel(r.ImmutableGuards, "immutable flag"))
	}
	return fmt.Sprintf("Found %s locking the storage. Apply removes them to unlock it.", strings.Join(parts, " and "))
}

// describeGuardApply renders the real-run outcome explanation.
func describeGuardApply(r orchestrator.GuardCleanupReport) string {
	if guardApplyClean(r) {
		return "Removed the restore mount guards — the storage is unlocked."
	}
	return "Some guards are still in place (hidden under a live mount). Unmount the datastore and run Cleanup guards again once it is offline."
}

// countLabel pluralizes "N thing" / "N things".
func countLabel(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
