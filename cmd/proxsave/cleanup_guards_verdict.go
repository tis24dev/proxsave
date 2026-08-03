package main

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// The guard-cleanup verdict, single-sourced for BOTH front-ends.
//
// Both drive orchestrator.CleanupMountGuardsReport, but they used to read it
// differently: the dashboard classified the report (CLEAN/FOUND, DONE/PENDING) while
// --cleanup-guards took the error-only CleanupMountGuards wrapper and exited 0 for
// anything short of an engine failure. A script gating on the exit code was told the
// storage was unlocked while guards were still holding it.
//
// What is shared here is the CLASSIFICATION and the FACTS. The call to action is not:
// the dashboard names a button ("Apply"), the CLI names a flag, and neither wording
// makes sense on the other front-end.

// guardApplyClean reports whether a real run left nothing behind. GuardsRemaining == -1
// is the fail-closed "unknown" sentinel, which counts as NOT clean: a cleanup that
// cannot confirm what remains must not be reported as having unlocked the storage.
func guardApplyClean(r orchestrator.GuardCleanupReport) bool {
	return r.GuardsRemaining == 0 && r.ImmutablePending == 0
}

// guardCleanupExitCode maps a report to the process exit code. dryRun selects the
// read-only CHECK rule (anything found is reported as pending, since nothing was
// removed) over the real-run rule (only what is LEFT counts).
//
// Both non-clean outcomes exit ExitGuardsPending rather than ExitGenericError: the
// cleanup did not fail, the storage is simply still locked. An engine error is the
// caller's to report, and keeps ExitGenericError.
func guardCleanupExitCode(r orchestrator.GuardCleanupReport, dryRun bool) types.ExitCode {
	if dryRun {
		if r.HasGuards() {
			return types.ExitGuardsPending
		}
		return types.ExitSuccess
	}
	if guardApplyClean(r) {
		return types.ExitSuccess
	}
	return types.ExitGuardsPending
}

// guardCheckFacts states what the read-only check found, and nothing about what to do
// next — see the note at the top of this file.
func guardCheckFacts(r orchestrator.GuardCleanupReport) string {
	if !r.HasGuards() {
		return "No restore mount guards are present. Nothing to unlock."
	}
	var parts []string
	if r.BindGuards > 0 {
		parts = append(parts, countLabel(r.BindGuards, "bind mount guard"))
	}
	if r.ImmutableGuards > 0 {
		parts = append(parts, countLabel(r.ImmutableGuards, "immutable flag"))
	}
	return fmt.Sprintf("Found %s locking the storage.", strings.Join(parts, " and "))
}

// guardApplyFacts states the outcome of a real run. The not-clean wording deliberately
// names the usual cause — a guard hidden under a live mount, which the engine refuses
// to unmount — because that is what tells the operator the retry needs the datastore
// offline rather than more privileges.
func guardApplyFacts(r orchestrator.GuardCleanupReport) string {
	if guardApplyClean(r) {
		return "Removed the restore mount guards. The storage is unlocked."
	}
	return "Some guards are still in place (hidden under a live mount)."
}

// logCLIGuardVerdict states the verdict in the CLI's voice: the shared facts plus a
// call to action naming the flag. Warning level for anything left behind, so it stands
// out in a cron log — the place this mode usually runs, and where nobody is watching.
func logCLIGuardVerdict(logger *logging.Logger, r orchestrator.GuardCleanupReport, dryRun bool) {
	switch {
	case dryRun && r.HasGuards():
		logger.Warning("%s Run without --dry-run to remove them.", guardCheckFacts(r))
	case dryRun:
		logger.Info("%s", guardCheckFacts(r))
	case guardApplyClean(r):
		logger.Info("%s", guardApplyFacts(r))
	default:
		logger.Warning("%s Unmount the datastore and run --cleanup-guards again once it is offline.", guardApplyFacts(r))
	}
}

// countLabel pluralizes "N thing" / "N things".
func countLabel(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
