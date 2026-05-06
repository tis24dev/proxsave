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

func dispatchRestoreMode(rt *appRuntime) modeResult {
	if !rt.args.Restore {
		return modeResult{exitCode: types.ExitSuccess.Int()}
	}

	restoreCLI := rt.args.ForceCLI
	logging.DebugStep(rt.logger, "main", "mode=restore cli=%v", restoreCLI)
	if restoreCLI {
		return runRestoreCLI(rt)
	}
	return runRestoreTUI(rt)
}

func runRestoreCLI(rt *appRuntime) modeResult {
	logging.Info("Restore mode enabled - starting CLI workflow...")
	err := orchestrator.RunRestoreWorkflow(rt.ctx, rt.cfg, rt.logger, rt.toolVersion)
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
	err := orchestrator.RunRestoreWorkflowTUI(rt.ctx, rt.cfg, rt.logger, rt.toolVersion, rt.args.ConfigPath, sig)
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
	return support.BuildSupportStats(rt.logger, resolveHostname(), rt.envInfo.Type, rt.envInfo.Version, rt.toolVersion, rt.startTime, time.Now(), exitCode, "restore")
}

func dispatchBackupMode(rt *appRuntime) modeResult {
	result := runBackupMode(backupModeOptions{
		ctx:              rt.ctx,
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
	})
	return modeResult{
		orch:            result.orch,
		earlyErrorState: result.earlyErrorState,
		supportStats:    result.supportStats,
		exitCode:        result.exitCode,
		handled:         true,
	}
}
