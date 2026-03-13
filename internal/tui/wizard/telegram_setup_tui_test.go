package wizard

import (
	"context"
	"errors"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui"
)

func stubTelegramSetupDeps(t *testing.T) {
	t.Helper()

	origRunner := telegramSetupWizardRunner
	origBuildBootstrap := telegramSetupBuildBootstrap
	origCheckRegistration := telegramSetupCheckRegistration
	origQueueUpdateDraw := telegramSetupQueueUpdateDraw
	origGo := telegramSetupGo

	t.Cleanup(func() {
		telegramSetupWizardRunner = origRunner
		telegramSetupBuildBootstrap = origBuildBootstrap
		telegramSetupCheckRegistration = origCheckRegistration
		telegramSetupQueueUpdateDraw = origQueueUpdateDraw
		telegramSetupGo = origGo
	})

	telegramSetupGo = func(fn func()) { fn() }
	telegramSetupQueueUpdateDraw = func(app *tui.App, f func()) { f() }
}

func eligibleTelegramSetupBootstrap() orchestrator.TelegramSetupBootstrap {
	return orchestrator.TelegramSetupBootstrap{
		Eligibility:       orchestrator.TelegramSetupEligibleCentralized,
		ConfigLoaded:      true,
		TelegramEnabled:   true,
		TelegramMode:      "centralized",
		ServerAPIHost:     "https://api.example.test",
		ServerID:          "123456789",
		IdentityFile:      "/tmp/.server_identity",
		IdentityPersisted: false,
	}
}

func TestRunTelegramSetupWizard_DisabledSkipsUIAndRunnerNotCalled(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupSkipDisabled,
			ConfigLoaded:    true,
			TelegramEnabled: false,
		}, nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatalf("runner should not be called when telegram is disabled")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/nope/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.Shown {
		t.Fatalf("expected wizard to not be shown")
	}
	if !result.ConfigLoaded {
		t.Fatalf("expected ConfigLoaded=true")
	}
	if result.TelegramEnabled {
		t.Fatalf("expected TelegramEnabled=false")
	}
}

func TestRunTelegramSetupWizard_ConfigErrorSkipsUI(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupSkipConfigError,
			ConfigError: "parse failed",
		}, nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatalf("runner should not be called when config bootstrap failed")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/nope/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.Shown {
		t.Fatalf("expected wizard to not be shown")
	}
	if result.ConfigLoaded {
		t.Fatalf("expected ConfigLoaded=false")
	}
	if result.ConfigError == "" {
		t.Fatalf("expected ConfigError to be set")
	}
}

func TestRunTelegramSetupWizard_PersonalModeSkipsUI(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:     orchestrator.TelegramSetupSkipPersonalMode,
			ConfigLoaded:    true,
			TelegramEnabled: true,
			TelegramMode:    "personal",
			ServerAPIHost:   "https://bot.tis24.it:1443",
		}, nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatalf("runner should not be called in personal mode")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.Shown {
		t.Fatalf("expected wizard to not be shown")
	}
	if result.TelegramMode != "personal" {
		t.Fatalf("TelegramMode=%q, want personal", result.TelegramMode)
	}
	if !result.TelegramEnabled {
		t.Fatalf("expected TelegramEnabled=true")
	}
}

func TestRunTelegramSetupWizard_IdentityUnavailableSkipsUI(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility:         orchestrator.TelegramSetupSkipIdentityUnavailable,
			ConfigLoaded:        true,
			TelegramEnabled:     true,
			TelegramMode:        "centralized",
			ServerAPIHost:       "https://api.example.test",
			IdentityDetectError: "detect failed",
		}, nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatalf("runner should not be called when server ID is unavailable")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.Shown {
		t.Fatalf("expected wizard to not be shown")
	}
	if result.IdentityDetectError == "" {
		t.Fatalf("expected IdentityDetectError to be set")
	}
	if result.ServerID != "" {
		t.Fatalf("ServerID=%q, want empty", result.ServerID)
	}
}

func TestRunTelegramSetupWizard_CentralizedSuccess_RequiresCheckBeforeContinue(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		state := eligibleTelegramSetupBootstrap()
		state.ServerID = "987654321"
		return state, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		if serverAPIHost != "https://api.example.test" {
			t.Fatalf("serverAPIHost=%q, want https://api.example.test", serverAPIHost)
		}
		if serverID != "987654321" {
			t.Fatalf("serverID=%q, want 987654321", serverID)
		}
		return notify.TelegramRegistrationStatus{Code: 200, Message: "ok"}
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form := focus.(*tview.Form)
		if form.GetButtonIndex("Continue") != -1 {
			t.Fatalf("expected no Continue button before verification")
		}
		if form.GetButtonIndex("Skip") == -1 {
			t.Fatalf("expected Skip button before verification")
		}
		if form.GetButtonIndex("Check") == -1 {
			t.Fatalf("expected Check button before verification")
		}

		pressFormButton(t, form, "Check")

		if form.GetButtonIndex("Skip") != -1 {
			t.Fatalf("expected Skip button to be removed after verification")
		}
		if form.GetButtonIndex("Continue") == -1 {
			t.Fatalf("expected Continue button after verification")
		}
		pressFormButton(t, form, "Continue")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.Shown {
		t.Fatalf("expected wizard to be shown")
	}
	if !result.ConfigLoaded {
		t.Fatalf("expected ConfigLoaded=true")
	}
	if result.TelegramMode != "centralized" {
		t.Fatalf("TelegramMode=%q, want centralized", result.TelegramMode)
	}
	if result.ServerAPIHost != "https://api.example.test" {
		t.Fatalf("ServerAPIHost=%q, want https://api.example.test", result.ServerAPIHost)
	}
	if result.IdentityPersisted {
		t.Fatalf("expected IdentityPersisted=false")
	}
	if !result.Verified {
		t.Fatalf("expected Verified=true")
	}
	if result.CheckAttempts != 1 {
		t.Fatalf("CheckAttempts=%d, want 1", result.CheckAttempts)
	}
	if result.LastStatusCode != 200 {
		t.Fatalf("LastStatusCode=%d, want 200", result.LastStatusCode)
	}
	if result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=false")
	}
}

func TestRunTelegramSetupWizard_CentralizedFailure_CanRetryAndSkip(t *testing.T) {
	stubTelegramSetupDeps(t)

	var calls int
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		state := eligibleTelegramSetupBootstrap()
		state.ServerID = "111222333"
		return state, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		calls++
		switch calls {
		case 1:
			return notify.TelegramRegistrationStatus{Code: 403, Error: errors.New("not registered")}
		case 2:
			return notify.TelegramRegistrationStatus{Code: 422, Message: "invalid"}
		default:
			return notify.TelegramRegistrationStatus{Code: 500, Message: "oops"}
		}
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form := focus.(*tview.Form)
		pressFormButton(t, form, "Check")
		pressFormButton(t, form, "Check")
		pressFormButton(t, form, "Check")
		if form.GetButtonIndex("Continue") != -1 {
			t.Fatalf("expected no Continue button when not verified")
		}
		pressFormButton(t, form, "Skip")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.Shown {
		t.Fatalf("expected wizard to be shown")
	}
	if result.Verified {
		t.Fatalf("expected Verified=false")
	}
	if !result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=true")
	}
	if result.CheckAttempts != 3 {
		t.Fatalf("CheckAttempts=%d, want 3", result.CheckAttempts)
	}
	if result.LastStatusCode != 500 {
		t.Fatalf("LastStatusCode=%d, want 500", result.LastStatusCode)
	}
	if calls != 3 {
		t.Fatalf("check calls=%d, want 3", calls)
	}
}

func TestRunTelegramSetupWizard_CentralizedEscSkipsWhenNotVerified(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		capture := app.GetInputCapture()
		if capture == nil {
			t.Fatalf("expected input capture to be set")
		}

		nonEsc := tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
		if got := capture(nonEsc); got != nonEsc {
			t.Fatalf("expected non-ESC to pass through")
		}

		capture(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=true when exiting with ESC before verification")
	}
	if result.Verified {
		t.Fatalf("expected Verified=false")
	}
	if result.CheckAttempts != 0 {
		t.Fatalf("CheckAttempts=%d, want 0", result.CheckAttempts)
	}
}

func TestRunTelegramSetupWizard_PropagatesRunnerError(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return errors.New("runner failed")
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err == nil {
		t.Fatalf("expected error")
	}
	if result != (TelegramSetupResult{}) {
		t.Fatalf("expected empty result on runner error, got %#v", result)
	}
}

func TestRunTelegramSetupWizard_CheckIgnoredWhileChecking_AndUpdateSuppressedAfterClose(t *testing.T) {
	stubTelegramSetupDeps(t)

	var pending func()
	var checkCalls int

	telegramSetupGo = func(fn func()) { pending = fn }
	telegramSetupQueueUpdateDraw = func(app *tui.App, f func()) { f() }
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		state := eligibleTelegramSetupBootstrap()
		state.ServerID = "999888777"
		return state, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		checkCalls++
		return notify.TelegramRegistrationStatus{Code: 200, Message: "ok"}
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form := focus.(*tview.Form)

		pressFormButton(t, form, "Check")
		if pending == nil {
			t.Fatalf("expected pending check goroutine")
		}

		pressFormButton(t, form, "Check")
		pressFormButton(t, form, "Skip")

		pending()
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=true")
	}
	if result.Verified {
		t.Fatalf("expected Verified=false when update is suppressed after close")
	}
	if result.CheckAttempts != 0 {
		t.Fatalf("CheckAttempts=%d, want 0 when update is suppressed after close", result.CheckAttempts)
	}
	if checkCalls != 1 {
		t.Fatalf("checkCalls=%d, want 1", checkCalls)
	}
}
