package orchestrator

import (
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/health"
)

// TestApplyHealthcheckDaemonStateKeywords pins the headline keyword for every daemon
// transmission state, including the defensive default, so the check screen distinguishes
// "reachable" from "actually monitoring" for each state.
func TestApplyHealthcheckDaemonStateKeywords(t *testing.T) {
	cases := []struct {
		state health.TxState
		want  string
	}{
		{health.TxTransmitting, "WORKING"},
		{health.TxNotInstalled, "NOT INSTALLED"},
		{health.TxNotActive, "NOT RUNNING"},
		{health.TxRunningNoReport, "RUNNING, NOT REPORTING"},
		{health.TxNoHeartbeat, "NOT RUNNING"},
		{health.TxStale, "STALE"},
		{health.TxNotProvisioned, "NOT PROVISIONED"},
		{health.TxUnreachable, "MONITOR UNREACHABLE"},
		{health.TxTransmitFailed, "TRANSMIT FAILED"},
		{health.TxState("bogus"), "UNKNOWN"},
	}
	for _, tt := range cases {
		st := applyHealthcheckDaemonState(HealthcheckSetupState{}, health.Diagnosis{State: tt.state})
		if st.Keyword != tt.want {
			t.Fatalf("state %q: Keyword=%q, want %q", tt.state, st.Keyword, tt.want)
		}
		if st.Message == "" {
			t.Fatalf("state %q: message must not be empty", tt.state)
		}
	}
}

// TestClassifyHealthcheckSelfResultUnreachable covers the two self-mode failure branches
// (a transport error, and a non-error but not-reachable result), both of which render
// UNREACHABLE.
func TestClassifyHealthcheckSelfResultUnreachable(t *testing.T) {
	st := ClassifyHealthcheckSelfResult(HealthcheckCheckResult{Err: errors.New("dial timeout")})
	if st.Keyword != "UNREACHABLE" {
		t.Fatalf("error case: Keyword=%q, want UNREACHABLE", st.Keyword)
	}

	st = ClassifyHealthcheckSelfResult(HealthcheckCheckResult{Reachable: false})
	if st.Keyword != "UNREACHABLE" {
		t.Fatalf("not-reachable case: Keyword=%q, want UNREACHABLE", st.Keyword)
	}
}
