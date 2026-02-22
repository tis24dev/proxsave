package backup

import "github.com/tis24dev/proxsave/internal/environment"

type unprivilegedContainerContext struct {
	Detected bool
	Details  string
}

func (c *Collector) depDetectUnprivilegedContainer() unprivilegedContainerContext {
	if c == nil {
		return unprivilegedContainerContext{}
	}

	c.unprivilegedOnce.Do(func() {
		if c.deps.DetectUnprivilegedContainer != nil {
			detected, details := c.deps.DetectUnprivilegedContainer()
			c.unprivilegedCtx = unprivilegedContainerContext{
				Detected: detected,
				Details:  details,
			}
			if c.logger != nil {
				c.logger.Debug("Unprivileged container detection (deps override): detected=%t details=%q", c.unprivilegedCtx.Detected, c.unprivilegedCtx.Details)
			}
			return
		}

		info := environment.DetectUnprivilegedContainer()
		c.unprivilegedCtx = unprivilegedContainerContext{
			Detected: info.Detected,
			Details:  info.Details,
		}
		if c.logger != nil {
			c.logger.Debug("Unprivileged container detection: detected=%t details=%q", c.unprivilegedCtx.Detected, c.unprivilegedCtx.Details)
		}
	})

	return c.unprivilegedCtx
}
