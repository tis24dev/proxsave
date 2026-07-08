package main

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// cleanupGuardsRun is the seam for orchestrator.CleanupMountGuards so the dashboard
// flow can be tested without touching real mounts / requiring root.
var cleanupGuardsRun = orchestrator.CleanupMountGuards

// runDashboardCleanupGuards runs the guard cleanup from the dashboard as a RESULT-ONLY,
// TWO-STEP flow (no streaming), matching the check/daemon result screens:
//   - step 1: a DRY RUN whose styled "Status:" screen previews what would happen and
//     offers Apply / Cancel;
//   - step 2 (only on Apply): the real run, whose "Status:" screen shows the outcome.
//
// It is non-blocking (a failure just renders a red "Status: FAILED" screen, e.g. the
// root-required error) and loops back to the menu. Runs in the live dashboard session.
func runDashboardCleanupGuards(ctx context.Context, session *shell.Session) {
	// Step 1: DRY RUN preview (changes nothing).
	level, keyword, explanation, err := cleanupGuardsOutcome(ctx, true)
	if err != nil {
		showDaemonResultScreen(ctx, session, "Cleanup guards", orchestrator.HealthcheckSetupLevelError, "FAILED", err.Error())
		return
	}
	if !showCleanupGuardsConfirm(ctx, session, level, keyword, explanation) {
		return // Cancel / esc -> back to the menu, nothing applied
	}

	// Step 2: apply for real.
	level, keyword, explanation, err = cleanupGuardsOutcome(ctx, false)
	if err != nil {
		showDaemonResultScreen(ctx, session, "Cleanup guards", orchestrator.HealthcheckSetupLevelError, "FAILED", err.Error())
		return
	}
	showDaemonResultScreen(ctx, session, "Cleanup guards", level, keyword, explanation)
}

// cleanupGuardsOutcome runs the guard cleanup (dry-run or real) capturing its log
// output, and derives the (level, keyword, explanation) for the styled "Status:"
// screen. Any failure (including the root-required error) is returned as err so the
// caller shows a red FAILED screen; the dry-run always previews (Warn), and the real
// run classifies the result (Ok/DONE, Ok/NOTHING TO CLEAN, or Warn/PENDING).
func cleanupGuardsOutcome(ctx context.Context, dryRun bool) (orchestrator.HealthcheckSetupLevel, string, string, error) {
	var buf bytes.Buffer
	lg := logging.New(types.LogLevelInfo, false)
	lg.SetOutput(&buf)
	if err := cleanupGuardsRun(ctx, lg, dryRun); err != nil {
		return orchestrator.HealthcheckSetupLevelError, "FAILED", "", err
	}

	explanation := cleanGuardLog(buf.String())
	if dryRun {
		return orchestrator.HealthcheckSetupLevelWarn, "DRY RUN", explanation, nil
	}
	low := strings.ToLower(explanation)
	switch {
	case strings.Contains(low, "still present") || strings.Contains(low, "still pending"):
		return orchestrator.HealthcheckSetupLevelWarn, "PENDING", explanation, nil
	case strings.Contains(low, "nothing to clean"):
		return orchestrator.HealthcheckSetupLevelOk, "NOTHING TO CLEAN", explanation, nil
	default:
		return orchestrator.HealthcheckSetupLevelOk, "DONE", explanation, nil
	}
}

// cleanupGuardsConfirmAction is the choice on the dry-run preview screen.
type cleanupGuardsConfirmAction int

const (
	cleanupGuardsApply cleanupGuardsConfirmAction = iota
	cleanupGuardsCancel
)

// showCleanupGuardsConfirm shows the dry-run outcome as a styled "Status:" screen (the
// SAME renderer the daemon/check results use) and asks whether to apply it for real.
// Returns true only on Apply; esc or Cancel returns false (nothing changes).
func showCleanupGuardsConfirm(ctx context.Context, session *shell.Session, level orchestrator.HealthcheckSetupLevel, keyword, explanation string) bool {
	errCleanupEsc := errors.New("cleanup guards: esc")
	prompt := buildDaemonResultPrompt(level, keyword, explanation)
	items := []components.SelectorItem[cleanupGuardsConfirmAction]{
		{Label: "Apply", Description: "run the cleanup for real", Value: cleanupGuardsApply},
		{Label: "Cancel", Description: "return to the dashboard without changes", Value: cleanupGuardsCancel},
	}
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Cleanup guards", items,
		components.WithSelectorPromptStyled[cleanupGuardsConfirmAction](prompt),
		components.WithSelectorBack[cleanupGuardsConfirmAction](errCleanupEsc),
	))
	if err != nil {
		return false
	}
	return action == cleanupGuardsApply
}

// cleanGuardLog strips the "[ts] LEVEL " prefix from each captured log line so the
// "Status:" explanation shows the plain messages, joining the non-empty ones.
func cleanGuardLog(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Drop "[ts] " then the level token: "[2026-..] INFO     msg" -> "msg".
		if idx := strings.Index(line, "] "); idx >= 0 {
			rest := strings.TrimSpace(line[idx+2:])
			if sp := strings.IndexByte(rest, ' '); sp >= 0 {
				rest = strings.TrimSpace(rest[sp+1:])
			}
			line = rest
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
