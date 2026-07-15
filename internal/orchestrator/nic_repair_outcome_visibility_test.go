package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// B/C2: a NIC-repair plan failure must NOT be swallowed - repairNICNamesWithUI
// returns a non-nil result marked Failed, and the commit screen renders it.
func TestNICRepairFailureIsSurfaced(t *testing.T) {
	origPlan := planNICNameRepairFn
	t.Cleanup(func() { planNICNameRepairFn = origPlan })
	planNICNameRepairFn = func(context.Context, string) (*nicRepairPlan, error) {
		return nil, errors.New("close decompression reader: signal: broken pipe")
	}

	ui := &recordingNICUI{}
	logger := logging.New(types.LogLevelError, false)
	res := repairNICNamesWithUI(context.Background(), ui, logger, "/x.tar.xz")
	if res == nil {
		t.Fatal("failure must return a non-nil result so the outcome is visible")
	}
	if !res.Failed || strings.TrimSpace(res.FailedReason) == "" {
		t.Fatalf("result must be marked Failed with a reason, got %+v", res)
	}
	if !strings.Contains(res.Summary(), "FAILED") {
		t.Fatalf("Summary must report the failure, got %q", res.Summary())
	}

	// The commit screen must show the failed line (rendered view, like the
	// existing netcommit view-contract test).
	view := newNetworkCommitConfirm(30*time.Second, auditNetcommitHealth(), res, "").View(100, 30)
	if !strings.Contains(view, "NIC repair: FAILED") {
		t.Fatalf("commit screen must surface the failed repair, view=%q", view)
	}
}
