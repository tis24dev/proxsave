// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"
	"os"
	"runtime/pprof"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/support"
)

type runDeferredAction func()

func runDeferredActions(rt *appRuntime, state *appRunState) []runDeferredAction {
	// runRuntime defers each returned action while iterating this slice, so these
	// entries execute in reverse (LIFO) order. Keep the ordering intentional:
	// dispatchDeferredEarlyErrorNotification must run before sendDeferredSupportEmail
	// because it sets state.pendingSupportStat, which sendDeferredSupportEmail
	// reads. Do not reorder these entries or change the defer pattern without
	// preserving that dependency.
	return []runDeferredAction{
		func() {
			// printFinalSummary routes through printRunFooter, which skips it for a
			// graphical run, so this only asks whether the run wants a summary at all.
			if state.showSummary {
				printFinalSummary(state.finalExitCode)
			}
		},
		func() {
			if state.finalExitCode == exitCodeInterrupted {
				if abortInfo := orchestrator.GetLastRestoreAbortInfo(); abortInfo != nil {
					printNetworkRollbackCountdown(abortInfo)
				}
			}
		},
		func() {
			sendDeferredSupportEmail(rt, state)
		},
		func() {
			dispatchDeferredEarlyErrorNotification(rt, state)
		},
		func() {
			closeRunProfiling(rt)
		},
		func() {
			cleanupAfterRun(rt.logger)
		},
	}
}

func sendDeferredSupportEmail(rt *appRuntime, state *appRunState) {
	// supportEmailSent means the streamed dashboard run already sent it inside the
	// viewport (visible to the user); skip here so it is not sent twice.
	if !rt.args.Support || state.pendingSupportStat == nil || state.supportEmailSent {
		return
	}
	emitSupportEmail(rt.ctx, rt.cfg, rt.logger, rt.envInfo.Type, state.pendingSupportStat, support.Meta{
		GitHubUser: rt.args.SupportGitHubUser,
		IssueID:    rt.args.SupportIssueID,
	})
}

func dispatchDeferredEarlyErrorNotification(rt *appRuntime, state *appRunState) {
	if state.earlyErrorState == nil || !state.earlyErrorState.HasError() || state.orch == nil {
		return
	}
	fmt.Println()
	logging.Step("Sending error notifications")
	stats := state.orch.DispatchEarlyErrorNotification(rt.ctx, state.earlyErrorState)
	if stats != nil {
		state.pendingSupportStat = stats
	}
	state.orch.FinalizeAndCloseLog(rt.ctx)
}

func closeRunProfiling(rt *appRuntime) {
	if rt.cpuProfileFile != nil {
		pprof.StopCPUProfile()
		_ = rt.cpuProfileFile.Close()
	}
	if rt.heapProfilePath == "" {
		return
	}
	f, err := os.Create(rt.heapProfilePath)
	if err != nil {
		logging.Warning("Failed to create heap profile file: %v", err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			logging.Warning("Failed to close heap profile file: %v", err)
		}
	}()
	if err := pprof.WriteHeapProfile(f); err != nil {
		logging.Warning("Failed to write heap profile: %v", err)
	}
}
