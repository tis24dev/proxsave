package main

import (
	"bufio"
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
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

const (
	version                       = "0.9.0" // Semantic version format required by cloud relay worker
	defaultLegacyEnvPath          = "/opt/proxsave/env/backup.env"
	legacyEnvFallbackPath         = "/opt/proxmox-backup/env/backup.env"
	goRuntimeMinVersion           = "1.25.4"
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

	// Handle SIGINT (Ctrl+C) and SIGTERM
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		bootstrap.Warning("\nReceived signal %v, initiating graceful shutdown...", sig)
		cancel() // Cancel context to stop all operations
		closeStdinOnce.Do(func() {
			if file := os.Stdin; file != nil {
				_ = file.Close()
			}
		})
	}()

	// Parse command-line arguments
	args := cli.Parse()

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

	// Validate support mode compatibility with other CLI modes
	if args.Support {
		incompatible := make([]string, 0, 6)
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
			bootstrap.Error("--support is only available for the standard backup run.")
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
	resolvedConfigPath, err := resolveInstallConfigPath(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}
	args.ConfigPath = resolvedConfigPath

	// Dedicated upgrade mode (download latest binary, no config changes)
	if args.Upgrade {
		return runUpgrade(ctx, args, bootstrap)
	}

	newKeyCLI := args.ForceCLI
	// Dedicated new key mode (no backup run)
	if args.ForceNewKey {
		if err := runNewKey(ctx, args.ConfigPath, bootstrap, newKeyCLI); err != nil {
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
		if err := runDecryptWorkflowOnly(ctx, args.ConfigPath, bootstrap, version, decryptCLI); err != nil {
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
		sessionLogger, cleanupSessionLog := startFlowSessionLog("new-install", bootstrap)
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
		sessionLogger, cleanupSessionLog := startFlowSessionLog("install", bootstrap)
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
	bootstrap.Printf("  Version: %s", version)
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
		return runEnvMigrationDry(ctx, args, bootstrap)
	}

	if args.EnvMigration {
		return runEnvMigration(ctx, args, bootstrap)
	}

	// Support mode: interactive pre-flight questionnaire (mandatory)
	if args.Support {
		continueRun, interrupted := runSupportIntro(ctx, bootstrap, args)
		if !continueRun {
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
			logging.Warning("WARNING: Network preflight disabled via DISABLE_NETWORK_PREFLIGHT; features: %s", strings.Join(reasons, ", "))
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

	// Initialize logger with configuration
	logger := logging.New(logLevel, cfg.UseColor)
	sessionLogActive := false
	sessionLogCloser := func() {}
	if args.Restore {
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
	bootstrap.Flush(logger)

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

	// Best-effort check for newer releases on GitHub.
	// If the installed version is up to date, nothing is printed at INFO/WARNING level
	// (only a DEBUG message is logged). If a newer version exists, a WARNING is emitted
	// suggesting the use of --upgrade.
	checkForUpdates(ctx, logger, version)

	// Apply backup permissions (optional, Bash-compatible behavior)
	if cfg.SetBackupPermissions {
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

	defer func() {
		if !args.Support || pendingSupportStats == nil {
			return
		}
		logging.Step("Support mode - sending support email with attached log")
		sendSupportEmail(ctx, cfg, logger, envInfo.Type, pendingSupportStats, args.SupportGitHubUser, args.SupportIssueID)
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
	if _, secErr := security.Run(ctx, logger, cfg, args.ConfigPath, execPath, envInfo); secErr != nil {
		logging.Error("Security checks failed: %v", secErr)
		return finalize(types.ExitSecurityError.Int())
	}
	fmt.Println()

	restoreCLI := args.ForceCLI
	if args.Restore {
		if restoreCLI {
			logging.Info("Restore mode enabled - starting CLI workflow...")
			if err := orchestrator.RunRestoreWorkflow(ctx, cfg, logger, version); err != nil {
				if errors.Is(err, orchestrator.ErrRestoreAborted) {
					logging.Info("Restore workflow aborted by user")
					return finalize(exitCodeInterrupted)
				}
				logging.Error("Restore workflow failed: %v", err)
				return finalize(types.ExitGenericError.Int())
			}
			if logger.HasWarnings() {
				logging.Warning("Restore workflow completed with warnings (see log above)")
			} else {
				logging.Info("Restore workflow completed successfully")
			}
			return finalize(types.ExitSuccess.Int())
		}

		logging.Info("Restore mode enabled - starting interactive workflow...")
		sig := buildSignature()
		if strings.TrimSpace(sig) == "" {
			sig = "n/a"
		}
		if err := orchestrator.RunRestoreWorkflowTUI(ctx, cfg, logger, version, args.ConfigPath, sig); err != nil {
			if errors.Is(err, orchestrator.ErrRestoreAborted) || errors.Is(err, orchestrator.ErrDecryptAborted) {
				logging.Info("Restore workflow aborted by user")
				return finalize(exitCodeInterrupted)
			}
			logging.Error("Restore workflow failed: %v", err)
			return finalize(types.ExitGenericError.Int())
		}
		if logger.HasWarnings() {
			logging.Warning("Restore workflow completed with warnings (see log above)")
		} else {
			logging.Info("Restore workflow completed successfully")
		}
		return finalize(types.ExitSuccess.Int())
	}

	if args.Decrypt {
		if decryptCLI {
			logging.Info("Decrypt mode enabled - starting CLI workflow...")
			if err := orchestrator.RunDecryptWorkflow(ctx, cfg, logger, version); err != nil {
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
			if err := orchestrator.RunDecryptWorkflowTUI(ctx, cfg, logger, version, args.ConfigPath, sig); err != nil {
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
	orch = orchestrator.New(logger, dryRun)
	orch.SetVersion(version)
	orch.SetConfig(cfg)
	orch.SetIdentity(serverIDValue, serverMACValue)
	orch.SetProxmoxVersion(envInfo.Version)
	orch.SetStartTime(startTime)

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

	logging.Info("✓ Orchestrator initialized")
	fmt.Println()

	// Verify directories
	logging.Step("Verifying directory structure")
	checkDir := func(name, path string) {
		if utils.DirExists(path) {
			logging.Info("✓ %s exists: %s", name, path)
		} else {
			logging.Warning("✗ %s not found: %s", name, path)
		}
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
		} else if isLocalPath(cloudLogPath) {
			checkDir("Cloud log directory", cloudLogPath)
		} else {
			logging.Info("Skipping local validation for cloud log directory (remote path): %s", cloudLogPath)
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
		checkerConfig.CloudPath = cfg.CloudRemote
	} else {
		checkerConfig.CloudPath = ""
	}
	checkerConfig.MinDiskPrimaryGB = cfg.MinDiskPrimaryGB
	checkerConfig.MinDiskSecondaryGB = cfg.MinDiskSecondaryGB
	checkerConfig.MinDiskCloudGB = cfg.MinDiskCloudGB
	checkerConfig.DryRun = dryRun
	if err := checkerConfig.Validate(); err != nil {
		logging.Error("Invalid checker configuration: %v", err)
		earlyErrorState = &orchestrator.EarlyErrorState{
			Phase:     "checker_config",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}
		return finalize(types.ExitConfigError.Int())
	}
	checker := checks.NewChecker(logger, checkerConfig)
	orch.SetChecker(checker)

	// Ensure lock is released on exit
	defer func() {
		if err := orch.ReleaseBackupLock(); err != nil {
			logging.Warning("Failed to release backup lock: %v", err)
		}
	}()

	logging.Debug("✓ Pre-backup checks configured")
	fmt.Println()

	// Initialize storage backends
	logging.Step("Initializing storage backends")

	// Primary (local) storage - always enabled
	localBackend, err := storage.NewLocalStorage(cfg, logger)
	if err != nil {
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
		logging.Error("Failed to prepare primary storage: %v", err)
		earlyErrorState = &orchestrator.EarlyErrorState{
			Phase:     "storage_init",
			Error:     err,
			ExitCode:  types.ExitConfigError,
			Timestamp: time.Now(),
		}
		return finalize(types.ExitConfigError.Int())
	}
	logging.Info("Path Primary: %s", formatDetailedFilesystemLabel(cfg.BackupPath, localFS))

	localStats := fetchStorageStats(ctx, localBackend, logger, "Local storage")
	localBackups := fetchBackupList(ctx, localBackend)

	localAdapter := orchestrator.NewStorageAdapter(localBackend, logger, cfg)
	localAdapter.SetFilesystemInfo(localFS)
	localAdapter.SetInitialStats(localStats)
	orch.RegisterStorageTarget(localAdapter)
	logStorageInitSummary(formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, localStats, localBackups))

	// Secondary storage - optional
	var secondaryFS *storage.FilesystemInfo
	if cfg.SecondaryEnabled {
		secondaryBackend, err := storage.NewSecondaryStorage(cfg, logger)
		if err != nil {
			logging.Warning("Failed to initialize secondary storage: %v", err)
			logging.Info("Path Secondary: %s", formatDetailedFilesystemLabel(cfg.SecondaryPath, nil))
		} else {
			secondaryFS, _ = detectFilesystemInfo(ctx, secondaryBackend, cfg.SecondaryPath, logger)
			logging.Info("Path Secondary: %s", formatDetailedFilesystemLabel(cfg.SecondaryPath, secondaryFS))
			secondaryStats := fetchStorageStats(ctx, secondaryBackend, logger, "Secondary storage")
			secondaryBackups := fetchBackupList(ctx, secondaryBackend)
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
		cloudBackend, err := storage.NewCloudStorage(cfg, logger)
		if err != nil {
			logging.Warning("Failed to initialize cloud storage: %v", err)
			logging.Info("Path Cloud: %s", formatDetailedFilesystemLabel(cfg.CloudRemote, nil))
			logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, nil, nil))
		} else {
			cloudFS, _ = detectFilesystemInfo(ctx, cloudBackend, cfg.CloudRemote, logger)
			if cloudFS == nil {
				cfg.CloudEnabled = false
				cfg.CloudLogPath = ""
				if checker != nil {
					checker.DisableCloud()
				}
				logStorageInitSummary(formatStorageInitSummary("Cloud storage", cfg, storage.LocationCloud, nil, nil))
				logging.Skip("Path Cloud: disabled")
			} else {
				logging.Info("Path Cloud: %s", formatDetailedFilesystemLabel(cfg.CloudRemote, cloudFS))
				cloudStats := fetchStorageStats(ctx, cloudBackend, logger, "Cloud storage")
				cloudBackups := fetchBackupList(ctx, cloudBackend)
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

	fmt.Println()

	// Initialize notification channels
	logging.Step("Initializing notification channels")

	// Email notifications
	if cfg.EmailEnabled {
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
		logging.Skip("Email: disabled")
	}

	// Telegram notifications
	if cfg.TelegramEnabled {
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
		logging.Skip("Telegram: disabled")
	}

	// Gotify notifications
	if cfg.GotifyEnabled {
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
		logging.Skip("Gotify: disabled")
	}

	// Webhook Notifications
	if cfg.WebhookEnabled {
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
		logging.Skip("Webhook: disabled")
	}

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
	logging.Debug("Go backup pipeline enabled")

	// Run backup orchestration
	if cfg.BackupEnabled {
		if err := orch.RunPreBackupChecks(ctx); err != nil {
			logging.Error("Pre-backup validation failed: %v", err)
			earlyErrorState = &orchestrator.EarlyErrorState{
				Phase:     "pre_backup_checks",
				Error:     err,
				ExitCode:  types.ExitBackupError,
				Timestamp: time.Now(),
			}
			return finalize(types.ExitBackupError.Int())
		}
		fmt.Println()

		logging.Step("Start Go backup orchestration")

		// Get hostname for backup naming
		hostname := resolveHostname()

		// Run Go-based backup (collection + archive)
		stats, err := orch.RunGoBackup(ctx, envInfo.Type, hostname)
		if err != nil {
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
	fmt.Println("https://buymeacoffee.com/tis24dev")
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
	fmt.Println("  --support          - Run backup in support mode (force debug log level and send email with attached log to github-support@tis24.it)")
	fmt.Println()
}

func sendSupportEmail(ctx context.Context, cfg *config.Config, logger *logging.Logger, proxmoxType types.ProxmoxType, stats *orchestrator.BackupStats, githubUser, issueID string) {
	if stats == nil {
		logging.Warning("Support mode: cannot send support email because stats are nil")
		return
	}

	subject := "SUPPORT REQUEST"
	if strings.TrimSpace(githubUser) != "" || strings.TrimSpace(issueID) != "" {
		subjectParts := []string{"SUPPORT REQUEST"}
		if strings.TrimSpace(githubUser) != "" {
			subjectParts = append(subjectParts, fmt.Sprintf("Nickname: %s", strings.TrimSpace(githubUser)))
		}
		if strings.TrimSpace(issueID) != "" {
			subjectParts = append(subjectParts, fmt.Sprintf("Issue: %s", strings.TrimSpace(issueID)))
		}
		subject = strings.Join(subjectParts, " - ")
	}

	if sig := buildSignature(); sig != "" {
		subject = fmt.Sprintf("%s - Build: %s", subject, sig)
	}

	emailConfig := notify.EmailConfig{
		Enabled:          true,
		DeliveryMethod:   notify.EmailDeliverySendmail,
		FallbackSendmail: false,
		AttachLogFile:    true,
		Recipient:        "github-support@tis24.it",
		From:             cfg.EmailFrom,
		SubjectOverride:  subject,
	}

	emailNotifier, err := notify.NewEmailNotifier(emailConfig, proxmoxType, logger)
	if err != nil {
		logging.Warning("Support mode: failed to initialize support email notifier: %v", err)
		return
	}

	adapter := orchestrator.NewNotificationAdapter(emailNotifier, logger)
	if err := adapter.Notify(ctx, stats); err != nil {
		logging.Critical("Support mode: FAILED to send support email: %v", err)
		fmt.Println("\033[33m⚠️  CRITICAL: Support email failed to send!\033[0m")
		return
	}

	logging.Info("Support mode: support email handed off to local MTA for github-support@tis24.it (check mailq and /var/log/mail.log for delivery)")
}

func runSupportIntro(ctx context.Context, bootstrap *logging.BootstrapLogger, args *cli.Args) (bool, bool) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("\033[32m================================================\033[0m")
	fmt.Println("\033[32m  SUPPORT & ASSISTANCE MODE\033[0m")
	fmt.Println("\033[32m================================================\033[0m")
	fmt.Println()
	fmt.Println("This mode will send the backup log to the developer for debugging.")
	fmt.Println("\033[33mIf your log contains personal or sensitive information, it will be shared.\033[0m")
	fmt.Println()

	accepted, err := promptYesNoSupport(reader, "Do you accept and continue? [y/N]: ")
	if err != nil {
		if ctx.Err() == context.Canceled {
			bootstrap.Warning("Support mode interrupted by signal")
			return false, true
		}
		bootstrap.Error("ERROR: %v", err)
		return false, false
	}
	if !accepted {
		bootstrap.Warning("Support mode aborted by user (consent not granted)")
		return false, false
	}

	fmt.Println()
	fmt.Println("Before proceeding, you must have an open GitHub issue for this problem.")
	fmt.Println("Emails without a corresponding GitHub issue will not be analyzed.")
	fmt.Println()

	hasIssue, err := promptYesNoSupport(reader, "Do you confirm that you have already opened a GitHub issue? [y/N]: ")
	if err != nil {
		if ctx.Err() == context.Canceled {
			bootstrap.Warning("Support mode interrupted by signal")
			return false, true
		}
		bootstrap.Error("ERROR: %v", err)
		return false, false
	}
	if !hasIssue {
		bootstrap.Warning("Support mode aborted: please open a GitHub issue first")
		return false, false
	}

	// GitHub nickname
	for {
		fmt.Print("Enter your GitHub nickname: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() == context.Canceled {
				bootstrap.Warning("Support mode interrupted by signal")
				return false, true
			}
			bootstrap.Error("ERROR: Failed to read input: %v", err)
			return false, false
		}
		nickname := strings.TrimSpace(line)
		if nickname == "" {
			fmt.Println("GitHub nickname cannot be empty. Please try again.")
			continue
		}
		args.SupportGitHubUser = nickname
		break
	}

	// GitHub issue number
	for {
		fmt.Print("Enter the GitHub issue number in the format #1234: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			if ctx.Err() == context.Canceled {
				bootstrap.Warning("Support mode interrupted by signal")
				return false, true
			}
			bootstrap.Error("ERROR: Failed to read input: %v", err)
			return false, false
		}
		issue := strings.TrimSpace(line)
		if issue == "" {
			fmt.Println("Issue number cannot be empty. Please try again.")
			continue
		}
		if !strings.HasPrefix(issue, "#") || len(issue) < 2 {
			fmt.Println("Issue must start with '#' and contain a numeric ID, for example: #1234.")
			continue
		}
		if _, err := strconv.Atoi(issue[1:]); err != nil {
			fmt.Println("Issue must be in the format #1234 with a numeric ID. Please try again.")
			continue
		}
		args.SupportIssueID = issue
		break
	}

	fmt.Println()
	fmt.Println("Support mode confirmed.")
	fmt.Println("The backup will run in DEBUG mode and a support email with the full log will be sent to github-support@tis24.it at the end.")
	fmt.Println()

	return true, false
}

func promptYesNoSupport(reader *bufio.Reader, prompt string) (bool, error) {
	for {
		fmt.Print(prompt)
		line, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		answer := strings.TrimSpace(strings.ToLower(line))
		if answer == "y" || answer == "yes" {
			return true, nil
		}
		if answer == "" || answer == "n" || answer == "no" {
			return false, nil
		}
		fmt.Println("Please answer with 'y' or 'n'.")
	}
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

// checkForUpdates performs a best-effort check against the latest GitHub release.
// - If the latest version cannot be determined or the current version is already up to date,
//   only a DEBUG log entry is written (no user-facing output).
// - If a newer version is available, a WARNING is logged suggesting the --upgrade command.
func checkForUpdates(ctx context.Context, logger *logging.Logger, currentVersion string) {
	if logger == nil {
		return
	}

	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		logger.Debug("Update check skipped: current version is empty")
		return
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, latestVersion, err := fetchLatestRelease(checkCtx)
	if err != nil {
		logger.Debug("Update check skipped (GitHub unreachable): %v", err)
		return
	}

	latestVersion = strings.TrimSpace(latestVersion)
	if latestVersion == "" {
		logger.Debug("Update check skipped: latest version from GitHub is empty")
		return
	}

	if !isNewerVersion(currentVersion, latestVersion) {
		logger.Debug("Update check: current version (%s) is up to date (latest: %s)", currentVersion, latestVersion)
		return
	}

	logger.Warning("A newer ProxSave version is available: %s (current: %s)", latestVersion, currentVersion)
	logger.Warning("Consider running 'proxsave --upgrade' to install the latest release.")
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
