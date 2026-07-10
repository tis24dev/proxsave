package main

import (
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// finishFailedRestore must mirror the decrypt entrypoint for the empty-state:
// a dashboard bare invocation (the user already saw the graceful "Status:"
// screen) exits cleanly, while a CLI --restore keeps its ERROR line + generic
// exit. A genuine failure is always ERROR + generic exit regardless.
func TestFinishFailedRestoreNoBackupsMirrorsDecrypt(t *testing.T) {
	origBare := dashboardIsBareInvocation
	t.Cleanup(func() { dashboardIsBareInvocation = origBare })

	rt := &appRuntime{args: &cli.Args{}}

	t.Run("dashboard bare invocation exits clean", func(t *testing.T) {
		dashboardIsBareInvocation = func() bool { return true }
		res := finishFailedRestore(rt, orchestrator.ErrDecryptNoBackups, true)
		if res.exitCode != types.ExitSuccess.Int() {
			t.Fatalf("exitCode=%d, want %d (clean exit, no ERROR)", res.exitCode, types.ExitSuccess.Int())
		}
	})

	t.Run("CLI --restore keeps the ERROR", func(t *testing.T) {
		dashboardIsBareInvocation = func() bool { return false }
		res := finishFailedRestore(rt, orchestrator.ErrDecryptNoBackups, false)
		if res.exitCode != types.ExitGenericError.Int() {
			t.Fatalf("exitCode=%d, want %d (ERROR + generic exit)", res.exitCode, types.ExitGenericError.Int())
		}
	})

	t.Run("genuine failure is unaffected even when bare", func(t *testing.T) {
		dashboardIsBareInvocation = func() bool { return true }
		res := finishFailedRestore(rt, errors.New("boom"), true)
		if res.exitCode != types.ExitGenericError.Int() {
			t.Fatalf("exitCode=%d, want %d", res.exitCode, types.ExitGenericError.Int())
		}
	})
}
