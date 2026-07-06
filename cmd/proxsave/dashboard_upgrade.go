package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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

func runDashboardUpgrade(ctx context.Context, session *shell.Session, configPath string) {
	cur := upgradeSafeToken(dashboardUpgradeVersion())
	kw, exp, sty := "unchecked", "Choose Check upgrade.", theme.WarningText
	avail := false
	back := errors.New("back")
	for {
		lbl := "Check upgrade"
		if avail {
			lbl = "Run upgrade"
		}
		p := theme.Emphasis.Render("Current version: ") + theme.Text.Render(cur) + "\n\n" +
			theme.Text.Render("Status: ") + sty.Render(kw) + "\n" + theme.Subtle.Render(exp)
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
				kw, exp, sty = "check failed", "Could not reach GitHub.", theme.WarningText
			case info.NewVersion:
				avail = true
				latest := upgradeSafeToken(info.Latest)
				if latest == "" {
					latest = "update available"
				}
				kw, exp, sty = latest, fmt.Sprintf("Newer release (current %s).", cur), theme.WarningText
			default:
				kw, exp, sty = "no upgrade", fmt.Sprintf("On the latest (%s).", cur), theme.SuccessText
			}
			continue
		}
		avail = false
		if upgRun(ctx, session, configPath) == types.ExitSuccess.Int() {
			kw, exp, sty = "upgraded", "Installed; relaunch proxsave, Restart daemon if active.", theme.SuccessText
			_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess, "Upgrade complete",
				"New binary on disk. This process still runs the old version; relaunch proxsave, and Restart daemon if active."))
		} else {
			kw, exp, sty = "failed", "Did not complete.", theme.ErrorText
			_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeError, "Upgrade failed",
				"Run 'proxsave --upgrade' from a shell for details."))
		}
	}
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
	ar := &cli.Args{Upgrade: true, UpgradeAutoYes: true, ConfigPath: configPath, LogLevel: types.LogLevelInfo}
	code := types.ExitGenericError.Int()
	_ = components.RunTask(ctx, session, "Running upgrade", "Installing...",
		func(tc context.Context, _ func(string)) error { code = dashboardUpgradeRun(tc, ar, bl); return nil })
	return code
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
