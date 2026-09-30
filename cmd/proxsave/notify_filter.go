package main

import (
	"context"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notifyfilter"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// The run decides its NOTIFY_ON threshold with notifyfilter.Decide at initialization, and again
// before dispatch when the relay's answer has expired.

// logNotifyFilterInit writes the initialization block (INFO "Applying notification filter...",
// the DEBUG evidence, the INFO details, the why-line of a filter not applied when there is one,
// the outcome last) and hands the decision to the orchestrator, with the refresh used before
// dispatch. No line of it is above INFO: a filter not applied still notifies every outcome, and a
// WARNING would raise the run's exit code.
func logNotifyFilterInit(opts backupModeOptions, orch *orchestrator.Orchestrator, section string) {
	cfg, logger := opts.cfg, opts.logger
	logging.Info("Applying notification filter...")
	logNotifyOnRead(cfg, logger)
	if cfg != nil && cfg.HealthcheckEnabled {
		logging.DebugStep(logger, "notifications init", "healthchecks mode=%s section=%s", cfg.HealthcheckMode, section)
	}
	d := notifyfilter.Decide(opts.ctx, cfg, logger, section, "notifications init")
	logging.Info("  Setting: %s", d.Requested)
	logging.Info("  Healthchecks status: %s", d.Status)
	logging.Info("  Filter in effect: %s", d.Effective)
	if why := notifyFilterWhy(d); why != "" {
		logging.Info("%s", why)
	}
	logging.Info("%s", d.Outcome())
	if orch == nil {
		return
	}
	orch.SetNotifyFilter(notifyFilterRefresh(d, cfg, logger, section))
}

// notifyFilterWhy is the INFO line the initialization block writes before a "not applied"
// outcome, naming what could not vouch for the filter: the relay (not reachable, not ready, did
// not confirm) or the operator's own Healthchecks server in self mode. It is "" for an applied
// filter, and when the Healthchecks status line already says why (disabled, not transmitting,
// not configured, not verified, degraded). Phase [7] writes no why-line.
func notifyFilterWhy(d notifyfilter.Decision) string {
	if d.Applied() {
		return ""
	}
	switch d.Reason {
	case notifyfilter.ReasonStatusUnavailable:
		switch d.Unavailable {
		case notifyfilter.UnavailableUnreachable:
			return "ProxSave HC Server not reachable"
		case notifyfilter.UnavailableHTTPStatus:
			return "ProxSave HC Server not ready"
		case notifyfilter.UnavailableUnusable:
			return "ProxSave HC Server did not confirm"
		}
	case notifyfilter.ReasonPolicyUnconfirmed:
		return "ProxSave HC Server did not confirm"
	case notifyfilter.ReasonAlertsNotVerified:
		// The relay answered that it cannot tell whether alerts reach the operator: the status
		// line reads "unknown", the same word as a relay that did not answer at all.
		if d.Status == notifyfilter.StatusUnknown {
			return "ProxSave HC Server did not confirm"
		}
	case notifyfilter.ReasonSelfNotifyChecks:
		return "Alerts on your own server not verified"
	}
	return ""
}

// logNotifyOnRead is the DEBUG evidence of the setting: the value read and where from (the
// configuration file's path when it came from backup.env), and the value requested when an
// unrecognised one is requested as always.
func logNotifyOnRead(cfg *config.Config, logger *logging.Logger) {
	requested := notifyfilter.Negotiated(cfg)
	value, source := requested, "default"
	if cfg != nil {
		value = cfg.NotifyOn
		if cfg.NotifyOnSource != "" {
			source = cfg.NotifyOnSource
		}
		if source == "backup.env" && cfg.ConfigPath != "" {
			source = cfg.ConfigPath
		}
	}
	if value == requested {
		logging.DebugStep(logger, "notifications init", "read notify_on=%s source=%s", value, source)
		return
	}
	logging.DebugStep(logger, "notifications init", "read notify_on=%q source=%s requested=%s", value, source, requested)
}

// notifyFilterRefresh is the refresh handed to the orchestrator for the decision before dispatch:
// it reuses the last decision while the relay's answer is still valid (notifyfilter.Validity at
// most), or when the decision read no relay answer at all, and decides again otherwise. Each
// reuse is recorded in DEBUG with the answer's age, so the dispatch outcome has its evidence.
func notifyFilterRefresh(d notifyfilter.Decision, cfg *config.Config, logger *logging.Logger, section string) func(context.Context) notifyfilter.Decision {
	const op = "notifications dispatch"
	from := "init"
	return func(ctx context.Context) notifyfilter.Decision {
		if d.ReadAt.IsZero() {
			logging.DebugStep(logger, op, "reused %s decision relay_read=none status=%s", from, d.Status)
			return d
		}
		if age := notifyfilter.Now().Sub(d.ReadAt); age < d.ValidFor {
			logging.DebugStep(logger, op, "reused %s decision age=%s valid_for=%s", from, age.Round(time.Millisecond), d.ValidFor)
			return d
		}
		d, from = notifyfilter.Decide(ctx, cfg, logger, section, op), "dispatch"
		return d
	}
}
