package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

func stubTelegramSetupCLIDeps(t *testing.T) {
	t.Helper()

	origBuildBootstrap := telegramSetupBuildBootstrap
	origCheckRegistration := telegramSetupCheckRegistration
	origPromptYesNo := telegramSetupPromptYesNo

	t.Cleanup(func() {
		telegramSetupBuildBootstrap = origBuildBootstrap
		telegramSetupCheckRegistration = origCheckRegistration
		telegramSetupPromptYesNo = origPromptYesNo
	})
}

func TestRunTelegramSetupCLI_SkipOnConfigError(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupSkipConfigError,
			ConfigError: "parse failed",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		t.Fatalf("prompt should not run for config skip")
		return false, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		t.Fatalf("registration check should not run for config skip")
		return notify.TelegramRegistrationStatus{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
}

func TestRunTelegramSetupCLI_SkipOnPersonalMode(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupSkipPersonalMode,
			ConfigLoaded:    true,
			TelegramEnabled: true,
			TelegramMode:    "personal",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		t.Fatalf("prompt should not run for personal mode")
		return false, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		t.Fatalf("registration check should not run for personal mode")
		return notify.TelegramRegistrationStatus{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
}

func TestRunTelegramSetupCLI_SkipOnMissingIdentity(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:         orchestrator.TelegramSetupSkipIdentityUnavailable,
			ConfigLoaded:        true,
			TelegramEnabled:     true,
			TelegramMode:        "centralized",
			IdentityDetectError: "detect failed",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		t.Fatalf("prompt should not run when identity is unavailable")
		return false, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		t.Fatalf("registration check should not run when identity is unavailable")
		return notify.TelegramRegistrationStatus{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
}

func TestRunTelegramSetupCLI_DeclineVerification(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupEligibleCentralized,
			ConfigLoaded:    true,
			TelegramEnabled: true,
			TelegramMode:    "centralized",
			ServerAPIHost:   "https://api.example.test",
			ServerID:        "123456789",
			IdentityFile:    "/tmp/.server_identity",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		if !strings.Contains(question, "Check Telegram pairing now?") {
			t.Fatalf("unexpected question: %s", question)
		}
		return false, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		t.Fatalf("registration check should not run when user declines")
		return notify.TelegramRegistrationStatus{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
}

func TestRunTelegramSetupCLI_VerifiesSuccessfully(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	var promptCalls int
	var checkCalls int
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupEligibleCentralized,
			ConfigLoaded:    true,
			TelegramEnabled: true,
			TelegramMode:    "centralized",
			ServerAPIHost:   "https://api.example.test",
			ServerID:        "123456789",
			IdentityFile:    "/tmp/.server_identity",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		promptCalls++
		if promptCalls != 1 {
			t.Fatalf("unexpected prompt call count: %d", promptCalls)
		}
		return true, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		checkCalls++
		if serverAPIHost != "https://api.example.test" {
			t.Fatalf("serverAPIHost=%q, want https://api.example.test", serverAPIHost)
		}
		if serverID != "123456789" {
			t.Fatalf("serverID=%q, want 123456789", serverID)
		}
		return notify.TelegramRegistrationStatus{Code: 200, Message: "ok"}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
	if promptCalls != 1 {
		t.Fatalf("promptCalls=%d, want 1", promptCalls)
	}
	if checkCalls != 1 {
		t.Fatalf("checkCalls=%d, want 1", checkCalls)
	}
}

func TestRunTelegramSetupCLI_StopsAfterMaxVerificationAttempts(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	var promptCalls int
	var checkCalls int
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupEligibleCentralized,
			ConfigLoaded:    true,
			TelegramEnabled: true,
			TelegramMode:    "centralized",
			ServerAPIHost:   "https://api.example.test",
			ServerID:        "123456789",
			IdentityFile:    "/tmp/.server_identity",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		promptCalls++
		return true, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		checkCalls++
		return notify.TelegramRegistrationStatus{
			Code:    409,
			Message: "not linked yet",
		}
	}

	bootstrap := logging.NewBootstrapLogger()
	var mirrorBuf bytes.Buffer
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&mirrorBuf)
	bootstrap.SetMirrorLogger(mirror)

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", bootstrap); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
	if checkCalls != maxTelegramSetupVerificationAttempts {
		t.Fatalf("checkCalls=%d, want %d", checkCalls, maxTelegramSetupVerificationAttempts)
	}
	if promptCalls != maxTelegramSetupVerificationAttempts {
		t.Fatalf("promptCalls=%d, want %d", promptCalls, maxTelegramSetupVerificationAttempts)
	}
	if !strings.Contains(mirrorBuf.String(), "Telegram setup: not verified (attempts=10 last=409 not linked yet)") {
		t.Fatalf("expected max-attempt failure log, got %q", mirrorBuf.String())
	}
}

func TestRunTelegramSetupCLI_BootstrapErrorNonBlocking(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{}, errors.New("boom")
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		t.Fatalf("prompt should not run on bootstrap error")
		return false, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		t.Fatalf("registration check should not run on bootstrap error")
		return notify.TelegramRegistrationStatus{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
}

func TestSanitizeTelegramSetupStatusMessage_StripsTerminalEscapes(t *testing.T) {
	raw := " \x1b[31mneeds\tpairing\r\nnow\x1b[0m\x07 "

	got := sanitizeTelegramSetupStatusMessage(raw)

	if got != "needs pairing now" {
		t.Fatalf("sanitizeTelegramSetupStatusMessage(%q) = %q, want %q", raw, got, "needs pairing now")
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("sanitized message should not contain escape characters: %q", got)
	}
}

func TestSanitizeTelegramSetupStatusMessage_FallsBackToQuotedSafeText(t *testing.T) {
	raw := strings.Repeat("\x1b", maxTelegramSetupStatusMessageLen+5)

	got := sanitizeTelegramSetupStatusMessage(raw)

	if got == "" {
		t.Fatal("expected fallback message")
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("fallback should not contain raw escape characters: %q", got)
	}
	if !strings.Contains(got, `\x1b`) {
		t.Fatalf("fallback should retain a safe escaped representation, got %q", got)
	}
	if !strings.Contains(got, "...(truncated)") {
		t.Fatalf("expected truncated fallback output, got %q", got)
	}
}

func TestRunTelegramSetupCLI_SanitizesRegistrationStatusOutput(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	promptCalls := 0
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupEligibleCentralized,
			ConfigLoaded:    true,
			TelegramEnabled: true,
			TelegramMode:    "centralized",
			ServerAPIHost:   "https://api.example.test",
			ServerID:        "123456789",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		promptCalls++
		return promptCalls == 1, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		return notify.TelegramRegistrationStatus{
			Code:    500,
			Message: "\x1b[31mneeds\tpairing\r\nnow\x1b[0m\x07",
			Error:   errors.New("unexpected status 500"),
		}
	}

	output := captureStdout(t, func() {
		if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("runTelegramSetupCLI error: %v", err)
		}
	})

	if !strings.Contains(output, "Telegram: needs pairing now") {
		t.Fatalf("expected sanitized Telegram status in output, got %q", output)
	}
	if strings.Contains(output, "\x1b") {
		t.Fatalf("output should not contain raw escape sequences, got %q", output)
	}
}
