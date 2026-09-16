package backup

import (
	"context"
	"errors"
	"fmt"
)

// CollectDualConfigs collects both PVE and PBS configurations on a coinstalled host.
//
// The two halves run as separate recipes against one shared state. They used to be one
// concatenated recipe, and runRecipe is fail-fast, so an abort anywhere in the PBS half
// ended the whole thing: on the host in issue #315 that meant 34 PVE bricks and 25 PBS
// bricks completed, the sixtieth aborted, and the workspace holding all of it was
// deleted. The host got no backup at all because of a role it does not even have.
//
// A half that fails is recorded and reported, and the other half is kept. Both failing
// is still an error, because then there is no role payload left to keep: the caller
// stops the run rather than shipping an archive of system files labelled dual.
func (c *Collector) CollectDualConfigs(ctx context.Context) error {
	c.logger.Info("Collecting dual-role configurations")
	state := newCollectionState(c)

	pveErr := runRecipe(ctx, newPVERecipe(), state)
	if pveErr != nil && isContextCancellationError(ctx, pveErr) {
		return pveErr
	}

	pbsErr := runRecipe(ctx, newPBSRecipe(), state)
	if pbsErr != nil && isContextCancellationError(ctx, pbsErr) {
		return pbsErr
	}

	switch {
	case pveErr != nil && pbsErr != nil:
		// Both causes are wrapped, not just the first. %w on one and %v on the other
		// put the PBS text in the message while leaving it unreachable to errors.Is,
		// so a caller testing for a sentinel saw only half the failure.
		return fmt.Errorf("both halves failed: %w", errors.Join(pveErr, pbsErr))
	case pveErr != nil:
		c.noteIncompleteTarget("pve", pveErr)
	case pbsErr != nil:
		c.noteIncompleteTarget("pbs", pbsErr)
	}

	if pveErr == nil && pbsErr == nil {
		c.logger.Info("Dual-role configuration collection completed")
	}
	return nil
}

// noteIncompleteTarget records a role that did not finish.
//
// It reports at warning, not error. The distinction is the exit code: an error makes
// the run a failed backup (ExitBackupError), and this run produced an archive that is
// worth keeping. A warning promotes a clean run to the generic exit code, so a monitor
// still sees that the night was not normal without being told the backup failed when
// it did not. The manifest carries the same record, so the gap travels with the
// archive rather than living only in a log that may not be kept.
func (c *Collector) noteIncompleteTarget(target string, cause error) {
	if cause == nil {
		return
	}
	reason := cause.Error()

	c.statsMu.Lock()
	c.incomplete = append(c.incomplete, incompleteTarget{Target: target, Reason: reason})
	c.statsMu.Unlock()

	c.logger.Warning("Collection: the %s half of this dual host did not finish - %s", target, reason)
	c.logger.Warning("Collection: this backup carries the other role and the system payload, and is marked incomplete for %s", target)
}

// incompleteSnapshot copies the recorded gaps under the same lock every other access
// to the slice takes. WriteManifest read it bare, which is a race the moment anything
// records a gap off the collection goroutine.
func (c *Collector) incompleteSnapshot() []incompleteTarget {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return append([]incompleteTarget(nil), c.incomplete...)
}

// IncompleteTargets returns the roles whose collection did not finish, for a caller
// that reports run status. Empty on a whole backup.
func (c *Collector) IncompleteTargets() []string {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()

	targets := make([]string, 0, len(c.incomplete))
	for _, entry := range c.incomplete {
		targets = append(targets, entry.Target)
	}
	return targets
}
