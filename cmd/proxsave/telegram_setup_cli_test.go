package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run for config skip")
		return notify.TelegramRegistrationResult{}
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run for personal mode")
		return notify.TelegramRegistrationResult{}
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run when identity is unavailable")
		return notify.TelegramRegistrationResult{}
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run when user declines")
		return notify.TelegramRegistrationResult{}
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		checkCalls++
		if serverAPIHost != "https://api.example.test" {
			t.Fatalf("serverAPIHost=%q, want https://api.example.test", serverAPIHost)
		}
		if serverID != "123456789" {
			t.Fatalf("serverID=%q, want 123456789", serverID)
		}
		return notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200, Message: "ok"}}
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		checkCalls++
		return notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{
			Code:    409,
			Message: "not linked yet",
		}}
	}

	bootstrap := logging.NewBootstrapLogger()
	var mirrorBuf bytes.Buffer
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&mirrorBuf)
	bootstrap.SetMirrorLogger(mirror)

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", bootstrap); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
	if checkCalls != orchestrator.TelegramSetupMaxVerificationAttempts {
		t.Fatalf("checkCalls=%d, want %d", checkCalls, orchestrator.TelegramSetupMaxVerificationAttempts)
	}
	if promptCalls != orchestrator.TelegramSetupMaxVerificationAttempts {
		t.Fatalf("promptCalls=%d, want %d", promptCalls, orchestrator.TelegramSetupMaxVerificationAttempts)
	}
	wantLog := fmt.Sprintf("Telegram setup: not verified (attempts=%d last=409 not linked yet)", orchestrator.TelegramSetupMaxVerificationAttempts)
	if !strings.Contains(mirrorBuf.String(), wantLog) {
		t.Fatalf("expected max-attempt failure log %q, got %q", wantLog, mirrorBuf.String())
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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run on bootstrap error")
		return notify.TelegramRegistrationResult{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("runTelegramSetupCLI error: %v", err)
	}
}

// TestRunTelegramSetupCLI_PromptAbortIsNonBlocking pins the BENIGN half of the
// optional-step contract: a plain input abort (Ctrl-D/EOF) with the run context
// still LIVE during the optional Telegram verification must NOT abort the install.
// It returns nil so runInstall still reaches the entrypoint/cron finalization,
// matching the TUI which demotes Telegram errors to a warning. The other half (a
// real Ctrl+C that cancels the run context) DOES abort, pinned by
// TestRunTelegramSetupCLI_CancelledCtxAbortsInstall. (The context here is live, so
// this stays non-blocking even though the stub returns errInteractiveAborted.)
func TestRunTelegramSetupCLI_PromptAbortIsNonBlocking(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupEligibleCentralized,
			ServerID:    "12345",
		}, nil
	}
	promptCalls := 0
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		promptCalls++
		return false, errInteractiveAborted
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run when the prompt aborts")
		return notify.TelegramRegistrationResult{}
	}

	if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
		t.Fatalf("prompt abort during Telegram setup must be non-blocking, got: %v", err)
	}
	if promptCalls != 1 {
		t.Fatalf("expected the verification prompt to be exercised exactly once, got %d calls", promptCalls)
	}
}

// TestRunTelegramSetupCLI_CancelledCtxAbortsInstall pins F10-01 end to end through
// a real optional step: a Ctrl+C that cancels the run context during Telegram
// setup must abort the install (isInstallAbortedError) so runInstall stops before
// the cron/scheduler finalization instead of reporting a false-green "completed".
// Contrast with TestRunTelegramSetupCLI_PromptAbortIsNonBlocking (live context).
func TestRunTelegramSetupCLI_CancelledCtxAbortsInstall(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupEligibleCentralized,
			ServerID:    "12345",
		}, nil
	}
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		return false, errInteractiveAborted
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		t.Fatalf("registration check should not run when the prompt aborts")
		return notify.TelegramRegistrationResult{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runTelegramSetupCLI(ctx, bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger())
	if err == nil {
		t.Fatal("Ctrl+C (cancelled context) during Telegram setup must abort the install, got nil")
	}
	if !isInstallAbortedError(err) {
		t.Fatalf("returned error must be an install abort, got %v", err)
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
	raw := strings.Repeat("\x1b", orchestrator.TelegramSetupStatusMessageMaxRunes+5)

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
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		return notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{
			Code:    500,
			Message: "\x1b[31mneeds\tpairing\r\nnow\x1b[0m\x07",
			Error:   errors.New("unexpected status 500"),
		}}
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

// TestRunTelegramSetupCLI_UpgradeRequiredStopsRetry pins the CLI parity with the
// TUI: a 426 (upgrade required) is fatal, so the CLI shows the distinct message
// and returns WITHOUT offering "Check again?" (no second prompt, single check).
func TestRunTelegramSetupCLI_UpgradeRequiredStopsRetry(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

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
	promptCalls := 0
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		promptCalls++
		if promptCalls != 1 {
			t.Fatalf("expected only the initial check prompt, got call %d: %q", promptCalls, question)
		}
		return true, nil
	}
	checkCalls := 0
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		checkCalls++
		return notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{
			Code:    426,
			Message: "426 - Upgrade ProxSave to v0.28.0 or later to complete pairing",
			Error:   errors.New("upgrade"),
		}}
	}

	output := captureStdout(t, func() {
		if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("runTelegramSetupCLI error: %v", err)
		}
	})

	if !strings.Contains(output, "Upgrade ProxSave to v0.28.0 or later to complete pairing.") {
		t.Fatalf("expected upgrade-required message in output, got %q", output)
	}
	if checkCalls != 1 {
		t.Fatalf("checkCalls=%d, want 1", checkCalls)
	}
	if promptCalls != 1 {
		t.Fatalf("promptCalls=%d, want 1 (no 'Check again?' after a fatal status)", promptCalls)
	}
}

// TestRunTelegramSetupCLI_PartialLinked pins that a 200 with a failed persist
// counts as verified (the CLI returns successfully after one check) but shows the
// distinct partial message instead of the clean success line.
func TestRunTelegramSetupCLI_PartialLinked(t *testing.T) {
	stubTelegramSetupCLIDeps(t)

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
	promptCalls := 0
	telegramSetupPromptYesNo = func(ctx context.Context, reader *bufio.Reader, question string, defaultYes bool) (bool, error) {
		promptCalls++
		if promptCalls != 1 {
			t.Fatalf("expected only the initial check prompt, got call %d: %q", promptCalls, question)
		}
		return true, nil
	}
	checkCalls := 0
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		checkCalls++
		return notify.TelegramRegistrationResult{
			Status:    notify.TelegramRegistrationStatus{Code: 200, Message: "ok"},
			Provision: notify.TelegramProvisionPersistFailed,
		}
	}

	output := captureStdout(t, func() {
		if err := runTelegramSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), t.TempDir(), "/fake/backup.env", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("runTelegramSetupCLI error: %v", err)
		}
	})

	if !strings.Contains(output, "could not be saved locally") {
		t.Fatalf("expected distinct partial message in output, got %q", output)
	}
	if checkCalls != 1 {
		t.Fatalf("checkCalls=%d, want 1", checkCalls)
	}
	if promptCalls != 1 {
		t.Fatalf("promptCalls=%d, want 1", promptCalls)
	}
}
