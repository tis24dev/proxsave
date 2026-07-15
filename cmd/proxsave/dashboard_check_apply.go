package main

import (
	"context"
	"errors"

	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// dashboardCheckResult is what a two-step action's initial CHECK found.
type dashboardCheckResult struct {
	Found       bool // is there something to apply?
	Level       orchestrator.HealthcheckSetupLevel
	Keyword     string
	Explanation string
}

// dashboardApplyResult is the outcome of the real APPLY step.
type dashboardApplyResult struct {
	Level       orchestrator.HealthcheckSetupLevel
	Keyword     string
	Explanation string
}

// runDashboardCheckApply is the reusable brick behind the two-step, RESULT-ONLY
// dashboard actions (Cleanup guards, Update config). It owns only the shared FLOW and
// SCREENS (the styled "Status:" look, no streaming); each caller supplies its own check
// and apply logic:
//   - a read-only CHECK; when it finds nothing it shows a GREEN result whose action is
//     "Check" (re-run) — never Apply;
//   - when it finds something it shows a YELLOW result whose action is Apply;
//   - Apply runs the real operation and shows its outcome.
//
// Any error renders a red FAILED screen. It is non-blocking and loops back to the menu.
func runDashboardCheckApply(ctx context.Context, session *shell.Session, title string,
	check func() (dashboardCheckResult, error),
	apply func() (dashboardApplyResult, error),
	applyLabel, applyDesc string) {
	for {
		res, err := check()
		if err != nil {
			showDaemonResultScreen(ctx, session, title, orchestrator.HealthcheckSetupLevelError, "FAILED", err.Error())
			return
		}

		if !res.Found {
			// Nothing to apply. No Apply — only re-Check / Back.
			if showDashboardCheckChoice(ctx, session, title, res.Level, res.Keyword, res.Explanation,
				"Re-check", "re-run the check",
				"Back", "return to the dashboard menu") {
				continue
			}
			return
		}

		if !showDashboardCheckChoice(ctx, session, title, res.Level, res.Keyword, res.Explanation,
			applyLabel, applyDesc,
			"Back", "return to the dashboard menu") {
			return
		}

		applied, err := apply()
		if err != nil {
			showDaemonResultScreen(ctx, session, title, orchestrator.HealthcheckSetupLevelError, "FAILED", err.Error())
			return
		}
		showDaemonResultScreen(ctx, session, title, applied.Level, applied.Keyword, applied.Explanation)
		return
	}
}

// dashboardCheckChoice is the primary/secondary choice on a check/apply screen.
type dashboardCheckChoice int

const (
	dashboardCheckPrimary dashboardCheckChoice = iota
	dashboardCheckSecondary
)

// showDashboardCheckChoice shows a styled "Status:" screen (the SAME renderer the
// daemon/check results use) with a primary and a secondary action, and reports whether
// the primary was chosen. esc / secondary return false.
func showDashboardCheckChoice(ctx context.Context, session *shell.Session, title string, level orchestrator.HealthcheckSetupLevel, keyword, explanation, primaryLabel, primaryDesc, secondaryLabel, secondaryDesc string) bool {
	errEsc := errors.New("dashboard check: esc")
	prompt := buildDaemonResultPrompt(level, keyword, explanation)
	items := []components.SelectorItem[dashboardCheckChoice]{
		{Label: primaryLabel, Description: primaryDesc, Value: dashboardCheckPrimary},
		{Label: secondaryLabel, Description: secondaryDesc, Value: dashboardCheckSecondary},
	}
	action, err := shell.Ask(ctx, session, components.NewSelector(
		title, items,
		components.WithSelectorPromptStyled[dashboardCheckChoice](prompt),
		components.WithSelectorBack[dashboardCheckChoice](errEsc),
	))
	if err != nil {
		return false
	}
	return action == dashboardCheckPrimary
}
