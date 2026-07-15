package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// buildInstallOutcomePrompt composes the pre-styled summary block shown at the bottom
// of the streamed finalization screen (StreamDoneMsg.Outcome): a completion banner +
// the "Daemon:" and "Permissions:" lines. It is the install's own Mattone C consumer,
// reusing the SHARED helpers so it never drifts from the CLI footer:
//   - installBanner (the same completed/aborted/failed title + severity the footer's
//     ANSI box uses), rendered here in the theme via renderInstallBanner;
//   - installVerifyVerdict + renderDaemonStatusLevel for the "Daemon:" line (the SAME
//     aligned / behind / not-running verdict as --daemon-status);
//   - the same permStatus strings printInstallFooter switches on for "Permissions:".
//
// The graphical finalization is reached only on the success path (failures/aborts
// return early and the footer prints the red/magenta banner), so the banner here is
// "completed"; the shared helper keeps the wording in one place.
//
// The whole string is caller-pre-styled and passed verbatim to the inline stream screen,
// mirroring how WithSelectorPromptStyled accepts an already-rendered prompt. It never
// renders the CLI footer, which stays the persistent scrollback record after Close.
func buildInstallOutcomePrompt(rv RestartVerifyResult, verified bool, permStatus, permMessage string) string {
	var b strings.Builder

	title, level := installBanner(nil)
	b.WriteString(renderInstallBanner(level, title))
	b.WriteString("\n\n")

	b.WriteString(theme.Text.Render("Daemon: "))
	if verified {
		// Same verdict the log line and --daemon-status show (NOT restartVerifyStatus,
		// whose success arm needs Restarted/FreshInfo the poll-only verify never sets).
		level, keyword := installVerifyVerdict(rv)
		b.WriteString(renderDaemonStatusLevel(level, keyword))
	} else {
		// No daemon verify ran (cron scheduler, install-daemon failure, or an
		// unreadable verify context): state the neutral scheduler fact instead
		// of an alignment verdict we did not measure.
		b.WriteString(theme.Text.Render("cron scheduler"))
	}

	b.WriteString("\n")
	b.WriteString(theme.Text.Render("Permissions: "))
	permText := strings.TrimSpace(permMessage)
	if permText == "" {
		permText = permStatus
	}
	switch permStatus {
	case "ok":
		b.WriteString(theme.SuccessText.Render(permText))
	case "warning":
		b.WriteString(theme.WarningText.Render(permText))
	case "error":
		b.WriteString(theme.ErrorText.Render(permText))
	default: // skipped / empty / unknown: neutral, matching the footer's plain line
		b.WriteString(theme.Text.Render(permText))
	}

	return b.String()
}

// renderInstallBanner renders the themed completion banner line (symbol + title) for
// the graphical finalization summary - the theme counterpart of printInstallFooter's
// ANSI box, sharing installBanner's severity so the wording matches the CLI.
func renderInstallBanner(level installBannerLevel, title string) string {
	switch level {
	case installBannerFailed:
		return theme.ErrorText.Render(theme.SymbolError + " " + title)
	case installBannerAborted:
		return theme.WarningText.Render(theme.SymbolWarning + " " + title)
	default:
		return theme.SuccessText.Render(theme.SymbolSuccess + " " + title)
	}
}
