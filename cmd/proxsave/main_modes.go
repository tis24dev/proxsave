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
	"github.com/tis24dev/proxsave/internal/version"
)

// seedWhatsnewOnInstallSuccess seeds last_seen = version.String() at an
// install-success boundary so a brand-new install never shows Screen 0 on its first
// bare interactive launch (STATE-03). It is best-effort: it resolves the base via
// detectedBaseDirOrFallback -- the SAME resolver the dashboard hook reads, so the
// write-path can never diverge from the read-path (open question A1) -- seeds only a
// non-empty base, and ignores any error so a seed failure never changes the install
// exit code. version.String() is used (not the discarded install version param):
// both yield the same value and version.String() is already normalized (strips a
// leading v, applies the 0.0.0-dev fallback), avoiding a raw/empty seed (Pitfall 4).
func seedWhatsnewOnInstallSuccess() {
	baseDir, _ := detectedBaseDirOrFallback()
	if strings.TrimSpace(baseDir) == "" {
		return
	}
	_ = whatsnewSaveSeen(baseDir, version.String())
}

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

	var allMessages []string
	for _, rule := range []modeCompatibilityRule{
		validateCleanupGuardsCompatibility,
		validateSupportCompatibility,
		validateInstallCompatibility,
		validateUpgradeCompatibility,
		validateDaemonCompatibility,
	} {
		if messages := rule(args); len(messages) > 0 {
			allMessages = append(allMessages, messages...)
		}
	}
	return allMessages
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
	if args.LocalFile && !args.Upgrade {
		return []string{"The --localfile flag only applies to --upgrade (use: --upgrade --localfile)."}
	}
	// Neither the upgrade nor its finalize phase has ever honoured --dry-run: not one
	// line of cmd/proxsave/upgrade.go reads args.DryRun, so the combination merges the
	// configuration, refreshes the support docs and symlinks, repoints legacy cron
	// entries, may install or restart the resident daemon, and normalizes permissions
	// on a live installation - while the operator was told nothing would change.
	//
	// This refuses the combination rather than implementing a dry run of it. A truthful
	// dry run would have to model every one of those effects, and a partial one is worse
	// than none: it teaches the operator that the flag is honoured here. The refusal
	// mirrors the one --daemon already carries below.
	//
	// It closes a gap that PREDATES --upgrade-finalize (`--upgrade --dry-run` has always
	// silently mutated), so it turns an invocation that used to be accepted into an
	// error. That is the point: it used to lie.
	if args.DryRun && (args.Upgrade || args.UpgradeFinalize) {
		return []string{"--dry-run is not supported with --upgrade: the upgrade and its finalize phase always modify the installation."}
	}
	return nil
}

func validateDaemonCompatibility(args *cli.Args) []string {
	daemonFlags := 0
	label := ""
	for _, f := range []struct {
		on   bool
		name string
	}{
		{args.Daemon, "--daemon"},
		{args.DaemonSetup, "--daemon-setup"},
		{args.DaemonRemove, "--daemon-remove"},
		{args.DaemonStatus, "--daemon-status"},
	} {
		if f.on {
			daemonFlags++
			label = f.name
		}
	}
	if daemonFlags == 0 {
		return nil
	}
	if daemonFlags > 1 {
		return []string{"Only one of --daemon, --daemon-setup, --daemon-remove, --daemon-status may be used at a time."}
	}
	if args.DryRun && (args.Daemon || args.DaemonSetup || args.DaemonRemove) {
		return []string{"--dry-run is not supported with --daemon, --daemon-setup, or --daemon-remove."}
	}
	incompatible := enabledModes([]incompatibleMode{
		{enabled: args.Install, label: "--install"},
		{enabled: args.NewInstall, label: "--new-install"},
		{enabled: args.Upgrade, label: "--upgrade"},
		{enabled: args.Restore, label: "--restore"},
		{enabled: args.Decrypt, label: "--decrypt"},
		{enabled: args.ForceNewKey, label: "--newkey"},
		{enabled: args.Backup, label: "--backup"},
		{enabled: args.Support, label: "--support"},
		{enabled: args.UpgradeConfig || args.UpgradeConfigDry || args.UpgradeConfigJSON, label: "--upgrade-config"},
		{enabled: args.CleanupGuards, label: "--cleanup-guards"},
	})
	if len(incompatible) > 0 {
		return []string{fmt.Sprintf("%s cannot be combined with: %s", label, strings.Join(incompatible, ", "))}
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
		{enabled: args.UpgradeConfig || args.UpgradeConfigDry || args.UpgradeConfigJSON, label: "--upgrade-config/--upgrade-config-dry-run/--upgrade-config-json"},
	})
}

func supportIncompatibleModes(args *cli.Args) []string {
	return enabledModes([]incompatibleMode{
		{enabled: args.Decrypt, label: "--decrypt"},
		{enabled: args.Install, label: "--install"},
		{enabled: args.NewInstall, label: "--new-install"},
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
		runShowWhatsnewMode,
		runUpgradeFinalizeMode,
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

	// The REPORT: exiting 0 with guards still holding the storage actively misleads a
	// script gating on the exit code, and the read that would have told us was being
	// thrown away. This mode used to take an error-only wrapper that did exactly that;
	// the wrapper is gone, so the report is now the only way in. Same seam the dashboard
	// uses, so one stub covers both front-ends in tests.
	report, err := cleanupGuardsReport(ctx, logger, args.DryRun)
	if err != nil {
		bootstrap.Error("%v", err)
		return types.ExitGenericError.Int(), true
	}

	// The verdict the dashboard shows as CLEAN/FOUND and DONE/PENDING, stated in the
	// CLI's voice and then reflected in the exit code.
	logCLIGuardVerdict(logger, report, args.DryRun)
	return guardCleanupExitCode(report, args.DryRun).Int(), true
}

func runUpgradeMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.Upgrade {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade")
	return runUpgrade(ctx, args, bootstrap), true
}

// runUpgradeFinalizeMode handles --upgrade-finalize: run only the post-install
// finalize phase and exit.
//
// This is the mode --upgrade re-invokes on the freshly INSTALLED binary, so the
// finalize policy that runs belongs to the release being installed rather than to
// the one being replaced - which is why every change to that policy used to take
// effect one upgrade late. It is not a user-facing entry point: run by hand it
// would refresh docs, restart the daemon and print an upgrade footer for a version
// nobody installed.
//
// Reached only after the caller verified and installed the binary, so upgradeErr is
// nil by construction here.
func runUpgradeFinalizeMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.UpgradeFinalize {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=upgrade-finalize")
	return runUpgradeFinalize(ctx, args, bootstrap), true
}

// runShowWhatsnewMode handles --show-whatsnew: open Screen 0 (what's new) once and exit.
// This is the mode the upgrade flow re-invokes on the freshly installed binary so Screen 0
// opens at the end of every interactive upgrade, rendered by the binary that carries the
// notes. It never fails the process (Screen 0 is best-effort and self-heals), so it always
// returns ExitSuccess.
func runShowWhatsnewMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, toolVersion string) (int, bool) {
	if !args.ShowWhatsnew {
		return types.ExitSuccess.Int(), false
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=show-whatsnew")
	showWhatsnewScreen(ctx, args, toolVersion)
	return types.ExitSuccess.Int(), true
}

// modeStdoutInteractive gates the sibling entrypoints onto their clean-stdout CLI
// variant when stdout is not a real terminal, mirroring dispatchRestoreMode (C6).
// It is a var so tests can force the non-interactive branch.
var modeStdoutInteractive = isTerminalInteractive

// modeUseCLI reports whether a sibling entrypoint must take its clean-stdout CLI
// variant: either the operator forced --cli, or stdout is not a real terminal (a
// TUI would write AltScreen escapes into the redirected stream).
func modeUseCLI(args *cli.Args) bool {
	return args.ForceCLI || !modeStdoutInteractive()
}

func runNewKeyMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.ForceNewKey {
		return types.ExitSuccess.Int(), false
	}
	newKeyCLI := modeUseCLI(args)
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=newkey cli=%v", newKeyCLI)
	if err := runNewKey(ctx, args.ConfigPath, cliFlowLogLevel(args), bootstrap, newKeyCLI); err != nil {
		if isInstallAbortedError(err) || errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("%v", err)
		return types.ExitConfigError.Int(), true
	}
	return types.ExitSuccess.Int(), true
}

func runDecryptOnlyMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, toolVersion string) (int, bool) {
	if !args.Decrypt {
		return types.ExitSuccess.Int(), false
	}
	decryptCLI := modeUseCLI(args)
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=decrypt cli=%v", decryptCLI)
	if err := runDecryptWorkflowOnly(ctx, args.ConfigPath, bootstrap, toolVersion, decryptCLI); err != nil {
		if errors.Is(err, orchestrator.ErrDecryptAborted) {
			bootstrap.Info("Decrypt workflow aborted by user")
			return types.ExitSuccess.Int(), true
		}
		if errors.Is(err, orchestrator.ErrDecryptNoBackups) && dashboardIsBareInvocation() {
			// ONLY the interactive dashboard (bare invocation): the user already saw the
			// graceful "Status:" empty-state screen, so exit cleanly with NO log line. A
			// CLI --decrypt execution falls through and keeps its original ERROR line
			// (its CLI-execution lines are left untouched).
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("%v", err)
		return types.ExitGenericError.Int(), true
	}
	bootstrap.Info("Decrypt workflow completed successfully")
	return types.ExitSuccess.Int(), true
}

func runNewInstallMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.NewInstall {
		return types.ExitSuccess.Int(), false
	}
	newInstallCLI := modeUseCLI(args)
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
		bootstrap.Error("%v", err)
		return types.ExitConfigError.Int(), true
	}
	if sessionLogger != nil {
		sessionLogger.Info("new-install completed successfully")
	}
	seedWhatsnewOnInstallSuccess()
	return types.ExitSuccess.Int(), true
}

func runInstallMode(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, _ string) (int, bool) {
	if !args.Install {
		return types.ExitSuccess.Int(), false
	}
	installCLI := modeUseCLI(args)
	logging.DebugStepBootstrap(bootstrap, "main run", "mode=install cli=%v", installCLI)
	sessionLogger, cleanupSessionLog := startFlowSessionLog("install", cliFlowLogLevel(args), bootstrap)
	defer cleanupSessionLog()
	if sessionLogger != nil {
		sessionLogger.Info("Starting --install (config=%s)", args.ConfigPath)
	}

	var err error
	if installCLI {
		err = runInstall(ctx, args.ConfigPath, bootstrap)
	} else {
		err = runInstallTUI(ctx, args.ConfigPath, bootstrap)
	}

	if err != nil {
		logInstallModeError(sessionLogger, "install", err)
		if isInstallAbortedError(err) {
			return types.ExitSuccess.Int(), true
		}
		bootstrap.Error("%v", err)
		return types.ExitConfigError.Int(), true
	}
	if sessionLogger != nil {
		sessionLogger.Info("install completed successfully")
	}
	seedWhatsnewOnInstallSuccess()
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
