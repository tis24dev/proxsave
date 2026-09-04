package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
)

type daemonUIDDiagnostic struct {
	Value          int
	Source         string
	FallbackReason string
}

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
	DaemonUID   daemonUIDDiagnostic
	Scripts     personalScriptsDiagnostics
}

var (
	daemonDiagnosticsCollector     = collectDaemonDiagnostics
	daemonStatusUnitInstalledProbe = daemonUnitInstalled
	daemonStatusActiveStateProbe   = daemonUnitActiveState
	daemonStatusNow                = time.Now
	daemonProcStatusReadFile       = os.ReadFile
	daemonCurrentEUID              = os.Geteuid
	daemonUIDResolver              = resolveDaemonEffectiveUID
	personalScriptsInspector       = inspectPersonalScripts
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
	daemonUID := daemonUIDResolver(state)
	scripts := personalScriptsInspector(cfg, daemonUID.Value)
	return daemonDiagnostics{
		Mode:        mode,
		Unit:        unit,
		Active:      active,
		State:       state,
		Level:       level,
		Keyword:     keyword,
		Explanation: explanation,
		DaemonUID:   daemonUID,
		Scripts:     scripts,
	}
}

// parseProcEffectiveUID reads the second numeric value from Linux's Uid line:
// real, effective, saved-set, then filesystem UID.
func parseProcEffectiveUID(data []byte) (int, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Uid:" {
			continue
		}
		if len(fields) < 3 {
			return 0, fmt.Errorf("malformed Uid line: %q", line)
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("parse effective uid %q: %w", fields[2], err)
		}
		if strconv.IntSize == 32 && uid > math.MaxInt32 {
			return 0, fmt.Errorf("effective uid %q exceeds the native int range", fields[2])
		}
		return int(uid), nil
	}
	return 0, fmt.Errorf("uid line not found")
}

func readProcessEffectiveUID(pid int) (int, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("invalid daemon pid %d", pid)
	}
	path := fmt.Sprintf("/proc/%d/status", pid)
	data, err := daemonProcStatusReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	uid, err := parseProcEffectiveUID(data)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return uid, nil
}

func resolveDaemonEffectiveUID(state health.DaemonState) daemonUIDDiagnostic {
	if state.ProcessAlive && state.PID > 0 {
		uid, err := readProcessEffectiveUID(state.PID)
		if err == nil {
			return daemonUIDDiagnostic{Value: uid, Source: "running daemon /proc"}
		}
		return daemonUIDDiagnostic{
			Value:          daemonCurrentEUID(),
			Source:         "current process",
			FallbackReason: err.Error(),
		}
	}
	return daemonUIDDiagnostic{
		Value:          daemonCurrentEUID(),
		Source:         "current process",
		FallbackReason: "daemon process is not live or has no PID",
	}
}

// logDaemonDiagnostics is the plain CLI renderer for the shared snapshot. The
// entire visible block is bracketed by the standard debug start/end markers.
func logDaemonDiagnostics(logger *logging.Logger, diagnostics daemonDiagnostics) {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	done := logging.DebugStart(logger, "daemon diagnostics", "daemon_uid=%d uid_source=%s",
		diagnostics.DaemonUID.Value, daemonDiagnosticText(diagnostics.DaemonUID.Source))
	defer done(nil)

	logger.Info("Daemon status: %s", daemonDiagnosticText(diagnostics.Keyword))
	logger.Info("Scheduler mode: %s", daemonDiagnosticText(diagnostics.Mode))
	logger.Info("Daemon service (%s): %s", daemonUnitName, daemonDiagnosticText(diagnostics.Unit))
	logger.Info("Service state (systemctl is-active): %s", daemonDiagnosticText(diagnostics.Active))
	if diagnostics.State.HaveInfo {
		logger.Info("Running version: %s (%s)", daemonDiagnosticText(diagnostics.State.Version), daemonDiagnosticText(diagnostics.State.Commit))
	}
	if diagnostics.State.HaveInfo || diagnostics.State.AlignChecked {
		alignment := "unknown"
		if diagnostics.State.AlignChecked {
			if diagnostics.State.Aligned {
				alignment = "aligned"
			} else {
				alignment = "BEHIND (restart needed)"
			}
		}
		logger.Info("Binary alignment: %s", alignment)
	}

	logPersonalScriptDiagnostic(logger, "Personal pre-run script", diagnostics.Scripts.Pre)
	logPersonalScriptDiagnostic(logger, "Personal post-run script", diagnostics.Scripts.Post)
	logging.DebugStep(logger, "daemon diagnostics", "daemon uid: value=%d source=%q fallback_reason=%q",
		diagnostics.DaemonUID.Value,
		daemonDiagnosticText(diagnostics.DaemonUID.Source),
		daemonDiagnosticText(diagnostics.DaemonUID.FallbackReason))
	logPersonalScriptEvidence(logger, "pre-run", diagnostics.Scripts.Pre)
	logPersonalScriptEvidence(logger, "post-run", diagnostics.Scripts.Post)
}

func logPersonalScriptDiagnostic(logger *logging.Logger, label string, diagnostic personalScriptDiagnostic) {
	path := daemonDiagnosticText(diagnostic.Path)
	reason := daemonDiagnosticText(diagnostic.Reason)
	switch diagnostic.State {
	case personalScriptReady:
		logger.Info("%s: READY — %s", label, path)
	case personalScriptRefused:
		if path == "" {
			logger.Warning("%s: REFUSED — %s", label, reason)
			return
		}
		logger.Warning("%s: REFUSED — %s — %s", label, path, reason)
	default:
		logger.Info("%s: NOT CONFIGURED", label)
	}
}

func logPersonalScriptEvidence(logger *logging.Logger, label string, diagnostic personalScriptDiagnostic) {
	logging.DebugStep(logger, "daemon diagnostics", "%s: key=%q state=%s path=%q daemon_uid=%d",
		label,
		daemonDiagnosticText(diagnostic.Key),
		diagnostic.State,
		daemonDiagnosticText(diagnostic.Path),
		diagnostic.DaemonUID)
	for _, component := range diagnostic.Components {
		logging.DebugStep(logger, "daemon diagnostics", "%s component: path=%q uid=%d mode=%04o",
			label, daemonDiagnosticText(component.Path), component.UID, component.Mode.Perm())
	}
}

func daemonDiagnosticText(value string) string {
	return strings.TrimSpace(components.SanitizeText(value))
}
