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
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	flowinstall "github.com/tis24dev/proxsave/internal/ui/flows/install"
	"github.com/tis24dev/proxsave/internal/ui/flows/menu"
	whatsnewflow "github.com/tis24dev/proxsave/internal/ui/flows/whatsnew"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
	whatsnew "github.com/tis24dev/proxsave/internal/whatsnew"
)

// dashboardIdleTimeout bounds how long the dashboard waits for a choice.
var dashboardIdleTimeout = input.DefaultIdleTimeout

// withDashboardIdle bounds an interactive dashboard screen with the idle timeout
// so an abandoned sub-screen cannot hold the terminal indefinitely (F04-04).
func withDashboardIdle(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, dashboardIdleTimeout)
}

// Test seams.
var (
	dashboardIsBareInvocation = dashboardBareInvocationCheck
	dashboardIsInteractive    = isTerminalInteractive
)

// Screen 0 (what's new) seams: package vars mirror the dashboard seam idiom
// (dashboardIsBareInvocation/readBuildInfo) so the mandatory continue-only-write
// test can drive the Decide/Run/MarkSeen outcomes without a real TTY or disk. The
// same whatsnewSaveSeen var is reused by the install seed in main_modes.go, so the
// continue-write and the seed share one spyable write path (open question A1).
var (
	whatsnewDecide   = whatsnew.Decide
	whatsnewRun      = whatsnewflow.Run
	whatsnewSaveSeen = whatsnew.MarkSeen
)

// whatsnewScreenTimeout is the TOTAL cap on Screen 0 (SCRN-04): a single
// context.WithTimeout around the flow run, NOT an idle-reset. On expiry the run's context
// cancels, whatsnewRun returns a non-nil deadline error, and the seen-flag is left
// untouched (the write sits inside `if err == nil`), so the fallback warning keeps firing
// on the next non-interactive run. Dedicated to Screen 0 and deliberately distinct from
// the shared withDashboardIdle/dashboardIdleTimeout used by the menu loop and sub-screens.
const whatsnewScreenTimeout = 10 * time.Minute

// maybeShowWhatsnew shows Screen 0 once, before the first menu.Run, when
// whatsnewDecide reports the installed version carries unseen notes. It fails toward
// SILENCE: a not-unseen verdict returns without touching the flag (mirroring the
// diagnostics screens that swallow errors so a broken state file never aborts or hangs
// the dashboard). A corrupt seen-flag (errors.Is(err, whatsnew.ErrStateParse))
// self-heals best-effort: it quarantines the unreadable file to .corrupt and re-seeds
// last_seen=current via whatsnewSaveSeen (the write is best-effort and silent; a failure
// just leaves the flag for the next run), then stays silent, so the next run reads a
// clean flag instead of nagging forever; any non-parse Decide error still returns
// WITHOUT writing, so a real IO/permission fault is never masked. The flow is bounded by
// the dedicated total whatsnewScreenTimeout so an accidental pty cannot hang it, and the
// seen-flag is cleared ONLY on an explicit continue (err == nil): a timeout
// (context.DeadlineExceeded) or Esc (shell.ErrAborted) is a non-nil error and must leave
// the flag untouched, so the write sits inside `if err == nil`, never in a
// defer/teardown (SCRN-03, SCRN-04, Pitfall 9).
func maybeShowWhatsnew(ctx context.Context, session *shell.Session, baseDir, toolVersion string) {
	show, body := whatsnewResolve(baseDir, toolVersion)
	if !show {
		return
	}
	whatsnewRender(ctx, session, baseDir, toolVersion, body)
}

// whatsnewResolve makes the Screen 0 SHOW/skip decision WITHOUT touching any TTY, so a
// caller can decide before starting a Bubble Tea program. It mirrors maybeShowWhatsnew's
// fail-toward-silence contract: a not-unseen verdict, or any non-parse Decide error,
// returns show=false; a corrupt seen-flag (whatsnew.ErrStateParse) self-heals best-effort
// (re-seed last_seen=current via whatsnewSaveSeen, silent) and also returns show=false. No
// dry-run gate is needed here: both callers guarantee no --dry-run before reaching this
// point (the dashboard is bare-invocation-only; showWhatsnewScreen returns early under
// args.DryRun), so the self-heal write can never coexist with --dry-run. Callers MUST NOT
// start a session unless this returns show=true: starting one only to Close it on a no-op
// leaks the terminal's async capability-query responses (mode 2026/2027) into the shell.
func whatsnewResolve(baseDir, toolVersion string) (show bool, body string) {
	show, body, err := whatsnewDecide(baseDir, toolVersion)
	if err != nil {
		if errors.Is(err, whatsnew.ErrStateParse) {
			_ = whatsnewSaveSeen(baseDir, toolVersion)
		}
		return false, ""
	}
	return show, body
}

// whatsnewRender pushes Screen 0 (body) onto session, bounded by the total
// whatsnewScreenTimeout, and marks the notes seen once the screen has been shown,
// HOWEVER the operator left it: continue, Esc, q, Ctrl+C (shell.ErrClosed) or the
// timeout.
//
// It used to write only on an explicit continue, so reading the notes and closing
// with Esc or Ctrl+C, which is what most people do, left the flag unwritten. The
// next scheduled backup then logged "has unseen release notes" as a WARNING,
// ParseLogCounts counted it, and applyIssueExitCode promoted an otherwise clean
// run to exit 1, which the daemon reports to Healthchecks as down (issue #305).
// Demanding a specific keystroke to disarm that is not a gate, it is a trap.
//
// Nothing is lost by dropping the confirmation, because presence is established
// BEFORE this point and not by which key was pressed: showWhatsnewScreen and
// maybeShowWhatsnew both gate on a real terminal (dashboardIsInteractive), the
// post-upgrade hand-off gates on whatsnewAfterUpgradeInteractive, and --dry-run
// returns before rendering. An unattended run never reaches this function, so
// reaching it means a person saw the screen.
func whatsnewRender(ctx context.Context, session *shell.Session, baseDir, toolVersion, body string) {
	wnCtx, cancel := context.WithTimeout(ctx, whatsnewScreenTimeout)
	defer cancel()
	err := whatsnewRun(wnCtx, session, body)
	// A torn-down PARENT means the screen never really ran: an external SIGINT or
	// SIGTERM, which setupRunContextWithSignals maps to ctx cancellation. A Ctrl+C
	// typed INTO the screen does not land here, because the terminal is in raw mode
	// and bubbletea reads it as a key the router turns into tea.Interrupt.
	if ctx.Err() != nil {
		return
	}
	// The 10-minute timeout is the other exit that does not count, and it is the only
	// one the interactivity gate cannot rule out. isTerminalInteractive proves a
	// TERMINAL, not a person: a detached tmux window, an expect script or an `ssh -t`
	// from a wrapper all carry a real TTY. Every OTHER resolution is a keystroke, so
	// it is evidence a person was there; sitting untouched for ten minutes is the
	// opposite.
	if errors.Is(err, context.DeadlineExceeded) {
		return
	}
	_ = whatsnewSaveSeen(baseDir, toolVersion)
}

// showWhatsnewScreen runs ONLY Screen 0 (what's new) and returns, without the dashboard
// menu. It is the body of the --show-whatsnew mode that the upgrade flow re-invokes on the
// freshly installed binary, so Screen 0 opens at the end of every interactive upgrade,
// rendered by the binary that actually carries the notes (each binary compiles in its own
// notes registry, so the download path -- where the OLD binary drives finalize -- MUST
// hand off to the installed binary to show the new release's notes). It shares the exact
// gating/self-heal (whatsnewResolve) and render/continue-write (whatsnewRender) helpers with
// the dashboard's maybeShowWhatsnew, so the seen-flag behavior is identical and dedupes
// against the dashboard trigger, and it builds its own shell.Session (the upgrade has none)
// via the same testDashboardSession seam. Crucially it resolves the SHOW/skip decision
// BEFORE building the session, so a no-op re-invocation never starts a TTY program (which
// would leak the terminal's async mode-2026/2027 replies into the shell). Interactive-gated
// so an automated/piped re-invocation is a no-op.
func showWhatsnewScreen(ctx context.Context, args *cli.Args, toolVersion string) {
	if !dashboardIsInteractive() {
		return
	}
	// --dry-run must not mutate the filesystem: maybeShowWhatsnew's self-heal and
	// continue-write both write the seen-flag, so a dry-run invocation of this (non-bare)
	// entry point skips Screen 0 entirely. The upgrade flow never passes --dry-run to the
	// re-invoked child; this guards a manual `proxsave --show-whatsnew --dry-run`.
	if args != nil && args.DryRun {
		return
	}
	// Decide BEFORE starting a Bubble Tea program. Unlike the dashboard (which keeps its
	// program alive for the menu loop that drains the terminal's replies), this entry
	// Closes immediately after Screen 0, so a program started for a no-op quits before the
	// input reader consumes the terminal's async mode-2026/2027 capability-query responses,
	// which then leak into the parent shell as stray input ("2026: command not found"). So
	// a not-unseen verdict (or a corrupt-flag self-heal) must never spin up a TTY at all.
	baseDir, _ := detectedBaseDirOrFallback()
	show, body := whatsnewResolve(baseDir, toolVersion)
	if !show {
		return
	}

	var session *shell.Session
	if s := testDashboardSession; s != nil {
		session = s(ctx)
	} else {
		buildSig := buildSignature()
		if strings.TrimSpace(buildSig) == "" {
			buildSig = "n/a"
		}
		configPath := ""
		if args != nil {
			configPath = args.ConfigPath
		}
		session = shell.Start(ctx, shell.Config{
			AppName:    "ProxSave",
			Subtitle:   "Dashboard",
			Version:    toolVersion,
			ConfigPath: configPath,
			BuildSig:   buildSig,
			UseColor:   true,
		})
	}
	defer func() { _ = session.Close() }()

	whatsnewRender(ctx, session, baseDir, toolVersion, body)
}

// dashboardBareInvocationCheck: only a completely bare `proxsave` (no flags
// at all) is eligible for the dashboard.
func dashboardBareInvocationCheck() bool { return len(os.Args) <= 1 }

// isTerminalInteractive reports whether stdin AND stdout are real interactive
// terminals (non-dumb TERM). Shared by the dashboard and the restore dispatch:
// any doubt means "behave predictably" (cron, systemd timers, pipes and
// ssh-without-tty all fail the checks). Dumb/serial terminals are excluded via
// TERM.
func isTerminalInteractive() bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return false
	}
	termEnv := strings.TrimSpace(os.Getenv("TERM"))
	if termEnv == "" || strings.EqualFold(termEnv, "dumb") {
		return false
	}
	return true
}

type dashboardActionDisposition uint8

const (
	dashboardActionUnhandled dashboardActionDisposition = iota
	dashboardActionHandled
	dashboardActionReload
)

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

	// Screen 0 (what's new): shown once, before the first menu.Run. No extra TTY or
	// bare-invocation check is needed here -- reaching this line already guarantees
	// bare + interactive (the early return at the top of maybeRunDashboard), so
	// Screen 0 stays bare-interactive-only (SCRN-02/05). The base is resolved via the
	// same detectedBaseDirOrFallback the install seed uses, so write-path == read-path
	// (open question A1).
	baseDir, _ := detectedBaseDirOrFallback()
	maybeShowWhatsnew(ctx, session, baseDir, toolVersion)

	for {
		// Idle timeout: a pty-allocating wrapper (script, tmux, ssh -tt) that
		// reaches the dashboard by accident must not hang forever. Exit, never
		// fall through to a surprise backup.
		menuCtx, cancelMenu := withDashboardIdle(ctx)
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

		// Install sub-menu: the single "Install" row opens an in-session chooser
		// (Edit install / Wipe install) that resolves to the --install or --new-install
		// flow; Back re-opens the menu. The resolved action then falls through to the
		// same flag dispatch below, so each install mode keeps its exact code path.
		if action == menu.ActionInstallMenu {
			subCtx, cancelSub := withDashboardIdle(ctx)
			sub, ok := runDashboardInstallChoice(subCtx, session)
			cancelSub()
			if !ok {
				continue
			}
			action = sub
		}

		// Diagnostics group: re-open an existing check screen in the live session
		// and loop back to the menu. These never leave the dashboard, so the flag
		// dispatch below is untouched.
		diagCtx, cancelDiag := withDashboardIdle(ctx)
		disposition := runDashboardDiagnostic(diagCtx, session, action, args, bootstrap)
		cancelDiag()
		switch disposition {
		case dashboardActionHandled:
			continue
		case dashboardActionReload:
			keepAlive = true
			closeDashboardAndRelaunch(ctx, session, getExecInfo().ExecPath, bootstrap)
			return types.ExitSuccess.Int(), true
		}

		switch action {
		case menu.ActionBackup:
			logging.DebugStepBootstrap(bootstrap, "dashboard", "action=backup")
			// Keep the graphical session OPEN and hand it off. Like the flow
			// actions below, the backup ADOPTS the altscreen program
			// (runBackupStreamed -> adoptDashboardSession) and streams its
			// [ts] LEVEL log lines into a CONTAINED, scrollable viewport panel
			// (components.RunStreamTask), so the run stays inside the frame. A
			// CLI/cron/daemon backup stashes nothing here, so it keeps running
			// plain (there is no session to adopt).
			keepAlive = true
			stashDashboardSession(session, bootstrap)
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
		case menu.ActionNewInstall:
			logging.DebugStepBootstrap(bootstrap, "dashboard", "action=new-install")
			// --new-install: the flow (runNewInstall) confirms the destructive wipe
			// itself (confirmNewInstallCharm) before resetting the base dir.
			args.NewInstall = true
		case menu.ActionSupport:
			logging.DebugStepBootstrap(bootstrap, "dashboard", "action=support")
			// Collect consent + GitHub metadata graphically; on cancel, loop back to the
			// menu. On confirm, arm support mode (DEBUG + email) with the meta already
			// collected (SupportMetaProvided skips the stdin intro), then fall through to
			// the SAME handoff as Backup so the run streams in-graphics identically.
			formCtx, cancelForm := withDashboardIdle(ctx)
			meta, ok := dashboardRunSupportForm(formCtx, session)
			cancelForm()
			if !ok {
				continue
			}
			args.Support = true
			args.SupportGitHubUser = meta.GitHubUser
			args.SupportIssueID = meta.IssueID
			args.SupportMetaProvided = true
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

// installChoice is the choice on the Install sub-menu chooser.
type installChoice int

const (
	installEdit installChoice = iota
	installWipe
	installBack
)

// runDashboardInstallChoice shows the in-session Install chooser (Edit install / Wipe
// install / Back) and resolves it to the corresponding install action: Edit install ->
// --install, Wipe install -> --new-install. Only the dashboard labels are new; the CLI
// flags are unchanged. Returns (action, true) to dispatch that flow, or (_, false) on
// Back/esc (the caller re-opens the menu).
func runDashboardInstallChoice(ctx context.Context, session *shell.Session) (menu.Action, bool) {
	errBack := errors.New("install: back")
	items := []components.SelectorItem[installChoice]{
		{Label: "Edit install", Description: "re-run the interactive installation/setup (--install)", Value: installEdit},
		{Label: "Wipe install", Description: "wipe the install directory (keep build/env/identity) then re-run the installer (--new-install)", Value: installWipe},
		{Label: "Back", Description: "return to the dashboard menu", Value: installBack},
	}
	choice, err := shell.Ask(ctx, session, components.NewSelector(
		"Install", items,
		components.WithSelectorPrompt[installChoice]("Install or re-install ProxSave."),
		components.WithSelectorBack[installChoice](errBack),
	))
	if err != nil {
		return menu.ActionExit, false
	}
	switch choice {
	case installEdit:
		return menu.ActionReconfigure, true
	case installWipe:
		return menu.ActionNewInstall, true
	default:
		return menu.ActionExit, false
	}
}

// runDashboardDiagnostic runs the check screen for a diagnostics-group action in
// the live dashboard session and reports how it handled the action. Ordinary
// diagnostics loop back to the menu; a successful upgrade requests a process reload.
// Non-diagnostic actions return dashboardActionUnhandled, leaving
// them for the normal flag dispatch. Every screen already exists and is reused
// verbatim; each is non-blocking (errors are swallowed - a failed check must never
// abort the dashboard). When a setup screen is not eligible (that feature is not
// configured on this host) it renders nothing, so a short notice is shown instead
// of a blank flicker.
func runDashboardDiagnostic(ctx context.Context, session *shell.Session, action menu.Action, args *cli.Args, bootstrap *logging.BootstrapLogger) dashboardActionDisposition {
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
			// Distinct copy per skip verdict (disabled / personal / config / identity),
			// so the twin no longer collapses to one generic "not enabled" line.
			st := orchestrator.ClassifyTelegramSetupSkip(res.TelegramSetupBootstrap)
			showDaemonResultScreen(ctx, session, "Telegram", st.Level, st.Keyword, st.Message)
		}
	case menu.ActionCheckHealthcheck:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=check-healthcheck")
		res, _ := dashboardRunHealthcheckSetup(ctx, session, baseDir, configPath)
		if !res.Shown {
			// Distinct copy per skip verdict; the centralized missing-secret state is
			// A-aware (auto-provisioned on the next daemon run, no Telegram pairing).
			st := orchestrator.ClassifyHealthcheckSetupSkip(res.HealthcheckSetupBootstrap)
			showDaemonResultScreen(ctx, session, "Backup monitoring", st.Level, st.Keyword, st.Message)
		}
	case menu.ActionPostInstallCheck:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=post-install-check")
		_, _ = dashboardRunPostInstallAudit(ctx, session, getExecInfo().ExecPath, configPath)
	case menu.ActionCheckUpgrade:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=check-upgrade")
		return runDashboardUpgradeMenu(ctx, session, configPath)
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
	case menu.ActionCleanupGuards:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=cleanup-guards")
		runDashboardCleanupGuards(ctx, session)
	default:
		return dashboardActionUnhandled
	}
	return dashboardActionHandled
}

// Seams so the daemon admin ops can be stubbed in tests (they otherwise run real
// systemctl / write /etc/systemd + backup.env).
var (
	daemonApplyDaemonMode = applyDaemonMode
	daemonApplyCronMode   = applyCronMode
	daemonRestartService  = restartDaemonService
)

// runDashboardDaemonRestart restarts the resident daemon in-session (RunTask + styled result
// screen), then loops back to the menu. Useful after a rebuild: systemd keeps the old process
// until an explicit restart, which is exactly the "installed+active but running a stale
// binary that no longer writes the status file" case the healthcheck checks now surface.
// It uses restartAndVerifyDaemon, so it also WAITS for an in-progress backup to finish
// (a restart would kill a daemon-supervised backup) and VERIFIES the daemon came back
// aligned before reporting success -- the result screen distinguishes aligned / deferred /
// not-confirmed / error.
func runDashboardDaemonRestart(ctx context.Context, session *shell.Session, configPath, baseDir string) {
	interval := time.Duration(0)
	// Fallback lock path (base-dir default) used only when the config is unreadable; the
	// normal path resolves the REAL <cfg.LockPath>/.backup.lock so the backup-wait probe
	// inspects the same lock the orchestrator acquires even under a custom LOCK_PATH.
	lockPath, lockKnown := backupLockFilePath(nil, baseDir)
	if cfg, err := daemonStatusLoadConfig(configPath, baseDir); err == nil && cfg != nil {
		interval = cfg.HealthcheckHeartbeatInterval
		if strings.TrimSpace(cfg.BaseDir) != "" {
			baseDir = cfg.BaseDir
		}
		lockPath, lockKnown = backupLockFilePath(cfg, baseDir)
	}
	var rv RestartVerifyResult
	_ = components.RunTask(ctx, session, "Restarting daemon", "Restarting "+daemonUnitName+"...", func(taskCtx context.Context, report func(string)) error {
		rv = restartAndVerifyDaemon(taskCtx, baseDir, lockPath, lockKnown, interval)
		return nil
	})
	level, keyword, explanation := restartVerifyStatus(rv)
	showDaemonResultScreen(ctx, session, "Daemon restart", level, keyword, explanation)
}

// restartVerifyStatus maps a restart+verify outcome to the styled daemon-result triple (a
// shared HealthcheckSetupLevel + a short colored keyword + a one-line explanation), shared by
// the "Restart daemon" button and the post-upgrade restart. The verdict and its severity come
// from classifyRestartVerify, shared with the CLI upgrade footer and the upgrade bootstrap
// log; only the keywords and explanations below are this surface's own.
//
// Success is green (Ok) and every gap -- including a failed restart -- is a yellow warning,
// matching both the CLI footer and daemonStatusStyle, the repo's other daemon verdict, which
// likewise has no red state. A restart that fails does not fail the upgrade: the new binary is
// already installed and the daemon simply still runs the old one.
//
// The version shown here is rv.State.Version, what the RUNNING daemon reports -- not the
// version the upgrade just installed, which is what summarizeRestartVerify shows instead.
//
// TimedOut and Unconfirmed deliberately render identically here (this screen has no useful
// distinction to draw between them), while the footer and the log word them differently. That
// is a rendering choice, which is why the two stay distinct outcomes upstream.
func restartVerifyStatus(rv RestartVerifyResult) (orchestrator.HealthcheckSetupLevel, string, string) {
	outcome := classifyRestartVerify(rv)
	switch outcome {
	case restartVerifyError:
		return outcome.level(), "RESTART FAILED", rv.Err.Error()
	case restartVerifyDeferredConfig:
		return outcome.level(), "DEFERRED - CONFIG UNREADABLE",
			"The config could not be read, so the real backup lock path is unknown; restart again once it is readable, or the daemon stays on the old binary."
	case restartVerifyDeferredBackup:
		return outcome.level(), "DEFERRED - BACKUP RUNNING",
			"Restart again once the backup finishes, or the daemon stays on the old binary."
	case restartVerifyAligned:
		// Success: the keyword ("RESTARTED, ALIGNED (vX)") already says everything, so no
		// explanation line -- a what-to-do suggestion only appears on a problem outcome.
		keyword := "RESTARTED, ALIGNED"
		if v := strings.TrimSpace(rv.State.Version); v != "" {
			keyword += " (v" + v + ")"
		}
		return outcome.level(), keyword, ""
	default: // restartVerifyTimedOut and restartVerifyUnconfirmed
		return outcome.level(), "RESTARTED, NOT CONFIRMED",
			"Open Daemon status to confirm it came back aligned."
	}
}

// daemonResultAction is the single choice on a daemon action-result screen: return to the
// dashboard menu (mirrors daemonStatusAction, which additionally offers a re-check).
type daemonResultAction int

const daemonResultActionBack daemonResultAction = iota

// showDaemonResultScreen presents a daemon action outcome (restart / install / revert / error)
// with the SAME look as the daemon-status screen: a styled "Status:" line (a colored keyword +
// a Subtle explanation) above a single Back item. It loops to Back/esc and is non-blocking on
// any UI failure, mirroring runDashboardDaemonStatus. This is the single styled result renderer
// shared by every daemon action result, so they can never disagree visually with the status screen.
// showDaemonResultScreenFn is the seam over the daemon result screen. It exists because
// this screen is the ONLY channel that reaches a TUI operator on these paths - the global
// logger is swapped for a discarding one and the bootstrap console is muted for the whole
// op - so what it carries is behaviour, not presentation, and behaviour has to be
// assertable. Driving the real screen instead means matching a string that
// orchestrator.BuildStatusPrompt has already reflowed to the terminal width, which passes
// or fails on where a line happened to wrap.
var showDaemonResultScreenFn = showDaemonResultScreen

// daemonFailureScreenText is the body of the generic daemon failure screen: the one fact the
// operator cannot get from the error string, then the error itself indented under it, which is
// the shape buildCronRevertScreen already uses for a teardown error.
//
// The fact is worth a line because installDaemonService fails in four places and they leave
// two different hosts behind. Validating the exec/config tokens and writing the unit file
// leave nothing (the write is atomic, so there is no half-file); daemon-reload and
// enable --now fail with the unit file already on disk. Told only the error, the operator
// could not tell whether there was something to clean up before retrying.
//
// The sentence claims exactly what daemonUnitInstalled measures, which is one os.Stat of the
// unit path. Not "enabled", not "not started": the probe cannot see either, and this screen is
// reached from the disable direction too, where a leftover unit means the opposite thing.
func daemonFailureScreenText(opErr error) string {
	unit := "Daemon service: no unit file on disk."
	if daemonInstalledProbe() {
		unit = "Daemon service: the unit file is on disk."
	}
	return unit + "\n  " + opErr.Error()
}

func showDaemonResultScreen(ctx context.Context, session *shell.Session, title string, level orchestrator.HealthcheckSetupLevel, keyword, explanation string) {
	errDaemonResultEsc := errors.New("daemon result: esc")
	prompt := orchestrator.BuildStatusPrompt(level, keyword, explanation)
	items := []components.SelectorItem[daemonResultAction]{
		{Label: "Back", Description: "return to the dashboard menu", Value: daemonResultActionBack},
	}
	for {
		action, err := shell.Ask(ctx, session, components.NewSelector(
			title, items,
			components.WithSelectorPromptStyled[daemonResultAction](prompt),
			components.WithSelectorBack[daemonResultAction](errDaemonResultEsc),
		))
		if err != nil {
			return
		}
		switch action {
		case daemonResultActionBack:
			return
		}
	}
}

// runDashboardDaemonAdmin installs (install=true) or reverts (install=false) the
// daemon scheduler WITHOUT leaving the graphical UI: it runs the same apply* op as
// the --daemon-setup / --daemon-remove flags inside a RunTask and shows the outcome
// via the SAME styled result screen as the daemon-status check (showDaemonResultScreen),
// then loops back to the menu. Console + bootstrap logging are muted for the duration
// so the ops (which log via the global logger + a bootstrap) can't corrupt the alternate
// screen.
func runDashboardDaemonAdmin(ctx context.Context, session *shell.Session, install bool, configPath, baseDir string) {
	cfg, err := daemonStatusLoadConfig(configPath, baseDir)
	if err != nil || cfg == nil {
		showDaemonResultScreenFn(ctx, session, "Daemon change failed", orchestrator.HealthcheckSetupLevelError,
			"CONFIG UNREADABLE", "Configuration: unreadable, the scheduler change was not applied.")
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
	doneKeyword := "REVERTED TO CRON"
	doneLevel := orchestrator.HealthcheckSetupLevelOk
	// doneMsg for the revert is completed after the op: "future upgrades will not reinstall it"
	// is a claim about a config write that is best-effort, so it may only be made once that
	// write is known to have landed.
	doneMsg := ""
	if install {
		title = "Installing daemon"
		work = "Installing and enabling " + daemonUnitName + "..."
		doneTitle = "Daemon installed"
		doneKeyword = "INSTALLED"
		// doneMsg for the install path is deliberately NOT set here. It has to state what
		// the cron removal actually did, and that is only known after the op below runs.
	}
	execToken := daemonSelfExecPath()
	var opErr error
	var cronOutcome cronRemovalOutcome
	var revert cronRevertReport
	_ = components.RunTask(ctx, session, title, work, func(taskCtx context.Context, report func(string)) error {
		if install {
			cronOutcome, opErr = daemonApplyDaemonMode(taskCtx, cfg, configPath, execToken, bl)
		} else {
			revert, opErr = daemonApplyCronMode(taskCtx, cfg, configPath, execToken, bl)
		}
		return nil
	})

	if opErr != nil {
		if errors.Is(opErr, errDaemonTeardownBackupRunning) {
			showDaemonResultScreenFn(ctx, session, "Daemon disable deferred", orchestrator.HealthcheckSetupLevelWarn,
				"DEFERRED - BACKUP RUNNING", "Daemon service: NOT removed, a backup is in progress.\nTry again once the backup finishes.")
			return
		}
		// A failed TEARDOWN is the only revert failure that arrives with a populated report, and
		// the generic screen below discards it: the duplicate-schedule finding, the /etc advisory
		// and the config-write fact were all lost on the one path whose logger is muted for the
		// whole operation and never flushed.
		//
		// The guard keys on the SENTINEL rather than on the report looking empty, because
		// applyCronMode's two sentinels abort before anything is written and hand back a ZERO
		// value whose fields are defaults and not measurements - and a teardown failure on a host
		// with no cron line, no config write and no findings produces a report byte-identical to
		// it. The install direction has nothing to lose either way: applyDaemonMode's single error
		// return is installDaemonService's, which fires before any cron or config work.
		if !install && !errors.Is(opErr, errDaemonTeardownConfigUnreadable) {
			level, keyword, text := buildCronRevertScreen(revert, opErr)
			showDaemonResultScreenFn(ctx, session, "Daemon disable failed", level, keyword, text)
			return
		}
		showDaemonResultScreenFn(ctx, session, title+" failed", orchestrator.HealthcheckSetupLevelError, "FAILED", daemonFailureScreenText(opErr))
		return
	}
	// The result screen is the only channel that reaches the operator here (console logging
	// and the bootstrap console are muted for the whole op above), so it must carry the same
	// verified crontab fact the CLI log line carries. On a host whose backup is scheduled
	// through a wrapper there was no proxsave cron entry to remove, and claiming one was
	// removed is how issue #298 stayed invisible on both front-ends at once.
	if install {
		// Same shape as the revert screen: one fact per line behind a fixed label. The removal
		// clause keeps its own wording, since three CLI call sites print the identical sentence
		// and the point of that renderer is that all four front-ends say the same thing.
		doneMsg = "Daemon service: active (" + daemonUnitName + ").\nCron entry: " + cronRemovalScreenClause(cronOutcome)
		// The install direction had no channel for the #298 finding at all: the warning it
		// produces goes through the bootstrap logger muted above, and the screen said INSTALLED
		// in green over a host that now runs the backup twice. The removal clause cannot carry
		// it either - "no proxsave cron entry was present to remove" is exactly what a clean
		// install says - so on a finding the screen states the duplication instead and the
		// level goes yellow.
		// An unverified removal already SAYS a proxsave entry may still be scheduled next to the
		// daemon just installed. Under a green tick that reads as reassurance, so the level has
		// to carry the same doubt the sentence does. The duplicate finding is stronger evidence
		// and wins the keyword when both hold.
		//
		// The keyword states what HAPPENED, not what survives. Verified=false means the crontab
		// could not be checked, which is not the same as knowing an entry is still there, and a
		// keyword asserting one would claim more than the sentence under it.
		if !cronOutcome.Verified {
			doneLevel = orchestrator.HealthcheckSetupLevelWarn
			doneKeyword = "INSTALLED - NO CRON ENTRY REMOVED"
		}
		if cronOutcome.UnmanagedSchedules > 0 {
			doneLevel = orchestrator.HealthcheckSetupLevelWarn
			doneKeyword = "INSTALLED - DUPLICATE SCHEDULE"
			// ADDED, not swapped in: replacing the message threw away the line saying what the
			// removal actually did, on the screen that is the only channel this path has.
			doneMsg += "\nCheck your crons to remove duplication."
		}
	}
	if !install {
		doneLevel, doneKeyword, doneMsg = buildCronRevertScreen(revert, nil)
	}
	showDaemonResultScreenFn(ctx, session, doneTitle, doneLevel, doneKeyword, doneMsg)
}

// buildCronRevertScreen renders the revert's result screen: the level, the keyword and the whole
// text, including the two habitats' advisories. It is a pure function of the report and of
// whether the teardown failed, so the wording is assertable without a TUI driver and without a
// 60-second deadline.
//
// It exists because the FAILURE path needs the same facts and the same advisory append as the
// success path, and the append used to sit below the early return that discarded them. Extracting
// it first means the failure arm is a wiring change rather than a second copy of thirty lines
// that can drift from the original.
//
// teardownErr is nil on every path that got as far as removing the daemon service. When it is
// set, everything the report established BEFORE the teardown is still true and is stated;
// everything the failed teardown killed is dropped. See the arm itself for which is which.
func buildCronRevertScreen(revert cronRevertReport, teardownErr error) (orchestrator.HealthcheckSetupLevel, string, string) {
	// One fact per LINE, each behind the label that names what it is about, in the same order on
	// every screen. The daemon-status screen already reads this way ("Scheduler mode:", "Daemon
	// service (...):", "Service state (...):"), and these screens did not: they were one
	// paragraph the box wrapped wherever it ran out of width, up to 296 characters on the
	// teardown-failure arm, sitting directly above advisory blocks that are already one line per
	// finding. Two styles in one screen, and the operator hunting for "did the cron entry get
	// written" inside prose.
	lines := []string{}
	daemon := "Daemon service: removed."
	cron := "Cron entry: in the crontab."
	config := "Configuration: SCHEDULER_MODE=cron recorded."
	if !revert.ModeRecorded {
		// cronModeRecordClause's own failure wording ends "...while no daemon is installed",
		// which the teardown arm below disproves, so the screen states the config fact itself and
		// leaves that clause to the CLI, where it is true.
		config = "Configuration: NOT updated, it still records the daemon engine."
	}
	found := len(revert.UnmanagedAdvisory) > 0 || len(revert.SystemCronAdvisory) > 0

	// A FAILED TEARDOWN. removeDaemonServiceFn is the last thing applyCronMode does, so
	// everything the report carries was established before it and none of it is invalidated by
	// it: the cron line was read back from the crontab, the wrapper lines were read from the
	// crontab ProxSave never edits, the config write already returned, and the /etc scan runs
	// after the teardown by design. What the failure DOES kill is the claim that the service was
	// removed, and that is the only line this arm rewrites.
	//
	// The host is left with a unit file still on disk (os.Remove failed) or deleted but not
	// reloaded (daemon-reload failed); the preceding "systemctl disable --now" is best-effort and
	// its failure is only Debug-logged, so the process may or may not still be alive. "May still
	// be running" over-warns on the host where it really did stop, which costs the operator one
	// check; claiming the daemon is gone when it is not is the expensive direction and is the bug
	// class this area keeps producing. See backup_notifications.go on a failed teardown leaving a
	// live, transmitting daemon on a host whose config already reads cron.
	if teardownErr != nil {
		level := orchestrator.HealthcheckSetupLevelError
		keyword := "DAEMON NOT REMOVED"
		if revert.CronScheduled {
			// Two schedulers at the same minute: the daemon runs at SCHEDULER_TIME and that is
			// the time the cron line carries. The per-run lock makes the loser exit 16, i.e. a
			// failed backup reported every night - the third duplicate source in issue #298.
			// The duplicate takes the keyword because it is what changes tonight.
			keyword = "DAEMON NOT REMOVED - DUPLICATE SCHEDULE"
			cron = "Cron entry: in the crontab, so the backup may run twice."
		} else {
			cron = "Cron entry: NOT written."
		}
		lines = append(lines,
			"Daemon service: NOT removed, may still be running.",
			// Indented on its own line: it is an external tool's message, not a sentence of ours,
			// and inside the prose it used to end up mid-paragraph between two facts.
			"  "+teardownErr.Error(),
			cron, config)
		return level, keyword, appendCronRevertAdvisories(strings.Join(lines, "\n"), revert)
	}

	level := orchestrator.HealthcheckSetupLevelOk
	keyword := "REVERTED TO CRON"
	if !revert.ModeRecorded {
		level = orchestrator.HealthcheckSetupLevelWarn
		keyword = "REVERTED - CONFIG NOT UPDATED"
	}
	// The revert writes its cron line even when something unmanaged already schedules ProxSave,
	// so it creates this duplicate itself and has to say so. The keyword takes precedence over
	// the config one because this is what changes tonight; the config fact keeps its own line
	// either way.
	//
	// BOTH habitats raise it. The CLI already emits a WARNING for the root crontab wrappers and
	// for the /etc findings alike, so a screen that reacted to only one handed the same host two
	// different levels depending on which channel the operator read.
	if found {
		level = orchestrator.HealthcheckSetupLevelWarn
		keyword = "REVERTED - DUPLICATE SCHEDULE"
	}
	// Nothing scheduling the backup outranks everything else, so it is decided last.
	//
	// CronScheduled is an assertion about the ROOT crontab alone: canonicalCronLinePresent reads
	// crontabReadLinesFn and nothing else, and the unmanaged entries it does not match are not
	// proxsave-named. So with anything listed below, the host may well still be scheduled - and
	// it is the same host where ProxSave could neither write its own line nor remove the other
	// ones. The denial is stated only when there is nothing underneath to contradict it.
	if !revert.CronScheduled {
		level = orchestrator.HealthcheckSetupLevelError
		keyword = "NO SCHEDULE"
		cron = "Cron entry: NOT written, nothing is scheduling the backup."
		if found {
			keyword = "CRON ENTRY NOT WRITTEN"
			cron = "Cron entry: NOT written."
		}
		// An unreadable crontab reaches here with CronScheduled false, and the denial above is
		// then a measurement nobody took: the write may well have landed. The level stays Error
		// because a host that cannot be checked may equally be a host with nothing scheduled,
		// and that is the expensive one.
		if !revert.CronVerified {
			keyword = "CRON ENTRY NOT CHECKED"
			cron = "Cron entry: could not be checked, the crontab was unreadable."
		}
	}
	lines = append(lines, daemon, cron, config)
	if len(revert.UnmanagedAdvisory) > 0 {
		lines = append(lines, "Check your crons to remove duplication.")
	}
	return level, keyword, appendCronRevertAdvisories(strings.Join(lines, "\n"), revert)
}

// appendCronRevertAdvisories appends the two habitats' pre-rendered advisory lines to the
// revert's message. The revert has the same problem in its own direction: applyCronMode emits its /etc
// schedule advisory through the bootstrap logger, which is console-quiet here and is
// never flushed, so the operator used to get a green "REVERTED TO CRON" over a host that
// may now be scheduled twice - the CLI said it, the TUI did not. Appending the lines to
// the result screen is the only channel left open on this path.
// BOTH habitats are listed, in the order the CLI prints them: the root crontab ProxSave owns
// and just rewrote, then the /etc files it may only read.
func appendCronRevertAdvisories(msg string, revert cronRevertReport) string {
	for _, advisory := range [][]string{revert.UnmanagedAdvisory, revert.SystemCronAdvisory} {
		if len(advisory) > 0 {
			msg = strings.TrimSpace(msg + "\n\n" + strings.Join(advisory, "\n"))
		}
	}
	return msg
}

// dashboardDaemonState decides which daemon command the menu offers, from the on-disk
// scheduler mode alone. Unreadable config -> only Status.
//
// It used to consult DAEMON_OPT_OUT as well, purely to label the same command "Re-enable"
// rather than "Install" on a host that had reverted. Both rows ran ActionDaemonSetup and the
// difference was never one the operator could act on, so the retired tombstone leaves nothing
// behind here: cron is cron however the host got there.
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
	if cfg.SchedulerMode == "daemon" {
		return menu.DaemonStateActive
	}
	return menu.DaemonStateOnCron
}

// daemonStatusAction is the choice on the daemon-status screen: re-run the state check,
// or return to the dashboard menu (mirrors healthcheckAction / telegramAction).
type daemonStatusAction int

const (
	daemonStatusActionCheck daemonStatusAction = iota
	daemonStatusActionBack
)

// runDashboardDaemonStatus shows the daemon-status screen with the SAME look as the Telegram
// and Healthchecks check screens: a styled prompt (a colored "Status:" keyword + explanation +
// a Details block) presented ABOVE a Check/Back selector. Check re-computes the state (so after
// the user restarts the daemon elsewhere it flips to aligned/running), Back/esc returns to the
// menu. The verdict comes from the daemon's REAL combined state (systemd existence refined with
// the heartbeat + on-disk binary alignment, the SAME verdict the run/healthcheck checks use), so
// this screen and the healthcheck checks can never disagree.
func runDashboardDaemonStatus(ctx context.Context, session *shell.Session, configPath, baseDir string) {
	errDaemonStatusEsc := errors.New("daemon status: esc")
	for {
		// cfgErr is kept, not discarded: this is the ONE screen that reaches the
		// collector without a configuration, and the personal-script block says so.
		// Without the cause it could only report that the file was unreadable, never
		// which file or why - the same half-answer runDashboardDaemonAdmin refuses.
		var cfg *config.Config
		loaded, cfgErr := daemonStatusLoadConfig(configPath, baseDir)
		if cfgErr == nil && loaded != nil {
			cfg = loaded
			if strings.TrimSpace(loaded.BaseDir) != "" {
				baseDir = loaded.BaseDir
			}
		}
		diagnostics := daemonDiagnosticsCollector(ctx, cfg, cfgErr, baseDir)
		// Dashboard "Status:" keywords are ALL-CAPS (the house convention across every
		// dashboard Status screen). daemonStatusStyle is shared with the plain-text
		// --daemon-status CLI line, so uppercase HERE (the graphical consumer) rather than at
		// the source, keeping the CLI/log readout in its natural case.
		diagnostics.Keyword = strings.ToUpper(diagnostics.Keyword)
		prompt := buildDaemonStatusPrompt(diagnostics)

		items := []components.SelectorItem[daemonStatusAction]{
			{Label: "Re-check", Description: "re-run the daemon state check", Value: daemonStatusActionCheck},
			{Label: "Back", Description: "return to the dashboard menu", Value: daemonStatusActionBack},
		}

		action, err := shell.Ask(ctx, session, components.NewSelector(
			"Daemon status", items,
			components.WithSelectorPromptStyled[daemonStatusAction](prompt),
			components.WithSelectorBack[daemonStatusAction](errDaemonStatusEsc),
		))
		if err != nil {
			// Esc/abort or any UI failure returns to the menu: this screen is non-blocking.
			return
		}
		switch action {
		case daemonStatusActionBack:
			return
		case daemonStatusActionCheck:
			// Loop: recompute the state so a restart done elsewhere shows up on the next render.
		}
	}
}

// renderDaemonStatusLevel is the colored-keyword renderer for the daemon-status "Status:" line.
// It delegates to the shared orchestrator.RenderStatusLevel so the daemon, workflow, and install
// healthcheck/audit screens can never drift apart.
func renderDaemonStatusLevel(level orchestrator.HealthcheckSetupLevel, text string) string {
	return orchestrator.RenderStatusLevel(level, text)
}

// buildDaemonStatusPrompt renders the styled prompt shown above the Check/Back choices (mirrors
// buildHealthcheckPrompt): a short intro, a two-line Status block (a colored keyword + a Subtle
// explanation), then a Details block with the scheduler/service facts. The wording/logic of the
// detail lines is unchanged from the old Notice body; only the presentation moved into the prompt.
func buildDaemonStatusPrompt(diagnostics daemonDiagnostics) string {
	// Every dynamic segment below carries text from outside this file (keyword/explanation from
	// daemonStatusStyle, mode from the config file, active from systemctl, Version/Commit RAW from
	// .daemon_info.json), so each is SanitizeText-scrubbed before theme rendering to keep raw
	// ANSI/OSC/C0/C1 escapes out of the verbatim WithSelectorPromptStyled path. Compile-time
	// literals (unit, the "Binary alignment" verdict) are left as-is: sanitizing them is a
	// no-op.
	var b strings.Builder
	b.WriteString(theme.Text.Render("Resident backup daemon (runs scheduled backups + healthchecks reporting)."))
	b.WriteString("\n\n")

	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(renderDaemonStatusLevel(diagnostics.Level, components.SanitizeText(diagnostics.Keyword)))
	if exp := components.SanitizeText(diagnostics.Explanation); exp != "" {
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render(exp))
	}

	b.WriteString("\n\n")
	b.WriteString(theme.Text.Render("Details:"))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Scheduler mode: " + components.SanitizeText(diagnostics.Mode)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Daemon service (" + daemonUnitName + "): " + diagnostics.Unit))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Service state (systemctl is-active): " + components.SanitizeText(diagnostics.Active)))
	// The running version comes from the identity record (HaveInfo). The alignment verdict comes from
	// the record-independent /proc probe, so show it whenever AlignChecked -- a live daemon on a
	// replaced binary reads "Binary alignment: BEHIND". Binary alignment is known only when
	// AlignChecked; otherwise it is UNKNOWN -- report "unknown" rather than imply "aligned" or a false
	// "behind".
	if diagnostics.State.HaveInfo {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Running version: " + components.SanitizeText(diagnostics.State.Version) + " (" + components.SanitizeText(diagnostics.State.Commit) + ")"))
	}
	if diagnostics.State.HaveInfo || diagnostics.State.AlignChecked {
		align := "unknown"
		if diagnostics.State.AlignChecked {
			if diagnostics.State.Aligned {
				align = "aligned"
			} else {
				align = "BEHIND (restart needed)"
			}
		}
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Binary alignment: " + align))
	}
	if diagnostics.Runtime.Availability == daemonRuntimeAvailable {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Running daemon configuration: " + components.SanitizeText(diagnostics.Runtime.ConfigPath)))
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Running daemon loaded at: " + time.Unix(diagnostics.Runtime.StartTS, 0).Format(time.RFC3339)))
	} else if diagnostics.Runtime.Availability != daemonRuntimeNotApplicable {
		b.WriteString("\n")
		b.WriteString(theme.WarningText.Render("Running daemon personal-script state: UNAVAILABLE"))
		if reason := components.SanitizeText(diagnostics.Runtime.Reason); reason != "" {
			b.WriteString(theme.Subtle.Render(" (" + reason + ")"))
		}
	}
	b.WriteString("\n")
	b.WriteString(buildDashboardPersonalScriptComparison("Personal pre-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Pre))
	b.WriteString("\n")
	b.WriteString(buildDashboardPersonalScriptComparison("Personal post-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Post))
	return b.String()
}

func buildDashboardPersonalScriptComparison(label string, runtime daemonRuntimeDiagnostic, comparison personalScriptComparison) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render(label + ":"))
	b.WriteString("\n")
	switch runtime.Availability {
	case daemonRuntimeAvailable:
		b.WriteString(buildDashboardPersonalScriptLine("  Running daemon", comparison.Running))
	case daemonRuntimeNotApplicable:
		b.WriteString(theme.Subtle.Render("  Running daemon: NOT RUNNING"))
	default:
		reason := components.SanitizeText(runtime.Reason)
		b.WriteString(theme.WarningText.Render("  Running daemon state: UNAVAILABLE"))
		if reason != "" {
			b.WriteString(theme.Subtle.Render(" (" + reason + ")"))
		}
	}
	b.WriteString("\n")
	b.WriteString(buildDashboardPersonalScriptLine("  Current configuration", comparison.Current))
	b.WriteString("\n")
	b.WriteString(buildDashboardPersonalScriptSynchronization(comparison))
	return b.String()
}

func buildDashboardPersonalScriptSynchronization(comparison personalScriptComparison) string {
	reason := components.SanitizeText(comparison.SyncReason)
	prefix := theme.Text.Render("  Synchronization: ")
	switch comparison.Synchronization {
	case personalScriptInSync:
		return prefix + theme.SuccessText.Render("IN SYNC")
	case personalScriptConfigurationDrift:
		return prefix + theme.WarningText.Render("OUT OF SYNC") + theme.Subtle.Render(" ("+reason+")")
	case personalScriptPathStateChanged:
		return prefix + theme.WarningText.Render("PATH STATE CHANGED SINCE STARTUP") + theme.Subtle.Render(" ("+reason+")")
	case personalScriptRuntimeUnavailable, personalScriptCurrentUnavailable:
		return prefix + theme.WarningText.Render("UNKNOWN") + theme.Subtle.Render(" ("+reason+")")
	case personalScriptSyncNotApplicable:
		return prefix + theme.Subtle.Render("NOT APPLICABLE")
	default:
		return prefix + theme.WarningText.Render("UNKNOWN")
	}
}

func buildDashboardPersonalScriptLine(label string, diagnostic personalScriptDiagnostic) string {
	line := theme.Text.Render(label + ": ")
	path := components.SanitizeText(diagnostic.Path)
	reason := components.SanitizeText(diagnostic.Reason)
	switch diagnostic.State {
	case personalScriptReady:
		line += theme.SuccessText.Render("READY")
		if path != "" {
			line += theme.Subtle.Render(" (" + path + ")")
		}
	case personalScriptReadyWithWarning:
		line += theme.WarningText.Render("READY WITH WARNING")
		if path != "" {
			line += theme.Subtle.Render(" (" + path + ")")
		}
		if reason != "" {
			line += theme.Subtle.Render(": " + reason)
		}
	case personalScriptRefused:
		line += theme.ErrorText.Render("REFUSED")
		if path != "" {
			line += theme.Subtle.Render(" (" + path + ")")
		}
		if reason != "" {
			line += theme.Subtle.Render(": " + reason)
		}
	case personalScriptUnknown:
		line += theme.WarningText.Render("UNKNOWN")
		if reason != "" {
			line += theme.Subtle.Render(": " + reason)
		}
	default:
		line += theme.Subtle.Render("NOT CONFIGURED")
	}
	return line
}

// daemonStatusStyle maps a composed daemon state to a shared HealthcheckSetupLevel (the SAME
// palette the Telegram/Healthchecks screens use), a short outcome keyword, and a one-line
// explanation. Green (Ok) only when the daemon is actually alive and beating; every gap is a
// yellow warning (Warn). It shares health's state vocabulary so the daemon-status screen agrees
// with the run/healthcheck checks by construction. The "behind" verdict (running an older binary
// than the one now on disk) is checked FIRST and is DISTINCT from the heartbeat-derived
// "running, not reporting" below. There is no red/Error daemon state today; all gaps stay Warn.
func daemonStatusStyle(ds health.DaemonState) (orchestrator.HealthcheckSetupLevel, string, string) {
	// AlignChecked already implies alignment was actually determined by the record-independent /proc
	// probe, so it is the sole correct gate here; a record (HaveInfo) is not required, which is
	// exactly what lets any live daemon on a replaced binary read as behind instead of a false GREEN.
	if ds.AlignChecked && !ds.Aligned && (ds.Active || ds.ProcessAlive) {
		return orchestrator.HealthcheckSetupLevelWarn, "behind - restart needed", "The daemon is running an older binary than the one now on disk; restart it to load the update."
	}
	switch ds.TxState() {
	case health.TxNotInstalled:
		return orchestrator.HealthcheckSetupLevelWarn, "not installed", "The resident daemon service is not installed; backups run from cron."
	case health.TxNotActive:
		return orchestrator.HealthcheckSetupLevelWarn, "not running", "The daemon service is installed but stopped."
	case health.TxRunningNoReport:
		return orchestrator.HealthcheckSetupLevelWarn, "running, not reporting", "The daemon is running but has not written a heartbeat; it may be a stale build that needs a restart."
	case health.TxStale:
		return orchestrator.HealthcheckSetupLevelWarn, "stale", "The daemon's last heartbeat is old; it may be stuck or stopped."
	case health.TxNoHeartbeat:
		return orchestrator.HealthcheckSetupLevelWarn, "not running", "No daemon heartbeat was found on this host."
	default: // fresh heartbeat: the daemon process is alive and beating.
		return orchestrator.HealthcheckSetupLevelOk, "running", "The daemon is running and beating on schedule."
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
	// graphical latches true once a flow actually ADOPTS the handed-off session
	// (adoptDashboardSession), i.e. the run was launched from the dashboard and
	// ran in-graphics. Unlike session/bootstrap it is NOT cleared by adoption, so
	// it still reports "this run was graphical" to the deferred final-summary
	// footer, which is a CLI affordance suppressed for graphical runs. Reset only
	// at end-of-process (releaseDashboardLeftovers) for test isolation.
	graphical bool
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

// dashboardRunWasGraphical reports whether this run adopted the dashboard's
// handed-off session (i.e. it was launched from the dashboard and ran
// in-graphics). Unlike dashboardHandoffPending it stays true AFTER adoption, so
// the deferred final-summary footer can be suppressed for graphical runs (the
// outcome is already shown on-screen) while CLI/cron/daemon runs still print it.
func dashboardRunWasGraphical() bool {
	dashboardHandoff.mu.Lock()
	defer dashboardHandoff.mu.Unlock()
	return dashboardHandoff.graphical
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
	if session != nil {
		// A real adoption: latch "this run is graphical" (never cleared here) so
		// the deferred CLI footer is suppressed for the rest of the run.
		dashboardHandoff.graphical = true
	}
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
	// Reset the graphical latch (before the nil-session early return: after a
	// successful adoption session is already nil here). Runs at process end, so
	// the footer has already read it; this is purely for test isolation across
	// the shared package global.
	dashboardHandoff.graphical = false
	dashboardHandoff.mu.Unlock()
	if session == nil {
		return
	}
	_ = session.Close()
	bootstrap.SetConsoleQuiet(false)
	logging.GetDefaultLogger().SetOutput(nil)
	bootstrap.ReplayConsoleSince(mark)
}
