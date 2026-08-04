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
// --cleanup-guards took an error-only wrapper beside it and exited 0 for anything short
// of an engine failure. A script gating on the exit code was told the storage was
// unlocked while guards were still holding it. That wrapper has since been removed, so
// the report is the engine's only exported entry point.
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

// guardApplyFacts states the outcome of a real run, keeping the distinctions the engine
// already makes instead of flattening them into clean-or-not.
//
// Three of them matter. A run that found no guard directory removed NOTHING, and saying
// otherwise contradicts the engine's own "nothing to clean up" line one screen earlier.
// GuardsRemaining == -1 is the fail-closed sentinel for a verification reread that
// failed, so the count is unknown and asserting a cause on top of it is two claims the
// run cannot support. And ImmutablePending covers three causes, not one -- the engine
// comments them as mounted, unresolvable, or the clear failed -- so naming only the live
// mount sends an operator to unmount a datastore that was never the problem.
func guardApplyFacts(r orchestrator.GuardCleanupReport) string {
	if !r.GuardDirPresent {
		return "No restore mount guards were present. Nothing to unlock."
	}
	if guardApplyClean(r) {
		return "Removed the restore mount guards. The storage is unlocked."
	}
	var parts []string
	switch {
	case r.GuardsRemaining < 0:
		parts = append(parts, "the number of bind mount guards still in place could not be confirmed")
	case r.GuardsRemaining > 0:
		parts = append(parts, countLabel(r.GuardsRemaining, "bind mount guard")+
			" still in place (hidden under a live mount, or the unmount failed)")
	}
	if r.ImmutablePending > 0 {
		parts = append(parts, countLabel(r.ImmutablePending, "immutable flag")+
			" still set (the target is mounted, unresolvable, or the clear failed)")
	}
	return "The storage is still locked: " + strings.Join(parts, ", and ") + "."
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
	case r.GuardsRemaining < 0:
		// Unknown, not stuck: telling the operator to unmount a datastore would be
		// advice for a diagnosis this run never reached.
		logger.Warning("%s Run --cleanup-guards again to get a confirmed count.", guardApplyFacts(r))
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
