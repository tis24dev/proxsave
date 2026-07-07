package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
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
	dashboardUpgradeMute    = defaultUpgradeMuteStdio
)

type upgAct int

const (
	upgGo upgAct = iota
	upgBack
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
	for {
		lbl := "Check upgrade"
		if avail {
			lbl = "Run upgrade"
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
			[]components.SelectorItem[upgAct]{{Label: lbl, Value: upgGo}, {Label: "Back", Value: upgBack}},
			components.WithSelectorPromptStyled[upgAct](p), components.WithSelectorBack[upgAct](back)))
		if err != nil || a == upgBack {
			return
		}
		if !avail {
			info := upgCheck(ctx, session, cur)
			switch {
			case info == nil || (!info.NewVersion && strings.TrimSpace(info.Latest) == ""):
				kw, sty, sym, notes, url = "CHECK FAILED", theme.WarningText, symWarn, "", ""
			case info.NewVersion:
				avail = true
				latest := upgradeSafeToken(info.Latest)
				if latest == "" {
					latest = "UPDATE AVAILABLE"
				}
				kw, sty, sym, notes, url = latest, theme.WarningText, symWarn, info.Notes, releaseTagURL(info.Tag)
			default:
				kw, sty, sym, notes, url = "NO UPGRADE ("+cur+")", theme.SuccessText, symOk, info.Notes, releaseTagURL(info.Tag)
			}
			continue
		}
		avail = false
		if upgRun(ctx, session, configPath) == types.ExitSuccess.Int() {
			kw, sty, sym = "UPGRADED", theme.SuccessText, symOk
			dashboardUpgradeRestartDaemon(ctx, session, configPath)
		} else {
			kw, sty, sym = "FAILED", theme.ErrorText, symErr
			_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeError, "Upgrade failed",
				"Run 'proxsave --upgrade' from a shell for details."))
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

func upgRun(ctx context.Context, session *shell.Session, configPath string) int {
	pl := logging.GetDefaultLogger()
	sl := logging.New(types.LogLevelError, false)
	sl.SetOutput(io.Discard)
	logging.SetDefaultLogger(sl)
	defer logging.SetDefaultLogger(pl)
	rs := dashboardUpgradeMute()
	defer rs()
	bl := logging.NewBootstrapLogger()
	bl.SetConsoleQuiet(true)
	// Suppress runUpgrade's inline daemon restart on the dashboard path: the dashboard
	// drives the restart itself (spinner + notice) after this returns, so an inline
	// restart here would be invisible (stdout is muted) and would double-restart.
	prevRestart := upgradeRestartsDaemon
	upgradeRestartsDaemon = false
	defer func() { upgradeRestartsDaemon = prevRestart }()
	ar := &cli.Args{Upgrade: true, UpgradeAutoYes: true, ConfigPath: configPath, LogLevel: types.LogLevelInfo}
	code := types.ExitGenericError.Int()
	_ = components.RunTask(ctx, session, "Running upgrade", "Installing...",
		func(tc context.Context, _ func(string)) error { code = dashboardUpgradeRun(tc, ar, bl); return nil })
	return code
}

// dashboardUpgradeRestartDaemon completes a successful dashboard upgrade by restarting
// the resident daemon (when active) so it loads the freshly installed binary, then shows
// the outcome as a notice. When the daemon is not active there is nothing to restart --
// the new binary just needs a relaunch of this process. This is the SINGLE restart on the
// dashboard path: runUpgrade's own inline restart is suppressed (upgradeRestartsDaemon is
// set false in upgRun), so there is no double restart.
func dashboardUpgradeRestartDaemon(ctx context.Context, session *shell.Session, configPath string) {
	if !daemonIsActive(ctx) {
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess, "Upgrade complete",
			"New binary on disk. This process still runs the old version; relaunch proxsave."))
		return
	}
	baseDir, _ := detectedBaseDirOrFallback()
	interval := upgradeHeartbeatInterval(configPath, baseDir)
	lockPath := upgradeBackupLockPath(configPath, baseDir)
	var rv RestartVerifyResult
	_ = components.RunTask(ctx, session, "Restarting daemon", "Loading the new binary...",
		func(taskCtx context.Context, _ func(string)) error {
			rv = restartAndVerifyDaemon(taskCtx, baseDir, lockPath, interval)
			return nil
		})
	kind, title, msg := restartVerifyNotice(rv)
	_, _ = shell.Ask(ctx, session, components.NewNotice(kind, title, msg))
}

func defaultUpgradeMuteStdio() func() {
	dn, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return func() {}
	}
	o, e := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = dn, dn
	return func() { os.Stdout, os.Stderr = o, e; _ = dn.Close() }
}
