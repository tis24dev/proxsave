package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// cleanupGuardsReport is the seam for orchestrator.CleanupMountGuardsReport so the
// dashboard flow can be tested without touching real mounts / requiring root.
var cleanupGuardsReport = orchestrator.CleanupMountGuardsReport

// runDashboardCleanupGuards runs the guard cleanup from the dashboard as a RESULT-ONLY,
// TWO-STEP flow (no streaming), matching the check/daemon result screens:
//   - step 1 is a read-only CHECK. If there is nothing to unlock it shows a GREEN
//     "Clean" screen whose action is "Check" (re-scan) — never Apply. If guards are
//     present it shows a YELLOW "Found" screen whose action is "Apply".
//   - step 2 (only on Apply) runs the cleanup for real and shows the outcome (DONE /
//     PENDING / FAILED).
//
// It is non-blocking (a failure — e.g. the root-required error — renders a red FAILED
// screen) and loops back to the menu. Runs in the live dashboard session.
func runDashboardCleanupGuards(ctx context.Context, session *shell.Session) {
	for {
		report, err := cleanupGuardsReport(ctx, discardLogger(), true) // read-only check
		if err != nil {
			showDaemonResultScreen(ctx, session, "Cleanup guards", orchestrator.HealthcheckSetupLevelError, "FAILED", err.Error())
			return
		}

		if !report.HasGuards() {
			// Clean: nothing to unlock. Green "Clean", no Apply — only re-Check / Back.
			if recheck := showCleanupGuardsChoice(ctx, session,
				orchestrator.HealthcheckSetupLevelOk, "Clean", describeGuardCheck(report),
				"Check", "re-scan for mount guards",
				"Back", "return to the dashboard menu"); recheck {
				continue
			}
			return
		}

		// Found: guards are locking the storage. Yellow "Found", action is Apply.
		if apply := showCleanupGuardsChoice(ctx, session,
			orchestrator.HealthcheckSetupLevelWarn, "Found", describeGuardCheck(report),
			"Apply", "remove the guards now to unlock the storage",
			"Cancel", "return to the dashboard without changes"); !apply {
			return
		}

		applied, err := cleanupGuardsReport(ctx, discardLogger(), false) // run for real
		if err != nil {
			showDaemonResultScreen(ctx, session, "Cleanup guards", orchestrator.HealthcheckSetupLevelError, "FAILED", err.Error())
			return
		}
		level, keyword := classifyGuardApply(applied)
		showDaemonResultScreen(ctx, session, "Cleanup guards", level, keyword, describeGuardApply(applied))
		return
	}
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

// cleanupGuardsChoice is the primary/secondary choice on a Cleanup guards screen.
type cleanupGuardsChoice int

const (
	cleanupGuardsPrimary cleanupGuardsChoice = iota
	cleanupGuardsSecondary
)

// showCleanupGuardsChoice shows a styled "Status:" screen (the SAME renderer the
// daemon/check results use) with a primary and a secondary action, and reports whether
// the primary was chosen. esc / secondary return false. Used for both the Clean screen
// (primary = Check / re-scan) and the Found screen (primary = Apply).
func showCleanupGuardsChoice(ctx context.Context, session *shell.Session, level orchestrator.HealthcheckSetupLevel, keyword, explanation, primaryLabel, primaryDesc, secondaryLabel, secondaryDesc string) bool {
	errEsc := errors.New("cleanup guards: esc")
	prompt := buildDaemonResultPrompt(level, keyword, explanation)
	items := []components.SelectorItem[cleanupGuardsChoice]{
		{Label: primaryLabel, Description: primaryDesc, Value: cleanupGuardsPrimary},
		{Label: secondaryLabel, Description: secondaryDesc, Value: cleanupGuardsSecondary},
	}
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Cleanup guards", items,
		components.WithSelectorPromptStyled[cleanupGuardsChoice](prompt),
		components.WithSelectorBack[cleanupGuardsChoice](errEsc),
	))
	if err != nil {
		return false
	}
	return action == cleanupGuardsPrimary
}
