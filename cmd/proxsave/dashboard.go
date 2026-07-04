package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/flows/menu"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// dashboardIdleTimeout bounds how long the dashboard waits for a choice.
const dashboardIdleTimeout = 10 * time.Minute

// Test seams.
var (
	dashboardIsBareInvocation = dashboardBareInvocationCheck
	dashboardIsInteractive    = isDashboardTerminalInteractive
)

// dashboardBareInvocationCheck: only a completely bare `proxsave` (no flags
// at all) is eligible for the dashboard.
func dashboardBareInvocationCheck() bool { return len(os.Args) <= 1 }

// isDashboardTerminalInteractive gates the dashboard conservatively: any
// doubt means "behave exactly like today" (run the backup). Cron, systemd
// timers, pipes and ssh-without-tty all fail the TTY checks; dumb/serial
// terminals are excluded via TERM.
func isDashboardTerminalInteractive() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	termEnv := strings.TrimSpace(os.Getenv("TERM"))
	if termEnv == "" || strings.EqualFold(termEnv, "dumb") {
		return false
	}
	return true
}

// maybeRunDashboard shows the interactive dashboard when proxsave is invoked
// completely bare (no flags at all) on an interactive terminal. The chosen
// action is dispatched by MUTATING args and letting the existing flag-driven
// flow proceed unchanged, so every action follows the exact same code path
// as its explicit flag. Returns (exitCode, handled=true) only when the run
// must stop here (Exit or a dashboard failure: never a surprise backup).
func maybeRunDashboard(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger, toolVersion string) (int, bool) {
	if args == nil || !dashboardIsBareInvocation() || !dashboardIsInteractive() {
		return types.ExitSuccess.Int(), false
	}

	buildSig := buildSignature()
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}
	session := newAgeSetupSession(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   "Dashboard",
		Version:    toolVersion,
		ConfigPath: args.ConfigPath,
		BuildSig:   buildSig,
		UseColor:   true,
	})
	defer func() { _ = session.Close() }()

	// Idle timeout: a pty-allocating wrapper (script, tmux, ssh -tt) that
	// reaches the dashboard by accident must not hang forever. Exit, never
	// fall through to a surprise backup.
	menuCtx, cancelMenu := context.WithTimeout(ctx, dashboardIdleTimeout)
	defer cancelMenu()
	action, err := menu.Run(menuCtx, session)
	closeErr := session.Close()
	if err != nil {
		logging.DebugStepBootstrap(bootstrap, "dashboard", "menu error: %v", err)
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Dashboard idle timeout: exiting without action. Use proxsave --backup for non-interactive runs.")
		} else {
			fmt.Fprintln(os.Stderr, "Dashboard unavailable: exiting without action. Use proxsave --backup to run a backup non-interactively.")
		}
		return types.ExitSuccess.Int(), true
	}
	if closeErr != nil {
		logging.DebugStepBootstrap(bootstrap, "dashboard", "session close: %v", closeErr)
	}

	switch action {
	case menu.ActionBackup:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=backup")
		// Fall through to the normal runtime path: backup is the default.
		return types.ExitSuccess.Int(), false
	case menu.ActionRestore:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=restore")
		args.Restore = true
		return types.ExitSuccess.Int(), false
	case menu.ActionDecrypt:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=decrypt")
		args.Decrypt = true
		return types.ExitSuccess.Int(), false
	case menu.ActionNewKey:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=newkey")
		args.ForceNewKey = true
		return types.ExitSuccess.Int(), false
	case menu.ActionReconfigure:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=install")
		args.Install = true
		return types.ExitSuccess.Int(), false
	default:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=exit")
		return types.ExitSuccess.Int(), true
	}
}
