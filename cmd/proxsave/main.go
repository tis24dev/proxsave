package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/security"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/types"
	buildinfo "github.com/tis24dev/proxsave/internal/version"
)

const (
	defaultLegacyEnvPath          = "/opt/proxsave/env/backup.env"
	legacyEnvFallbackPath         = "/opt/proxmox-backup/env/backup.env"
	goRuntimeMinVersion           = "1.25.5"
	networkPreflightTimeout       = 2 * time.Second
	bytesPerMegabyte        int64 = 1024 * 1024
	defaultDirPerm                = 0o755
	exitCodeInterrupted           = 128 + int(syscall.SIGINT)
)

// Build-time variables (injected via ldflags)
var (
	buildTime = "" // Will be set during compilation via -ldflags "-X main.buildTime=..."
)

func main() {
	os.Exit(run())
}

var closeStdinOnce sync.Once

func run() int {
	bootstrap := logging.NewBootstrapLogger()

	// Resolve the effective tool version once for the entire run.
	toolVersion := buildinfo.String()
	runDone := logging.DebugStartBootstrap(bootstrap, "main run", "version=%s", toolVersion)

	finalExitCode := types.ExitSuccess.Int()
	showSummary := false
	finalize := func(code int) int {
		finalExitCode = code
		return code
	}

	// Track early errors that occur before backup starts
	// This ensures notifications are sent even for initialization/config errors
	var earlyErrorState *orchestrator.EarlyErrorState
	var orch *orchestrator.Orchestrator
	var pendingSupportStats *orchestrator.BackupStats

	defer func() {
		logging.DebugStepBootstrap(bootstrap, "main run", "exit_code=%d", finalExitCode)
		runDone(nil)
		if r := recover(); r != nil {
			stack := debug.Stack()
			bootstrap.Error("PANIC: %v", r)
			fmt.Fprintf(os.Stderr, "panic: %v\n%s\n", r, stack)
			os.Exit(types.ExitPanicError.Int())
		}
	}()

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tui.SetAbortContext(ctx)

	// Handle SIGINT (Ctrl+C) and SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logging.DebugStepBootstrap(bootstrap, "signal", "received=%v", sig)
		bootstrap.Info("\nReceived signal %v, initiating graceful shutdown...", sig)
		cancel() // Cancel context to stop all operations
		closeStdinOnce.Do(func() {
			if file := os.Stdin; file != nil {
				_ = file.Close()
			}
		})
	}()

	// Parse command-line arguments
	args := cli.Parse()
	logging.DebugStepBootstrap(bootstrap, "main run", "args parsed")

	// Handle version flag
	if args.ShowVersion {
		cli.ShowVersion()
		return types.ExitSuccess.Int()
	}

	// Handle help flag
	if args.ShowHelp {
		cli.ShowHelp()
		return types.ExitSuccess.Int()
	}

	if args.CleanupGuards {
		incompatible := make([]string, 0, 8)
		if args.Support {
			incompatible = append(incompatible, "--support")
		}
		if args.Restore {
			incompatible = append(incompatible, "--restore")
		}
		if args.Decrypt {
			incompatible = append(incompatible, "--decrypt")
		}
		if args.Install {
			incompatible = append(incompatible, "--install")
		}
		if args.NewInstall {
			incompatible = append(incompatible, "--new-install")
		}
		if args.Upgrade {
			incompatible = append(incompatible, "--upgrade")
		}
		if args.ForceNewKey {
			incompatible = append(incompatible, "--newkey")
		}
		if args.EnvMigration || args.EnvMigrationDry {
			incompatible = append(incompatible, "--env-migration/--env-migration-dry-run")
		}
		if args.UpgradeConfig || args.UpgradeConfigDry {
			incompatible = append(incompatible, "--upgrade-config/--upgrade-config-dry-run")
		}

		if len(incompatible) > 0 {
			bootstrap.Error("--cleanup-guards cannot be combined with: %s", strings.Join(incompatible, ", "))
			return types.ExitConfigError.Int()
		}

		level := types.LogLevelInfo
		if args.LogLevel != types.LogLevelNone {
			level = args.LogLevel
		}
		logger := logging.New(level, false)

		if err := orchestrator.CleanupMountGuards(ctx, logger, args.DryRun); err != nil {
			bootstrap.Error("ERROR: %v", err)
			return types.ExitGenericError.Int()
		}
		return types.ExitSuccess.Int()
	}

	// Validate support mode compatibility with other CLI modes
	logging.DebugStepBootstrap(bootstrap, "main run", "support_mode=%v", args.Support)
	if args.Support {
		incompatible := make([]string, 0, 6)
		if args.Restore {
			// allowed
		}
		if args.Decrypt {
			incompatible = append(incompatible, "--decrypt")
		}
		if args.Install {
			incompatible = append(incompatible, "--install")
		}
		if args.NewInstall {
			incompatible = append(incompatible, "--new-install")
		}
		if args.EnvMigration || args.EnvMigrationDry {
			incompatible = append(incompatible, "--env-migration")
		}
		if args.UpgradeConfig || args.UpgradeConfigDry {
			incompatible = append(incompatible, "--upgrade-config")
		}
		if args.ForceNewKey {
			incompatible = append(incompatible, "--newkey")
		}

		if len(incompatible) > 0 {
			bootstrap.Error("Support mode cannot be combined with: %s", strings.Join(incompatible, ", "))
			bootstrap.Error("--support is only available for the standard backup run or --restore.")
			return types.ExitConfigError.Int()
		}
	}

	if args.Install && args.NewInstall {
		bootstrap.Error("Cannot use --install and --new-install together. Choose one installation mode.")
		return types.ExitConfigError.Int()
	}

	if args.Upgrade && (args.Install || args.NewInstall) {
		bootstrap.Error("Cannot use --upgrade together with --install or --new-install.")
		return types.ExitConfigError.Int()
	}

	// Resolve configuration path relative to the executable's base directory so
	// that configs/ is located consistently next to the binary, regardless of
	// the current working directory.
	logging.DebugStepBootstrap(bootstrap, "main run", "resolving config path")
	resolvedConfigPath, err := resolveInstallConfigPath(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}
	args.ConfigPath = resolvedConfigPath

	// Dedicated upgrade mode (download latest binary, no config changes)
	if args.Upgrade {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade")
		return runUpgrade(ctx, args, bootstrap)
	}

	newKeyCLI := args.ForceCLI
	// Dedicated new key mode (no backup run)
	if args.ForceNewKey {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=newkey cli=%v", newKeyCLI)
		flowLogLevel := types.LogLevelInfo
		if args.LogLevel != types.LogLevelNone {
			flowLogLevel = args.LogLevel
		}
		if err := runNewKey(ctx, args.ConfigPath, flowLogLevel, bootstrap, newKeyCLI); err != nil {
			if isInstallAbortedError(err) || errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
				return types.ExitSuccess.Int()
			}
			bootstrap.Error("ERROR: %v", err)
			return types.ExitConfigError.Int()
		}
		return types.ExitSuccess.Int()
	}

	decryptCLI := args.ForceCLI
	if args.Decrypt {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=decrypt cli=%v", decryptCLI)
		if err := runDecryptWorkflowOnly(ctx, args.ConfigPath, bootstrap, toolVersion, decryptCLI); err != nil {
			if errors.Is(err, orchestrator.ErrDecryptAborted) {
				bootstrap.Info("Decrypt workflow aborted by user")
				return types.ExitSuccess.Int()
			}
			bootstrap.Error("ERROR: %v", err)
			return types.ExitGenericError.Int()
		}
		bootstrap.Info("Decrypt workflow completed successfully")
		return types.ExitSuccess.Int()
	}

	newInstallCLI := args.ForceCLI
	if args.NewInstall {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=new-install cli=%v", newInstallCLI)
		flowLogLevel := types.LogLevelInfo
		if args.LogLevel != types.LogLevelNone {
			flowLogLevel = args.LogLevel
		}
		sessionLogger, cleanupSessionLog := startFlowSessionLog("new-install", flowLogLevel, bootstrap)
		defer cleanupSessionLog()
		if sessionLogger != nil {
			sessionLogger.Info("Starting --new-install (config=%s)", args.ConfigPath)
		}
		if err := runNewInstall(ctx, args.ConfigPath, bootstrap, newInstallCLI); err != nil {
			if sessionLogger != nil {
				if isInstallAbortedError(err) {
					sessionLogger.Warning("new-install aborted by user: %v", err)
				} else {
					sessionLogger.Error("new-install failed: %v", err)
				}
			}
			// Interactive aborts (Ctrl+C, explicit cancel) are treated as a graceful exit
			// and already summarized by the install footer.
			if isInstallAbortedError(err) {
				return types.ExitSuccess.Int()
			}
			bootstrap.Error("ERROR: %v", err)
			return types.ExitConfigError.Int()
		}
		if sessionLogger != nil {
			sessionLogger.Info("new-install completed successfully")
		}
		return types.ExitSuccess.Int()
	}

	// Handle configuration upgrade dry-run (plan-only, no writes).
	if args.UpgradeConfigDry {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade-config-dry")
		if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
			bootstrap.Error("ERROR: %v", err)
			return types.ExitConfigError.Int()
		}

		bootstrap.Printf("Planning configuration upgrade using embedded template: %s", args.ConfigPath)
		result, err := config.PlanUpgradeConfigFile(args.ConfigPath)
		if err != nil {
			bootstrap.Error("ERROR: Failed to plan configuration upgrade: %v", err)
			return types.ExitConfigError.Int()
		}
		if !result.Changed {
			bootstrap.Println("Configuration is already up to date with the embedded template; no changes are required.")
			return types.ExitSuccess.Int()
		}

		if len(result.MissingKeys) > 0 {
			bootstrap.Printf("Missing keys that would be added from the template (%d): %s",
				len(result.MissingKeys), strings.Join(result.MissingKeys, ", "))
		}
		if result.PreservedValues > 0 {
			bootstrap.Printf("Existing values that would be preserved: %d", result.PreservedValues)
		}
		if len(result.ExtraKeys) > 0 {
			bootstrap.Printf("Custom keys that would be preserved (not present in template) (%d): %s",
				len(result.ExtraKeys), strings.Join(result.ExtraKeys, ", "))
		}
		bootstrap.Println("Dry run only: no files were modified. Use --upgrade-config to apply these changes.")
		return types.ExitSuccess.Int()
	}

	// Handle install wizard (runs before normal execution)
	if args.Install {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=install cli=%v", args.ForceCLI)
		flowLogLevel := types.LogLevelInfo
		if args.LogLevel != types.LogLevelNone {
			flowLogLevel = args.LogLevel
		}
		sessionLogger, cleanupSessionLog := startFlowSessionLog("install", flowLogLevel, bootstrap)
		defer cleanupSessionLog()
		if sessionLogger != nil {
			sessionLogger.Info("Starting --install (config=%s)", args.ConfigPath)
		}

		var err error
		if args.ForceCLI {
			err = runInstall(ctx, args.ConfigPath, bootstrap)
		} else {
			err = runInstallTUI(ctx, args.ConfigPath, bootstrap)
		}

		if err != nil {
			if sessionLogger != nil {
				if isInstallAbortedError(err) {
					sessionLogger.Warning("install aborted by user: %v", err)
				} else {
					sessionLogger.Error("install failed: %v", err)
				}
			}
			// Interactive aborts (Ctrl+C, explicit cancel) are treated as a graceful exit
			// and already summarized by the install footer.
			if isInstallAbortedError(err) {
				return types.ExitSuccess.Int()
			}
			bootstrap.Error("ERROR: %v", err)
			return types.ExitConfigError.Int()
		}
		if sessionLogger != nil {
			sessionLogger.Info("install completed successfully")
		}
		return types.ExitSuccess.Int()
	}

	// Pre-flight: enforce Go runtime version
	if err := checkGoRuntimeVersion(goRuntimeMinVersion); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitEnvironmentError.Int()
	}

	// Print header
	bootstrap.Println("===========================================")
	bootstrap.Println("  ProxSave - Go Version")
	bootstrap.Printf("  Version: %s", toolVersion)
	if sig := buildSignature(); sig != "" {
		bootstrap.Printf("  Build Signature: %s", sig)
	}
	bootstrap.Println("===========================================")
	bootstrap.Println("")

	// Detect Proxmox environment
	bootstrap.Println("Detecting Proxmox environment...")
	envInfo, err := environment.Detect()
	if err != nil {
		bootstrap.Warning("WARNING: %v", err)
		bootstrap.Println("Continuing with limited functionality...")
	}
	bootstrap.Printf("✓ Proxmox Type: %s", envInfo.Type)
	bootstrap.Printf("  Version: %s", envInfo.Version)
	bootstrap.Println("")

	// Handle configuration upgrade (schema-aware merge with embedded template).
	if args.UpgradeConfig {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade-config")
		if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
			bootstrap.Error("ERROR: %v", err)
			return types.ExitConfigError.Int()
		}

		bootstrap.Printf("Upgrading configuration file: %s", args.ConfigPath)
		result, err := config.UpgradeConfigFile(args.ConfigPath)
		if err != nil {
			bootstrap.Error("ERROR: Failed to upgrade configuration: %v", err)
			return types.ExitConfigError.Int()
		}
		if !result.Changed {
			bootstrap.Println("Configuration is already up to date with the embedded template; no changes were made.")
			return types.ExitSuccess.Int()
		}

		bootstrap.Println("Configuration upgraded successfully!")
		if len(result.MissingKeys) > 0 {
			bootstrap.Printf("- Added %d missing key(s): %s",
				len(result.MissingKeys), strings.Join(result.MissingKeys, ", "))
		} else {
			bootstrap.Println("- No new keys were required from the template")
		}
		if result.PreservedValues > 0 {
			bootstrap.Printf("- Preserved %d existing value(s) from current configuration", result.PreservedValues)
		}
		if len(result.ExtraKeys) > 0 {
			bootstrap.Printf("- Kept %d custom key(s) not present in the template: %s",
				len(result.ExtraKeys), strings.Join(result.ExtraKeys, ", "))
		}
		if result.BackupPath != "" {
			bootstrap.Printf("- Backup saved to: %s", result.BackupPath)
		}
		bootstrap.Println("✓ Configuration upgrade completed successfully.")
		return types.ExitSuccess.Int()
	}

	if args.EnvMigrationDry {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=env-migration-dry")
		return runEnvMigrationDry(ctx, args, bootstrap)
	}

	if args.EnvMigration {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=env-migration")
		return runEnvMigration(ctx, args, bootstrap)
	}

	// Support mode: interactive pre-flight questionnaire (mandatory)
	if args.Support {
		logging.DebugStepBootstrap(bootstrap, "main run", "mode=support")
		meta, continueRun, interrupted := support.RunIntro(ctx, bootstrap)
		if continueRun {
			args.SupportGitHubUser = meta.GitHubUser
			args.SupportIssueID = meta.IssueID
		} else {
			if interrupted {
				// Interrupted by signal (Ctrl+C): set exit code and still show footer.
				finalize(exitCodeInterrupted)
				printFinalSummary(finalExitCode)
				return finalExitCode
			}
			// Graceful abort (user declined support flow) - show standard footer.
			finalize(types.ExitGenericError.Int())
			printFinalSummary(finalExitCode)
			return finalExitCode
		}
	}

	// Load configuration
	autoBaseDir, autoFound := detectBaseDir()
	if autoBaseDir == "" {
		if _, err := os.Stat("/opt/proxsave"); err == nil {
			autoBaseDir = "/opt/proxsave"
		} else {
			autoBaseDir = "/opt/proxmox-backup"
		}
	}
	initialEnvBaseDir := os.Getenv("BASE_DIR")
	if initialEnvBaseDir == "" {
		_ = os.Setenv("BASE_DIR", autoBaseDir)
	}

	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	bootstrap.Printf("Loading configuration from: %s", args.ConfigPath)
	logging.DebugStepBootstrap(bootstrap, "main run", "loading configuration")
	cfg, err := config.LoadConfig(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: Failed to load configuration: %v", err)
		return types.ExitConfigError.Int()
	}
	if cfg.BaseDir == "" {
		cfg.BaseDir = autoBaseDir
	}
	_ = os.Setenv("BASE_DIR", cfg.BaseDir)
	bootstrap.Println("✓ Configuration loaded successfully")

	// Show dry-run status early in bootstrap phase
	dryRun := args.DryRun || cfg.DryRun
	if dryRun {
		if args.DryRun {
			bootstrap.Println("⚠ DRY RUN MODE (enabled via --dry-run flag)")
		} else {
			bootstrap.Println("⚠ DRY RUN MODE (enabled via DRY_RUN config)")
		}
	}
	bootstrap.Println("")

	if err := validateFutureFeatures(cfg); err != nil {
		bootstrap.Error("ERROR: Invalid configuration: %v", err)
		return types.ExitConfigError.Int()
	}

	// Validate log path configuration early to avoid "cosmetic only" logging.
	// If a log feature is enabled but its path is empty, disable the path-driven
	// behavior and document the detection to the user.
	if strings.TrimSpace(cfg.LogPath) == "" {
		bootstrap.Warning("WARNING: LOG_PATH is empty - file logging disabled, using stdout only")
	}
	if cfg.SecondaryEnabled && strings.TrimSpace(cfg.SecondaryLogPath) == "" {
		bootstrap.Warning("WARNING: Secondary storage enabled but SECONDARY_LOG_PATH is empty - secondary log copy and cleanup will be disabled for this run")
	}
	if cfg.CloudEnabled && strings.TrimSpace(cfg.CloudLogPath) == "" {
		bootstrap.Warning("WARNING: Cloud storage enabled but CLOUD_LOG_PATH is empty - cloud log copy and cleanup will be disabled for this run")
	}

	// Pre-flight: if features require network, verify basic connectivity
	if needs, reasons := featuresNeedNetwork(cfg); needs {
		if cfg.DisableNetworkPreflight {
			bootstrap.Warning("WARNING: Network preflight disabled via DISABLE_NETWORK_PREFLIGHT; features: %s", strings.Join(reasons, ", "))
		} else {
			if err := checkInternetConnectivity(networkPreflightTimeout); err != nil {
				bootstrap.Warning("WARNING: Network connectivity unavailable for: %s. %v", strings.Join(reasons, ", "), err)
				bootstrap.Warning("WARNING: Disabling network-dependent features for this run")
				disableNetworkFeaturesForRun(cfg, bootstrap)
			}
		}
	}

	// Determine log level (CLI overrides config)
	logLevel := cfg.DebugLevel
	if args.Support {
		bootstrap.Println("Support mode enabled: forcing log level to DEBUG")
		logLevel = types.LogLevelDebug
	} else if args.LogLevel != types.LogLevelNone {
		logLevel = args.LogLevel
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "log_level=%s", logLevel.String())

	// Initialize logger with configuration
	logger := logging.New(logLevel, cfg.UseColor)
	sessionLogActive := false
	sessionLogCloser := func() {}
	if args.Restore {
		logging.DebugStepBootstrap(bootstrap, "main run", "restore log enabled")
		if restoreLogger, restoreLogPath, closeFn, err := logging.StartSessionLogger("restore", logLevel, cfg.UseColor); err == nil {
			logger = restoreLogger
			sessionLogCloser = closeFn
			sessionLogActive = true
			bootstrap.Info("Restore log: %s", restoreLogPath)
			_ = os.Setenv("LOG_FILE", restoreLogPath)
		} else {
			bootstrap.Warning("WARNING: Unable to start restore log: %v", err)
		}
	}

	logging.SetDefaultLogger(logger)
	bootstrap.SetLevel(logLevel)

	// Open log file for real-time writing (will be closed after notifications)
	hostname := resolveHostname()
	startTime := time.Now()
	timestampStr := startTime.Format("20060102-150405")

	if sessionLogActive {
		defer sessionLogCloser()
	} else {
		logFileName := fmt.Sprintf("backup-%s-%s.log", hostname, timestampStr)
		logFilePath := filepath.Join(cfg.LogPath, logFileName)

		// Ensure log directory exists
		if err := os.MkdirAll(cfg.LogPath, defaultDirPerm); err != nil {
			logging.Warning("Failed to create log directory %s: %v", cfg.LogPath, err)
		} else {
			if err := logger.OpenLogFile(logFilePath); err != nil {
				logging.Warning("Failed to open log file %s: %v", logFilePath, err)
			} else {
				logging.Info("Log file opened: %s", logFilePath)
				// Store log path in environment for backup stats
				_ = os.Setenv("LOG_FILE", logFilePath)
			}
		}
	}

	// Flush bootstrap logs into the main logger now that log files (if any)
	// are attached, so that early banners and messages appear at the top
	// of the corresponding log.
	bootstrap.Flush(logger)

	// Best-effort check for newer releases on GitHub.
	// If the installed version is up to date, nothing is printed at INFO/WARNING level
	// (only a DEBUG message is logged). If a newer version exists, a WARNING is emitted
	// suggesting the use of --upgrade.
	updateInfo := checkForUpdates(ctx, logger, toolVersion)

	// Apply backup permissions (optional, Bash-compatible behavior)
	if cfg.SetBackupPermissions {
		logging.DebugStep(logger, "main", "applying backup permissions")
		if err := applyBackupPermissions(cfg, logger); err != nil {
			logging.Warning("Failed to apply backup permissions: %v", err)
		}
	}

	// Optional CPU/heap profiling (pprof) - controlled by PROFILING_ENABLED
	var cpuProfileFile *os.File
	var heapProfilePath string
	if cfg.ProfilingEnabled {
		cpuProfilePath := filepath.Join(cfg.LogPath, fmt.Sprintf("cpu-%s-%s.pprof", hostname, timestampStr))
		f, err := os.Create(cpuProfilePath)
		if err != nil {
			logging.Warning("Failed to create CPU profile file: %v", err)
		} else {
			if err := pprof.StartCPUProfile(f); err != nil {
				logging.Warning("Failed to start CPU profiling: %v", err)
				_ = f.Close()
			} else {
				cpuProfileFile = f
				logging.Info("CPU profiling enabled: %s", cpuProfilePath)

				tmpProfileDir := filepath.Join("/tmp", "proxsave")
				if err := os.MkdirAll(tmpProfileDir, defaultDirPerm); err != nil {
					logging.Warning("Failed to create temp profile directory %s: %v", tmpProfileDir, err)
				} else {
					heapProfilePath = filepath.Join(tmpProfileDir, fmt.Sprintf("heap-%s-%s.pprof", hostname, timestampStr))
				}
			}
		}
	}

	defer func() {
		if showSummary {
			printFinalSummary(finalExitCode)
		}
	}()

	// Defer for network rollback countdown (LIFO: executes BEFORE footer)
	defer func() {
		if finalExitCode == exitCodeInterrupted {
			if abortInfo := orchestrator.GetLastRestoreAbortInfo(); abortInfo != nil {
				printNetworkRollbackCountdown(abortInfo)
			}
		}
	}()

	defer func() {
		if !args.Support || pendingSupportStats == nil {
			return
		}
		logging.Step("Support mode - sending support email with attached log")
		support.SendEmail(ctx, cfg, logger, envInfo.Type, pendingSupportStats, support.Meta{
			GitHubUser: args.SupportGitHubUser,
			IssueID:    args.SupportIssueID,
		}, buildSignature())
	}()

	// Defer for early error notifications
	// This executes BEFORE the footer defer (LIFO order)
	// Ensures notifications are sent even for errors that occur before backup starts
	defer func() {
		if earlyErrorState != nil && earlyErrorState.HasError() && orch != nil {
			fmt.Println()
			logging.Step("Sending error notifications")
			stats := orch.DispatchEarlyErrorNotification(ctx, earlyErrorState)
			if stats != nil {
				pendingSupportStats = stats
			}
			orch.FinalizeAndCloseLog(ctx)
		}
	}()

	defer func() {
		if cpuProfileFile != nil {
			pprof.StopCPUProfile()
			_ = cpuProfileFile.Close()
		}
		if heapProfilePath != "" {
			if f, err := os.Create(heapProfilePath); err == nil {
				if err := pprof.WriteHeapProfile(f); err != nil {
					logging.Warning("Failed to write heap profile: %v", err)
				}
				_ = f.Close()
			} else {
				logging.Warning("Failed to create heap profile file: %v", err)
			}
		}
	}()

	defer cleanupAfterRun(logger)
	showSummary = true

	// Log dry-run status in main logger (already shown in bootstrap)
	if dryRun {
		if args.DryRun {
			logging.Info("DRY RUN MODE: No actual changes will be made (enabled via --dry-run flag)")
		} else {
			logging.Info("DRY RUN MODE: No actual changes will be made (enabled via DRY_RUN config)")
		}
	}

	// Determine base directory source for logging
	baseDirSource := "default fallback"
	if rawBaseDir, ok := cfg.Get("BASE_DIR"); ok && strings.TrimSpace(rawBaseDir) != "" {
		baseDirSource = "configured in backup.env"
	} else if initialEnvBaseDir != "" {
		baseDirSource = "from environment (BASE_DIR)"
	} else if autoFound {
		baseDirSource = "auto-detected from executable path"
	}

	// Log environment info
	logging.Info("Environment: %s %s", envInfo.Type, envInfo.Version)
	logging.Info("Backup enabled: %v", cfg.BackupEnabled)
	logging.Info("Debug level: %s", logLevel.String())
	logging.Info("Compression: %s (level %d, mode %s)", cfg.CompressionType, cfg.CompressionLevel, cfg.CompressionMode)
	logging.Info("Base directory: %s (%s)", cfg.BaseDir, baseDirSource)
	configSource := args.ConfigPathSource
	if configSource == "" {
		configSource = "configured path"
	}
	logging.Info("Configuration file: %s (%s)", args.ConfigPath, configSource)

	var identityInfo *identity.Info
	serverIDValue := strings.TrimSpace(cfg.ServerID)
	serverMACValue := ""
	telegramServerStatus := "Telegram disabled"
	if info, err := identity.Detect(cfg.BaseDir, logger); err != nil {
		logging.Warning("WARNING: Failed to load server identity: %v", err)
		identityInfo = info
	} else {
		identityInfo = info
	}

	if identityInfo != nil {
		if identityInfo.ServerID != "" {
			serverIDValue = identityInfo.ServerID
		}
		if identityInfo.PrimaryMAC != "" {
			serverMACValue = identityInfo.PrimaryMAC
		}
	}

	if serverIDValue != "" && cfg.ServerID == "" {
		cfg.ServerID = serverIDValue
	}

	logServerIdentityValues(serverIDValue, serverMACValue)
	logTelegramInfo := true
	if cfg.TelegramEnabled {
		if strings.EqualFold(cfg.TelegramBotType, "centralized") {
			logging.Debug("Contacting remote Telegram server...")
			logging.Debug("Telegram server URL: %s", cfg.TelegramServerAPIHost)
			status := notify.CheckTelegramRegistration(ctx, cfg.TelegramServerAPIHost, serverIDValue, logger)
			if status.Error != nil {
				logging.Warning("Telegram: %s", status.Message)
				logging.Debug("Telegram connection error: %v", status.Error)
				logging.Skip("Telegram: disabled")
				cfg.TelegramEnabled = false
				logTelegramInfo = false
			} else {
				logging.Debug("Remote server contacted: Bot token / chat ID verified (handshake)")
			}
			telegramServerStatus = status.Message
		} else {
			telegramServerStatus = "Personal mode - no remote contact"
		}
	}
	if logTelegramInfo {
		logging.Info("Server Telegram: %s", telegramServerStatus)
	}
	fmt.Println()

	execInfo := getExecInfo()
	execPath := execInfo.ExecPath
	logging.DebugStep(logger, "main", "running security checks")
	if _, secErr := security.Run(ctx, logger, cfg, args.ConfigPath, execPath, envInfo); secErr != nil {
		logging.Error("Security checks failed: %v", secErr)
		return finalize(types.ExitSecurityError.Int())
	}
	fmt.Println()

	restoreCLI := args.ForceCLI
	if args.Restore {
		logging.DebugStep(logger, "main", "mode=restore cli=%v", restoreCLI)
		if restoreCLI {
			logging.Info("Restore mode enabled - starting CLI workflow...")
			if err := orchestrator.RunRestoreWorkflow(ctx, cfg, logger, toolVersion); err != nil {
				if errors.Is(err, orchestrator.ErrRestoreAborted) {
					logging.Warning("Restore workflow aborted by user")
					if args.Support {
						pendingSupportStats = support.BuildSupportStats(logger, resolveHostname(), envInfo.Type, envInfo.Version, toolVersion, startTime, time.Now(), exitCodeInterrupted, "restore")
					}
					return finalize(exitCodeInterrupted)
				}
				logging.Error("Restore workflow failed: %v", err)
				if args.Support {
					pendingSupportStats = support.BuildSupportStats(logger, resolveHostname(), envInfo.Type, envInfo.Version, toolVersion, startTime, time.Now(), types.ExitGenericError.Int(), "restore")
				}
				return finalize(types.ExitGenericError.Int())
			}
			if logger.HasWarnings() {
				logging.Warning("Restore workflow completed with warnings (see log above)")
			} else {
				logging.Info("Restore workflow completed successfully")
			}
			if args.Support {
				pendingSupportStats = support.BuildSupportStats(logger, resolveHostname(), envInfo.Type, envInfo.Version, toolVersion, startTime, time.Now(), types.ExitSuccess.Int(), "restore")
			}
			return finalize(types.ExitSuccess.Int())
		}

		logging.Info("Restore mode enabled - starting interactive workflow...")
		sig := buildSignature()
		if strings.TrimSpace(sig) == "" {
			sig = "n/a"
		}
		if err := orchestrator.RunRestoreWorkflowTUI(ctx, cfg, logger, toolVersion, args.ConfigPath, sig); err != nil {
			if errors.Is(err, orchestrator.ErrRestoreAborted) || errors.Is(err, orchestrator.ErrDecryptAborted) {
				logging.Warning("Restore workflow aborted by user")
				if args.Support {
					pendingSupportStats = support.BuildSupportStats(logger, resolveHostname(), envInfo.Type, envInfo.Version, toolVersion, startTime, time.Now(), exitCodeInterrupted, "restore")
				}
				return finalize(exitCodeInterrupted)
			}
			logging.Error("Restore workflow failed: %v", err)
			if args.Support {
				pendingSupportStats = support.BuildSupportStats(logger, resolveHostname(), envInfo.Type, envInfo.Version, toolVersion, startTime, time.Now(), types.ExitGenericError.Int(), "restore")
			}
			return finalize(types.ExitGenericError.Int())
		}
		if logger.HasWarnings() {
			logging.Warning("Restore workflow completed with warnings (see log above)")
		} else {
			logging.Info("Restore workflow completed successfully")
		}
		if args.Support {
			pendingSupportStats = support.BuildSupportStats(logger, resolveHostname(), envInfo.Type, envInfo.Version, toolVersion, startTime, time.Now(), types.ExitSuccess.Int(), "restore")
		}
		return finalize(types.ExitSuccess.Int())
	}

	if args.Decrypt {
		logging.DebugStep(logger, "main", "mode=decrypt cli=%v", decryptCLI)
		if decryptCLI {
			logging.Info("Decrypt mode enabled - starting CLI workflow...")
			if err := orchestrator.RunDecryptWorkflow(ctx, cfg, logger, toolVersion); err != nil {
				if errors.Is(err, orchestrator.ErrDecryptAborted) {
					logging.Info("Decrypt workflow aborted by user")
					return finalize(types.ExitSuccess.Int())
				}
				logging.Error("Decrypt workflow failed: %v", err)
				return finalize(types.ExitGenericError.Int())
			}
			logging.Info("Decrypt workflow completed successfully")
		} else {
			logging.Info("Decrypt mode enabled - starting interactive workflow...")
			sig := buildSignature()
			if strings.TrimSpace(sig) == "" {
				sig = "n/a"
			}
			if err := orchestrator.RunDecryptWorkflowTUI(ctx, cfg, logger, toolVersion, args.ConfigPath, sig); err != nil {
				if errors.Is(err, orchestrator.ErrDecryptAborted) {
					logging.Info("Decrypt workflow aborted by user")
					return finalize(types.ExitSuccess.Int())
				}
				logging.Error("Decrypt workflow failed: %v", err)
				return finalize(types.ExitGenericError.Int())
			}
			logging.Info("Decrypt workflow completed successfully")
		}
		return finalize(types.ExitSuccess.Int())
	}

	// Initialize orchestrator
	logging.Step("Initializing backup orchestrator")
	orchInitDone := logging.DebugStart(logger, "orchestrator init", "dry_run=%v", dryRun)
	orch = orchestrator.New(logger, dryRun)
	orch.SetVersion(toolVersion)
	orch.SetConfig(cfg)
	orch.SetIdentity(serverIDValue, serverMACValue)
	orch.SetProxmoxVersion(envInfo.Version)
	orch.SetStartTime(startTime)
	if updateInfo != nil {
		orch.SetUpdateInfo(updateInfo.NewVersion, updateInfo.Current, updateInfo.Latest)
	}

	// Configure backup paths and compression
	excludePatterns := append([]string(nil), cfg.ExcludePatterns...)
	excludePatterns = addPathExclusion(excludePatterns, cfg.BackupPath)
	if cfg.SecondaryEnabled {
		excludePatterns = addPathExclusion(excludePatterns, cfg.SecondaryPath)
	}
	if cfg.CloudEnabled && isLocalPath(cfg.CloudRemote) {
		excludePatterns = addPathExclusion(excludePatterns, cfg.CloudRemote)
	}

	orch.SetBackupConfig(
		cfg.BackupPath,
		cfg.LogPath,
		cfg.CompressionType,
		cfg.CompressionLevel,
		cfg.CompressionThreads,
		cfg.CompressionMode,
		excludePatterns,
	)

	orch.SetOptimizationConfig(backup.OptimizationConfig{
		EnableChunking:            cfg.EnableSmartChunking,
		EnableDeduplication:       cfg.EnableDeduplication,
		EnablePrefilter:           cfg.EnablePrefilter,
		ChunkSizeBytes:            int64(cfg.ChunkSizeMB) * bytesPerMegabyte,
		ChunkThresholdBytes:       int64(cfg.ChunkThresholdMB) * bytesPerMegabyte,
		PrefilterMaxFileSizeBytes: int64(cfg.PrefilterMaxFileSizeMB) * bytesPerMegabyte,
	})

	if err := orch.EnsureAgeRecipientsReady(ctx); err != nil {
		orchInitDone(err)
		if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			logging.Warning("Encryption setup aborted by user. Exiting...")
			earlyErrorState = &orchestrator.EarlyErrorState{
				Phase:     "encryption_setup",
				Error:     err,
				ExitCode:  types.ExitGenericError,
				Timestamp: time.Now(),
			}
			return finalize(types.ExitGenericError.Int())
		}
		logging.Error("ERROR: %v", err)
		earlyErrorState = &orchestrator.EarlyErrorState{
			Phase:     "encryption_setup",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}
		return finalize(types.ExitConfigError.Int())
	}
	orchInitDone(nil)

	logging.Info("✓ Orchestrator initialized")
	fmt.Println()

	// Verify directories
	logging.Step("Verifying directory structure")
	checkDir := func(name, path string) {
		ensureDirectoryExists(logger, name, path)
	}

	checkDir("Backup directory", cfg.BackupPath)
	checkDir("Log directory", cfg.LogPath)
	if cfg.SecondaryEnabled {
		secondaryLogPath := strings.TrimSpace(cfg.SecondaryLogPath)
		if secondaryLogPath != "" {
			checkDir("Secondary log directory", secondaryLogPath)
		} else {
			logging.Warning("✗ Secondary log directory not configured (secondary storage enabled)")
		}
	}
	if cfg.CloudEnabled {
		cloudLogPath := strings.TrimSpace(cfg.CloudLogPath)
		if cloudLogPath == "" {
			logging.Warning("✗ Cloud log directory not configured (cloud storage enabled)")
		} else if strings.Contains(cloudLogPath, ":") {
			// Legacy format with explicit remote (e.g., "gdrive:/logs")
			logging.Info("Cloud log path (legacy): %s", cloudLogPath)
		} else {
			// New format without remote - will use CLOUD_REMOTE (e.g., "/logs")
			remoteName := extractRemoteName(cfg.CloudRemote)
			if remoteName != "" {
				logging.Info("Cloud log path: %s (using remote: %s)", cloudLogPath, remoteName)
			} else {
				logging.Warning("Cloud log path %s requires CLOUD_REMOTE to be set", cloudLogPath)
			}
		}
	}
	checkDir("Lock directory", cfg.LockPath)

	// Initialize pre-backup checker
	logging.Debug("Configuring pre-backup validation checks...")
	checkerConfig := checks.GetDefaultCheckerConfig(cfg.BackupPath, cfg.LogPath, cfg.LockPath)
	checkerConfig.SecondaryEnabled = cfg.SecondaryEnabled
	if cfg.SecondaryEnabled && strings.TrimSpace(cfg.SecondaryPath) != "" {
		checkerConfig.SecondaryPath = cfg.SecondaryPath
	} else {
		checkerConfig.SecondaryPath = ""
	}
	checkerConfig.CloudEnabled = cfg.CloudEnabled
	if cfg.CloudEnabled && strings.TrimSpace(cfg.CloudRemote) != "" {
		if isLocalPath(cfg.CloudRemote) {
			checkerConfig.CloudPath = cfg.CloudRemote
		} else {
			checkerConfig.CloudPath = ""
			logging.Info("Skipping cloud disk-space check: %s is a remote rclone path (no local mount detected)", cfg.CloudRemote)
		}
	} else {
		checkerConfig.CloudPath = ""
	}
	checkerConfig.MinDiskPrimaryGB = cfg.MinDiskPrimaryGB
	checkerConfig.MinDiskSecondaryGB = cfg.MinDiskSecondaryGB
	checkerConfig.MinDiskCloudGB = cfg.MinDiskCloudGB
	checkerConfig.DryRun = dryRun
	checkerDone := logging.DebugStart(logger, "pre-backup check config", "dry_run=%v", dryRun)
	if err := checkerConfig.Validate(); err != nil {
		checkerDone(err)
		logging.Error("Invalid checker configuration: %v", err)
		earlyErrorState = &orchestrator.EarlyErrorState{
			Phase:     "checker_config",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}
		return finalize(types.ExitConfigError.Int())
	}
	checkerDone(nil)
	checker := checks.NewChecker(logger, checkerConfig)
	orch.SetChecker(checker)

	// Ensure lock is released on exit
	defer func() {
		if err := orch.ReleaseBackupLock(); err != nil {
			logging.Warning("Failed to release backup lock: %v", err)
		}
	}()

	logging.Info("✓ Pre-backup checks configured")
	fmt.Println()

	// Initialize storage backends
	logging.Step("Initializing storage backends")
	storageDone := logging.DebugStart(logger, "storage init", "primary=%s secondary=%v cloud=%v", cfg.BackupPath, cfg.SecondaryEnabled, cfg.CloudEnabled)

	// Primary (local) storage - always enabled
	logging.DebugStep(logger, "storage init", "primary backend")
	localBackend, err := storage.NewLocalStorage(cfg, logger)
	if err != nil {
		storageDone(err)
		logging.Error("Failed to initialize local storage: %v", err)
		earlyErrorState = &orchestrator.EarlyErrorState{
			Phase:     "storage_init",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}
		return finalize(types.ExitConfigError.Int())
	}
	localFS, err := detectFilesystemInfo(ctx, localBackend, cfg.BackupPath, logger)
	if err != nil {
		storageDone(err)
		logging.Error("Failed to prepare primary storage: %v", err)
		earlyErrorState = &orchestrator.EarlyErrorState{
			Phase:     "storage_init",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}
		return finalize(types.ExitConfigError.Int())
	}
	logging.DebugStep(logger, "storage init", "primary filesystem=%s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))
	logging.Info("Path Primary: %s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))

	localStats := fetchStorageStats(ctx, localBackend, logger, "Local storage")
	localBackups := fetchBackupList(ctx, localBackend)
	logging.DebugStep(logger, "storage init", "primary stats=%v backups=%d", localStats != nil, len(localBackups))

	localAdapter := orchestrator.NewStorageAdapter(localBackend, logger, cfg)
	localAdapter.SetFilesystemInfo(localFS)
	localAdapter.SetInitialStats(localStats)
	orch.RegisterStorageTarget(localAdapter)
	logStorageInitSummary(formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, localStats, localBackups))

	// Secondary storage - optional
	var secondaryFS *storage.FilesystemInfo
	if cfg.SecondaryEnabled {
		logging.DebugStep(logger, "storage init", "secondary backend")
		secondaryBackend, err := storage.NewSecondaryStorage(cfg, logger)
		if err != nil {
			logging.Warning("Failed to initialize secondary storage: %v", err)
			logging.Info("Path Secondary: %s", formatDetailedFilesystemLabel(cfg.SecondaryPath, nil))
		} else {
			secondaryFS, _ = detectFilesystemInfo(ctx, secondaryBackend, cfg.SecondaryPath, logger)
			logging.DebugStep(logger, "storage init", "secondary filesystem=%s", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS))
			logging.Info("Path Secondary: %s", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS))
			secondaryStats := fetchStorageStats(ctx, secondaryBackend, logger, "Secondary storage")
			secondaryBackups := fetchBackupList(ctx, secondaryBackend)
			logging.DebugStep(logger, "storage init", "secondary stats=%v backups=%d", secondaryStats != nil, len(secondaryBackups))
			secondaryAdapter := orchestrator.NewStorageAdapter(secondaryBackend, logger, cfg)
			secondaryAdapter.SetFilesystemInfo(secondaryFS)
			secondaryAdapter.SetInitialStats(secondaryStats)
			orch.RegisterStorageTarget(secondaryAdapter)
			logStorageInitSummary(formatStorageInitSummary("Secondary storage", cfg, storage.LocationSecondary, secondaryStats, secondaryBackups))
		}
	} else {
		logging.Skip("Path Secondary: disabled")
	}

	// Cloud storage - optional
	var cloudFS *storage.FilesystemInfo
	if cfg.CloudEnabled {
		logging.DebugStep(logger, "storage init", "cloud backend")
		cloudBackend, err := storage.NewCloudStorage(cfg, logger)
		if err != nil {
			logging.Warning("Failed to initialize cloud storage: %v", err)
			logging.Info("Path Cloud: %s", formatDetailedFilesystemLabel(cfg.CloudRemote, nil))
			logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, nil, nil))
		} else {
			cloudFS, _ = detectFilesystemInfo(ctx, cloudBackend, cfg.CloudRemote, logger)
			if cloudFS == nil {
				logging.DebugStep(logger, "storage init", "cloud unavailable, disabling")
				cfg.CloudEnabled = false
				cfg.CloudLogPath = ""
				if checker != nil {
					checker.DisableCloud()
				}
				logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, nil, nil))
				logging.Skip("Path Cloud: disabled")
			} else {
				logging.DebugStep(logger, "storage init", "cloud filesystem=%s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
				logging.Info("Path Cloud: %s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
				cloudStats := fetchStorageStats(ctx, cloudBackend, logger, "Cloud storage")
				cloudBackups := fetchBackupList(ctx, cloudBackend)
				logging.DebugStep(logger, "storage init", "cloud stats=%v backups=%d", cloudStats != nil, len(cloudBackups))
				cloudAdapter := orchestrator.NewStorageAdapter(cloudBackend, logger, cfg)
				cloudAdapter.SetFilesystemInfo(cloudFS)
				cloudAdapter.SetInitialStats(cloudStats)
				orch.RegisterStorageTarget(cloudAdapter)
				logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, cloudStats, cloudBackups))
			}
		}
	} else {
		logging.Skip("Path Cloud: disabled")
	}
	storageDone(nil)

	fmt.Println()

	// Initialize notification channels
	logging.Step("Initializing notification channels")
	notifyDone := logging.DebugStart(logger, "notifications init", "")

	// Email notifications
	if cfg.EmailEnabled {
		logging.DebugStep(logger, "notifications init", "email enabled")
		emailConfig := notify.EmailConfig{
			Enabled:          true,
			DeliveryMethod:   notify.EmailDeliveryMethod(cfg.EmailDeliveryMethod),
			FallbackSendmail: cfg.EmailFallbackSendmail,
			Recipient:        cfg.EmailRecipient,
			From:             cfg.EmailFrom,
			CloudRelayConfig: notify.CloudRelayConfig{
				WorkerURL:   cfg.CloudflareWorkerURL,
				WorkerToken: cfg.CloudflareWorkerToken,
				HMACSecret:  cfg.CloudflareHMACSecret,
				Timeout:     cfg.WorkerTimeout,
				MaxRetries:  cfg.WorkerMaxRetries,
				RetryDelay:  cfg.WorkerRetryDelay,
			},
		}
		emailNotifier, err := notify.NewEmailNotifier(emailConfig, envInfo.Type, logger)
		if err != nil {
			logging.Warning("Failed to initialize Email notifier: %v", err)
		} else {
			emailAdapter := orchestrator.NewNotificationAdapter(emailNotifier, logger)
			orch.RegisterNotificationChannel(emailAdapter)
			logging.Info("✓ Email initialized (method: %s)", cfg.EmailDeliveryMethod)
		}
	} else {
		logging.DebugStep(logger, "notifications init", "email disabled")
		logging.Skip("Email: disabled")
	}

	// Telegram notifications
	if cfg.TelegramEnabled {
		logging.DebugStep(logger, "notifications init", "telegram enabled (mode=%s)", cfg.TelegramBotType)
		telegramConfig := notify.TelegramConfig{
			Enabled:       true,
			Mode:          notify.TelegramMode(cfg.TelegramBotType),
			BotToken:      cfg.TelegramBotToken,
			ChatID:        cfg.TelegramChatID,
			ServerAPIHost: cfg.TelegramServerAPIHost,
			ServerID:      cfg.ServerID,
		}
		telegramNotifier, err := notify.NewTelegramNotifier(telegramConfig, logger)
		if err != nil {
			logging.Warning("Failed to initialize Telegram notifier: %v", err)
		} else {
			telegramAdapter := orchestrator.NewNotificationAdapter(telegramNotifier, logger)
			orch.RegisterNotificationChannel(telegramAdapter)
			logging.Info("✓ Telegram initialized (mode: %s)", cfg.TelegramBotType)
		}
	} else {
		logging.DebugStep(logger, "notifications init", "telegram disabled")
		logging.Skip("Telegram: disabled")
	}

	// Gotify notifications
	if cfg.GotifyEnabled {
		logging.DebugStep(logger, "notifications init", "gotify enabled")
		gotifyConfig := notify.GotifyConfig{
			Enabled:         true,
			ServerURL:       cfg.GotifyServerURL,
			Token:           cfg.GotifyToken,
			PrioritySuccess: cfg.GotifyPrioritySuccess,
			PriorityWarning: cfg.GotifyPriorityWarning,
			PriorityFailure: cfg.GotifyPriorityFailure,
		}
		gotifyNotifier, err := notify.NewGotifyNotifier(gotifyConfig, logger)
		if err != nil {
			logging.Warning("Failed to initialize Gotify notifier: %v", err)
		} else {
			gotifyAdapter := orchestrator.NewNotificationAdapter(gotifyNotifier, logger)
			orch.RegisterNotificationChannel(gotifyAdapter)
			logging.Info("✓ Gotify initialized")
		}
	} else {
		logging.DebugStep(logger, "notifications init", "gotify disabled")
		logging.Skip("Gotify: disabled")
	}

	// Webhook Notifications
	if cfg.WebhookEnabled {
		logging.DebugStep(logger, "notifications init", "webhook enabled")
		logging.Debug("Initializing webhook notifier...")
		webhookConfig := cfg.BuildWebhookConfig()
		logging.Debug("Webhook config built: %d endpoints configured", len(webhookConfig.Endpoints))

		webhookNotifier, err := notify.NewWebhookNotifier(webhookConfig, logger)
		if err != nil {
			logging.Warning("Failed to initialize Webhook notifier: %v", err)
		} else {
			logging.Debug("Creating webhook notification adapter...")
			webhookAdapter := orchestrator.NewNotificationAdapter(webhookNotifier, logger)

			logging.Debug("Registering webhook notification channel with orchestrator...")
			orch.RegisterNotificationChannel(webhookAdapter)
			logging.Info("✓ Webhook initialized (%d endpoint(s))", len(webhookConfig.Endpoints))
		}
	} else {
		logging.DebugStep(logger, "notifications init", "webhook disabled")
		logging.Skip("Webhook: disabled")
	}
	notifyDone(nil)

	fmt.Println()

	if !cfg.EnableGoBackup && !args.Support {
		logging.Warning("ENABLE_GO_BACKUP=false is ignored; the Go backup pipeline is always used.")
	} else {
		logging.Debug("Go backup pipeline enabled")
	}
	fmt.Println()

	// Storage info
	logging.Info("Storage configuration:")
	logging.Info("  Primary: %s", formatStorageLabel(cfg.BackupPath, localFS))
	if cfg.SecondaryEnabled {
		logging.Info("  Secondary storage: %s", formatStorageLabel(cfg.SecondaryPath, secondaryFS))
	} else {
		logging.Skip("  Secondary storage: disabled")
	}
	if cfg.CloudEnabled {
		logging.Info("  Cloud storage: %s", formatStorageLabel(cfg.CloudRemote, cloudFS))
	} else {
		logging.Skip("  Cloud storage: disabled")
	}
	fmt.Println()

	// Log configuration info
	logging.Info("Log configuration:")
	logging.Info("  Primary: %s", cfg.LogPath)
	if cfg.SecondaryEnabled {
		if strings.TrimSpace(cfg.SecondaryLogPath) != "" {
			logging.Info("  Secondary: %s", cfg.SecondaryLogPath)
		} else {
			logging.Skip("  Secondary: disabled (log path not configured)")
		}
	} else {
		logging.Skip("  Secondary: disabled")
	}
	if cfg.CloudEnabled {
		if strings.TrimSpace(cfg.CloudLogPath) != "" {
			logging.Info("  Cloud: %s", cfg.CloudLogPath)
		} else {
			logging.Skip("  Cloud: disabled (log path not configured)")
		}
	} else {
		logging.Skip("  Cloud: disabled")
	}
	fmt.Println()

	// Notification info
	logging.Info("Notification configuration:")
	logging.Info("  Telegram: %v", cfg.TelegramEnabled)
	logging.Info("  Email: %v", cfg.EmailEnabled)
	logging.Info("  Gotify: %v", cfg.GotifyEnabled)
	logging.Info("  Webhook: %v", cfg.WebhookEnabled)
	logging.Info("  Metrics: %v", cfg.MetricsEnabled)
	fmt.Println()

	// Run backup orchestration
	if cfg.BackupEnabled {
		preCheckDone := logging.DebugStart(logger, "pre-backup checks", "")
		if err := orch.RunPreBackupChecks(ctx); err != nil {
			preCheckDone(err)
			logging.Error("Pre-backup validation failed: %v", err)
			earlyErrorState = &orchestrator.EarlyErrorState{
				Phase:     "pre_backup_checks",
				Error:     err,
				ExitCode:  types.ExitBackupError,
				Timestamp: time.Now(),
			}
			return finalize(types.ExitBackupError.Int())
		}
		preCheckDone(nil)
		fmt.Println()

		logging.Step("Start Go backup orchestration")

		// Get hostname for backup naming
		hostname := resolveHostname()

		// Run Go-based backup (collection + archive)
		backupDone := logging.DebugStart(logger, "backup run", "proxmox=%s host=%s", envInfo.Type, hostname)
		stats, err := orch.RunGoBackup(ctx, envInfo.Type, hostname)
		if err != nil {
			backupDone(err)
			// Check if error is due to cancellation
			if ctx.Err() == context.Canceled {
				logging.Warning("Backup was canceled")
				orch.FinalizeAfterRun(ctx, stats)
				if stats != nil {
					pendingSupportStats = stats
				}
				return finalize(exitCodeInterrupted) // Standard Unix exit code for SIGINT
			}

			// Check if it's a BackupError with specific exit code
			var backupErr *orchestrator.BackupError
			if errors.As(err, &backupErr) {
				logging.Error("Backup %s failed: %v", backupErr.Phase, backupErr.Err)
				orch.FinalizeAfterRun(ctx, stats)
				if stats != nil {
					pendingSupportStats = stats
				}
				return finalize(backupErr.Code.Int())
			}

			// Generic backup error
			logging.Error("Backup orchestration failed: %v", err)
			orch.FinalizeAfterRun(ctx, stats)
			if stats != nil {
				pendingSupportStats = stats
			}
			return finalize(types.ExitBackupError.Int())
		}
		backupDone(nil)

		if err := orch.SaveStatsReport(stats); err != nil {
			logging.Warning("Failed to persist backup statistics: %v", err)
		} else if stats.ReportPath != "" {
			logging.Info("✓ Statistics report saved to %s", stats.ReportPath)
		}

		// Display backup statistics
		fmt.Println()
		logging.Info("=== Backup Statistics ===")
		logging.Info("Files collected: %d", stats.FilesCollected)
		if stats.FilesFailed > 0 {
			logging.Warning("Files failed: %d", stats.FilesFailed)
		}
		logging.Info("Directories created: %d", stats.DirsCreated)
		logging.Info("Data collected: %s", formatBytes(stats.BytesCollected))
		logging.Info("Archive size: %s", formatBytes(stats.ArchiveSize))
		switch {
		case stats.CompressionSavingsPercent > 0:
			logging.Info("Compression ratio: %.1f%%", stats.CompressionSavingsPercent)
		case stats.CompressionRatioPercent > 0:
			logging.Info("Compression ratio: %.1f%%", stats.CompressionRatioPercent)
		case stats.BytesCollected > 0:
			ratio := float64(stats.ArchiveSize) / float64(stats.BytesCollected) * 100
			logging.Info("Compression ratio: %.1f%%", ratio)
		default:
			logging.Info("Compression ratio: N/A")
		}
		logging.Info("Compression used: %s (level %d, mode %s)", stats.Compression, stats.CompressionLevel, stats.CompressionMode)
		if stats.RequestedCompression != stats.Compression {
			logging.Info("Requested compression: %s", stats.RequestedCompression)
		}
		logging.Info("Duration: %s", formatDuration(stats.Duration))
		if stats.BundleCreated {
			logging.Info("Bundle path: %s", stats.ArchivePath)
			logging.Info("Bundle contents: archive + checksum + metadata")
		} else {
			logging.Info("Archive path: %s", stats.ArchivePath)
			if stats.ManifestPath != "" {
				logging.Info("Manifest path: %s", stats.ManifestPath)
			}
			if stats.Checksum != "" {
				logging.Info("Archive checksum (SHA256): %s", stats.Checksum)
			}
		}
		fmt.Println()

		logging.Info("✓ Go backup orchestration completed")
		logServerIdentityValues(serverIDValue, serverMACValue)

		if heapProfilePath != "" {
			logging.Info("Heap profiling saved: %s", heapProfilePath)
		}

		exitCode := stats.ExitCode
		status := notify.StatusFromExitCode(exitCode)
		statusLabel := strings.ToUpper(status.String())
		emoji := notify.GetStatusEmoji(status)
		logging.Info("Exit status: %s %s (code=%d)", emoji, statusLabel, exitCode)

		pendingSupportStats = stats

		finalExitCode = exitCode
	} else {
		logging.Warning("Backup is disabled in configuration")
	}

	return finalExitCode
}

const rollbackCountdownDisplayDuration = 10 * time.Second

func printNetworkRollbackCountdown(abortInfo *orchestrator.RestoreAbortInfo) {
	if abortInfo == nil {
		return
	}

	color := "\033[33m" // yellow
	colorReset := "\033[0m"

	markerExists := false
	if strings.TrimSpace(abortInfo.NetworkRollbackMarker) != "" {
		if _, err := os.Stat(strings.TrimSpace(abortInfo.NetworkRollbackMarker)); err == nil {
			markerExists = true
		}
	}

	status := "UNKNOWN"
	switch {
	case markerExists:
		status = "ARMED (will execute automatically)"
	case !abortInfo.RollbackDeadline.IsZero() && time.Now().After(abortInfo.RollbackDeadline):
		status = "EXECUTED (marker removed)"
	case strings.TrimSpace(abortInfo.NetworkRollbackMarker) != "":
		status = "DISARMED/CLEARED (marker removed before deadline)"
	case abortInfo.NetworkRollbackArmed:
		status = "ARMED (status from snapshot)"
	default:
		status = "NOT ARMED"
	}

	fmt.Println()
	fmt.Printf("%s===========================================\n", color)
	fmt.Printf("NETWORK ROLLBACK%s\n", colorReset)
	fmt.Println()

	// Static info
	fmt.Printf("  Status: %s\n", status)
	if strings.TrimSpace(abortInfo.OriginalIP) != "" && abortInfo.OriginalIP != "unknown" {
		fmt.Printf("  Pre-apply IP (from snapshot): %s\n", strings.TrimSpace(abortInfo.OriginalIP))
	}
	if strings.TrimSpace(abortInfo.CurrentIP) != "" && abortInfo.CurrentIP != "unknown" {
		fmt.Printf("  Post-apply IP (observed): %s\n", strings.TrimSpace(abortInfo.CurrentIP))
	}
	if strings.TrimSpace(abortInfo.NetworkRollbackLog) != "" {
		fmt.Printf("  Rollback log: %s\n", strings.TrimSpace(abortInfo.NetworkRollbackLog))
	}
	fmt.Println()

	switch {
	case markerExists && !abortInfo.RollbackDeadline.IsZero() && time.Until(abortInfo.RollbackDeadline) > 0:
		fmt.Println("Connection will be temporarily interrupted during restore.")
		if strings.TrimSpace(abortInfo.OriginalIP) != "" && abortInfo.OriginalIP != "unknown" {
			fmt.Printf("Remember to reconnect using the pre-apply IP: %s\n", strings.TrimSpace(abortInfo.OriginalIP))
		}
	case !markerExists && !abortInfo.RollbackDeadline.IsZero() && time.Now().After(abortInfo.RollbackDeadline):
		if strings.TrimSpace(abortInfo.OriginalIP) != "" && abortInfo.OriginalIP != "unknown" {
			fmt.Printf("Rollback executed: reconnect using the pre-apply IP: %s\n", strings.TrimSpace(abortInfo.OriginalIP))
		}
	case !markerExists && strings.TrimSpace(abortInfo.NetworkRollbackMarker) != "":
		if strings.TrimSpace(abortInfo.CurrentIP) != "" && abortInfo.CurrentIP != "unknown" {
			fmt.Printf("Rollback will NOT run: reconnect using the post-apply IP: %s\n", strings.TrimSpace(abortInfo.CurrentIP))
		}
	}

	// Live countdown for max 10 seconds (only when rollback is still armed).
	if !markerExists || abortInfo.RollbackDeadline.IsZero() {
		fmt.Printf("%s===========================================%s\n", color, colorReset)
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	displayEnd := time.Now().Add(rollbackCountdownDisplayDuration)

	for {
		remaining := time.Until(abortInfo.RollbackDeadline)
		if remaining <= 0 {
			fmt.Printf("\r  Remaining: Rollback executing now...          \n")
			break
		}
		if time.Now().After(displayEnd) {
			fmt.Printf("\r  Remaining: %ds (exiting, rollback will proceed)\n", int(remaining.Seconds()))
			break
		}
		fmt.Printf("\r  Remaining: %ds   ", int(remaining.Seconds()))

		select {
		case <-ticker.C:
			continue
		}
	}

	fmt.Printf("%s===========================================%s\n", color, colorReset)
}

func printFinalSummary(finalExitCode int) {
	fmt.Println()

	summarySig := buildSignature()
	if summarySig == "" {
		summarySig = "unknown"
	}

	colorReset := "\033[0m"
	color := ""
	logger := logging.GetDefaultLogger()
	hasWarnings := logger != nil && logger.HasWarnings()

	switch {
	case finalExitCode == exitCodeInterrupted:
		color = "\033[35m" // magenta for Ctrl+C
	case finalExitCode == 0 && hasWarnings:
		color = "\033[33m" // yellow for success with warnings
	case finalExitCode == 0:
		color = "\033[32m" // green for clean success
	case finalExitCode == types.ExitGenericError.Int():
		color = "\033[33m" // yellow for generic error (non-fatal)
	default:
		color = "\033[31m" // red for all other errors
	}

	if color != "" {
		fmt.Printf("%s===========================================\n", color)
		fmt.Printf("ProxSave - Go - %s\n", summarySig)
		fmt.Printf("===========================================%s\n", colorReset)
	} else {
		fmt.Println("===========================================")
		fmt.Printf("ProxSave - Go - %s\n", summarySig)
		fmt.Println("===========================================")
	}

	fmt.Println()
	fmt.Println("\033[31mEXTRA STEP - IF YOU FIND THIS TOOL USEFUL AND WANT TO THANK ME, A COFFEE IS ALWAYS WELCOME!\033[0m")
	fmt.Println("https://github.com/sponsors/tis24dev")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  proxsave (alias: proxmox-backup) - Start backup")
	fmt.Println("  --help             - Show all options")
	fmt.Println("  --dry-run          - Test without changes")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep env/identity) then run installer")
	fmt.Println("  --env-migration    - Run installer and migrate legacy Bash backup.env to Go template")
	fmt.Println("  --env-migration-dry-run - Preview installer/migration without writing files")
	fmt.Println("  --upgrade          - Update proxsave binary to latest release (no config changes)")
	fmt.Println("  --newkey           - Generate a new encryption key for backups")
	fmt.Println("  --decrypt          - Decrypt an existing backup archive")
	fmt.Println("  --restore          - Run interactive restore workflow (select bundle, decrypt if needed, apply to system)")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println("  --support          - Run in support mode (force debug log level and send email with attached log to github-support@tis24.it); available for standard backup and --restore")
	fmt.Println()
}

// checkGoRuntimeVersion ensures the running binary was built with at least the specified Go version (semver: major.minor.patch).
func checkGoRuntimeVersion(min string) error {
	rt := runtime.Version() // e.g., "go1.25.4"
	// Normalize versions to x.y.z
	parse := func(v string) (int, int, int) {
		// Accept forms: go1.25.4, go1.25, 1.25.4, 1.25
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
	minMaj, minMin, minPatch := parse(min)

	newer := func(aMaj, aMin, aPatch, bMaj, bMin, bPatch int) bool {
		if aMaj != bMaj {
			return aMaj > bMaj
		}
		if aMin != bMin {
			return aMin > bMin
		}
		return aPatch >= bPatch
	}

	if !newer(rtMaj, rtMin, rtPatch, minMaj, minMin, minPatch) {
		return fmt.Errorf("go runtime version %s is below required %s — rebuild with go %s or set GOTOOLCHAIN=auto", rt, "go"+min, "go"+min)
	}
	return nil
}

// featuresNeedNetwork returns whether current configuration requires outbound network, and human reasons.
func featuresNeedNetwork(cfg *config.Config) (bool, []string) {
	reasons := []string{}
	// Telegram (any mode uses network)
	if cfg.TelegramEnabled {
		if strings.EqualFold(cfg.TelegramBotType, "centralized") {
			reasons = append(reasons, "Telegram centralized registration")
		} else {
			reasons = append(reasons, "Telegram personal notifications")
		}
	}
	// Email via relay
	if cfg.EmailEnabled && strings.EqualFold(cfg.EmailDeliveryMethod, "relay") {
		reasons = append(reasons, "Email relay delivery")
	}
	// Gotify
	if cfg.GotifyEnabled {
		reasons = append(reasons, "Gotify notifications")
	}
	// Webhooks
	if cfg.WebhookEnabled {
		reasons = append(reasons, "Webhooks")
	}
	// Cloud uploads via rclone
	if cfg.CloudEnabled {
		reasons = append(reasons, "Cloud storage (rclone)")
	}
	return len(reasons) > 0, reasons
}

// disableNetworkFeaturesForRun disables all network-dependent features when connectivity is unavailable.
func disableNetworkFeaturesForRun(cfg *config.Config, bootstrap *logging.BootstrapLogger) {
	if cfg == nil {
		return
	}
	warn := func(format string, args ...interface{}) {
		if bootstrap != nil {
			bootstrap.Warning(format, args...)
			return
		}
		logging.Warning(format, args...)
	}

	if cfg.CloudEnabled {
		warn("WARNING: Disabling cloud storage (rclone) due to missing network connectivity")
		cfg.CloudEnabled = false
		cfg.CloudLogPath = ""
	}

	if cfg.TelegramEnabled {
		warn("WARNING: Disabling Telegram notifications due to missing network connectivity")
		cfg.TelegramEnabled = false
	}

	if cfg.EmailEnabled && strings.EqualFold(cfg.EmailDeliveryMethod, "relay") {
		if cfg.EmailFallbackSendmail {
			warn("WARNING: Network unavailable; switching Email delivery to sendmail for this run")
			cfg.EmailDeliveryMethod = "sendmail"
		} else {
			warn("WARNING: Disabling Email relay notifications due to missing network connectivity")
			cfg.EmailEnabled = false
		}
	}

	if cfg.GotifyEnabled {
		warn("WARNING: Disabling Gotify notifications due to missing network connectivity")
		cfg.GotifyEnabled = false
	}

	if cfg.WebhookEnabled {
		warn("WARNING: Disabling Webhook notifications due to missing network connectivity")
		cfg.WebhookEnabled = false
	}

}

// UpdateInfo holds information about the version check result.
type UpdateInfo struct {
	NewVersion bool
	Current    string
	Latest     string
}

// checkForUpdates performs a best-effort check against the latest GitHub release.
//   - If the latest version cannot be determined or the current version is already up to date,
//     only a DEBUG log entry is written (no user-facing output).
//   - If a newer version is available, a WARNING is logged suggesting the --upgrade command.
//     Additionally, a populated *UpdateInfo is returned so that callers can propagate
//     structured information into notifications/metrics.
func checkForUpdates(ctx context.Context, logger *logging.Logger, currentVersion string) *UpdateInfo {
	if logger == nil {
		return nil
	}

	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		logger.Debug("Update check skipped: current version is empty")
		return nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	logger.Debug("Checking for ProxSave updates (current: %s)", currentVersion)

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	logger.Debug("Fetching latest release from GitHub: %s", apiURL)

	_, latestVersion, err := fetchLatestRelease(checkCtx)
	if err != nil {
		logger.Debug("Update check skipped: GitHub unreachable: %v", err)
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
		}
	}

	latestVersion = strings.TrimSpace(latestVersion)
	if latestVersion == "" {
		logger.Debug("Update check skipped: latest version from GitHub is empty")
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
		}
	}

	if !isNewerVersion(currentVersion, latestVersion) {
		logger.Debug("Update check completed: latest=%s current=%s (up to date)", latestVersion, currentVersion)
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
			Latest:     latestVersion,
		}
	}

	logger.Debug("Update check completed: latest=%s current=%s (new version available)", latestVersion, currentVersion)
	logger.Warning("New ProxSave version %s (current %s): run 'proxsave --upgrade' to install.", latestVersion, currentVersion)

	return &UpdateInfo{
		NewVersion: true,
		Current:    currentVersion,
		Latest:     latestVersion,
	}
}

// isNewerVersion returns true if latest is strictly newer than current,
// comparing MAJOR.MINOR.PATCH (ignoring any leading 'v' and pre-release suffixes).
func isNewerVersion(current, latest string) bool {
	parse := func(v string) (int, int, int) {
		v = strings.TrimSpace(v)
		v = strings.TrimPrefix(v, "v")
		if i := strings.IndexByte(v, '-'); i >= 0 {
			v = v[:i]
		}

		parts := strings.Split(v, ".")
		toInt := func(s string) int {
			n, _ := strconv.Atoi(s)
			return n
		}

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

	curMaj, curMin, curPatch := parse(current)
	latMaj, latMin, latPatch := parse(latest)

	if latMaj != curMaj {
		return latMaj > curMaj
	}
	if latMin != curMin {
		return latMin > curMin
	}
	return latPatch > curPatch
}
