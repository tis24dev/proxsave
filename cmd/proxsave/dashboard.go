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
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	flowinstall "github.com/tis24dev/proxsave/internal/ui/flows/install"
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

	for {
		// Idle timeout: a pty-allocating wrapper (script, tmux, ssh -tt) that
		// reaches the dashboard by accident must not hang forever. Exit, never
		// fall through to a surprise backup.
		menuCtx, cancelMenu := context.WithTimeout(ctx, dashboardIdleTimeout)
		action, err := menu.Run(menuCtx, session, dashboardDaemonState(args))
		cancelMenu()
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

		// Diagnostics group: re-open an existing check screen in the live session
		// and loop back to the menu. These never leave the dashboard, so the flag
		// dispatch below is untouched.
		if runDashboardDiagnostic(ctx, session, action, args, bootstrap) {
			continue
		}

		switch action {
		case menu.ActionBackup:
			logging.DebugStepBootstrap(bootstrap, "dashboard", "action=backup")
			// The backup run owns the plain terminal: leave the UI for real
			// (explicit close so the altscreen is gone before any run output).
			_ = session.Close()
			return types.ExitSuccess.Int(), false
		case menu.ActionBackupDebug:
			logging.DebugStepBootstrap(bootstrap, "dashboard", "action=backup-debug")
			// Same backup, but force verbose logging (equivalent to --log-level debug).
			// resolveRunLogLevel honours args.LogLevel when set, so this run is debug.
			args.LogLevel = types.LogLevelDebug
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
}

// runDashboardDiagnostic runs the check screen for a diagnostics-group action in
// the live dashboard session and reports whether it handled the action (so the
// dashboard loops back to the menu). Non-diagnostic actions return false, leaving
// them for the normal flag dispatch. Every screen already exists and is reused
// verbatim; each is non-blocking (errors are swallowed - a failed check must never
// abort the dashboard). When a setup screen is not eligible (that feature is not
// configured on this host) it renders nothing, so a short notice is shown instead
// of a blank flicker.
func runDashboardDiagnostic(ctx context.Context, session *shell.Session, action menu.Action, args *cli.Args, bootstrap *logging.BootstrapLogger) bool {
	configPath := ""
	if args != nil {
		configPath = args.ConfigPath
	}
	baseDir, _ := detectedBaseDirOrFallback()
	switch action {
	case menu.ActionCheckTelegram:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=check-telegram")
		res, _ := dashboardRunTelegramSetup(ctx, session, baseDir, configPath)
		if !res.Shown {
			dashboardNotConfiguredNotice(ctx, session, "Telegram", "Telegram notifications are not enabled (centralized) on this host.")
		}
	case menu.ActionCheckHealthcheck:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=check-healthcheck")
		res, _ := dashboardRunHealthcheckSetup(ctx, session, baseDir, configPath)
		if !res.Shown {
			dashboardNotConfiguredNotice(ctx, session, "Backup monitoring", "Centralized healthchecks monitoring is not enabled on this host.")
		}
	case menu.ActionPostInstallCheck:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=post-install-check")
		_, _ = dashboardRunPostInstallAudit(ctx, session, getExecInfo().ExecPath, configPath)
	case menu.ActionCheckUpgrade:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=check-upgrade")
		runDashboardUpgrade(ctx, session, configPath)
	case menu.ActionDaemonSetup:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=daemon-setup")
		runDashboardDaemonAdmin(ctx, session, true, configPath, baseDir)
	case menu.ActionDaemonRemove:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=daemon-remove")
		runDashboardDaemonAdmin(ctx, session, false, configPath, baseDir)
	case menu.ActionDaemonRestart:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=daemon-restart")
		runDashboardDaemonRestart(ctx, session, configPath, baseDir)
	case menu.ActionDaemonStatus:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=daemon-status")
		runDashboardDaemonStatus(ctx, session, configPath, baseDir)
	default:
		return false
	}
	return true
}

// Seams so the daemon admin ops can be stubbed in tests (they otherwise run real
// systemctl / write /etc/systemd + backup.env).
var (
	daemonApplyDaemonMode = applyDaemonMode
	daemonApplyCronMode   = applyCronMode
	daemonRestartService  = restartDaemonService
)

// runDashboardDaemonRestart restarts the resident daemon in-session (RunTask + Notice),
// then loops back to the menu. Useful after a rebuild: systemd keeps the old process
// until an explicit restart, which is exactly the "installed+active but running a stale
// binary that no longer writes the status file" case the healthcheck checks now surface.
// It uses restartAndVerifyDaemon, so it also WAITS for an in-progress backup to finish
// (a restart would kill a daemon-supervised backup) and VERIFIES the daemon came back
// aligned before reporting success -- the notice distinguishes aligned / deferred /
// not-confirmed / error.
func runDashboardDaemonRestart(ctx context.Context, session *shell.Session, configPath, baseDir string) {
	interval := time.Duration(0)
	// Fallback lock path (base-dir default) used only when the config is unreadable; the
	// normal path resolves the REAL <cfg.LockPath>/.backup.lock so the backup-wait probe
	// inspects the same lock the orchestrator acquires even under a custom LOCK_PATH.
	lockPath := backupLockFilePath(nil, baseDir)
	if cfg, err := daemonStatusLoadConfig(configPath, baseDir); err == nil && cfg != nil {
		interval = cfg.HealthcheckHeartbeatInterval
		if strings.TrimSpace(cfg.BaseDir) != "" {
			baseDir = cfg.BaseDir
		}
		lockPath = backupLockFilePath(cfg, baseDir)
	}
	var rv RestartVerifyResult
	_ = components.RunTask(ctx, session, "Restarting daemon", "Restarting proxsave-daemon.service...", func(taskCtx context.Context, report func(string)) error {
		rv = restartAndVerifyDaemon(taskCtx, baseDir, lockPath, interval)
		return nil
	})
	kind, title, msg := restartVerifyNotice(rv)
	_, _ = shell.Ask(ctx, session, components.NewNotice(kind, title, msg))
}

// restartVerifyNotice maps a restart+verify outcome to a dashboard notice (severity +
// title + body), shared by the "Restart daemon" button and the post-upgrade restart.
// Success is green; a deferral (backup running), a not-confirmed alignment, and an
// ambiguous restart are yellow warnings; a restart error is red.
func restartVerifyNotice(rv RestartVerifyResult) (components.NoticeKind, string, string) {
	switch {
	case rv.Err != nil:
		return components.NoticeError, "Daemon restart failed", rv.Err.Error()
	case rv.BackupWaitTimedOut:
		return components.NoticeWarning, "Daemon restart deferred",
			"A backup is currently running; the restart was deferred to avoid interrupting it. Restart again once the backup finishes, or the daemon stays on the old binary."
	case rv.TimedOut:
		return components.NoticeWarning, "Daemon restarted",
			"The daemon was restarted but could not be confirmed aligned within the timeout. Check the daemon status."
	case rv.Restarted && rv.ProcessAlive && rv.Aligned && rv.FreshInfo:
		msg := "The resident daemon (proxsave-daemon.service) was restarted and is now running the current binary"
		if v := strings.TrimSpace(rv.State.Version); v != "" {
			msg += " (v" + v + ")"
		}
		return components.NoticeSuccess, "Daemon restarted", msg + "."
	default:
		return components.NoticeWarning, "Daemon restarted",
			"The daemon was restarted but alignment could not be confirmed. Check the daemon status."
	}
}

// runDashboardDaemonAdmin installs (install=true) or reverts (install=false) the
// daemon scheduler WITHOUT leaving the graphical UI: it runs the same apply* op as
// the --daemon-setup / --daemon-remove flags inside a RunTask and shows the outcome
// as a Notice, then loops back to the menu. Console + bootstrap logging are muted
// for the duration so the ops (which log via the global logger + a bootstrap) can't
// corrupt the alternate screen.
func runDashboardDaemonAdmin(ctx context.Context, session *shell.Session, install bool, configPath, baseDir string) {
	cfg, err := daemonStatusLoadConfig(configPath, baseDir)
	if err != nil || cfg == nil {
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeError,
			"Daemon change failed", "Could not read the configuration to apply the scheduler change."))
		return
	}

	// Mute the console for the op: swap the global logger to a discarding one and
	// use a console-quiet bootstrap. Restored right after.
	prev := logging.GetDefaultLogger()
	silent := logging.New(types.LogLevelError, false)
	silent.SetOutput(io.Discard)
	logging.SetDefaultLogger(silent)
	defer logging.SetDefaultLogger(prev)
	bl := logging.NewBootstrapLogger()
	bl.SetConsoleQuiet(true)

	title := "Disabling daemon"
	work := "Reverting to the cron scheduler..."
	doneTitle := "Daemon disabled"
	doneMsg := "Reverted to the cron scheduler and removed the daemon service. Future upgrades will not reinstall it."
	if install {
		title = "Installing daemon"
		work = "Installing and enabling proxsave-daemon.service..."
		doneTitle = "Daemon installed"
		doneMsg = "The resident daemon (proxsave-daemon.service) is active. The cron entry was removed."
	}
	execToken := daemonSelfExecPath()
	var opErr error
	_ = components.RunTask(ctx, session, title, work, func(taskCtx context.Context, report func(string)) error {
		if install {
			opErr = daemonApplyDaemonMode(taskCtx, cfg, configPath, execToken, bl)
		} else {
			opErr = daemonApplyCronMode(taskCtx, cfg, configPath, execToken, bl, true)
		}
		return nil
	})

	if opErr != nil {
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeError, title+" failed", opErr.Error()))
		return
	}
	_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess, doneTitle, doneMsg))
}

// dashboardDaemonState decides which daemon command the menu offers, from the
// on-disk scheduler mode + opt-out tombstone. Unreadable config -> only Status.
func dashboardDaemonState(args *cli.Args) menu.DaemonState {
	configPath := ""
	if args != nil {
		configPath = args.ConfigPath
	}
	baseDir, _ := detectedBaseDirOrFallback()
	cfg, err := daemonStatusLoadConfig(configPath, baseDir)
	if err != nil || cfg == nil {
		return menu.DaemonStateUnknown
	}
	switch {
	case cfg.SchedulerMode == "daemon":
		return menu.DaemonStateActive
	case cfg.DaemonOptOut:
		return menu.DaemonStateDisabled
	default:
		return menu.DaemonStateOnCron
	}
}

// runDashboardDaemonStatus is a read-only, auto-on-entry check (no button), styled like
// the other diagnostics: a colored outcome keyword (from the daemon's REAL combined state
// - systemd existence refined with the heartbeat, the SAME verdict the run/healthcheck
// checks use) plus a description of the scheduler details. The notice severity colors the
// title/border so the outcome reads at a glance.
func runDashboardDaemonStatus(ctx context.Context, session *shell.Session, configPath, baseDir string) {
	mode := "unknown"
	optOut := "unknown"
	var interval time.Duration
	if cfg, err := daemonStatusLoadConfig(configPath, baseDir); err == nil && cfg != nil {
		mode = cfg.SchedulerMode
		optOut = "no"
		if cfg.DaemonOptOut {
			optOut = "yes"
		}
		interval = cfg.HealthcheckHeartbeatInterval
	}
	unit := "not installed"
	if daemonUnitInstalled() {
		unit = "installed"
	}
	active := daemonUnitActiveState(ctx)
	if active == "" {
		active = "unknown"
	}

	// Combined verdict via the SHARED daemon-state checker (systemd existence refined onto the
	// heartbeat, plus on-disk binary alignment), so this screen and the healthcheck checks can
	// never disagree about the daemon. probeProxsaveDaemonAlive is the stricter signal-0 +
	// /proc/cmdline liveness gate. The raw unit/active words above stay from the direct probes
	// so the systemctl vocabulary is shown verbatim.
	ds := health.CheckDaemonState(health.DaemonStateInput{
		BaseDir:           baseDir,
		SchedulerMode:     mode,
		HeartbeatInterval: interval,
		Now:               time.Now(),
		Presence:          daemonPresenceProbe(ctx),
		ProcAlive:         probeProxsaveDaemonAlive,
		ProcStale:         procBinaryStaleProbe,
	})
	kind, outcome, explanation := daemonStatusStyle(ds)

	body := explanation + "\n\n" +
		"Scheduler mode: " + mode + "\n" +
		"Daemon service (proxsave-daemon.service): " + unit + "\n" +
		"Service state (systemctl is-active): " + active + "\n" +
		"Opted out of auto-migration (--daemon-remove): " + optOut
	// The running version comes from the identity record (HaveInfo). The alignment verdict comes from
	// the record-independent /proc probe, so show it whenever AlignChecked -- a live daemon on a
	// replaced binary reads "Binary alignment: BEHIND". Binary alignment is known only when
	// AlignChecked; otherwise it is UNKNOWN -- report "unknown" rather than imply "aligned" or a false
	// "behind".
	if ds.HaveInfo {
		body += "\n" + "Running version: " + ds.Version + " (" + ds.Commit + ")"
	}
	if ds.HaveInfo || ds.AlignChecked {
		align := "unknown"
		switch {
		case !ds.AlignChecked:
			align = "unknown"
		case ds.Aligned:
			align = "aligned"
		default:
			align = "BEHIND (restart needed)"
		}
		body += "\n" + "Binary alignment: " + align
	}
	_, _ = shell.Ask(ctx, session, components.NewNotice(kind, "Daemon: "+outcome, body))
}

// daemonStatusStyle maps a composed daemon state to the dashboard notice severity, a short
// outcome keyword for the (colored) title, and a one-line explanation. Green only when the
// daemon is actually alive and beating; every gap is a yellow warning. It shares health's
// state vocabulary so the daemon-status screen agrees with the run/healthcheck checks by
// construction. The "behind" verdict (running an older binary than the one now on disk) is
// checked FIRST and is DISTINCT from the heartbeat-derived "running, not reporting" below.
func daemonStatusStyle(ds health.DaemonState) (components.NoticeKind, string, string) {
	// AlignChecked already implies alignment was actually determined by the record-independent /proc
	// probe, so it is the sole correct gate here; a record (HaveInfo) is not required, which is
	// exactly what lets any live daemon on a replaced binary read as behind instead of a false GREEN.
	if ds.AlignChecked && !ds.Aligned && (ds.Active || ds.ProcessAlive) {
		return components.NoticeWarning, "behind - restart needed", "The daemon is running an older binary than the one now on disk; restart it to load the update."
	}
	switch ds.TxState() {
	case health.TxNotInstalled:
		return components.NoticeWarning, "not installed", "The resident daemon service is not installed; backups run from cron."
	case health.TxNotActive:
		return components.NoticeWarning, "not running", "The daemon service is installed but stopped."
	case health.TxRunningNoReport:
		return components.NoticeWarning, "running, not reporting", "The daemon is running but has not written a heartbeat; it may be a stale build that needs a restart."
	case health.TxStale:
		return components.NoticeWarning, "stale", "The daemon's last heartbeat is old; it may be stuck or stopped."
	case health.TxNoHeartbeat:
		return components.NoticeWarning, "not running", "No daemon heartbeat was found on this host."
	default: // fresh heartbeat: the daemon process is alive and beating.
		return components.NoticeSuccess, "running", "The daemon is running and beating on schedule."
	}
}

// daemonStatusLoadConfig is a seam so tests can drive the daemon menu/status
// without an on-disk config.
var daemonStatusLoadConfig = config.LoadConfigWithBaseDir

// Seams so tests can drive the diagnostics loop without the real setup screens or
// an on-disk config. The closures pin backToMenu=true so the shared install screens
// show a "Back" leave item (return to the dashboard menu) instead of "Skip"/"Continue".
var (
	dashboardRunTelegramSetup = func(ctx context.Context, session *shell.Session, baseDir, configPath string) (installer.TelegramSetupResult, error) {
		return flowinstall.RunTelegramSetup(ctx, session, baseDir, configPath, true)
	}
	dashboardRunHealthcheckSetup = func(ctx context.Context, session *shell.Session, baseDir, configPath string) (installer.HealthcheckSetupResult, error) {
		return flowinstall.RunHealthcheckSetup(ctx, session, baseDir, configPath, true)
	}
	dashboardRunPostInstallAudit = func(ctx context.Context, session *shell.Session, execPath, configPath string) (installer.PostInstallAuditResult, error) {
		return flowinstall.RunPostInstallAudit(ctx, session, execPath, configPath, true)
	}
)

// dashboardNotConfiguredNotice shows a dismissible info notice when a diagnostics
// screen has nothing to show because the feature is not enabled on this host.
func dashboardNotConfiguredNotice(ctx context.Context, session *shell.Session, title, msg string) {
	_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeInfo, title+" not configured", msg))
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
