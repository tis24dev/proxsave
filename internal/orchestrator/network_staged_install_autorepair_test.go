package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// Bug 2 (Batch-B follow-up): maybeRepairNICNamesAuto must surface plan and apply
// failures as a non-nil Failed result (matching the B/C2 nicRepairResult.Failed
// contract) instead of swallowing them by returning nil.
func TestMaybeRepairNICNamesAuto_SurfacesPlanError(t *testing.T) {
	origPlan := planNICNameRepairFn
	t.Cleanup(func() { planNICNameRepairFn = origPlan })
	planNICNameRepairFn = func(context.Context, string) (*nicRepairPlan, error) {
		return nil, errors.New("plan boom")
	}

	got := maybeRepairNICNamesAuto(context.Background(), newDiscardLogger(), "/backup.tar.xz")
	if got == nil {
		t.Fatal("expected a non-nil Failed result on plan error, got nil")
	}
	if !got.Failed || !strings.Contains(got.FailedReason, "plan boom") {
		t.Fatalf("expected Failed result mentioning the plan error, got %#v", got)
	}
}

func TestMaybeRepairNICNamesAuto_SurfacesApplyError(t *testing.T) {
	origPlan := planNICNameRepairFn
	origDetect := detectNICNamingOverrideRulesFn
	origApply := applyNICNameRepairFn
	t.Cleanup(func() {
		planNICNameRepairFn = origPlan
		detectNICNamingOverrideRulesFn = origDetect
		applyNICNameRepairFn = origApply
	})
	planNICNameRepairFn = func(context.Context, string) (*nicRepairPlan, error) {
		return &nicRepairPlan{}, nil
	}
	detectNICNamingOverrideRulesFn = func(*logging.Logger) (nicNamingOverrideReport, error) {
		return nicNamingOverrideReport{}, nil
	}
	applyNICNameRepairFn = func(*logging.Logger, *nicRepairPlan, bool) (*nicRepairResult, error) {
		return nil, errors.New("apply boom")
	}

	got := maybeRepairNICNamesAuto(context.Background(), newDiscardLogger(), "/backup.tar.xz")
	if got == nil {
		t.Fatal("expected a non-nil Failed result on apply error, got nil")
	}
	if !got.Failed || !strings.Contains(got.FailedReason, "apply boom") {
		t.Fatalf("expected Failed result mentioning the apply error, got %#v", got)
	}
}
