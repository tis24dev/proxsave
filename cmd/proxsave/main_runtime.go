// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func printVersionHeader(bootstrap *logging.BootstrapLogger, toolVersion string) {
	bootstrap.Println("===========================================")
	bootstrap.Println("  ProxSave - Go Version")
	bootstrap.Printf("  Version: %s", toolVersion)
	if sig := buildSignature(); sig != "" {
		bootstrap.Printf("  Build Signature: %s", sig)
	}
	bootstrap.Println("===========================================")
	bootstrap.Println("")
}

func detectAndPrintEnvironment(bootstrap *logging.BootstrapLogger) *environment.EnvironmentInfo {
	bootstrap.Println("Detecting Proxmox environment...")
	envInfo, err := environment.Detect()
	if err != nil {
		bootstrap.Warning("WARNING: %v", err)
		bootstrap.Println("Continuing with limited functionality...")
	}
	bootstrap.Printf("✓ Proxmox Type: %s", envInfo.Type)
	if envInfo.Type == types.ProxmoxDual {
		bootstrap.Printf("  PVE Version: %s", envInfo.PVEVersion)
		bootstrap.Printf("  PBS Version: %s", envInfo.PBSVersion)
	} else {
		bootstrap.Printf("  Version: %s", envInfo.Version)
	}
	bootstrap.Println("")
	return envInfo
}

func bootstrapRuntime(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, envInfo *environment.EnvironmentInfo, toolVersion string) (*appRuntime, int, bool) {
	rt := &appRuntime{
		ctx:              ctx,
		args:             args,
		bootstrap:        bootstrap,
		deps:             defaultAppDeps(),
		envInfo:          envInfo,
		toolVersion:      toolVersion,
		sessionLogCloser: func() {},
	}

	cfg, initialEnvBaseDir, autoFound, exitCode, ok := loadRunConfig(args, bootstrap)
	if !ok {
		return nil, exitCode, false
	}
	rt.cfg = cfg
	rt.initialEnvBaseDir = initialEnvBaseDir
	rt.autoBaseDirFound = autoFound
	rt.dryRun = args.DryRun || cfg.DryRun

	if exitCode, ok := validateRunConfig(rt); !ok {
		return nil, exitCode, false
	}

	rt.logLevel = resolveRunLogLevel(args, cfg, bootstrap)
	rt.logger = initializeRunLogger(rt)
	initializeRunLogFile(rt)
	bootstrap.Flush(rt.logger)
	rt.updateInfo = checkForUpdates(ctx, rt.logger, toolVersion)
	applyRunPermissions(rt)
	initializeRunProfiling(rt)
	rt.unprivilegedInfo = environment.DetectUnprivilegedContainer()
	return rt, types.ExitSuccess.Int(), true
}

func loadRunConfig(args *cli.Args, bootstrap *logging.BootstrapLogger) (*config.Config, string, bool, int, bool) {
	autoBaseDir, autoFound := detectBaseDir()
	if autoBaseDir == "" {
		autoBaseDir = fallbackBaseDir()
	}

	initialEnvBaseDir := os.Getenv("BASE_DIR")
	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return nil, "", false, types.ExitConfigError.Int(), false
	}

	bootstrap.Printf("Loading configuration from: %s", args.ConfigPath)
	logging.DebugStepBootstrap(bootstrap, "main run", "loading configuration")
	cfg, err := config.LoadConfigWithBaseDir(args.ConfigPath, autoBaseDir)
	if err != nil {
		bootstrap.Error("ERROR: Failed to load configuration: %v", err)
		return nil, "", false, types.ExitConfigError.Int(), false
	}
	_ = os.Setenv("BASE_DIR", cfg.BaseDir)
	bootstrap.Println("✓ Configuration loaded successfully")
	return cfg, initialEnvBaseDir, autoFound, types.ExitSuccess.Int(), true
}

func validateRunConfig(rt *appRuntime) (int, bool) {
	printDryRunBootstrapStatus(rt)
	if err := validateFutureFeatures(rt.cfg); err != nil {
		rt.bootstrap.Error("ERROR: Invalid configuration: %v", err)
		return types.ExitConfigError.Int(), false
	}
	warnLogPathConfiguration(rt.cfg, rt.bootstrap)
	runNetworkPreflight(rt.cfg, rt.bootstrap)
	return types.ExitSuccess.Int(), true
}

func printDryRunBootstrapStatus(rt *appRuntime) {
	if rt.dryRun {
		if rt.args.DryRun {
			rt.bootstrap.Println("⚠ DRY RUN MODE (enabled via --dry-run flag)")
		} else {
			rt.bootstrap.Println("⚠ DRY RUN MODE (enabled via DRY_RUN config)")
		}
	}
	rt.bootstrap.Println("")
}

func warnLogPathConfiguration(cfg *config.Config, bootstrap *logging.BootstrapLogger) {
	if strings.TrimSpace(cfg.LogPath) == "" {
		bootstrap.Warning("WARNING: LOG_PATH is empty - file logging disabled, using stdout only")
	}
	if cfg.SecondaryEnabled && strings.TrimSpace(cfg.SecondaryLogPath) == "" {
		bootstrap.Warning("WARNING: Secondary storage enabled but SECONDARY_LOG_PATH is empty - secondary log copy and cleanup will be disabled for this run")
	}
	if cfg.CloudEnabled && strings.TrimSpace(cfg.CloudLogPath) == "" {
		bootstrap.Warning("WARNING: Cloud storage enabled but CLOUD_LOG_PATH is empty - cloud log copy and cleanup will be disabled for this run")
	}
}

func runNetworkPreflight(cfg *config.Config, bootstrap *logging.BootstrapLogger) {
	needs, reasons := featuresNeedNetwork(cfg)
	if !needs {
		return
	}
	if cfg.DisableNetworkPreflight {
		bootstrap.Warning("WARNING: Network preflight disabled via DISABLE_NETWORK_PREFLIGHT; features: %s", strings.Join(reasons, ", "))
		return
	}
	if err := checkInternetConnectivity(networkPreflightTimeout); err != nil {
		bootstrap.Warning("WARNING: Network connectivity unavailable for: %s. %v", strings.Join(reasons, ", "), err)
		bootstrap.Warning("WARNING: Disabling network-dependent features for this run")
		disableNetworkFeaturesForRun(cfg, bootstrap)
	}
}

func resolveRunLogLevel(args *cli.Args, cfg *config.Config, bootstrap *logging.BootstrapLogger) types.LogLevel {
	logLevel := cfg.DebugLevel
	if args.Support {
		bootstrap.Println("Support mode enabled: forcing log level to DEBUG")
		logLevel = types.LogLevelDebug
	} else if args.LogLevel != types.LogLevelNone {
		logLevel = args.LogLevel
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "log_level=%s", logLevel.String())
	return logLevel
}

func initializeRunLogger(rt *appRuntime) *logging.Logger {
	logger := logging.New(rt.logLevel, rt.cfg.UseColor)
	if rt.args.Restore {
		logger = initializeRestoreSessionLogger(rt, logger)
	}
	logging.SetDefaultLogger(logger)
	rt.bootstrap.SetLevel(rt.logLevel)
	return logger
}

func initializeRestoreSessionLogger(rt *appRuntime, fallback *logging.Logger) *logging.Logger {
	logging.DebugStepBootstrap(rt.bootstrap, "main run", "restore log enabled")
	restoreLogger, restoreLogPath, closeFn, err := logging.StartSessionLogger("restore", rt.logLevel, rt.cfg.UseColor)
	if err != nil {
		rt.bootstrap.Warning("WARNING: Unable to start restore log: %v", err)
		return fallback
	}
	rt.sessionLogCloser = closeFn
	rt.bootstrap.Info("Restore log: %s", restoreLogPath)
	_ = os.Setenv("LOG_FILE", restoreLogPath)
	return restoreLogger
}

func initializeRunLogFile(rt *appRuntime) {
	rt.hostname = resolveHostname()
	rt.startTime = rt.deps.now()
	rt.timestampStr = rt.startTime.Format("20060102-150405")
	if rt.args.Restore {
		return
	}

	logFileName := fmt.Sprintf("backup-%s-%s.log", rt.hostname, rt.timestampStr)
	logFilePath := filepath.Join(rt.cfg.LogPath, logFileName)
	if err := os.MkdirAll(rt.cfg.LogPath, defaultDirPerm); err != nil {
		logging.Warning("Failed to create log directory %s: %v", rt.cfg.LogPath, err)
		return
	}
	if err := rt.logger.OpenLogFile(logFilePath); err != nil {
		logging.Warning("Failed to open log file %s: %v", logFilePath, err)
		return
	}
	logging.Info("Log file opened: %s", logFilePath)
	_ = os.Setenv("LOG_FILE", logFilePath)
}

func applyRunPermissions(rt *appRuntime) {
	if !rt.cfg.SetBackupPermissions {
		return
	}
	logging.DebugStep(rt.logger, "main", "applying backup permissions")
	if err := applyBackupPermissions(rt.cfg, rt.logger); err != nil {
		logging.Warning("Failed to apply backup permissions: %v", err)
	}
}

func initializeRunProfiling(rt *appRuntime) {
	if !rt.cfg.ProfilingEnabled {
		return
	}
	cpuProfilePath := filepath.Join(rt.cfg.LogPath, fmt.Sprintf("cpu-%s-%s.pprof", rt.hostname, rt.timestampStr))
	f, err := os.Create(cpuProfilePath)
	if err != nil {
		logging.Warning("Failed to create CPU profile file: %v", err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		logging.Warning("Failed to start CPU profiling: %v", err)
		_ = f.Close()
		return
	}
	rt.cpuProfileFile = f
	logging.Info("CPU profiling enabled: %s", cpuProfilePath)
	rt.heapProfilePath = buildHeapProfilePath(rt)
}

func buildHeapProfilePath(rt *appRuntime) string {
	tmpProfileDir := filepath.Join("/tmp", "proxsave")
	if err := os.MkdirAll(tmpProfileDir, defaultDirPerm); err != nil {
		logging.Warning("Failed to create temp profile directory %s: %v", tmpProfileDir, err)
		return ""
	}
	return filepath.Join(tmpProfileDir, fmt.Sprintf("heap-%s-%s.pprof", rt.hostname, rt.timestampStr))
}

// checkGoRuntimeVersion ensures the running binary was built with at least the specified Go version (semver: major.minor.patch).
func checkGoRuntimeVersion(minimum string) error {
	rt := runtime.Version() // e.g., "go1.25.10"
	// Normalize versions to x.y.z
	parse := func(v string) (int, int, int) {
		// Accept forms: go1.25.10, go1.25, 1.25.10, 1.25
		v = strings.TrimPrefix(v, "go")
		parts := strings.Split(v, ".")
		toInt := func(s string) int { n, _ := strconv.Atoi(s); return n }
		major, minor, patch := 0, 0, 0
		if len(parts) > 0 {
			major = toInt(parts[0])
		}
		if len(parts) > 1 {
			minor = toInt(parts[1])
		}
		if len(parts) > 2 {
			patch = toInt(parts[2])
		}
		return major, minor, patch
	}

	rtMaj, rtMin, rtPatch := parse(rt)
	minMaj, minMin, minPatch := parse(minimum)

	meetsMinimum := func(aMaj, aMin, aPatch, bMaj, bMin, bPatch int) bool {
		if aMaj != bMaj {
			return aMaj > bMaj
		}
		if aMin != bMin {
			return aMin > bMin
		}
		return aPatch >= bPatch
	}

	if !meetsMinimum(rtMaj, rtMin, rtPatch, minMaj, minMin, minPatch) {
		return fmt.Errorf("go runtime version %s is below required %s — rebuild with go %s or set GOTOOLCHAIN=auto", rt, "go"+minimum, "go"+minimum)
	}
	return nil
}
