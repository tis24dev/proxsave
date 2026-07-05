package install

import (
	"context"
	"errors"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Seams for tests.
var (
	healthcheckBuildBootstrap = orchestrator.BuildHealthcheckSetupBootstrap
	healthcheckCheck          = orchestrator.CheckHealthcheckConnection
)

type healthcheckAction int

const (
	healthcheckActionCheck healthcheckAction = iota
	healthcheckActionContinue
	healthcheckActionSkip
)

// RunHealthcheckSetup shows the install-time healthchecks guide/check screen
// (mirrors RunTelegramSetup): a short guide, the portal magic-link, and a
// connection check with the shared attempt cap. The verified flag latches, a fatal
// status removes Check, Esc skips unless already verified. Renders only when the
// daemon engine with centralized monitoring was chosen and the identity/secret
// exist (decided by BuildHealthcheckSetupBootstrap re-reading the written config).
func RunHealthcheckSetup(ctx context.Context, session *shell.Session, baseDir, configPath string, backToMenu bool) (installer.HealthcheckSetupResult, error) {
	state, err := healthcheckBuildBootstrap(configPath, baseDir)
	if err != nil {
		return installer.HealthcheckSetupResult{}, err
	}
	result := installer.HealthcheckSetupResult{
		HealthcheckSetupBootstrap: state,
		Shown:                     true,
	}
	if result.Eligibility != orchestrator.HealthcheckSetupEligibleCentralized {
		result.Shown = false
		return result, nil
	}

	statusMsg := "Not checked yet. Choose Check to verify the monitoring connection."
	statusVerified := false // last check verified (green)
	statusFailed := false   // last check was a fatal failure (red)
	magicLink := ""
	errHCEsc := errors.New("healthcheck setup: esc")

	for {
		prompt := buildHealthcheckPrompt(magicLink, statusMsg, statusVerified, statusFailed)

		items := make([]components.SelectorItem[healthcheckAction], 0, 3)
		if !result.LastFatal && (result.Verified || result.CheckAttempts < orchestrator.HealthcheckSetupMaxVerificationAttempts) {
			items = append(items, components.SelectorItem[healthcheckAction]{
				Label: "Check", Description: "verify the monitoring connection now", Value: healthcheckActionCheck,
			})
		}
		leaveLabel, leaveDesc, leaveVal := "Skip", "finish and verify later", healthcheckActionSkip
		if result.Verified {
			leaveLabel, leaveDesc, leaveVal = "Continue", "monitoring verified", healthcheckActionContinue
		}
		if backToMenu {
			leaveLabel, leaveDesc = "Back", "return to the dashboard menu"
		}
		items = append(items, components.SelectorItem[healthcheckAction]{
			Label: leaveLabel, Description: leaveDesc, Value: leaveVal,
		})

		action, err := shell.Ask(ctx, session, components.NewSelector(
			"Backup monitoring (healthchecks)", items,
			components.WithSelectorPromptStyled[healthcheckAction](prompt),
			components.WithSelectorBack[healthcheckAction](errHCEsc),
		))
		if err != nil {
			if errors.Is(err, errHCEsc) || shell.IsAbort(err) {
				result.SkippedVerification = !result.Verified
				return result, nil
			}
			return result, err
		}

		switch action {
		case healthcheckActionContinue:
			result.SkippedVerification = false
			return result, nil
		case healthcheckActionSkip:
			result.SkippedVerification = true
			return result, nil
		case healthcheckActionCheck:
			var res orchestrator.HealthcheckCheckResult
			cancelled := false
			runErr := components.RunTask(ctx, session, "Checking monitoring", "Contacting the monitor...", func(taskCtx context.Context, report func(string)) error {
				res = healthcheckCheck(taskCtx, result.ServerAPIHost, result.ServerID, baseDir)
				if taskCtx.Err() != nil {
					cancelled = true
				}
				return nil
			})
			if runErr != nil {
				return result, runErr
			}
			if cancelled {
				continue
			}

			result.CheckAttempts++
			st := orchestrator.ClassifyHealthcheckSetupResult(res)
			result.LastFatal = st.Fatal
			result.LastMessage = st.Message
			if link := strings.TrimSpace(st.LoginURL); link != "" {
				magicLink = link
				result.MagicLinkSeen = true
			}
			if st.Verified { // latch
				result.Verified = true
			}

			switch {
			case st.Verified:
				statusVerified, statusFailed = true, false
				statusMsg = st.Message
			case st.Fatal:
				statusVerified, statusFailed = false, true
				statusMsg = st.Message
			default:
				statusVerified, statusFailed = false, false
				hint := orchestrator.HealthcheckSetupRetryHint
				if result.CheckAttempts >= orchestrator.HealthcheckSetupMaxVerificationAttempts {
					hint = orchestrator.HealthcheckSetupMaxAttemptsHint
				}
				statusMsg = st.Message + "\n" + hint
			}
		}
	}
}

// buildHealthcheckPrompt renders the styled prompt shown above the Check/Continue/
// Skip choices: the guide, the portal magic-link boxed for emphasis, and a Status
// line whose keyword is green when verified and red on a fatal failure. The magic
// link is already sanitized upstream (serverbot.SanitizeLoginURL: http(s), printable ASCII).
func buildHealthcheckPrompt(magicLink, statusMsg string, verified, failed bool) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Backup monitoring (healthchecks) is enabled for this host."))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("It reports each backup outcome + a liveness heartbeat to an external"))
	b.WriteString("\n")
	b.WriteString(theme.Text.Render("monitor, so a silent failure (crash, hang, host down) is still caught."))
	b.WriteString("\n\n")

	if magicLink != "" {
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Orange).
			Padding(0, 1).
			Render(theme.Subtle.Render("Your monitoring portal (single-use link, valid ~1h):") +
				"\n" + theme.Emphasis.Render(magicLink))
		b.WriteString(box)
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render("Open it to set a password and configure alert channels."))
		b.WriteString("\n\n")
	}

	b.WriteString(theme.Text.Render("Status: "))
	switch {
	case verified:
		b.WriteString(theme.SuccessText.Render(theme.SymbolSuccess + " VERIFIED"))
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
