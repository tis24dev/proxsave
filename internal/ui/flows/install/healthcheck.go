package install

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/tis24dev/proxsave/internal/health"
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
	healthcheckSelfCheck      = orchestrator.CheckHealthcheckSelfConnection
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
	// BuildHealthcheckSetupBootstrap can run an up-to-10s relay-secret provisioning handshake
	// (hook c) when this centralized host has a resolved ServerID but no secret on disk yet.
	// Run it under the SAME spinner the connection check uses so the screen shows progress
	// instead of appearing frozen; an already-provisioned host returns instantly.
	var state orchestrator.HealthcheckSetupBootstrap
	var bootErr error
	if runErr := components.RunTask(ctx, session, "Checking monitoring setup", "Reading configuration...", func(taskCtx context.Context, report func(string)) error {
		state, bootErr = healthcheckBuildBootstrap(taskCtx, configPath, baseDir)
		return nil
	}); runErr != nil {
		return installer.HealthcheckSetupResult{}, runErr
	}
	if bootErr != nil {
		return installer.HealthcheckSetupResult{}, bootErr
	}
	result := installer.HealthcheckSetupResult{
		HealthcheckSetupBootstrap: state,
		Shown:                     true,
	}
	// Both centralized (portal magic-link + full diagnosis) and self (pure
	// reachability of the user's own alive URL) render this screen; every other
	// verdict renders nothing. selfMode swaps the check seam, the classifier, and
	// the intro copy - the magic-link box and sensor list stay naturally empty.
	if result.Eligibility != orchestrator.HealthcheckSetupEligibleCentralized &&
		result.Eligibility != orchestrator.HealthcheckSetupEligibleSelf {
		result.Shown = false
		return result, nil
	}
	selfMode := result.Eligibility == orchestrator.HealthcheckSetupEligibleSelf

	statusKeyword := "NOT CHECKED"
	statusExplanation := "Choose Check to verify the monitoring connection."
	statusLevel := orchestrator.HealthcheckSetupLevelNeutral // pre-check: yellow, no symbol
	magicLink := ""
	var sensors []health.SensorRow // per-sensor rows, populated after each Check
	// In the dashboard (backToMenu) the check runs automatically on entry, like Daemon
	// status; the installer keeps it manual (the user presses Check).
	pendingCheck := backToMenu
	errHCEsc := errors.New("healthcheck setup: esc")

	for {
		if pendingCheck {
			pendingCheck = false
			if !result.LastFatal && (result.Verified || result.CheckAttempts < orchestrator.HealthcheckSetupMaxVerificationAttempts) {
				var res orchestrator.HealthcheckCheckResult
				cancelled := false
				runErr := components.RunTask(ctx, session, "Checking monitoring", "Contacting the monitor...", func(taskCtx context.Context, report func(string)) error {
					if selfMode {
						res = healthcheckSelfCheck(taskCtx, result.HealthcheckAliveURL)
					} else {
						res = healthcheckCheck(taskCtx, result.ServerAPIHost, result.ServerID, baseDir, result.HealthcheckHeartbeatInterval)
					}
					if taskCtx.Err() != nil {
						cancelled = true
					}
					return nil
				})
				if runErr != nil {
					return result, runErr
				}
				if !cancelled {
					result.CheckAttempts++
					if res.HaveStatus {
						sensors = health.SensorRows(res.RawStatus, result.HealthcheckHeartbeatInterval, result.HealthcheckUpdateInterval, time.Now())
					} else {
						sensors = nil
					}
					var st orchestrator.HealthcheckSetupState
					if selfMode {
						st = orchestrator.ClassifyHealthcheckSelfResult(res)
					} else {
						st = orchestrator.ClassifyHealthcheckSetupResult(res)
					}
					result.LastFatal = st.Fatal
					result.LastMessage = st.Message
					if link := strings.TrimSpace(st.LoginURL); link != "" {
						magicLink = link
						result.MagicLinkSeen = true
					}
					if st.Verified {
						result.Verified = true
					}
					statusKeyword = st.Keyword
					statusExplanation = st.Message
					statusLevel = st.Level
					if !st.Verified && !st.Fatal {
						hint := orchestrator.HealthcheckSetupRetryHint
						if result.CheckAttempts >= orchestrator.HealthcheckSetupMaxVerificationAttempts {
							hint = orchestrator.HealthcheckSetupMaxAttemptsHint
						}
						statusExplanation = st.Message + " " + hint
					}
				}
			}
		}
		prompt := buildHealthcheckPrompt(selfMode, magicLink, statusKeyword, statusExplanation, statusLevel, sensors)

		items := make([]components.SelectorItem[healthcheckAction], 0, 3)
		if !result.LastFatal && (result.Verified || result.CheckAttempts < orchestrator.HealthcheckSetupMaxVerificationAttempts) {
			checkLabel, checkDesc := "Check", "verify the monitoring connection now"
			if backToMenu {
				checkLabel, checkDesc = "Re-check", "re-run the monitoring check"
			}
			items = append(items, components.SelectorItem[healthcheckAction]{
				Label: checkLabel, Description: checkDesc, Value: healthcheckActionCheck,
			})
		}
		leaveLabel, leaveDesc, leaveVal := "Skip", "finish and verify later", healthcheckActionSkip
		if result.Verified {
			leaveLabel, leaveDesc, leaveVal = "Continue", "connection verified", healthcheckActionContinue
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
			pendingCheck = true // (re-)run the check at the top of the loop
			continue
		}
	}
}

// buildHealthcheckPrompt renders the styled prompt shown above the Check/Continue/
// Skip choices: the guide, the portal magic-link boxed for emphasis, and a two-line
// Status block - a state keyword (green only when monitoring is actually WORKING, red
// on a hard blocker, yellow otherwise) on the first line and its plain-language
// explanation on the second. The magic link is already sanitized upstream
// (serverbot.SanitizeLoginURL: http(s), printable ASCII).
func buildHealthcheckPrompt(selfMode bool, magicLink, keyword, explanation string, level orchestrator.HealthcheckSetupLevel, sensors []health.SensorRow) string {
	var b strings.Builder
	b.WriteString(theme.Text.Render("Backup monitoring (healthchecks) is enabled for this host."))
	b.WriteString("\n")
	if selfMode {
		b.WriteString(theme.Text.Render("Self mode: the daemon reports to YOUR own healthchecks server using the"))
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("ping URLs you entered. The check below verifies that alive URL is reachable."))
	} else {
		b.WriteString(theme.Text.Render("It reports each backup outcome + a liveness heartbeat to an external"))
		b.WriteString("\n")
		b.WriteString(theme.Text.Render("monitor, so a silent failure (crash, hang, host down) is still caught."))
	}
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

	// Colored keyword: green ✓ ok, red ✗ error, yellow ⚠ warning. The pre-check (Neutral)
	// state is yellow with NO symbol, so it reads yellow-without-triangle like the upgrade
	// and Telegram check screens (only a real post-check warning carries the ⚠).
	b.WriteString(theme.Text.Render("Status: "))
	b.WriteString(renderHealthcheckLevel(level, components.SanitizeText(keyword)))
	// explanation embeds free-form probe error text (orNA(d.Err) read raw from the
	// status file): scrub it before the verbatim styled-prompt path.
	if exp := components.SanitizeText(explanation); exp != "" {
		b.WriteString("\n")
		b.WriteString(theme.Subtle.Render(exp))
	}

	// Per-sensor list: what we actually transmit + its real state, one colored line each,
	// reusing the SAME palette as the Status line. Rendered only once a Check has populated
	// the rows (before that the block is omitted).
	if len(sensors) > 0 {
		b.WriteString("\n\n")
		b.WriteString(theme.Text.Render("Sensors:"))
		for _, s := range sensors {
			line := s.Name + ": " + s.State
			if s.Age != "" {
				line += " (last ping " + s.Age + ")"
			}
			b.WriteString("\n")
			// s.Name is a status-file record key read raw: scrub the composed line.
			b.WriteString(renderHealthcheckLevel(sensorSetupLevel(s.Level), components.SanitizeText(line)))
		}
	}
	return b.String()
}

// renderHealthcheckLevel is the colored-keyword renderer shared by the Status line and every
// sensor line. It delegates to the shared orchestrator.RenderStatusLevel so the healthcheck,
// audit, daemon, and workflow screens can never drift apart.
func renderHealthcheckLevel(level orchestrator.HealthcheckSetupLevel, text string) string {
	return orchestrator.RenderStatusLevel(level, text)
}

// sensorSetupLevel maps a health.SensorLevel onto the shared HealthcheckSetupLevel so the
// sensor lines reuse renderHealthcheckLevel (and its exact palette) instead of a second
// color switch. SensorError (an available update) maps to the red Error level.
func sensorSetupLevel(l health.SensorLevel) orchestrator.HealthcheckSetupLevel {
	switch l {
	case health.SensorOk:
		return orchestrator.HealthcheckSetupLevelOk
	case health.SensorError:
		return orchestrator.HealthcheckSetupLevelError
	case health.SensorNeutral:
		return orchestrator.HealthcheckSetupLevelNeutral
	default: // health.SensorWarn
		return orchestrator.HealthcheckSetupLevelWarn
	}
}
