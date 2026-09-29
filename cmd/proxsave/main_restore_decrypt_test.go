package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
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

// DRY_RUN=true in the configuration refuses the restore at the dispatch, before
// either workflow starts, with the exit code of the --dry-run flag refusal. The
// --dry-run flag itself never gets here: validateModeCompatibility refused it
// already. The dashboard's Restore entry goes through this same dispatch.
func TestDispatchRestoreModeRefusesDryRunFromTheConfiguration(t *testing.T) {
	origInteractive := restoreIsInteractive
	origCLI := runRestoreCLIFn
	origTUI := runRestoreTUIFn
	t.Cleanup(func() {
		restoreIsInteractive = origInteractive
		runRestoreCLIFn = origCLI
		runRestoreTUIFn = origTUI
	})
	runRestoreCLIFn = func(rt *appRuntime) modeResult {
		t.Fatalf("the CLI restore workflow started under DRY_RUN=true")
		return modeResult{}
	}
	runRestoreTUIFn = func(rt *appRuntime) modeResult {
		t.Fatalf("the TUI restore workflow started under DRY_RUN=true")
		return modeResult{}
	}

	for _, interactive := range []bool{false, true} {
		restoreIsInteractive = func() bool { return interactive }
		rt := &appRuntime{args: &cli.Args{Restore: true}, cfg: &config.Config{DryRun: true}}
		res := dispatchRestoreMode(rt)
		if !res.handled || res.exitCode != types.ExitConfigError.Int() {
			t.Fatalf("interactive=%v: handled=%v exitCode=%d, want handled with %d", interactive, res.handled, res.exitCode, types.ExitConfigError.Int())
		}
	}
}

// The engine refuses a dry-run restore too, as a backstop. If that refusal ever
// reaches the entrypoint it keeps the configuration-error exit code instead of
// becoming a generic restore failure.
func TestFinishFailedRestoreKeepsTheDryRunRefusalAConfigError(t *testing.T) {
	rt := &appRuntime{args: &cli.Args{}}
	for _, err := range []error{
		orchestrator.ErrRestoreDryRun,
		fmt.Errorf("DRY_RUN is true: %w", orchestrator.ErrRestoreDryRun),
	} {
		for _, includeDecryptAbort := range []bool{false, true} {
			res := finishFailedRestore(rt, err, includeDecryptAbort)
			if res.exitCode != types.ExitConfigError.Int() {
				t.Fatalf("finishFailedRestore(%q, %v): exitCode=%d, want %d", err, includeDecryptAbort, res.exitCode, types.ExitConfigError.Int())
			}
		}
	}
}
