package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
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

	status := "Not checked yet. Choose Check after sending the Server ID to the bot."
	// errTelegramEsc distinguishes Esc from a hard Ctrl+C abort.
	errTelegramEsc := errors.New("telegram setup: esc")

	for {
		var prompt strings.Builder
		prompt.WriteString("Mode: centralized\n\n")
		prompt.WriteString("1) Open Telegram and start @ProxmoxAN_bot\n")
		fmt.Fprintf(&prompt, "2) Send the Server ID below (digits only)\n")
		prompt.WriteString("3) Choose Check to verify\n\n")
		fmt.Fprintf(&prompt, "Server ID: %s\n", result.ServerID)
		if result.IdentityFile != "" {
			persisted := "not persisted"
			if result.IdentityPersisted {
				persisted = "persisted"
			}
			fmt.Fprintf(&prompt, "Identity file: %s (%s)\n", result.IdentityFile, persisted)
		}
		prompt.WriteString("\nStatus: " + status)

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
			components.WithSelectorPrompt[telegramAction](prompt.String()),
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
				status = fmt.Sprintf("VERIFIED: %s", st.Message)
			case st.Verified && st.Partial:
				status = fmt.Sprintf("PARTIAL: %s", st.Message)
			case st.Fatal:
				status = fmt.Sprintf("FAILED: %s", st.Message)
			default:
				hint := orchestrator.TelegramSetupRetryHint
				if result.CheckAttempts >= orchestrator.TelegramSetupMaxVerificationAttempts {
					hint = orchestrator.TelegramSetupMaxAttemptsHint
				}
				status = fmt.Sprintf("%s\n%s", st.Message, hint)
			}
		}
	}
}
