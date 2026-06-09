package orchestrator

import (
	"context"
	"errors"
	"testing"
)

// Regression for ctx-cancel-swallowed-staged-loop (2026-06-09 audit): the staged
// apply loop never re-checked w.ctx.Err() between steps. The PVE step degrades all
// of its sub-errors (including a context.Canceled from applyStorageCfg) to a
// warning+nil, so an operator's Ctrl+C mid-PVE-apply was lost and the loop kept
// applying later sensitive steps (PVE SDN, access-control secrets, notifications).
// Written after adding the between-steps cancellation re-check.

func TestRunStagedApplySteps_AbortsBetweenStepsOnSwallowedCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &restoreUIWorkflowRun{ctx: ctx, logger: newTestLogger()}

	laterRan := false
	steps := []restoreStageApplyStep{
		// Simulates the PVE step swallowing a context.Canceled into a warning+nil.
		{name: "swallows cancel", run: func() error { cancel(); return nil }},
		{name: "sensitive later step", run: func() error { laterRan = true; return nil }},
	}

	err := w.runStagedApplySteps(steps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runStagedApplySteps = %v, want context.Canceled", err)
	}
	if laterRan {
		t.Errorf("a sensitive step after a swallowed cancellation must NOT run")
	}
}

func TestRunStagedApplySteps_RunsAllWhenNotCancelled(t *testing.T) {
	w := &restoreUIWorkflowRun{ctx: context.Background(), logger: newTestLogger()}

	var order []string
	steps := []restoreStageApplyStep{
		{name: "a", run: func() error { order = append(order, "a"); return nil }},
		{name: "b", run: func() error { order = append(order, "b"); return nil }},
		{name: "c", run: func() error { order = append(order, "c"); return nil }},
	}

	if err := w.runStagedApplySteps(steps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("steps order = %v, want [a b c]", order)
	}
}
