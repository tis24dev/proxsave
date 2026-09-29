package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/notifyfilter"
)

// healthcheckNotifyFilterLoad reads backup.env for CheckHealthcheckNotifyFilter; tests replace it.
var healthcheckNotifyFilterLoad = config.LoadConfigWithBaseDir

// HealthcheckNotifyFilter is what the Healthchecks check screen shows about NOTIFY_ON: the decision
// the run takes (notifyfilter.Decide), from backup.env as it is now.
type HealthcheckNotifyFilter struct {
	Loaded      bool   // backup.env was read; nothing else is set when it was not
	Self        bool   // self mode
	Invalid     bool   // NOTIFY_ON holds a value the loader did not recognise
	Raw         string // that value, as written
	FromDefault bool   // NOTIFY_ON is absent or empty in backup.env
	notifyfilter.Decision
}

// CheckHealthcheckNotifyFilter takes the run's NOTIFY_ON decision for the check screen. serverAPIHost
// and serverID are the ones the same check used (the bootstrap resolves the ServerID from the host
// identity, as the run does: backup.env does not carry it). daemon is the diagnosis that check made
// (HealthcheckCheckResult.Daemon), so the screen's Status line and its filter agree on whether the
// daemon is transmitting. Self mode takes no decision here: the screen states the mode.
func CheckHealthcheckNotifyFilter(ctx context.Context, configPath, baseDir, serverAPIHost, serverID string, daemon health.Diagnosis) HealthcheckNotifyFilter {
	cfg, err := healthcheckNotifyFilterLoad(configPath, baseDir)
	if err != nil || cfg == nil {
		return HealthcheckNotifyFilter{}
	}
	nf := HealthcheckNotifyFilter{
		Loaded:      true,
		Self:        cfg.HealthcheckMode == config.HealthcheckModeSelf,
		Invalid:     cfg.NotifyOn != "" && !config.IsValidNotifyOn(cfg.NotifyOn),
		Raw:         cfg.NotifyOn,
		FromDefault: cfg.NotifyOnSource == "default",
	}
	if !cfg.HealthcheckEnabled { // switched off in the file since the screen opened: what the run applies
		nf.Self, nf.Decision = false, notifyfilter.Decide(ctx, cfg, nil, notifyfilter.SectionDisabled, "")
		return nf
	}
	if nf.Self {
		return nf
	}
	cfg.ServerAPIHost, cfg.ServerID = serverAPIHost, serverID
	section := notifyfilter.SectionInitialized
	if health.DaemonProblem(daemon) != "" {
		section = notifyfilter.SectionNotTransmitting
	}
	nf.Decision = notifyfilter.Decide(ctx, cfg, nil, section, "")
	return nf
}

// HealthcheckNotifyLines is the Notifications block both check screens print, the dashboard and the
// CLI installer (maintainer-approved text): the setting and the filter applied now. ok is false when
// backup.env could not be read, and the block is then not printed.
func HealthcheckNotifyLines(selfMode bool, nf HealthcheckNotifyFilter) (setting, current string, ok bool) {
	if selfMode || nf.Self {
		return "Self mode", "Self mode", true
	}
	if !nf.Loaded {
		return "", "", false
	}
	setting, current = "NOTIFY_ON="+nf.Requested, nf.Effective
	switch {
	case nf.Invalid:
		setting = "NOTIFY_ON=" + nf.Raw + " (not valid, always used)"
	case nf.FromDefault:
		setting += " (default)"
	}
	if why := healthcheckNotifyReason(nf.Decision); why != "" {
		current += " (" + why + ")"
	}
	return setting, current, true
}

// healthcheckNotifyReason says why the threshold applied differs from the one requested.
func healthcheckNotifyReason(d notifyfilter.Decision) string {
	switch d.Reason {
	case notifyfilter.ReasonPolicyUnconfirmed:
		return "setting not yet applied by the server"
	case notifyfilter.ReasonStatusUnavailable:
		return "Healthchecks status unavailable"
	case notifyfilter.ReasonNotTransmitting:
		return "daemon not transmitting"
	case notifyfilter.ReasonAlertsNotVerified:
		switch d.Status {
		case notifyfilter.StatusNotConfigured:
			return "Healthchecks not configured"
		case notifyfilter.StatusNotVerified:
			return "Healthchecks not verified"
		case notifyfilter.StatusDegraded:
			return "Healthchecks degraded"
		default:
			return "Healthchecks status unknown"
		}
	}
	return ""
}
