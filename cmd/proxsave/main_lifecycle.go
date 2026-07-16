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

// capturePanicExit is deferred inside runRuntime BEFORE the summary footer (registered after
// it, so LIFO runs this first). On a panic it makes the crash authoritative for BOTH the exit
// code and the printed footer: it sets finalExitCode to the panic code and stashes the ORIGINAL
// stack, then re-panics so finishMainRun stays the single os.Exit(13) site. On a clean return
// (recover()==nil) it is a no-op and the success path is unchanged (F02-15).
func capturePanicExit(state *appRunState) {
	r := recover()
	if r == nil {
		return
	}
	if state != nil {
		state.finalExitCode = types.ExitPanicError.Int()
		state.panicStack = debug.Stack()
	}
	panic(r)
}

func finishMainRun(run runBootstrap) {
	var panicErr error
	exitAfterCleanup := false
	defer func() {
		if run.bootstrap != nil && run.state != nil {
			logging.DebugStepBootstrap(run.bootstrap, "main run", "exit_code=%d", run.state.finalExitCode)
		}
		if run.runDone != nil {
			run.runDone(panicErr)
		}
		if exitAfterCleanup {
			os.Exit(types.ExitPanicError.Int())
		}
	}()

	r := recover()
	if r == nil {
		return
	}

	stack := debug.Stack()
	// capturePanicExit already ran earlier in the unwind and stashed the ORIGINAL crash
	// stack; prefer it so the operator sees the panic origin, not this re-panic site.
	if run.state != nil && len(run.state.panicStack) > 0 {
		stack = run.state.panicStack
	}
	panicErr = fmt.Errorf("panic: %v", r)
	exitAfterCleanup = true
	if run.state != nil {
		run.state.finalExitCode = types.ExitPanicError.Int()
	}
	if run.bootstrap != nil {
		run.bootstrap.Error("PANIC: %v\n%s", r, stack)
	}
	fmt.Fprintf(os.Stderr, "panic: %v\n%s\n", r, stack)
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
	if exitCode, handled := maybeRunDashboard(ctx, args, bootstrap, toolVersion); handled {
		return args, exitCode, true
	}
	// Re-validate after a possible dashboard mutation so any future
	// single-flag compatibility rule cannot be bypassed by the menu.
	if exitCode, handled := rejectIncompatibleModes(args, bootstrap); handled {
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
	// Registered AFTER the deferred actions so LIFO runs it FIRST in a panic unwind, before
	// the success footer (printFinalSummary) reads finalExitCode. Without it the footer,
	// running earlier than finishMainRun, prints a GREEN success banner just before the
	// process exits 13 (F02-15).
	defer capturePanicExit(state)
	state.showSummary = true

	logRunContext(rt)
	initializeServerIdentity(rt)
	// Daemon run + admin commands run before the backup security preflight: the
	// resident daemon must not crash-loop on a transient preflight issue (each
	// supervised child runs its own preflight), and the admin commands only touch
	// systemd/cron/config.
	if result := dispatchDaemonAdminMode(rt); result.handled {
		return finalizeModeResult(state, result)
	}
	if result := dispatchDaemonMode(rt); result.handled {
		return finalizeModeResult(state, result)
	}
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
