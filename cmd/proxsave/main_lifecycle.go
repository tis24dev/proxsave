// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	buildinfo "github.com/tis24dev/proxsave/internal/version"
)

type runBootstrap struct {
	bootstrap   *logging.BootstrapLogger
	toolVersion string
	runDone     func(error)
	state       *appRunState
}

func startMainRun() runBootstrap {
	bootstrap := logging.NewBootstrapLogger()
	toolVersion := buildinfo.String()
	return runBootstrap{
		bootstrap:   bootstrap,
		toolVersion: toolVersion,
		runDone:     logging.DebugStartBootstrap(bootstrap, "main run", "version=%s", toolVersion),
		state:       newAppRunState(),
	}
}

func finishMainRun(run runBootstrap) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			run.bootstrap.Error("PANIC: %v", r)
			fmt.Fprintf(os.Stderr, "panic: %v\n%s\n", r, stack)
			os.Exit(types.ExitPanicError.Int())
		}
		logging.DebugStepBootstrap(run.bootstrap, "main run", "exit_code=%d", run.state.finalExitCode)
		run.runDone(nil)
	}()
}

func preparePreRuntimeArgs(ctx context.Context, bootstrap *logging.BootstrapLogger, toolVersion string) (*cli.Args, int, bool) {
	args := cli.Parse()
	logging.DebugStepBootstrap(bootstrap, "main run", "args parsed")
	if exitCode, handled := dispatchFlagOnlyModes(args); handled {
		return args, exitCode, true
	}
	if exitCode, handled := rejectIncompatibleModes(args, bootstrap); handled {
		return args, exitCode, true
	}
	if exitCode, handled := runCleanupGuardsMode(ctx, args, bootstrap); handled {
		return args, exitCode, true
	}
	logging.DebugStepBootstrap(bootstrap, "main run", "support_mode=%v", args.Support)
	if exitCode, ok := resolveRunConfigPath(args, bootstrap); !ok {
		return args, exitCode, true
	}
	if exitCode, handled := runUpgradeConfigJSONMode(args); handled {
		return args, exitCode, true
	}
	if exitCode, handled := dispatchPreRuntimeModes(ctx, args, bootstrap, toolVersion); handled {
		return args, exitCode, true
	}
	return args, types.ExitSuccess.Int(), false
}

func dispatchFlagOnlyModes(args *cli.Args) (int, bool) {
	if args.ShowVersion {
		cli.ShowVersion()
		return types.ExitSuccess.Int(), true
	}
	if args.ShowHelp {
		cli.ShowHelp()
		return types.ExitSuccess.Int(), true
	}
	return types.ExitSuccess.Int(), false
}

func rejectIncompatibleModes(args *cli.Args, bootstrap *logging.BootstrapLogger) (int, bool) {
	messages := validateModeCompatibility(args)
	if len(messages) == 0 {
		return types.ExitSuccess.Int(), false
	}
	for _, message := range messages {
		bootstrap.Error("%s", message)
	}
	return types.ExitConfigError.Int(), true
}

func resolveRunConfigPath(args *cli.Args, bootstrap *logging.BootstrapLogger) (int, bool) {
	logging.DebugStepBootstrap(bootstrap, "main run", "resolving config path")
	resolvedConfigPath, err := resolveInstallConfigPath(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int(), false
	}
	args.ConfigPath = resolvedConfigPath
	return types.ExitSuccess.Int(), true
}

func prepareRuntime(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, state *appRunState, toolVersion string) (*appRuntime, int, bool) {
	if exitCode, ok := enforceGoRuntimeVersion(bootstrap); !ok {
		return nil, exitCode, false
	}
	printVersionHeader(bootstrap, toolVersion)
	envInfo := detectAndPrintEnvironment(bootstrap)
	if exitCode, handled := dispatchPostHeaderConfigModes(ctx, args, bootstrap); handled {
		return nil, exitCode, false
	}
	if exitCode, handled := handleSupportIntro(ctx, args, bootstrap, state); handled {
		return nil, exitCode, false
	}
	return bootstrapRuntime(ctx, args, bootstrap, envInfo, toolVersion)
}

func enforceGoRuntimeVersion(bootstrap *logging.BootstrapLogger) (int, bool) {
	if err := checkGoRuntimeVersion(goRuntimeMinVersion); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitEnvironmentError.Int(), false
	}
	return types.ExitSuccess.Int(), true
}

func runRuntime(rt *appRuntime, state *appRunState) int {
	defer rt.sessionLogCloser()
	for _, deferredAction := range runDeferredActions(rt, state) {
		defer deferredAction()
	}
	state.showSummary = true

	logRunContext(rt)
	initializeServerIdentity(rt)
	if exitCode, ok := runSecurityPreflight(rt); !ok {
		return state.finalize(exitCode)
	}
	if result := dispatchRestoreMode(rt); result.handled {
		return finalizeModeResult(state, result)
	}
	return finalizeModeResult(state, dispatchBackupMode(rt))
}

func finalizeModeResult(state *appRunState, result modeResult) int {
	state.applyModeResult(result)
	return state.finalize(result.exitCode)
}
