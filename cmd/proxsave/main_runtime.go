// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

func printVersionHeader(bootstrap *logging.BootstrapLogger, toolVersion string) {
	bootstrap.Println("===========================================")
	bootstrap.Println("  ProxSave")
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

// redetectHostBackupEnvironment re-runs Proxmox detection under SYSTEM_ROOT_PREFIX
// once config is loaded, so an HA-LXC backup appliance reports the mounted host
// (issue #255) rather than the container the pre-config best-effort detection saw.
// The corrected envInfo is what reaches the collector, backup phases, stats and the
// archive manifest, and detection must run before collection (a ProxmoxUnknown type
// skips all PVE/PBS collection and silently under-restores), so it is fixed here
// before any of those consume it. A non-empty prefix alone drives this; the
// HOST_BACKUP_MODE flag is not required for the detection correctness fix.
func redetectHostBackupEnvironment(rt *appRuntime) {
	prefix := strings.TrimSpace(rt.cfg.SystemRootPrefix)
	if prefix == "" || prefix == string(filepath.Separator) {
		if rt.cfg.HostBackupMode {
			rt.bootstrap.Warning("WARNING: HOST_BACKUP_MODE is set but SYSTEM_ROOT_PREFIX is empty; host-backup mode has no effect")
		}
		return
	}

	// Under a prefix the container ProxSave runs in is NOT the backup target: the
	// target is the Proxmox host mounted under the prefix. DetectHostUnderPrefix uses
	// ONLY the re-anchored filesystem markers (the same ones bare-metal PVE/PBS
	// detection uses; host command probes are skipped) and never returns nil or a
	// live-container type. Its result REPLACES envInfo unconditionally, so the
	// pre-config live-container type can never survive a prefix run and mislabel a
	// hollow archive as a valid backup.
	liveType := types.ProxmoxUnknown
	if rt.envInfo != nil {
		liveType = rt.envInfo.Type
	}
	info := environment.DetectHostUnderPrefix(prefix)
	rt.envInfo = info

	if info.Type == types.ProxmoxUnknown {
		// Fail closed: PVE/PBS collectors are skipped and the manifest records
		// "unknown" instead of a false type over an empty tree. Warn when the
		// operator signaled host intent: either HOST_BACKUP_MODE is set, or the live
		// system itself IS a Proxmox (a Proxmox host/container pointed at a prefix
		// with no Proxmox under it is a mis-mount). A plain prefix run
		// (chroot/snapshot/CI) on a non-Proxmox live system stays quiet to avoid spam.
		switch {
		case rt.cfg.HostBackupMode:
			rt.bootstrap.Warning("WARNING: no Proxmox host detected under SYSTEM_ROOT_PREFIX=%s; the host filesystem may not be mounted (archive labeled unknown)", prefix)
		case liveType.SupportsPVE() || liveType.SupportsPBS():
			rt.bootstrap.Warning("WARNING: %s detected on the live system but none under SYSTEM_ROOT_PREFIX=%s; the host filesystem may not be mounted (archive labeled unknown)", liveType, prefix)
		default:
			rt.bootstrap.Debug("no Proxmox detected under SYSTEM_ROOT_PREFIX=%s; archive labeled unknown", prefix)
		}
		return
	}
	if rt.cfg.HostBackupMode {
		rt.bootstrap.Printf("✓ Proxmox Type (host-backup mode, %s): %s", prefix, info.Type)
	} else {
		rt.bootstrap.Printf("✓ Proxmox Type (prefix %s): %s", prefix, info.Type)
	}
	if info.Type == types.ProxmoxDual {
		rt.bootstrap.Printf("  PVE Version: %s", info.PVEVersion)
		rt.bootstrap.Printf("  PBS Version: %s", info.PBSVersion)
	} else {
		rt.bootstrap.Printf("  Version: %s", info.Version)
	}
	rt.bootstrap.Println("")
	warnHostBackupMountShape(rt, prefix, info.Type)
}

// warnHostBackupMountShape surfaces the most likely host-backup misconfiguration:
// the host root is mounted (mp0) but the pmxcfs bind (mp1 /etc/pve) is not, so
// cluster and Ceph configuration would be silently missing. It only warns; a
// partial mount never fails the backup. It applies only to host-backup mode on a
// PVE or dual host: PBS has no /etc/pve, and a plain prefix (snapshot/chroot) has
// no bind mount to be missing, so neither is warned about.
func warnHostBackupMountShape(rt *appRuntime, prefix string, detected types.ProxmoxType) {
	if !rt.cfg.HostBackupMode {
		return
	}
	if detected != types.ProxmoxVE && detected != types.ProxmoxDual {
		return
	}
	pvePath := filepath.Join(prefix, "etc", "pve")
	if info, err := os.Stat(pvePath); err != nil || !info.IsDir() {
		rt.bootstrap.Warning("WARNING: %s is not present; the /etc/pve bind mount may be missing (cluster and Ceph config will be incomplete)", pvePath)
	}
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
	redetectHostBackupEnvironment(rt)
	rt.initialEnvBaseDir = initialEnvBaseDir
	rt.autoBaseDirFound = autoFound
	rt.dryRun = args.DryRun || cfg.DryRun
	// Make the effective dry-run (CLI flag OR config) visible to every cfg.DryRun
	// consumer, including the security preflight. Without this, a --dry-run *flag*
	// run would still let the preflight create directories / mutate the filesystem
	// because cfg.DryRun only reflects the DRY_RUN config key. This only ever adds
	// the flag's effect (rt.dryRun already ORs in cfg.DryRun).
	rt.cfg.DryRun = rt.dryRun

	if exitCode, ok := validateRunConfig(rt); !ok {
		return nil, exitCode, false
	}

	rt.logLevel = resolveRunLogLevel(args, cfg, bootstrap)
	rt.logger = initializeRunLogger(rt)
	initializeRunLogFile(rt)
	bootstrap.Flush(rt.logger)
	rt.updateInfo = checkForUpdates(ctx, rt.logger, toolVersion)
	maybeWarnWhatsnew(rt.logger, rt.cfg.BaseDir, toolVersion)
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
	if dashboardHandoffPending() {
		// The dashboard's alternate screen is still up: keep the fresh run
		// logger off the console until the flow adopts the session (the
		// adoption lifts the mute; the flow then applies its own).
		logger.SwapOutput(io.Discard)
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
	// Bounded so a dead/stale LOG_PATH mount cannot wedge the run here, before the
	// security preflight even starts, in an uninterruptible syscall.
	if err := safefs.MkdirAll(rt.ctx, rt.cfg.LogPath, defaultDirPerm, fsIoTimeoutDuration(rt.cfg)); err != nil {
		logging.Warning("Failed to create log directory %s: %v", rt.cfg.LogPath, err)
		return
	}
	// Bound the log-file open/write/close on the run logger so a dead/stale LOG_PATH
	// mount cannot wedge the run (the open here, or any later O_SYNC write under the
	// logger mutex). Session loggers (local /tmp) keep the unbounded default.
	rt.logger.SetIOTimeout(fsIoTimeoutDuration(rt.cfg))
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
	if err := applyBackupPermissions(rt.ctx, rt.cfg, rt.logger, rt.dryRun); err != nil {
		logging.Warning("Failed to apply backup permissions: %v", err)
	}
}

// profileBaseDir is the local base dir for pprof artifacts. Both the CPU and heap
// profiles live under <profileBaseDir>/proxsave, never on LOG_PATH, so a dead/stale
// LOG_PATH mount cannot wedge the create, the runtime's periodic CPU-sample writes,
// StopCPUProfile's flush, or Close (issue #242). A var (not const) so tests redirect it.
var profileBaseDir = "/tmp"

func initializeRunProfiling(rt *appRuntime) {
	if !rt.cfg.ProfilingEnabled {
		return
	}
	profileDir := buildProfileDir()
	if profileDir == "" {
		return // could not create the local profile dir; skip profiling (best-effort)
	}
	cpuProfilePath := filepath.Join(profileDir, fmt.Sprintf("cpu-%s-%s.pprof", rt.hostname, rt.timestampStr))
	f, err := createProfileFile(cpuProfilePath)
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
	rt.heapProfilePath = filepath.Join(profileDir, fmt.Sprintf("heap-%s-%s.pprof", rt.hostname, rt.timestampStr))
}

// buildProfileDir creates and returns the local temp directory used for BOTH the cpu
// and heap pprof output (<profileBaseDir>/proxsave). It is intentionally OFF LOG_PATH
// so a dead/stale LOG_PATH mount can never wedge profiling I/O. Returns "" (after a
// warning) if the directory cannot be created or fails the safety guard, signalling
// the caller to skip profiling (best-effort; the backup flow is never affected).
func buildProfileDir() string {
	profileDir := filepath.Join(profileBaseDir, "proxsave")
	if err := os.MkdirAll(profileDir, defaultDirPerm); err != nil {
		logging.Warning("Failed to create temp profile directory %s: %v", profileDir, err)
		return ""
	}
	if err := validateProfileDir(profileDir); err != nil {
		logging.Warning("Refusing unsafe profile directory %s: %v", profileDir, err)
		return ""
	}
	return profileDir
}

// profileEUID is a seam so tests can force an owner mismatch in validateProfileDir.
var profileEUID = func() int { return os.Geteuid() }

// validateProfileDir rejects a profile directory that a local unprivileged user
// could have interposed before us (F02-06, CWE-59). /tmp is world-writable + sticky,
// so on a first run the attacker may pre-create <base>/proxsave as a symlink to a dir
// they control, or as a directory they own, and MkdirAll would accept both. Reject a
// symlink, a non-directory, a foreign-owned dir, or a group/other-writable dir (where
// they could plant symlink files).
func validateProfileDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("profile dir is a symlink: %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("profile path is not a directory: %s", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != profileEUID() {
			return fmt.Errorf("profile dir owner uid %d != euid %d: %s", st.Uid, profileEUID(), dir)
		}
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("profile dir is group/other-writable (%#o): %s", info.Mode().Perm(), dir)
	}
	return nil
}

// createProfileFile opens a pprof output file refusing to follow symlinks or to
// truncate a pre-existing file (F02-06, CWE-59). The profile filenames are
// predictable and /tmp is world-writable, so O_EXCL (no pre-existing symlink/file)
// plus O_NOFOLLOW (final component not a symlink) close the truncate vector. 0600
// keeps the artifacts root-only. The create is additionally confined to the
// profile directory via os.Root (structural gosec G304): O_EXCL over a
// pre-existing symlink returns EEXIST, an escaping symlink is refused, and
// os.Root still accepts O_NOFOLLOW (kept as belt-and-suspenders).
func createProfileFile(path string) (*os.File, error) {
	return safefs.OpenFileUnderRoot(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
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
		return fmt.Errorf("go runtime version %s is below required %s; rebuild with go %s or set GOTOOLCHAIN=auto", rt, "go"+minimum, "go"+minimum)
	}
	return nil
}
