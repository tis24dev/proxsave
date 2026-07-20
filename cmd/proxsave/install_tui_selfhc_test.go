package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func TestApplySelfHealthcheckParamsCancelContinues(t *testing.T) {
	orig := runHealthcheckSelfParamsFn
	t.Cleanup(func() { runHealthcheckSelfParamsFn = orig })

	// fatal mimics mapUIDeath: only a session close is fatal.
	fatal := func(err error) error {
		if errors.Is(err, shell.ErrClosed) {
			return wrapInstallError(errInteractiveAborted)
		}
		return err
	}

	cases := []struct {
		name      string
		stepErr   error
		wantAbort bool
	}{
		{"cancel is non-blocking", installer.ErrInstallCancelled, false},
		{"session death aborts", shell.ErrClosed, true},
		{"success", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHealthcheckSelfParamsFn = func(context.Context, *shell.Session, string, string) error {
				return tc.stepErr
			}
			err := applySelfHealthcheckParams(context.Background(), nil, "/base", "/cfg", nil, fatal)
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
