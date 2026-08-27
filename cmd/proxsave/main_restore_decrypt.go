// Package main contains the proxsave command entrypoint.
package main

import (
	"errors"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/types"
)

// Test seams.
var (
	restoreIsInteractive = isTerminalInteractive
	runRestoreCLIFn      = runRestoreCLI
	runRestoreTUIFn      = runRestoreTUI
)

func dispatchRestoreMode(rt *appRuntime) modeResult {
	if !rt.args.Restore {
		return modeResult{exitCode: types.ExitSuccess.Int()}
	}

	restoreCLI := rt.args.ForceCLI || !restoreIsInteractive()
	logging.DebugStep(rt.logger, "main", "mode=restore cli=%v", restoreCLI)
	if restoreCLI {
		return runRestoreCLIFn(rt)
	}
	return runRestoreTUIFn(rt)
}

func runRestoreCLI(rt *appRuntime) modeResult {
	logging.Info("Restore mode enabled - starting CLI workflow...")
	err := orchestrator.RunRestoreWorkflow(rt.ctx, rt.cfg, rt.logger, rt.toolVersion, rt.hostname)
	if err != nil {
		return finishFailedRestore(rt, err, false)
	}
	return finishSuccessfulRestore(rt)
}

func runRestoreTUI(rt *appRuntime) modeResult {
	logging.Info("Restore mode enabled - starting interactive workflow...")
	sig := buildSignature()
	if strings.TrimSpace(sig) == "" {
		sig = "n/a"
	}
	err := orchestrator.RunRestoreWorkflowTUI(rt.ctx, rt.cfg, rt.logger, rt.toolVersion, rt.args.ConfigPath, sig, rt.hostname)
	if err != nil {
		return finishFailedRestore(rt, err, true)
	}
	return finishSuccessfulRestore(rt)
}

func finishFailedRestore(rt *appRuntime, err error, includeDecryptAbort bool) modeResult {
	if isRestoreAbort(err, includeDecryptAbort) {
		logging.Warning("Restore workflow aborted by user")
		return restoreModeResult(rt, exitCodeInterrupted)
	}
	if errors.Is(err, orchestrator.ErrDecryptNoBackups) && dashboardIsBareInvocation() {
		// Dashboard bare invocation: the user already saw the graceful "Status:"
		// empty-state screen, so exit cleanly with NO log line, mirroring the
		// decrypt entrypoint (runDecryptOnlyMode). A CLI --restore is not bare, so
		// it falls through and keeps its ERROR line (CLI-execution lines untouched).
		return restoreModeResult(rt, types.ExitSuccess.Int())
	}
	logging.Error("Restore workflow failed: %v", err)
	return restoreModeResult(rt, types.ExitGenericError.Int())
}

func isRestoreAbort(err error, includeDecryptAbort bool) bool {
	if errors.Is(err, orchestrator.ErrRestoreAborted) {
		return true
	}
	return includeDecryptAbort && errors.Is(err, orchestrator.ErrDecryptAborted)
}

func finishSuccessfulRestore(rt *appRuntime) modeResult {
	if rt.logger.HasWarnings() {
		logging.Warning("Restore workflow completed with warnings (see log above)")
	} else {
		logging.Info("Restore workflow completed successfully")
	}
	return restoreModeResult(rt, types.ExitSuccess.Int())
}

func restoreModeResult(rt *appRuntime, exitCode int) modeResult {
	return modeResult{
		exitCode:     exitCode,
		handled:      true,
		supportStats: restoreSupportStats(rt, exitCode),
	}
}

func restoreSupportStats(rt *appRuntime, exitCode int) *orchestrator.BackupStats {
	if !rt.args.Support {
		return nil
	}
	return support.BuildSupportStats(rt.logger, supportBundleHostname(rt.logger, rt.hostname), rt.envInfo.Type, rt.envInfo.Version, rt.toolVersion, rt.startTime, time.Now(), exitCode, "restore")
}

// supportBundleHostname returns the name THIS RUN is using, which is the only name
// a support bundle may carry.
//
// The run resolves its name exactly once (initializeRunLogFile assigns
// rt.hostname = resolveHostname(), main_runtime.go) and hands that one value down:
// dispatchBackupMode copies it into the storage constructors, and both restore
// workflows above are passed it for the access control host check. On a BACKUP run
// the log file is named with it too; a restore is not, because initializeRunLogFile
// returns at its "if rt.args.Restore" guard before opening one, and the restore
// session log is named by logging.detectHostname, which reads os.Hostname alone.
// Resolving it a SECOND time here was not a free repeat of the
// first. resolveHostname shells out to "hostname -f" and falls back to the kernel
// name when that fails, and the probe depends on getaddrinfo, on /etc/hosts and on
// DNS, all of which can change or fail between two calls in one process. Two calls
// can therefore return different answers, which is the mechanism of discussion
// #292, and a bundle naming a host the run never used is precisely the artefact an
// operator sends when they are debugging a hostname problem. The second call also
// spawned a subprocess on a path that already had the answer.
//
// On a restore the divergence is not always drift, and the field is pinned to the
// run's name knowing that. Restoring the "network" category rewrites /etc/hosts
// live (internal/orchestrator/network_staged_apply.go), so a probe run afterwards
// can legitimately answer with the name the machine is about to have. The bundle
// still reports the name the RUN used, because that is the name its log file, its
// access control check and its own decisions were made under, and a bundle whose
// header disagrees with its own contents cannot be read.
//
// The empty branch is unreachable on every path that reaches here today, and is
// checked rather than assumed because a nameless bundle would be worse than a
// divergent one: nothing in it would identify the machine that produced it.
// initializeRunLogFile assigns rt.hostname as its FIRST statement, above the
// "if rt.args.Restore { return }" guard, bootstrapRuntime calls it unconditionally
// before handing a runtime back, dispatchRestoreMode is reached only from that
// runtime, and resolveHostname has no return path yielding "" (it falls back to the
// "unknown" sentinel). Empty can therefore only mean the plumb was cut in source.
//
// It warns rather than papering over, for the reason runHostnameOrReport
// (backup_execution.go) gives: quietly resolving a name a second time is exactly
// what let a dropped plumb look correct while the run's identity had already been
// lost. This is deliberately NOT that helper: its message names the archives and
// retention, which is the wrong consequence here and would misdirect the reader of
// a restore log.
func supportBundleHostname(logger *logging.Logger, runHostname string) string {
	if runHostname != "" {
		return runHostname
	}
	if logger != nil {
		logger.Warning("The run's hostname did not reach the support bundle: it is being named by a second \"hostname -f\" probe, which can answer differently from the name this run used for its log file and its access control check (discussion #292)")
	}
	return resolveHostname()
}

func dispatchBackupMode(rt *appRuntime) modeResult {
	result := runBackupMode(backupModeOptions{
		ctx:              rt.ctx,
		bootstrap:        rt.bootstrap,
		cfg:              rt.cfg,
		logger:           rt.logger,
		envInfo:          rt.envInfo,
		unprivilegedInfo: rt.unprivilegedInfo,
		updateInfo:       rt.updateInfo,
		toolVersion:      rt.toolVersion,
		dryRun:           rt.dryRun,
		startTime:        rt.startTime,
		heapProfilePath:  rt.heapProfilePath,
		serverIDValue:    rt.serverIDValue,
		serverMACValue:   rt.serverMACValue,
		hostname:         rt.hostname,
		support:          rt.args.Support,
		supportMeta: support.Meta{
			GitHubUser: rt.args.SupportGitHubUser,
			IssueID:    rt.args.SupportIssueID,
		},
	})
	return modeResult{
		orch:             result.orch,
		earlyErrorState:  result.earlyErrorState,
		supportStats:     result.supportStats,
		supportEmailSent: result.supportEmailSent,
		exitCode:         result.exitCode,
		handled:          true,
	}
}
