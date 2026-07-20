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

// F01-03: without --cli, a non-TTY stdout must force the plain CLI restore
// workflow, never the altscreen TUI (which would write escape bytes into the
// redirected file). An interactive terminal must still get the TUI.
func TestDispatchRestoreModeGatesTUIonNonTTY(t *testing.T) {
	origInteractive := restoreIsInteractive
	origCLI := runRestoreCLIFn
	origTUI := runRestoreTUIFn
	t.Cleanup(func() {
		restoreIsInteractive = origInteractive
		runRestoreCLIFn = origCLI
		runRestoreTUIFn = origTUI
	})

	const cliSentinel = 111
	const tuiSentinel = 222
	runRestoreCLIFn = func(rt *appRuntime) modeResult { return modeResult{exitCode: cliSentinel, handled: true} }
	runRestoreTUIFn = func(rt *appRuntime) modeResult { return modeResult{exitCode: tuiSentinel, handled: true} }

	t.Run("non-TTY stdout forces the CLI branch", func(t *testing.T) {
		restoreIsInteractive = func() bool { return false }
		rt := &appRuntime{args: &cli.Args{Restore: true, ForceCLI: false}}
		res := dispatchRestoreMode(rt)
		if res.exitCode != cliSentinel {
			t.Fatalf("exitCode=%d, want CLI sentinel %d (non-TTY must force the CLI restore, not the altscreen TUI)", res.exitCode, cliSentinel)
		}
	})

	t.Run("interactive terminal keeps the TUI branch", func(t *testing.T) {
		restoreIsInteractive = func() bool { return true }
		rt := &appRuntime{args: &cli.Args{Restore: true, ForceCLI: false}}
		res := dispatchRestoreMode(rt)
		if res.exitCode != tuiSentinel {
			t.Fatalf("exitCode=%d, want TUI sentinel %d (an interactive terminal must still get the TUI)", res.exitCode, tuiSentinel)
		}
	})
}
