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
