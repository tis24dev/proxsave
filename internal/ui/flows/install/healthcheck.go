package install

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
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
func RunHealthcheckSetup(ctx context.Context, session *shell.Session, baseDir, configPath string) (installer.HealthcheckSetupResult, error) {
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

	status := "Not checked yet. Choose Check to verify the monitoring connection."
	magicLink := ""
	errHCEsc := errors.New("healthcheck setup: esc")

	for {
		var prompt strings.Builder
		prompt.WriteString("Backup monitoring (healthchecks) is enabled for this host.\n")
		prompt.WriteString("It reports each backup outcome + a liveness heartbeat to an external\n")
		prompt.WriteString("monitor, so a silent failure (crash, hang, host down) is still caught.\n\n")
		if magicLink != "" {
			prompt.WriteString("Your monitoring portal (single-use link, valid ~1h):\n")
			fmt.Fprintf(&prompt, "  %s\n", magicLink)
			prompt.WriteString("Open it to set a password and configure alert channels.\n\n")
		}
		prompt.WriteString("Status: " + status)

		items := make([]components.SelectorItem[healthcheckAction], 0, 3)
		if !result.LastFatal && (result.Verified || result.CheckAttempts < orchestrator.HealthcheckSetupMaxVerificationAttempts) {
			items = append(items, components.SelectorItem[healthcheckAction]{
				Label: "Check", Description: "verify the monitoring connection now", Value: healthcheckActionCheck,
			})
		}
		if result.Verified {
			items = append(items, components.SelectorItem[healthcheckAction]{
				Label: "Continue", Description: "monitoring verified", Value: healthcheckActionContinue,
			})
		} else {
			items = append(items, components.SelectorItem[healthcheckAction]{
				Label: "Skip", Description: "finish and verify later", Value: healthcheckActionSkip,
			})
		}

		action, err := shell.Ask(ctx, session, components.NewSelector(
			"Backup monitoring (healthchecks)", items,
			components.WithSelectorPrompt[healthcheckAction](prompt.String()),
			components.WithSelectorBack[healthcheckAction](errHCEsc),
		))
		if err != nil {
			if errors.Is(err, errHCEsc) || errors.Is(err, shell.ErrAborted) {
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
				status = fmt.Sprintf("VERIFIED: %s", st.Message)
			case st.Fatal:
				status = fmt.Sprintf("FAILED: %s", st.Message)
			default:
				hint := orchestrator.HealthcheckSetupRetryHint
				if result.CheckAttempts >= orchestrator.HealthcheckSetupMaxVerificationAttempts {
					hint = orchestrator.HealthcheckSetupMaxAttemptsHint
				}
				status = fmt.Sprintf("%s\n%s", st.Message, hint)
			}
		}
	}
}
