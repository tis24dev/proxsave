package backup

import "context"

// CollectDualConfigs collects both PVE and PBS configurations on a coinstalled host.
func (c *Collector) CollectDualConfigs(ctx context.Context) error {
	c.logger.Info("Collecting dual-role configurations")
	state := newCollectionState(c)
	if err := runRecipe(ctx, newDualRecipe(), state); err != nil {
		return err
	}

	c.logger.Info("Dual-role configuration collection completed")
	return nil
}
