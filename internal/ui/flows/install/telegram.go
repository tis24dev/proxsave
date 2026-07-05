package install

import (
	"context"
	"errors"
	"io"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Seams for tests.
var (
	telegramBuildBootstrap    = orchestrator.BuildTelegramSetupBootstrap
	telegramCheckRegistration = notify.CheckTelegramRegistrationAndProvision
)

type telegramAction int

const (
	telegramActionCheck telegramAction = iota
	telegramActionContinue
	telegramActionSkip
)

// RunTelegramSetup guides the centralized-bot pairing with an explicit
// verification step (Check with the shared attempt cap, Skip, Continue once
// verified). Semantics mirror the deleted tview wizard: the verified flag
// latches, a fatal status removes Check, Esc skips unless already verified.
func RunTelegramSetup(ctx context.Context, session *shell.Session, baseDir, configPath string) (installer.TelegramSetupResult, error) {
	state, err := telegramBuildBootstrap(configPath, baseDir)
	if err != nil {
		return installer.TelegramSetupResult{}, err
	}
	result := installer.TelegramSetupResult{
		TelegramSetupBootstrap: state,
		Shown:                  true,
	}
	if result.Eligibility != orchestrator.TelegramSetupEligibleCentralized {
		result.Shown = false
		return result, nil
	}

	// Real-but-silent logger: the reused provision path registers and masks
	// the relay secret via this logger; io.Discard keeps debug lines off the
	// UI surface.
	silentLogger := logging.New(types.LogLevelDebug, false)
	silentLogger.SetOutput(io.Discard)

	statusMsg := "Not checked yet. Choose Check after sending the Server ID to the bot."
	statusVerified := false // last check linked (green)
	statusPartial := false  // last check linked but partial (yellow)
	statusFailed := false   // last check was a fatal failure (red)
	// errTelegramEsc distinguishes Esc from a hard Ctrl+C abort.
	errTelegramEsc := errors.New("telegram setup: esc")

	for {
		prompt := buildTelegramPrompt(result.ServerID, result.IdentityFile, result.IdentityPersisted,
			statusMsg, statusVerified, statusPartial, statusFailed)

		items := make([]components.SelectorItem[telegramAction], 0, 3)
		if !result.LastStatusFatal && (result.Verified || result.CheckAttempts < orchestrator.TelegramSetupMaxVerificationAttempts) {
			items = append(items, components.SelectorItem[telegramAction]{
				Label: "Check", Description: "verify the pairing now", Value: telegramActionCheck,
			})
		}
		if result.Verified {
			items = append(items, components.SelectorItem[telegramAction]{
				Label: "Continue", Description: "pairing verified", Value: telegramActionContinue,
			})
		} else {
			items = append(items, components.SelectorItem[telegramAction]{
				Label: "Skip", Description: "complete pairing later", Value: telegramActionSkip,
			})
		}

		action, err := shell.Ask(ctx, session, components.NewSelector(
			"Telegram setup", items,
			components.WithSelectorPromptStyled[telegramAction](prompt),
			components.WithSelectorBack[telegramAction](errTelegramEsc),
		))
		if err != nil {
			if errors.Is(err, errTelegramEsc) || errors.Is(err, shell.ErrAborted) {
				// Esc/abort: skip unless already verified (tview parity).
				result.SkippedVerification = !result.Verified
				return result, nil
			}
			return result, err
		}

		switch action {
		case telegramActionContinue:
			result.SkippedVerification = false
			return result, nil
		case telegramActionSkip:
			result.SkippedVerification = true
			return result, nil
		case telegramActionCheck:
			var res notify.TelegramRegistrationResult
			cancelled := false
			runErr := components.RunTask(ctx, session, "Checking registration", "Contacting the relay...", func(taskCtx context.Context, report func(string)) error {
				res = telegramCheckRegistration(taskCtx, result.ServerAPIHost, result.ServerID, baseDir, silentLogger)
				if taskCtx.Err() != nil {
					cancelled = true
				}
				return nil
			})
			if runErr != nil {
				// UI death or hard failure: surface it, the caller treats
				// the whole step as non-blocking.
				return result, runErr
			}
			if cancelled {
				// User cancelled the check: back to the menu without
				// consuming a verification attempt.
				continue
			}

			result.CheckAttempts++
			result.LastStatusCode = res.Status.Code
			result.LastStatusMessage = res.Status.Message // RAW preserved (parity with tview)
			if res.Status.Error != nil {
				result.LastStatusError = res.Status.Error.Error()
			} else {
				result.LastStatusError = ""
			}

			st := orchestrator.ClassifyTelegramSetupResult(res)
			result.LastStatusFatal = st.Fatal
			if st.Verified { // latch: a later re-check can never un-verify
				result.Verified = true
				result.Partial = st.Partial
			}

			switch {
			case st.Verified && !st.Partial:
				statusVerified, statusPartial, statusFailed = true, false, false
				statusMsg = st.Message
			case st.Verified && st.Partial:
				statusVerified, statusPartial, statusFailed = true, true, false
				statusMsg = st.Message
			case st.Fatal:
				statusVerified, statusPartial, statusFailed = false, false, true
				statusMsg = st.Message
			default:
				statusVerified, statusPartial, statusFailed = false, false, false
				hint := orchestrator.TelegramSetupRetryHint
				if result.CheckAttempts >= orchestrator.TelegramSetupMaxVerificationAttempts {
					hint = orchestrator.TelegramSetupMaxAttemptsHint
				}
				statusMsg = st.Message + "\n" + hint
			}
		}
	}
}

// buildTelegramPrompt renders the styled prompt above the Check/Continue/Skip
// choices: the pairing steps, the Server ID boxed for emphasis (the user must send
// it to the bot), and a Status line whose keyword is green when verified, yellow
// when partially verified, and red on a fatal failure. Every dynamic input is safe:
// the Server ID is the local digit identity, statusMsg is our own copy (the only
// upstream case is pre-sanitized by SanitizeTelegramSetupStatusMessage), and the
// identity path is local.
func buildTelegramPrompt(serverID, identityFile string, identityPersisted bool, statusMsg string, verified, partial, failed bool) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Mode: centralized"))
	b.WriteString("\n\n")
	b.WriteString(theme.Text.Render("1) Open Telegram and start @ProxmoxAN_bot"))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("2) Send the Server ID below (digits only)"))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("3) Choose Check to verify"))
	b.WriteString("\n\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Orange).
		Padding(0, 1).
		Render(theme.Subtle.Render("Server ID (send this to the bot):") +
			"\n" + theme.Emphasis.Render(serverID))
	b.WriteString(box)
	b.WriteString("\n")
	if identityFile != "" {
		persisted := "not persisted"
		if identityPersisted {
			persisted = "persisted"
		}
		b.WriteString(theme.Subtle.Render("Identity file: " + identityFile + " (" + persisted + ")"))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString(theme.Text.Render("Status: "))
	switch {
	case verified && !partial:
		b.WriteString(theme.SuccessText.Render(theme.SymbolSuccess + " VERIFIED"))
		if statusMsg != "" {
			b.WriteString(theme.Text.Render(": " + statusMsg))
		}
	case verified && partial:
		b.WriteString(theme.WarningText.Render(theme.SymbolWarning + " PARTIAL"))
		if statusMsg != "" {
			b.WriteString(theme.Text.Render(": " + statusMsg))
		}
	case failed:
		b.WriteString(theme.ErrorText.Render(theme.SymbolError + " FAILED"))
		if statusMsg != "" {
			b.WriteString(theme.Text.Render(": " + statusMsg))
		}
	default:
		b.WriteString(theme.Text.Render(statusMsg))
	}
	return b.String()
}
