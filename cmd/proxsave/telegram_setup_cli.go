package main

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
const maxTelegramSetupStatusMessageLen = 200

func sanitizeTelegramSetupStatusMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	if msg == "" {
		return ""
	}

	sanitized := stripTelegramTerminalSequences(msg)
	sanitized = truncateTelegramSetupStatusMessage(sanitized, maxTelegramSetupStatusMessageLen)
	if sanitized != "" {
		return sanitized
	}

	quoted := strconv.QuoteToASCII(msg)
	quoted = strings.TrimPrefix(quoted, `"`)
	quoted = strings.TrimSuffix(quoted, `"`)
	return truncateTelegramSetupStatusMessage(quoted, maxTelegramSetupStatusMessageLen)
}

func truncateTelegramSetupStatusMessage(msg string, max int) string {
	msg = strings.TrimSpace(msg)
	if msg == "" || max <= 0 {
		return ""
	}
	runes := []rune(msg)
	if len(runes) <= max {
		return msg
	}
	return string(runes[:max]) + "...(truncated)"
}

func stripTelegramTerminalSequences(msg string) string {
	var b strings.Builder
	b.Grow(len(msg))
	pendingSpace := false

	for i := 0; i < len(msg); {
		switch msg[i] {
		case 0x1b:
			i = skipTelegramEscapeSequence(msg, i)
			pendingSpace = true
			continue
		case 0x9b:
			i = skipTelegramCSI(msg, i+1)
			pendingSpace = true
			continue
		}

		r, size := utf8.DecodeRuneInString(msg[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			pendingSpace = true
			i += size
			continue
		}
		if !unicode.IsPrint(r) {
			i += size
			continue
		}
		if pendingSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		pendingSpace = false
		b.WriteRune(r)
		i += size
	}

	return strings.TrimSpace(b.String())
}

func skipTelegramEscapeSequence(msg string, i int) int {
	if i >= len(msg) || msg[i] != 0x1b {
		return i + 1
	}
	i++
	if i >= len(msg) {
		return i
	}
	switch msg[i] {
	case '[':
		return skipTelegramCSI(msg, i+1)
	case ']':
		return skipTelegramOSC(msg, i+1)
	case 'P', 'X', '^', '_':
		return skipTelegramST(msg, i+1)
	default:
		return i + 1
	}
}

func skipTelegramCSI(msg string, i int) int {
	for i < len(msg) {
		b := msg[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			return i
		}
	}
	return i
}

func skipTelegramOSC(msg string, i int) int {
	for i < len(msg) {
		switch msg[i] {
		case 0x07:
			return i + 1
		case 0x1b:
			if i+1 < len(msg) && msg[i+1] == '\\' {
				return i + 2
			}
		}
		i++
	}
	return i
}

func skipTelegramST(msg string, i int) int {
	for i < len(msg) {
		if msg[i] == 0x1b && i+1 < len(msg) && msg[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
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

		msg := sanitizeTelegramSetupStatusMessage(status.Message)
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
