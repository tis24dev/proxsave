package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

var (
	telegramSetupBuildBootstrap    = orchestrator.BuildTelegramSetupBootstrap
	telegramSetupCheckRegistration = notify.CheckTelegramRegistrationAndProvision
	telegramSetupPromptYesNo       = promptYesNo
)

func sanitizeTelegramSetupStatusMessage(raw string) string {
	// Delegates to the shared orchestrator sanitizer so the CLI, the TUI, and the
	// classifier all scrub control/terminal sequences and truncate identically.
	return orchestrator.SanitizeTelegramSetupStatusMessage(raw)
}

func logTelegramSetupBootstrapOutcome(bootstrap *logging.BootstrapLogger, state orchestrator.TelegramSetupBootstrap) {
	switch state.Eligibility {
	case orchestrator.TelegramSetupSkipConfigError:
		if strings.TrimSpace(state.ConfigError) != "" {
			logBootstrapWarning(bootstrap, "Telegram setup: unable to load config (skipping): %s", state.ConfigError)
		}
	case orchestrator.TelegramSetupSkipPersonalMode:
		logBootstrapInfo(bootstrap, "Telegram setup: personal mode selected (no centralized pairing check)")
	case orchestrator.TelegramSetupSkipIdentityUnavailable:
		if strings.TrimSpace(state.IdentityDetectError) != "" {
			logBootstrapWarning(bootstrap, "Telegram setup: identity detection failed (non-blocking): %s", state.IdentityDetectError)
			return
		}
		logBootstrapWarning(bootstrap, "Telegram setup: server ID unavailable; skipping")
	}
}

func runTelegramSetupCLI(ctx context.Context, reader *bufio.Reader, baseDir, configPath string, bootstrap *logging.BootstrapLogger) error {
	state, err := telegramSetupBuildBootstrap(configPath, baseDir)
	if err != nil {
		logBootstrapWarning(bootstrap, "Telegram setup bootstrap failed (non-blocking): %v", err)
		return nil
	}

	logTelegramSetupBootstrapOutcome(bootstrap, state)
	if state.Eligibility != orchestrator.TelegramSetupEligibleCentralized {
		return nil
	}

	fmt.Println("\n--- Telegram setup (optional) ---")
	fmt.Println("You enabled Telegram notifications (centralized bot).")
	fmt.Printf("Server ID: %s\n", state.ServerID)
	if strings.TrimSpace(state.IdentityFile) != "" {
		fmt.Printf("Identity file: %s\n", strings.TrimSpace(state.IdentityFile))
	}
	fmt.Println()
	fmt.Println("1) Open Telegram and start @ProxmoxAN_bot")
	fmt.Println("2) Send the Server ID above (digits only)")
	fmt.Println("3) Verify pairing (recommended)")
	fmt.Println()

	check, err := telegramSetupPromptYesNo(ctx, reader, "Check Telegram pairing now? [Y/n]: ", true)
	if err != nil {
		return skipOptionalInstallStepOnAbort(bootstrap, "Telegram setup", err)
	}
	if !check {
		fmt.Println("Skipped verification. You can verify later by running proxsave.")
		logBootstrapInfo(bootstrap, "Telegram setup: verification skipped by user")
		return nil
	}

	// Real-but-silent logger: the reused provision path registers and masks the
	// relay secret via this logger, but io.Discard keeps every debug line off the
	// install console (full debug stays on the runtime backup path via rt.logger).
	silentLogger := logging.New(types.LogLevelDebug, false)
	silentLogger.SetOutput(io.Discard)

	attempts := 0
	for {
		attempts++
		res := telegramSetupCheckRegistration(ctx, state.ServerAPIHost, state.ServerID, baseDir, silentLogger)
		st := orchestrator.ClassifyTelegramSetupResult(res)

		if st.Verified {
			if st.Partial {
				fmt.Println(st.Message)
				logBootstrapInfo(bootstrap, "Telegram setup: verified with pending follow-up (attempts=%d state=%s)", attempts, st.Code)
			} else {
				fmt.Printf("✓ %s\n", st.Message)
				logBootstrapInfo(bootstrap, "Telegram setup: verified (attempts=%d)", attempts)
			}
			return nil
		}

		// rawMsg drives ONLY the byte-identical failure log line (sanitized RAW
		// server message). The user-facing line is always the classifier message.
		rawMsg := sanitizeTelegramSetupStatusMessage(res.Status.Message)
		if rawMsg == "" {
			rawMsg = "Registration not active yet"
		}
		fmt.Printf("Telegram: %s\n", sanitizeTelegramSetupStatusMessage(st.Message))

		if st.Fatal { // 422 / 426: re-checking cannot help, do NOT offer "Check again?"
			logBootstrapInfo(bootstrap, "Telegram setup: not verified (attempts=%d last=%d %s)", attempts, res.Status.Code, rawMsg)
			return nil
		}

		if attempts >= orchestrator.TelegramSetupMaxVerificationAttempts {
			fmt.Println(orchestrator.TelegramSetupMaxAttemptsHint)
			logBootstrapInfo(bootstrap, "Telegram setup: not verified (attempts=%d last=%d %s)", attempts, res.Status.Code, rawMsg)
			return nil
		}

		fmt.Println(orchestrator.TelegramSetupRetryHint)
		retry, err := telegramSetupPromptYesNo(ctx, reader, "Check again? [y/N]: ", false)
		if err != nil {
			return skipOptionalInstallStepOnAbort(bootstrap, "Telegram setup", err)
		}
		if !retry {
			fmt.Println("Verification not completed. You can retry later by running proxsave.")
			logBootstrapInfo(bootstrap, "Telegram setup: not verified (attempts=%d last=%d %s)", attempts, res.Status.Code, rawMsg)
			return nil
		}
	}
}
