package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
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
	session := shell.Start(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   "Dashboard",
		Version:    toolVersion,
		ConfigPath: args.ConfigPath,
		BuildSig:   buildSig,
		UseColor:   true,
	})
	if s := testDashboardSession; s != nil {
		session = s(ctx)
	}
	keepAlive := false
	defer func() {
		if !keepAlive {
			_ = session.Close()
		}
	}()

	// Idle timeout: a pty-allocating wrapper (script, tmux, ssh -tt) that
	// reaches the dashboard by accident must not hang forever. Exit, never
	// fall through to a surprise backup.
	menuCtx, cancelMenu := context.WithTimeout(ctx, dashboardIdleTimeout)
	defer cancelMenu()
	action, err := menu.Run(menuCtx, session)
	if err != nil {
		// The deferred Close releases the terminal before these prints.
		_ = session.Close()
		logging.DebugStepBootstrap(bootstrap, "dashboard", "menu error: %v", err)
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "Dashboard idle timeout: exiting without action. Use proxsave --backup for non-interactive runs.")
		} else {
			fmt.Fprintln(os.Stderr, "Dashboard unavailable: exiting without action. Use proxsave --backup to run a backup non-interactively.")
		}
		return types.ExitSuccess.Int(), true
	}

	switch action {
	case menu.ActionBackup:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=backup")
		// The backup run owns the plain terminal: leave the UI for real
		// (explicit close so the altscreen is gone before any run output).
		_ = session.Close()
		return types.ExitSuccess.Int(), false
	case menu.ActionRestore:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=restore")
		args.Restore = true
	case menu.ActionDecrypt:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=decrypt")
		args.Decrypt = true
	case menu.ActionNewKey:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=newkey")
		args.ForceNewKey = true
	case menu.ActionReconfigure:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=install")
		args.Install = true
	default:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=exit")
		return types.ExitSuccess.Int(), true
	}

	// Flow actions: hand the live session to the chosen flow so the frame
	// never leaves the screen (no altscreen teardown flash). Console output
	// is muted for the gap between the menu and the flow's own session
	// takeover; the flow (or the leftover cleanup) restores it.
	keepAlive = true
	stashDashboardSession(session, bootstrap)
	return types.ExitSuccess.Int(), false
}

// testDashboardSession lets tests inject a renderless session (the seam used
// to be newAgeSetupSession; the handoff needs the dashboard to own a real
// Start by default).
var testDashboardSession func(ctx context.Context) *shell.Session

// --- Dashboard session handoff -------------------------------------------
//
// The dashboard stays open while the chosen flow spins up; the flow adopts
// the same program via adoptDashboardSession (wired into the session seams
// and orchestrator.SetUISessionHandoff). If the flow dies before adopting,
// releaseDashboardLeftovers closes the session and replays the warnings and
// errors that were muted in between, so failures never become invisible.

type dashboardHandoffState struct {
	mu        sync.Mutex
	session   *shell.Session
	bootstrap *logging.BootstrapLogger
	entryMark int
}

var dashboardHandoff dashboardHandoffState

func stashDashboardSession(session *shell.Session, bootstrap *logging.BootstrapLogger) {
	dashboardHandoff.mu.Lock()
	dashboardHandoff.session = session
	dashboardHandoff.bootstrap = bootstrap
	dashboardHandoff.entryMark = bootstrap.EntryCount()
	dashboardHandoff.mu.Unlock()

	// Mute the console for the handoff window: anything printed now would
	// land inside the still-open alternate screen.
	bootstrap.SetConsoleQuiet(true)
	logging.GetDefaultLogger().SwapOutput(io.Discard)
	orchestrator.SetUISessionHandoff(adoptDashboardSession)
}

// dashboardHandoffPending reports whether a stashed session is waiting to be
// adopted (used to keep freshly created loggers muted in the gap).
func dashboardHandoffPending() bool {
	dashboardHandoff.mu.Lock()
	defer dashboardHandoff.mu.Unlock()
	return dashboardHandoff.session != nil
}

// adoptDashboardSession consumes the stashed session (once): the flow's
// chrome replaces the dashboard's and the console mute is lifted, right
// before the flow applies its own session-scoped silencing.
func adoptDashboardSession(cfg shell.Config) *shell.Session {
	dashboardHandoff.mu.Lock()
	session := dashboardHandoff.session
	bootstrap := dashboardHandoff.bootstrap
	dashboardHandoff.session = nil
	dashboardHandoff.bootstrap = nil
	dashboardHandoff.mu.Unlock()
	if session == nil {
		return nil
	}
	session.Adopt(cfg)
	bootstrap.SetConsoleQuiet(false)
	logging.GetDefaultLogger().SetOutput(nil) // back to stdout
	return session
}

// releaseDashboardLeftovers runs at the end of the process: if the chosen
// flow never adopted the session (early failure), close it, restore the
// console, and replay the muted warnings/errors to stderr.
func releaseDashboardLeftovers() {
	dashboardHandoff.mu.Lock()
	session := dashboardHandoff.session
	bootstrap := dashboardHandoff.bootstrap
	mark := dashboardHandoff.entryMark
	dashboardHandoff.session = nil
	dashboardHandoff.bootstrap = nil
	dashboardHandoff.mu.Unlock()
	if session == nil {
		return
	}
	_ = session.Close()
	bootstrap.SetConsoleQuiet(false)
	logging.GetDefaultLogger().SetOutput(nil)
	bootstrap.ReplayConsoleSince(mark)
}
