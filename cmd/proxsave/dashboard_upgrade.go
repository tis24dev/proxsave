package main

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
	buildinfo "github.com/tis24dev/proxsave/internal/version"
)

var (
	dashboardUpgradeCheck   = checkForUpdates
	dashboardUpgradeRun     = runUpgrade
	dashboardUpgradeVersion = buildinfo.String
)

type upgAct int

const (
	upgGo upgAct = iota
	upgBack
	upgConfig // open the two-step config-update flow (--upgrade-config) in-session
)

// upgradeTokenAllowed is the allowlist for a version string shown on screen. The
// "latest" version comes from the GitHub API (attacker-influenceable under a spoofed
// API), and it is rendered on the NON-sanitizing WithSelectorPromptStyled path, so it
// MUST be scrubbed of any ANSI/control bytes before display (a real version tag only
// uses these characters). The current version is our own build but is scrubbed too.
const upgradeTokenAllowed = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.-+_"

func upgradeSafeToken(v string) string {
	v = strings.Map(func(r rune) rune {
		if strings.ContainsRune(upgradeTokenAllowed, r) {
			return r
		}
		return -1
	}, strings.TrimSpace(v))
	if r := []rune(v); len(r) > 40 {
		return string(r[:40])
	}
	return v
}

// releaseTagURL builds the GitHub release-page URL for a tag. The base is a constant; only
// the remote tag is scrubbed (upgradeSafeToken), so a spoofed release tag cannot inject
// control bytes into the displayed URL. Empty when the tag is empty/unusable.
func releaseTagURL(tag string) string {
	tag = upgradeSafeToken(tag)
	if tag == "" {
		return ""
	}
	return "https://github.com/" + githubRepo + "/releases/tag/" + tag
}

// runDashboardUpgradeMenu is the "Upgrade" chooser. It splits the two upgrades so neither
// screen carries the other's button: "Check upgrade" opens the binary upgrade screen
// (runDashboardUpgrade), "Check config" opens the config upgrade flow (--upgrade-config),
// and Back/esc returns to the dashboard menu. It loops so each sub-flow returns here.
func runDashboardUpgradeMenu(ctx context.Context, session *shell.Session, configPath string) {
	back := errors.New("back")
	for {
		prompt := theme.Emphasis.Render("Current version: ") + theme.Text.Render(upgradeSafeToken(dashboardUpgradeVersion()))
		a, err := shell.Ask(ctx, session, components.NewSelector("Upgrade",
			[]components.SelectorItem[upgAct]{
				{Label: "Check upgrade", Description: "update the proxsave binary to a newer release", Value: upgGo},
				{Label: "Check config", Description: "add new template keys to the configuration file", Value: upgConfig},
				{Label: "Back", Description: "return to the dashboard menu", Value: upgBack},
			},
			components.WithSelectorPromptStyled[upgAct](prompt),
			components.WithSelectorBack[upgAct](back)))
		if err != nil || a == upgBack {
			return
		}
		switch a {
		case upgGo:
			runDashboardUpgrade(ctx, session, configPath)
		case upgConfig:
			runDashboardUpdateConfig(ctx, session, configPath)
		}
	}
}

// runDashboardUpgrade is the in-session binary-upgrade screen. It auto-runs the release
// check on entry, shows the available version (sanitized) with the release notes, and on
// "Run upgrade" streams the upgrade INSIDE the altscreen session via components.RunStreamTask
// (upgRun), the same contained viewport panel backup and install-finalize use, then drives
// the single daemon restart. It loops back to the menu on Back.
func runDashboardUpgrade(ctx context.Context, session *shell.Session, configPath string) {
	cur := upgradeSafeToken(dashboardUpgradeVersion())
	// Symbols on the RESULT keyword (consistent with the Telegram/healthcheck check
	// screens): green ✓ ok, yellow ⚠ attention, red ✗ error. The pre-check "NOT CHECKED"
	// carries NO symbol - yellow-without-triangle.
	symOk, symWarn, symErr := theme.SymbolSuccess+" ", theme.SymbolWarning+" ", theme.SymbolError+" "
	kw, sty, sym := "NOT CHECKED", theme.WarningText, ""
	notes := "" // latest release's CodeRabbit summary (remote text; rendered via renderReleaseNotes)
	url := ""   // latest release page URL (tag portion scrubbed via releaseTagURL)
	avail := false
	back := errors.New("back")
	pendingCheck := true // auto-run the release check on entry (like Daemon status), so the
	//                      screen shows the result immediately instead of "NOT CHECKED".
	for {
		if pendingCheck {
			info := upgCheck(ctx, session, cur)
			switch {
			case info == nil || (!info.NewVersion && strings.TrimSpace(info.Latest) == ""):
				avail, kw, sty, sym, notes, url = false, "CHECK FAILED", theme.WarningText, symWarn, "", ""
			case info.NewVersion:
				latest := upgradeSafeToken(info.Latest)
				if latest == "" {
					latest = "UPDATE AVAILABLE"
				}
				avail, kw, sty, sym, notes, url = true, latest, theme.WarningText, symWarn, info.Notes, releaseTagURL(info.Tag)
			default:
				avail, kw, sty, sym, notes, url = false, "NO UPGRADE ("+cur+")", theme.SuccessText, symOk, info.Notes, releaseTagURL(info.Tag)
			}
			pendingCheck = false
		}
		lbl, lblDesc := "Re-check", "check GitHub for a newer release"
		if avail {
			lbl, lblDesc = "Run upgrade", "download and install the latest release"
		}
		p := theme.Emphasis.Render("Current version: ") + theme.Text.Render(cur) + "\n\n" +
			theme.Text.Render("Last available release: ") + sty.Render(sym+kw)
		if url != "" {
			p += "\n\n" + theme.Text.Render(url)
		}
		if notes != "" {
			p += "\n\n" + renderReleaseNotes(notes)
		}
		a, err := shell.Ask(ctx, session, components.NewSelector("Upgrade",
			[]components.SelectorItem[upgAct]{
				{Label: lbl, Description: lblDesc, Value: upgGo},
				{Label: "Back", Description: "return to the dashboard menu", Value: upgBack},
			},
			components.WithSelectorPromptStyled[upgAct](p), components.WithSelectorBack[upgAct](back)))
		if err != nil || a == upgBack {
			return
		}
		if !avail {
			pendingCheck = true // "Re-check" pressed: re-run the check on the next loop
			continue
		}
		avail = false
		if upgRun(ctx, session, configPath) == types.ExitSuccess.Int() {
			kw, sty, sym = "UPGRADED", theme.SuccessText, symOk
			dashboardUpgradeRestartDaemon(ctx, session, configPath)
		} else {
			kw, sty, sym = "FAILED", theme.ErrorText, symErr
			showDaemonResultScreen(ctx, session, "Upgrade failed", orchestrator.HealthcheckSetupLevelError,
				"FAILED", "Run 'proxsave --upgrade' from a shell for details.")
		}
	}
}

// renderReleaseNotes formats the latest release's CodeRabbit summary for the styled
// prompt. The notes are REMOTE-controlled (GitHub release body), and the styled-prompt
// path is NOT sanitized by the component, so every line is scrubbed of ANSI/control bytes
// here. Bold markers are dropped, markdown headers are emphasized, and the block is capped
// so it can never push the menu items off-screen.
func renderReleaseNotes(notes string) string {
	const (
		maxLines = 16
		maxWidth = 100 // cap each line so a hostile newline-free body can't soft-wrap the menu away
	)
	notes = strings.ReplaceAll(notes, "**", "")
	var b strings.Builder
	shown := 0
	for _, raw := range strings.Split(notes, "\n") {
		if shown >= maxLines {
			b.WriteString(theme.Subtle.Render("  ..."))
			b.WriteString("\n")
			break
		}
		line := sanitizeNotesLine(raw)
		if r := []rune(line); len(r) > maxWidth {
			line = string(r[:maxWidth]) + "…"
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			// blank separator line
		case strings.HasPrefix(trimmed, "## "):
			b.WriteString(theme.Emphasis.Render(strings.TrimPrefix(trimmed, "## ")))
		case strings.HasPrefix(trimmed, "# "):
			b.WriteString(theme.Emphasis.Render(strings.TrimPrefix(trimmed, "# ")))
		default:
			b.WriteString(theme.Text.Render(line))
		}
		b.WriteString("\n")
		shown++
	}
	return strings.TrimRight(b.String(), "\n")
}

// sanitizeNotesLine strips ANSI sequences and C0/DEL/C1 control bytes from one line of
// remote release-notes text (tabs -> space), so a hostile release body cannot inject
// terminal escapes into the dashboard.
func sanitizeNotesLine(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}

func upgCheck(ctx context.Context, session *shell.Session, cur string) *UpdateInfo {
	lg := logging.New(types.LogLevelError, false)
	lg.SetOutput(io.Discard)
	var i *UpdateInfo
	_ = components.RunTask(ctx, session, "Checking for updates", "Contacting GitHub...",
		func(tc context.Context, _ func(string)) error { i = dashboardUpgradeCheck(tc, lg, cur); return nil })
	return i
}

// upgRun runs the binary upgrade INSIDE the dashboard altscreen session, streaming its
// [ts] LEVEL log lines into a CONTAINED, scrollable, colored viewport panel
// (components.RunStreamTask), the same contained panel the backup and install-finalize
// screens use, so the three long ops look identical. captureRunOutput routes the default +
// colored bootstrap loggers AND raw os.Stdout through one pipe into the panel. runUpgrade's
// own inline daemon restart is suppressed (upgradeRestartsDaemon=false): the dashboard drives
// the SINGLE restart itself after this returns, so there is no double restart. Returns the
// upgrade exit code so the caller can branch UPGRADED vs FAILED.
func upgRun(ctx context.Context, session *shell.Session, configPath string) int {
	bl := logging.NewBootstrapLogger()
	prevRestart := upgradeRestartsDaemon
	upgradeRestartsDaemon = false
	defer func() { upgradeRestartsDaemon = prevRestart }()
	ar := &cli.Args{Upgrade: true, UpgradeAutoYes: true, ConfigPath: configPath, LogLevel: types.LogLevelInfo}
	code := types.ExitGenericError.Int()
	streamErr := components.RunStreamTask(ctx, session, "Running upgrade",
		func(taskCtx context.Context, emit func(line string)) (string, error) {
			// Route the default + colored bootstrap loggers AND raw os.Stdout through one
			// pipe into the panel (restored on return/panic), so the panel shows the same
			// colored [ts] LEVEL lines as the CLI instead of losing them to the altscreen.
			defer captureRunOutput(bl, emit)()
			code = dashboardUpgradeRun(taskCtx, ar, bl)
			return buildUpgradeOutcomePrompt(code), nil
		})
	if streamErr != nil {
		// The stream is best-effort UI: an abort/UI-death never changes the upgrade
		// outcome (code already holds it), so only trace it.
		logging.DebugStepBootstrap(bl, "dashboard", "upgrade stream: %v", streamErr)
	}
	return code
}

// buildUpgradeOutcomePrompt is the pre-styled banner shown at the bottom of the streamed
// upgrade panel (StreamDoneMsg.Outcome). Unlike backup, the upgrade is all-or-nothing: the
// caller (runDashboardUpgrade) treats ANY non-zero code as FAILED and shows a red result
// screen next, so the banner must agree and render red "Upgrade failed" for any non-zero code
// (a non-zero ExitGenericError would otherwise read yellow "completed with warnings" here yet
// red "FAILED" on the following screen). The shared exitCodeSeverity is used only to tell a
// clean success from a success-with-warnings at code 0, matching the CLI final-summary color.
func buildUpgradeOutcomePrompt(code int) string {
	if code != types.ExitSuccess.Int() {
		return theme.ErrorText.Render(theme.SymbolError + " Upgrade failed")
	}
	if exitCodeSeverity(code, logging.GetDefaultLogger()) == severityWarning {
		return theme.WarningText.Render(theme.SymbolWarning + " Upgrade completed with warnings")
	}
	return theme.SuccessText.Render(theme.SymbolSuccess + " Upgrade completed")
}

// dashboardUpgradeRestartDaemon completes a successful dashboard upgrade by restarting
// the resident daemon (when active) so it loads the freshly installed binary, then shows
// the outcome via the SAME styled result screen as the daemon-status check
// (showDaemonResultScreen), so the post-upgrade restart result matches the daemon menu.
// When the daemon is not active there is nothing to restart -- the new binary just needs a
// relaunch of this process. This is the SINGLE restart on the dashboard path: runUpgrade's
// own inline restart is suppressed (upgradeRestartsDaemon is set false in upgRun), so there
// is no double restart.
func dashboardUpgradeRestartDaemon(ctx context.Context, session *shell.Session, configPath string) {
	if !daemonIsActive(ctx) {
		showDaemonResultScreen(ctx, session, "Upgrade complete", orchestrator.HealthcheckSetupLevelOk,
			"NEW BINARY ON DISK", "New binary on disk. This process still runs the old version; relaunch proxsave.")
		return
	}
	baseDir, _ := detectedBaseDirOrFallback()
	interval := upgradeHeartbeatInterval(configPath, baseDir)
	lockPath, lockKnown := upgradeBackupLockPath(configPath, baseDir)
	var rv RestartVerifyResult
	_ = components.RunTask(ctx, session, "Restarting daemon", "Loading the new binary...",
		func(taskCtx context.Context, _ func(string)) error {
			rv = restartAndVerifyDaemon(taskCtx, baseDir, lockPath, lockKnown, interval)
			return nil
		})
	level, keyword, explanation := restartVerifyStatus(rv)
	showDaemonResultScreen(ctx, session, "Daemon restart", level, keyword, explanation)
}
