package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// TestApplySelfHealthcheckParamsCancelContinues covers the optional self-params step's
// abort decision. The cancelled-context row is the one the old local rule missed: it
// knew only about session death, so a SIGTERM during this step was demoted to a warning
// and the install carried on to finalization.
func TestApplySelfHealthcheckParamsCancelContinues(t *testing.T) {
	orig := runHealthcheckSelfParamsFn
	t.Cleanup(func() { runHealthcheckSelfParamsFn = orig })

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name      string
		ctx       context.Context
		stepErr   error
		wantAbort bool
	}{
		{"cancel is non-blocking", context.Background(), installer.ErrInstallCancelled, false},
		{"session death aborts", context.Background(), shell.ErrClosed, true},
		{"a cancelled run context aborts", cancelled, installer.ErrInstallCancelled, true},
		{"success", context.Background(), nil, false},
		{"success is not an abort even on a dead context", cancelled, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHealthcheckSelfParamsFn = func(context.Context, *shell.Session, string, string) error {
				return tc.stepErr
			}
			err := applySelfHealthcheckParams(tc.ctx, nil, "/base", "/cfg", nil)
			if tc.wantAbort {
				if err == nil || !errors.Is(err, errInteractiveAborted) {
					t.Fatalf("want abort (errInteractiveAborted), got %v", err)
				}
			} else if err != nil {
				t.Fatalf("want continue (nil), got %v", err)
			}
		})
	}
}
