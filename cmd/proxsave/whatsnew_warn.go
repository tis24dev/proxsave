package main

import (
	"errors"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// whatsnewShouldWarn is the non-interactive gate seam. Like whatsnewDecide/whatsnewSaveSeen
// in dashboard.go, it is a package-var func indirection so the wiring below is unit-testable
// without a real disk read or a GitHub fetch: a test swaps it for a stub that returns a
// fixed (show, version, err) verdict.
var whatsnewShouldWarn = whatsnew.ShouldWarn

// maybeWarnWhatsnew emits the non-interactive "what's new" nudge on an automated run. It is
// the counterpart of Screen 0 (maybeShowWhatsnew): while the seen-flag is unseen it writes a
// single WARNING line that rides the existing ParseLogCounts -> LogCategories -> email/webhook
// capture path (no new NotificationData field, no dispatch, no goroutine). It is a pure gated
// logger call: one filesystem read via the gate, a semver compare, then a buffered write, so
// it never touches backup outcome or timing (NOTF-03). It fails toward SILENCE: on a gate error
// or a seen verdict it emits only DEBUG lines, never the WARNING. A corrupt seen-flag
// (errors.Is(err, whatsnew.ErrStateParse)) self-heals best-effort: it quarantines the unreadable
// file to .corrupt and re-seeds last_seen=current via whatsnewSaveSeen (a failed write logs a
// distinct DEBUG line and leaves the flag for the next run), then stays silent; any non-parse
// error still emits only the generic gate-error DEBUG line without writing. The DEBUG bracket
// lines are bare-fact English; the single imperative lives only in the locked WARNING copy.
func maybeWarnWhatsnew(logger *logging.Logger, baseDir, toolVersion string) {
	if logger == nil {
		return
	}
	logger.Debug("Checking for unseen ProxSave release notes (current %s)", toolVersion)
	show, ver, err := whatsnewShouldWarn(baseDir, toolVersion)
	switch {
	case err != nil:
		if errors.Is(err, whatsnew.ErrStateParse) {
			// Log the ACTUAL self-heal outcome: a best-effort MarkSeen can fail (read-only
			// identity dir), and the DEBUG line must not claim a re-seed that did not happen.
			if serr := whatsnewSaveSeen(baseDir, toolVersion); serr != nil {
				logger.Debug("Release notes check: corrupt seen-flag self-heal write failed: %v", serr)
			} else {
				logger.Debug("Release notes check self-healed: corrupt seen-flag quarantined to .corrupt and re-seeded to the current version")
			}
		} else {
			logger.Debug("Release notes check skipped: gate error: %v", err)
		}
	case show:
		logger.Warning("ProxSave %s has unseen release notes. Open proxsave to view the new features.", ver)
	default:
		logger.Debug("Release notes check completed: notes already seen")
	}
	logger.Debug("Release notes check done")
}
