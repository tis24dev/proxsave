package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
)

type daemonRuntimeAvailability string

const (
	daemonRuntimeAvailable     daemonRuntimeAvailability = "available"
	daemonRuntimeMissing       daemonRuntimeAvailability = "missing"
	daemonRuntimeStale         daemonRuntimeAvailability = "stale"
	daemonRuntimeInvalid       daemonRuntimeAvailability = "invalid"
	daemonRuntimeUnsupported   daemonRuntimeAvailability = "unsupported"
	daemonRuntimeNotApplicable daemonRuntimeAvailability = "not-applicable"
)

type personalScriptSynchronization string

const (
	personalScriptInSync             personalScriptSynchronization = "in-sync"
	personalScriptConfigurationDrift personalScriptSynchronization = "configuration-drift"
	personalScriptPathStateChanged   personalScriptSynchronization = "path-state-changed"
	personalScriptRuntimeUnavailable personalScriptSynchronization = "runtime-state-unavailable"
	personalScriptCurrentUnavailable personalScriptSynchronization = "current-configuration-unavailable"
	personalScriptSyncNotApplicable  personalScriptSynchronization = "not-applicable"
)

type daemonRuntimeDiagnostic struct {
	Availability daemonRuntimeAvailability
	Reason       string
	ConfigPath   string
	StartTS      int64
	DaemonUID    int
}

type personalScriptComparison struct {
	Running         personalScriptDiagnostic
	Current         personalScriptDiagnostic
	Synchronization personalScriptSynchronization
	SyncReason      string
}

type personalScriptComparisons struct {
	Pre  personalScriptComparison
	Post personalScriptComparison
}

type daemonUIDDiagnostic struct {
	Value          int
	Source         string
	FallbackReason string
}

// daemonDiagnostics is the single presentation-neutral snapshot consumed by
// both the plain CLI and the dashboard daemon-status renderers.
type daemonDiagnostics struct {
	Mode              string
	Unit              string
	Active            string
	State             health.DaemonState
	Level             orchestrator.HealthcheckSetupLevel
	Keyword           string
	Explanation       string
	DaemonUID         daemonUIDDiagnostic
	Runtime           daemonRuntimeDiagnostic
	ScriptComparisons personalScriptComparisons
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
	daemonRuntimeReader            = health.ReadDaemonRuntime
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
	currentScripts := personalScriptsInspector(cfg, daemonUID.Value)
	runtimeDiagnostic, runningScripts := resolveDaemonRuntime(state, baseDir)

	currentConfigPath := ""
	if cfg != nil {
		currentConfigPath = cfg.ConfigPath
	}
	scripts := personalScriptComparisons{
		Pre: comparePersonalScript(
			runtimeDiagnostic, currentConfigPath, runningScripts.Pre, currentScripts.Pre,
		),
		Post: comparePersonalScript(
			runtimeDiagnostic, currentConfigPath, runningScripts.Post, currentScripts.Post,
		),
	}
	return daemonDiagnostics{
		Mode:              mode,
		Unit:              unit,
		Active:            active,
		State:             state,
		Level:             level,
		Keyword:           keyword,
		Explanation:       explanation,
		DaemonUID:         daemonUID,
		Runtime:           runtimeDiagnostic,
		ScriptComparisons: scripts,
	}
}

func resolveDaemonRuntime(state health.DaemonState, baseDir string) (daemonRuntimeDiagnostic, personalScriptsDiagnostics) {
	if !state.ProcessAlive {
		return daemonRuntimeDiagnostic{
			Availability: daemonRuntimeNotApplicable,
			Reason:       "daemon process is not live",
		}, personalScriptsDiagnostics{}
	}
	if !state.HaveInfo {
		return daemonRuntimeDiagnostic{
			Availability: daemonRuntimeMissing,
			Reason:       "daemon identity record is unavailable",
		}, personalScriptsDiagnostics{}
	}

	record, found, err := daemonRuntimeReader(baseDir)
	if err != nil {
		return daemonRuntimeDiagnostic{Availability: daemonRuntimeInvalid, Reason: err.Error()}, personalScriptsDiagnostics{}
	}
	if !found {
		return daemonRuntimeDiagnostic{
			Availability: daemonRuntimeMissing,
			Reason:       "running daemon did not publish runtime state",
		}, personalScriptsDiagnostics{}
	}
	if record.SchemaVersion != health.DaemonRuntimeSchemaVersion {
		return daemonRuntimeDiagnostic{
			Availability: daemonRuntimeUnsupported,
			Reason:       fmt.Sprintf("runtime schema %d is unsupported", record.SchemaVersion),
		}, personalScriptsDiagnostics{}
	}
	if record.PID != state.PID || record.StartTS != state.StartTS {
		return daemonRuntimeDiagnostic{
			Availability: daemonRuntimeStale,
			Reason:       "runtime PID/start does not match the live daemon",
		}, personalScriptsDiagnostics{}
	}

	pre, okPre := personalScriptDiagnosticFromRuntime(
		"PERSONAL_SCRIPT_PRE_RUN", record.DaemonUID, record.PersonalScripts.Pre,
	)
	post, okPost := personalScriptDiagnosticFromRuntime(
		"PERSONAL_SCRIPT_POST_RUN", record.DaemonUID, record.PersonalScripts.Post,
	)
	if !okPre || !okPost {
		return daemonRuntimeDiagnostic{
			Availability: daemonRuntimeInvalid,
			Reason:       "runtime record contains an unknown personal-script state",
		}, personalScriptsDiagnostics{}
	}
	return daemonRuntimeDiagnostic{
		Availability: daemonRuntimeAvailable,
		ConfigPath:   record.ConfigPath,
		StartTS:      record.StartTS,
		DaemonUID:    record.DaemonUID,
	}, personalScriptsDiagnostics{Pre: pre, Post: post}
}

func personalScriptDiagnosticFromRuntime(
	key string,
	daemonUID int,
	in health.DaemonRuntimeScript,
) (personalScriptDiagnostic, bool) {
	state := personalScriptState(in.State)
	switch state {
	case personalScriptNotConfigured,
		personalScriptReady,
		personalScriptReadyWithWarning,
		personalScriptRefused:
	default:
		return personalScriptDiagnostic{}, false
	}
	// Not named "components": this file imports internal/ui/components, and a local
	// of that name shadows the package for the rest of the function.
	var pathComponents []personalScriptPathComponent
	for _, component := range in.Components {
		pathComponents = append(pathComponents, personalScriptPathComponent{
			Path: component.Path,
			UID:  component.UID,
			Mode: os.FileMode(component.Mode),
		})
	}
	return personalScriptDiagnostic{
		Key:        key,
		Path:       in.Path,
		State:      state,
		Reason:     in.Reason,
		DaemonUID:  daemonUID,
		Components: pathComponents,
	}, true
}

func comparePersonalScript(
	runtime daemonRuntimeDiagnostic,
	currentConfigPath string,
	running personalScriptDiagnostic,
	current personalScriptDiagnostic,
) personalScriptComparison {
	comparison := personalScriptComparison{Running: running, Current: current}
	// The CURRENT side is checked first and on purpose. With no configuration to
	// read there is nothing to compare against, whatever the daemon is doing, and
	// the config-path arm below would otherwise measure the daemon's real path
	// against an empty one and blame the daemon for the operator's unreadable file.
	if current.State == personalScriptUnknown {
		comparison.Synchronization = personalScriptCurrentUnavailable
		comparison.SyncReason = current.Reason
		return comparison
	}
	if runtime.Availability == daemonRuntimeNotApplicable {
		comparison.Synchronization = personalScriptSyncNotApplicable
		comparison.SyncReason = "no live daemon exists; current configuration is prospective"
		return comparison
	}
	if runtime.Availability != daemonRuntimeAvailable {
		comparison.Synchronization = personalScriptRuntimeUnavailable
		comparison.SyncReason = runtime.Reason
		return comparison
	}
	if filepath.Clean(runtime.ConfigPath) != filepath.Clean(currentConfigPath) ||
		running.Path != current.Path {
		comparison.Synchronization = personalScriptConfigurationDrift
		comparison.SyncReason = "restart the daemon to apply current personal-script configuration"
		return comparison
	}
	if running.State != current.State || running.Reason != current.Reason ||
		!reflect.DeepEqual(running.Components, current.Components) {
		comparison.Synchronization = personalScriptPathStateChanged
		comparison.SyncReason = "path ownership or mode changed after daemon startup"
		return comparison
	}
	comparison.Synchronization = personalScriptInSync
	return comparison
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
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, fmt.Errorf("parse effective uid %q: %w", fields[2], err)
		}
		if uid < 0 {
			return 0, fmt.Errorf("effective uid %q must not be negative", fields[2])
		}
		return uid, nil
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

	if diagnostics.Runtime.Availability == daemonRuntimeAvailable {
		logger.Info("Running daemon configuration: %s", daemonDiagnosticText(diagnostics.Runtime.ConfigPath))
		logger.Info("Running daemon loaded at: %s", time.Unix(diagnostics.Runtime.StartTS, 0).Format(time.RFC3339))
	} else if diagnostics.Runtime.Availability != daemonRuntimeNotApplicable {
		logger.Warning("Running daemon personal-script state: UNAVAILABLE (%s)", daemonDiagnosticText(diagnostics.Runtime.Reason))
	}
	logPersonalScriptComparison(logger, "Personal pre-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Pre)
	logPersonalScriptComparison(logger, "Personal post-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Post)
	logging.DebugStep(logger, "daemon diagnostics", "daemon uid: value=%d source=%q fallback_reason=%q",
		diagnostics.DaemonUID.Value,
		daemonDiagnosticText(diagnostics.DaemonUID.Source),
		daemonDiagnosticText(diagnostics.DaemonUID.FallbackReason))
	logPersonalScriptEvidence(logger, "running daemon pre-run", diagnostics.ScriptComparisons.Pre.Running)
	logPersonalScriptEvidence(logger, "current config pre-run", diagnostics.ScriptComparisons.Pre.Current)
	logPersonalScriptEvidence(logger, "running daemon post-run", diagnostics.ScriptComparisons.Post.Running)
	logPersonalScriptEvidence(logger, "current config post-run", diagnostics.ScriptComparisons.Post.Current)
}

func logPersonalScriptComparison(logger *logging.Logger, label string, runtime daemonRuntimeDiagnostic, comparison personalScriptComparison) {
	logger.Info("%s:", label)
	switch runtime.Availability {
	case daemonRuntimeAvailable:
		logPersonalScriptDiagnostic(logger, "  Running daemon", comparison.Running)
	case daemonRuntimeNotApplicable:
		logger.Info("  Running daemon: NOT RUNNING")
	default:
		logger.Warning("  Running daemon state: UNAVAILABLE (%s)", daemonDiagnosticText(runtime.Reason))
	}
	logPersonalScriptDiagnostic(logger, "  Current configuration", comparison.Current)
	logPersonalScriptSynchronization(logger, comparison)
}

func logPersonalScriptSynchronization(logger *logging.Logger, comparison personalScriptComparison) {
	reason := daemonDiagnosticText(comparison.SyncReason)
	switch comparison.Synchronization {
	case personalScriptInSync:
		logger.Info("  Synchronization: IN SYNC")
	case personalScriptConfigurationDrift:
		logger.Warning("  Synchronization: OUT OF SYNC (%s)", reason)
	case personalScriptPathStateChanged:
		logger.Warning("  Synchronization: PATH STATE CHANGED SINCE STARTUP (%s)", reason)
	case personalScriptRuntimeUnavailable, personalScriptCurrentUnavailable:
		logger.Warning("  Synchronization: UNKNOWN (%s)", reason)
	case personalScriptSyncNotApplicable:
		logger.Info("  Synchronization: NOT APPLICABLE")
	default:
		logger.Warning("  Synchronization: UNKNOWN")
	}
}

func logPersonalScriptDiagnostic(logger *logging.Logger, label string, diagnostic personalScriptDiagnostic) {
	path := daemonDiagnosticText(diagnostic.Path)
	reason := daemonDiagnosticText(diagnostic.Reason)
	switch diagnostic.State {
	case personalScriptReady:
		logger.Info("%s: READY (%s)", label, path)
	case personalScriptReadyWithWarning:
		logger.Warning("%s: READY WITH WARNING (%s): %s", label, path, reason)
	case personalScriptRefused:
		if path == "" {
			logger.Warning("%s: REFUSED: %s", label, reason)
			return
		}
		logger.Warning("%s: REFUSED (%s): %s", label, path, reason)
	case personalScriptUnknown:
		logger.Warning("%s: UNKNOWN: %s", label, reason)
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
