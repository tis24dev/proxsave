package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

var (
	telegramSetupBuildBootstrap    = orchestrator.BuildTelegramSetupBootstrap
	telegramSetupCheckRegistration = notify.CheckTelegramRegistration
	telegramSetupPromptYesNo       = promptYesNo
)

const maxTelegramSetupVerificationAttempts = 10

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
		return wrapInstallError(err)
	}
	if !check {
		fmt.Println("Skipped verification. You can verify later by running proxsave.")
		logBootstrapInfo(bootstrap, "Telegram setup: verification skipped by user")
		return nil
	}

	attempts := 0
	for {
		attempts++
		status := telegramSetupCheckRegistration(ctx, state.ServerAPIHost, state.ServerID, nil)
		if status.Code == 200 && status.Error == nil {
			fmt.Println("✓ Telegram linked successfully.")
			logBootstrapInfo(bootstrap, "Telegram setup: verified (attempts=%d)", attempts)
			return nil
		}

		msg := strings.TrimSpace(status.Message)
		if msg == "" {
			msg = "Registration not active yet"
		}
		fmt.Printf("Telegram: %s\n", msg)
		switch status.Code {
		case 403, 409:
			fmt.Println("Hint: Start the bot, send the Server ID, then retry.")
		case 422:
			fmt.Println("Hint: The Server ID appears invalid. If this persists, re-run the installer.")
		default:
			if status.Error != nil {
				fmt.Printf("Hint: Check failed: %v\n", status.Error)
			}
		}

		if attempts >= maxTelegramSetupVerificationAttempts {
			fmt.Println("Maximum verification attempts reached. You can retry later by running proxsave.")
			logBootstrapInfo(bootstrap, "Telegram setup: not verified (attempts=%d last=%d %s)", attempts, status.Code, msg)
			return nil
		}

		retry, err := telegramSetupPromptYesNo(ctx, reader, "Check again? [y/N]: ", false)
		if err != nil {
			return wrapInstallError(err)
		}
		if !retry {
			fmt.Println("Verification not completed. You can retry later by running proxsave.")
			logBootstrapInfo(bootstrap, "Telegram setup: not verified (attempts=%d last=%d %s)", attempts, status.Code, msg)
			return nil
		}
	}
}
