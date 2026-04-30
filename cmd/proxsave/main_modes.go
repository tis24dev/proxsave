// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

type incompatibleMode struct {
	enabled bool
	label   string
}

type modeCompatibilityRule func(*cli.Args) []string

type preRuntimeModeHandler func(context.Context, *cli.Args, *logging.BootstrapLogger, string) (int, bool)

func validateModeCompatibility(args *cli.Args) []string {
	if args == nil {
		return []string{"command-line arguments are required"}
	}

	for _, rule := range []modeCompatibilityRule{
		validateCleanupGuardsCompatibility,
		validateSupportCompatibility,
		validateInstallCompatibility,
		validateUpgradeCompatibility,
	} {
		if messages := rule(args); len(messages) > 0 {
			return messages
		}
	}
	return nil
}

func validateCleanupGuardsCompatibility(args *cli.Args) []string {
	if args.CleanupGuards {
		if incompatible := cleanupGuardsIncompatibleModes(args); len(incompatible) > 0 {
			return []string{fmt.Sprintf("--cleanup-guards cannot be combined with: %s", strings.Join(incompatible, ", "))}
		}
		return nil
	}
	return nil
}

func validateSupportCompatibility(args *cli.Args) []string {
	if args.Support {
		if incompatible := supportIncompatibleModes(args); len(incompatible) > 0 {
			return []string{
				fmt.Sprintf("Support mode cannot be combined with: %s", strings.Join(incompatible, ", ")),
				"--support is only available for the standard backup run or --restore.",
			}
		}
	}
	return nil
}

func validateInstallCompatibility(args *cli.Args) []string {
	if args.Install && args.NewInstall {
		return []string{"Cannot use --install and --new-install together. Choose one installation mode."}
	}
	return nil
}

func validateUpgradeCompatibility(args *cli.Args) []string {
	if args.Upgrade && (args.Install || args.NewInstall) {
		return []string{"Cannot use --upgrade together with --install or --new-install."}
	}
	return nil
}

func cleanupGuardsIncompatibleModes(args *cli.Args) []string {
	return enabledModes([]incompatibleMode{
		{enabled: args.Support, label: "--support"},
		{enabled: args.Restore, label: "--restore"},
		{enabled: args.Decrypt, label: "--decrypt"},
		{enabled: args.Install, label: "--install"},
		{enabled: args.NewInstall, label: "--new-install"},
		{enabled: args.Upgrade, label: "--upgrade"},
		{enabled: args.ForceNewKey, label: "--newkey"},
		{enabled: args.EnvMigration || args.EnvMigrationDry, label: "--env-migration/--env-migration-dry-run"},
		{enabled: args.UpgradeConfig || args.UpgradeConfigDry || args.UpgradeConfigJSON, label: "--upgrade-config/--upgrade-config-dry-run/--upgrade-config-json"},
	})
}

func supportIncompatibleModes(args *cli.Args) []string {
	return enabledModes([]incompatibleMode{
		{enabled: args.Decrypt, label: "--decrypt"},
		{enabled: args.Install, label: "--install"},
		{enabled: args.NewInstall, label: "--new-install"},
		{enabled: args.EnvMigration || args.EnvMigrationDry, label: "--env-migration"},
		{enabled: args.UpgradeConfig || args.UpgradeConfigDry || args.UpgradeConfigJSON, label: "--upgrade-config"},
		{enabled: args.ForceNewKey, label: "--newkey"},
	})
}

func enabledModes(modes []incompatibleMode) []string {
	incompatible := make([]string, 0, len(modes))
	for _, mode := range modes {
		if mode.enabled {
			incompatible = append(incompatible, mode.label)
		}
	}
	return incompatible
}

func dispatchPreRuntimeModes(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, toolVersion string) (int, bool) {
	for _, handler := range []preRuntimeModeHandler{
		runUpgradeMode,
		runNewKeyMode,
		runDecryptOnlyMode,
		runNewInstallMode,
		runUpgradeConfigDryMode,
		runInstallMode,
	} {
		if exitCode, handled := handler(ctx, args, bootstrap, toolVersion); handled {
			return exitCode, true
		}
	}
	return types.ExitSuccess.Int(), false
}

func runCleanupGuardsMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) (int, bool) {
	if !args.CleanupGuards {
		return types.ExitSuccess.Int(), false
	}

	level := types.LogLevelInfo
	if args.LogLevel != types.LogLevelNone {
		level = args.LogLevel
	}
	logger := logging.New(level, false)

	if err := orchestrator.CleanupMountGuards(ctx, logger, args.DryRun); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitGenericError.Int(), true
	}
	return types.ExitSuccess.Int(), true
}

func runUpgradeMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.Upgrade {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade")
	return runUpgrade(ctx, args, bootstrap), true
}

func runNewKeyMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.ForceNewKey {
		return types.ExitSuccess.Int(), false
	}
	newKeyCLI := args.ForceCLI
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=newkey cli=%v", newKeyCLI)
	if err := runNewKey(ctx, args.ConfigPath, cliFlowLogLevel(args), bootstrap, newKeyCLI); err != nil {
		if isInstallAbortedError(err) || errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int(), true
	}
	return types.ExitSuccess.Int(), true
}

func runDecryptOnlyMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, toolVersion string) (int, bool) {
	if !args.Decrypt {
		return types.ExitSuccess.Int(), false
	}
	decryptCLI := args.ForceCLI
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=decrypt cli=%v", decryptCLI)
	if err := runDecryptWorkflowOnly(ctx, args.ConfigPath, bootstrap, toolVersion, decryptCLI); err != nil {
		if errors.Is(err, orchestrator.ErrDecryptAborted) {
			bootstrap.Info("Decrypt workflow aborted by user")
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("ERROR: %v", err)
		return types.ExitGenericError.Int(), true
	}
	bootstrap.Info("Decrypt workflow completed successfully")
	return types.ExitSuccess.Int(), true
}

func runNewInstallMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.NewInstall {
		return types.ExitSuccess.Int(), false
	}
	newInstallCLI := args.ForceCLI
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=new-install cli=%v", newInstallCLI)
	sessionLogger, cleanupSessionLog := startFlowSessionLog("new-install", cliFlowLogLevel(args), bootstrap)
	defer cleanupSessionLog()
	if sessionLogger != nil {
		sessionLogger.Info("Starting --new-install (config=%s)", args.ConfigPath)
	}
	if err := runNewInstall(ctx, args.ConfigPath, bootstrap, newInstallCLI); err != nil {
		logInstallModeError(sessionLogger, "new-install", err)
		if isInstallAbortedError(err) {
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int(), true
	}
	if sessionLogger != nil {
		sessionLogger.Info("new-install completed successfully")
	}
	return types.ExitSuccess.Int(), true
}

func runInstallMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.Install {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=install cli=%v", args.ForceCLI)
	sessionLogger, cleanupSessionLog := startFlowSessionLog("install", cliFlowLogLevel(args), bootstrap)
	defer cleanupSessionLog()
	if sessionLogger != nil {
		sessionLogger.Info("Starting --install (config=%s)", args.ConfigPath)
	}

	err := runInstallTUI(ctx, args.ConfigPath, bootstrap)
	if args.ForceCLI {
		err = runInstall(ctx, args.ConfigPath, bootstrap)
	}

	if err != nil {
		logInstallModeError(sessionLogger, "install", err)
		if isInstallAbortedError(err) {
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int(), true
	}
	if sessionLogger != nil {
		sessionLogger.Info("install completed successfully")
	}
	return types.ExitSuccess.Int(), true
}

func cliFlowLogLevel(args *cli.Args) types.LogLevel {
	if args.LogLevel != types.LogLevelNone {
		return args.LogLevel
	}
	return types.LogLevelInfo
}

func logInstallModeError(sessionLogger *logging.Logger, flowName string, err error) {
	if sessionLogger == nil {
		return
	}
	if isInstallAbortedError(err) {
		sessionLogger.Warning("%s aborted by user: %v", flowName, err)
		return
	}
	sessionLogger.Error("%s failed: %v", flowName, err)
}
