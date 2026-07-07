package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// buildInstallOutcomePrompt composes the pre-styled outcome block shown under
// the streamed finalization lines (StreamDoneMsg.Outcome). It is the install's
// own Mattone C consumer: it reuses the shared daemon renderers
// (restartVerifyStatus + renderDaemonStatusLevel) for the "Daemon:" line and
// colors the "Permissions:" line with the same status strings printInstallFooter
// switches on (ok / warning / error / skipped).
//
// The whole string is caller-pre-styled and passed verbatim to the StreamTask
// screen, mirroring how WithSelectorPromptStyled accepts an already-rendered
// prompt. It never renders the CLI footer, which stays the persistent
// scrollback record after the session closes.
func buildInstallOutcomePrompt(rv RestartVerifyResult, verified bool, permStatus, permMessage string) string {
	var b strings.Builder

	b.WriteString(theme.Text.Render("Daemon: "))
	if verified {
		level, keyword, explanation := restartVerifyStatus(rv)
		b.WriteString(renderDaemonStatusLevel(level, keyword))
		if strings.TrimSpace(explanation) != "" {
			b.WriteString("\n")
			b.WriteString(theme.Subtle.Render(explanation))
		}
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
