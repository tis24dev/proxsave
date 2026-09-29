package main

import (
	"context"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notifyfilter"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// The run decides its NOTIFY_ON threshold with notifyfilter.Decide at initialization, and again
// before dispatch when the relay's answer has expired.

// logNotifyFilterInit writes the three INFO lines of the initialization block and hands the
// decision to the orchestrator, with the refresh used before dispatch.
func logNotifyFilterInit(opts backupModeOptions, orch *orchestrator.Orchestrator, section string) {
	cfg, logger := opts.cfg, opts.logger
	source := "default"
	if cfg != nil && cfg.NotifyOnSource != "" {
		source = cfg.NotifyOnSource
	}
	requested := notifyfilter.Negotiated(cfg)
	logging.Info("Notification setting: NOTIFY_ON=%s", requested)
	logging.DebugStep(logger, "notifications init", "notify_on=%s source=%s", requested, source)
	if cfg != nil && cfg.HealthcheckEnabled {
		logging.DebugStep(logger, "notifications init", "healthchecks mode=%s", cfg.HealthcheckMode)
	}
	d := notifyfilter.Decide(opts.ctx, cfg, logger, section, "notifications init")
	logging.Info("Healthchecks status: %s", d.Status)
	logging.Info("Notification filter: %s", d.Effective)
	if orch == nil {
		return
	}
	orch.SetNotifyFilter(notifyFilterRefresh(d, cfg, logger, section))
}

// notifyFilterRefresh is the refresh handed to the orchestrator for the decision before dispatch:
// it reuses the last decision while the relay's answer is still valid (notifyfilter.Validity at
// most), or when the decision read no relay answer at all, and decides again otherwise.
func notifyFilterRefresh(d notifyfilter.Decision, cfg *config.Config, logger *logging.Logger, section string) func(context.Context) string {
	return func(ctx context.Context) string {
		if d.ReadAt.IsZero() || notifyfilter.Now().Sub(d.ReadAt) < d.ValidFor {
			return d.Effective
		}
		d = notifyfilter.Decide(ctx, cfg, logger, section, "notifications dispatch")
		return d.Effective
	}
}
