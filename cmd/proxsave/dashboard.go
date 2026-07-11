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
	"github.com/tis24dev/proxsave/internal/ui/theme"
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

		// Install sub-menu: the single "Install" row opens an in-session chooser
		// (Edit install / Wipe install) that resolves to the --install or --new-install
		// flow; Back re-opens the menu. The resolved action then falls through to the
		// same flag dispatch below, so each install mode keeps its exact code path.
		if action == menu.ActionInstallMenu {
			sub, ok := runDashboardInstallChoice(ctx, session)
			if !ok {
				continue
			}
			action = sub
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
			meta, ok := dashboardRunSupportForm(ctx, session)
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
			dashboardNotConfiguredNotice(ctx, session, "Backup monitoring", "Backup monitoring (healthchecks) is not enabled on this host.")
		}
	case menu.ActionPostInstallCheck:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=post-install-check")
		_, _ = dashboardRunPostInstallAudit(ctx, session, getExecInfo().ExecPath, configPath)
	case menu.ActionCheckUpgrade:
		logging.DebugStepBootstrap(bootstrap, "dashboard", "action=check-upgrade")
		runDashboardUpgradeMenu(ctx, session, configPath)
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
	level, keyword, explanation := restartVerifyStatus(rv)
	showDaemonResultScreen(ctx, session, "Daemon restart", level, keyword, explanation)
}

// restartVerifyStatus maps a restart+verify outcome to the styled daemon-result triple (a
// shared HealthcheckSetupLevel + a short colored keyword + a one-line explanation), shared by
// the "Restart daemon" button and the post-upgrade restart. Success is green (Ok); a deferral
// (backup running), a not-confirmed alignment, and an ambiguous restart are yellow warnings
// (Warn); a restart error is red (Error). The explanation strings are unchanged from the old
// notice bodies -- only the outcome keyword is added for the colored "Status:" line.
func restartVerifyStatus(rv RestartVerifyResult) (orchestrator.HealthcheckSetupLevel, string, string) {
	switch {
	case rv.Err != nil:
		return orchestrator.HealthcheckSetupLevelError, "restart failed", rv.Err.Error()
	case rv.BackupWaitTimedOut:
		return orchestrator.HealthcheckSetupLevelWarn, "deferred - backup running",
			"Restart again once the backup finishes, or the daemon stays on the old binary."
	case rv.TimedOut:
		return orchestrator.HealthcheckSetupLevelWarn, "restarted, not confirmed",
			"Open Daemon status to confirm it came back aligned."
	case rv.Restarted && rv.ProcessAlive && rv.Aligned && rv.FreshInfo:
		// Success: the keyword ("restarted, aligned (vX)") already says everything, so no
		// explanation line -- a what-to-do suggestion only appears on a problem outcome.
		keyword := "restarted, aligned"
		if v := strings.TrimSpace(rv.State.Version); v != "" {
			keyword += " (v" + v + ")"
		}
		return orchestrator.HealthcheckSetupLevelOk, keyword, ""
	default:
		return orchestrator.HealthcheckSetupLevelWarn, "restarted, not confirmed",
			"Open Daemon status to confirm it came back aligned."
	}
}

// daemonResultAction is the single choice on a daemon action-result screen: return to the
// dashboard menu (mirrors daemonStatusAction, which additionally offers a re-check).
type daemonResultAction int

const daemonResultActionBack daemonResultAction = iota

// buildDaemonResultPrompt renders the styled "Status:" block for a daemon action-result screen,
// mirroring buildDaemonStatusPrompt's Status block: a colored keyword line + a Subtle
// explanation on the next line. This is the single styled renderer for the daemon action outcomes.
func buildDaemonResultPrompt(level orchestrator.HealthcheckSetupLevel, keyword, explanation string) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(renderDaemonStatusLevel(level, components.SanitizeText(keyword)))
	// A what-to-do suggestion is shown only on a problem outcome (success passes ""), with a
	// blank line separating it from the Status line. keyword and explanation are free-form (may
	// embed external tool output, error strings, or the daemon Version), so both are
	// SanitizeText-scrubbed before theme rendering to keep raw ANSI/OSC/C0/C1 escapes out of the
	// verbatim WithSelectorPromptStyled path. The scrub-then-render shape is BYTE-IDENTICAL to
	// buildWorkflowStatusPrompt; the exp != "" guard preserves the success-passes-"" behavior.
	if exp := components.SanitizeText(explanation); exp != "" {
		b.WriteString("\n\n")
		b.WriteString(theme.Subtle.Render(exp))
	}
	return b.String()
}

// showDaemonResultScreen presents a daemon action outcome (restart / install / revert / error)
// with the SAME look as the daemon-status screen: a styled "Status:" line (a colored keyword +
// a Subtle explanation) above a single Back item. It loops to Back/esc and is non-blocking on
// any UI failure, mirroring runDashboardDaemonStatus. This is the single styled result renderer
// shared by every daemon action result, so they can never disagree visually with the status screen.
func showDaemonResultScreen(ctx context.Context, session *shell.Session, title string, level orchestrator.HealthcheckSetupLevel, keyword, explanation string) {
	errDaemonResultEsc := errors.New("daemon result: esc")
	prompt := buildDaemonResultPrompt(level, keyword, explanation)
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
		showDaemonResultScreen(ctx, session, "Daemon change failed", orchestrator.HealthcheckSetupLevelError,
			"config unreadable", "Could not read the configuration to apply the scheduler change.")
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
	doneKeyword := "reverted to cron"
	doneMsg := "Reverted to the cron scheduler and removed the daemon service. Future upgrades will not reinstall it."
	if install {
		title = "Installing daemon"
		work = "Installing and enabling proxsave-daemon.service..."
		doneTitle = "Daemon installed"
		doneKeyword = "installed"
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
		showDaemonResultScreen(ctx, session, title+" failed", orchestrator.HealthcheckSetupLevelError, "failed", opErr.Error())
		return
	}
	showDaemonResultScreen(ctx, session, doneTitle, orchestrator.HealthcheckSetupLevelOk, doneKeyword, doneMsg)
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
		// heartbeat, plus on-disk binary alignment). probeProxsaveDaemonAlive is the stricter signal-0
		// + /proc/cmdline liveness gate. The raw unit/active words stay from the direct probes so the
		// systemctl vocabulary is shown verbatim. This is IDENTICAL to before -- presentation only.
		ds := health.CheckDaemonState(health.DaemonStateInput{
			BaseDir:           baseDir,
			SchedulerMode:     mode,
			HeartbeatInterval: interval,
			Now:               time.Now(),
			Presence:          daemonPresenceProbe(ctx),
			ProcAlive:         probeProxsaveDaemonAlive,
			ProcStale:         procBinaryStaleProbe,
		})
		level, keyword, explanation := daemonStatusStyle(ds)
		prompt := buildDaemonStatusPrompt(level, keyword, explanation, mode, unit, active, optOut, ds)

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
func buildDaemonStatusPrompt(level orchestrator.HealthcheckSetupLevel, keyword, explanation, mode, unit, active, optOut string, ds health.DaemonState) string {
	// Every dynamic segment below carries text from outside this file (keyword/explanation from
	// daemonStatusStyle, mode from the config file, active from systemctl, Version/Commit RAW from
	// .daemon_info.json), so each is SanitizeText-scrubbed before theme rendering to keep raw
	// ANSI/OSC/C0/C1 escapes out of the verbatim WithSelectorPromptStyled path. Compile-time
	// literals (unit/optOut, the "Binary alignment" verdict) are left as-is: sanitizing them is a
	// no-op.
	var b strings.Builder
	b.WriteString(theme.Text.Render("Resident backup daemon (runs scheduled backups + healthchecks reporting)."))
	b.WriteString("\n\n")

	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(renderDaemonStatusLevel(level, components.SanitizeText(keyword)))
	if exp := components.SanitizeText(explanation); exp != "" {
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render(exp))
	}

	b.WriteString("\n\n")
	b.WriteString(theme.Text.Render("Details:"))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Scheduler mode: " + components.SanitizeText(mode)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Daemon service (proxsave-daemon.service): " + unit))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Service state (systemctl is-active): " + components.SanitizeText(active)))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Opted out of auto-migration (--daemon-remove): " + optOut))
	// The running version comes from the identity record (HaveInfo). The alignment verdict comes from
	// the record-independent /proc probe, so show it whenever AlignChecked -- a live daemon on a
	// replaced binary reads "Binary alignment: BEHIND". Binary alignment is known only when
	// AlignChecked; otherwise it is UNKNOWN -- report "unknown" rather than imply "aligned" or a false
	// "behind".
	if ds.HaveInfo {
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Running version: " + components.SanitizeText(ds.Version) + " (" + components.SanitizeText(ds.Commit) + ")"))
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
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("Binary alignment: " + align))
	}
	return b.String()
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

// dashboardNotConfiguredNotice shows a not-configured diagnostic when a screen has
// nothing to show because the feature is not enabled on this host. It reuses the
// shared styled result screen (showDaemonResultScreen) so it reads
// "Status: ⚠ NOT CONFIGURED" exactly like the configured check screens.
func dashboardNotConfiguredNotice(ctx context.Context, session *shell.Session, title, msg string) {
	showDaemonResultScreen(ctx, session, title, orchestrator.HealthcheckSetupLevelWarn, "NOT CONFIGURED", msg)
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
