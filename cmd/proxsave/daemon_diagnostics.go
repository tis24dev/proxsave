package main

import (
	"context"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// daemonDiagnostics is the single presentation-neutral snapshot consumed by
// both the plain CLI and the dashboard daemon-status renderers.
type daemonDiagnostics struct {
	Mode        string
	Unit        string
	Active      string
	State       health.DaemonState
	Level       orchestrator.HealthcheckSetupLevel
	Keyword     string
	Explanation string
}

var (
	daemonDiagnosticsCollector     = collectDaemonDiagnostics
	daemonStatusUnitInstalledProbe = daemonUnitInstalled
	daemonStatusActiveStateProbe   = daemonUnitActiveState
	daemonStatusNow                = time.Now
)

// collectDaemonDiagnostics owns every probe and verdict used by the two
// daemon-status frontends. Renderers receive facts; they never recompute them.
func collectDaemonDiagnostics(ctx context.Context, cfg *config.Config, baseDir string) daemonDiagnostics {
	mode := "unknown"
	var interval time.Duration
	if cfg != nil {
		mode = cfg.SchedulerMode
		interval = cfg.HealthcheckHeartbeatInterval
		if configured := strings.TrimSpace(cfg.BaseDir); configured != "" {
			baseDir = configured
		}
	}
	if strings.TrimSpace(baseDir) == "" {
		baseDir, _ = detectedBaseDirOrFallback()
	}

	unit := "not installed"
	if daemonStatusUnitInstalledProbe() {
		unit = "installed"
	}
	active := daemonStatusActiveStateProbe(ctx)
	if active == "" {
		active = "unknown"
	}

	state := health.CheckDaemonState(health.DaemonStateInput{
		BaseDir:           baseDir,
		SchedulerMode:     mode,
		HeartbeatInterval: interval,
		Now:               daemonStatusNow(),
		Presence:          daemonPresenceProbe(ctx),
		ProcAlive:         probeProxsaveDaemonAlive,
		ProcStale:         procBinaryStaleProbe,
	})
	level, keyword, explanation := daemonStatusStyle(state)
	return daemonDiagnostics{
		Mode:        mode,
		Unit:        unit,
		Active:      active,
		State:       state,
		Level:       level,
		Keyword:     keyword,
		Explanation: explanation,
	}
}
