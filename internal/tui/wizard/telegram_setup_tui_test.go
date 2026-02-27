package wizard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/tui"
)

func stubTelegramSetupDeps(t *testing.T) {
	t.Helper()

	origRunner := telegramSetupWizardRunner
	origLoadConfig := telegramSetupLoadConfig
	origReadFile := telegramSetupReadFile
	origStat := telegramSetupStat
	origIdentityDetect := telegramSetupIdentityDetect
	origCheckRegistration := telegramSetupCheckRegistration
	origQueueUpdateDraw := telegramSetupQueueUpdateDraw
	origGo := telegramSetupGo

	t.Cleanup(func() {
		telegramSetupWizardRunner = origRunner
		telegramSetupLoadConfig = origLoadConfig
		telegramSetupReadFile = origReadFile
		telegramSetupStat = origStat
		telegramSetupIdentityDetect = origIdentityDetect
		telegramSetupCheckRegistration = origCheckRegistration
		telegramSetupQueueUpdateDraw = origQueueUpdateDraw
		telegramSetupGo = origGo
	})

	telegramSetupGo = func(fn func()) { fn() }
	telegramSetupQueueUpdateDraw = func(app *tui.App, f func()) { f() }
}

func TestRunTelegramSetupWizard_DisabledSkipsUIAndRunnerNotCalled(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TelegramEnabled: false}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		t.Fatalf("identity detect should not be called when telegram is disabled")
		return nil, nil
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

func TestRunTelegramSetupWizard_ConfigLoadAndReadFailSkipsUI(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("parse failed")
	}
	telegramSetupReadFile = func(path string) ([]byte, error) {
		return nil, errors.New("read failed")
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		t.Fatalf("runner should not be called when env cannot be read")
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

func TestRunTelegramSetupWizard_FallbackPersonalMode_Continue(t *testing.T) {
	stubTelegramSetupDeps(t)

	identityFile := filepath.Join(t.TempDir(), ".server_identity")
	if err := os.WriteFile(identityFile, []byte("id"), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New(strings.Repeat("x", 250))
	}
	telegramSetupReadFile = func(path string) ([]byte, error) {
		return []byte("TELEGRAM_ENABLED=true\nBOT_TELEGRAM_TYPE=Personal\n"), nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: " 123 ", IdentityFile: " " + identityFile + " "}, nil
	}
	telegramSetupStat = os.Stat
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form := focus.(*tview.Form)
		if form.GetButtonIndex("Check") != -1 {
			t.Fatalf("expected no Check button in personal mode")
		}
		if form.GetButtonIndex("Continue") == -1 {
			t.Fatalf("expected Continue button in personal mode")
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
	if result.ConfigLoaded {
		t.Fatalf("expected ConfigLoaded=false for fallback mode")
	}
	if result.ConfigError == "" {
		t.Fatalf("expected ConfigError to be set")
	}
	if !result.TelegramEnabled {
		t.Fatalf("expected TelegramEnabled=true")
	}
	if result.TelegramMode != "personal" {
		t.Fatalf("TelegramMode=%q, want personal", result.TelegramMode)
	}
	if result.ServerAPIHost != "https://bot.tis24.it:1443" {
		t.Fatalf("ServerAPIHost=%q, want default", result.ServerAPIHost)
	}
	if result.ServerID != "123" {
		t.Fatalf("ServerID=%q, want 123", result.ServerID)
	}
	if !result.IdentityPersisted {
		t.Fatalf("expected IdentityPersisted=true")
	}
	if result.Verified {
		t.Fatalf("expected Verified=false")
	}
	if result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=false")
	}
	if result.CheckAttempts != 0 {
		t.Fatalf("CheckAttempts=%d, want 0", result.CheckAttempts)
	}
}

func TestRunTelegramSetupWizard_CentralizedSuccess_RequiresCheckBeforeContinue(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "   ",
			TelegramServerAPIHost: " https://api.example.test ",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: " 987654321 ", IdentityFile: " /missing "}, nil
	}
	telegramSetupStat = func(path string) (os.FileInfo, error) {
		return nil, os.ErrNotExist
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
	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: "111222333"}, nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		calls++
		if calls == 1 {
			return notify.TelegramRegistrationStatus{Code: 403, Error: errors.New("not registered")}
		}
		if calls == 2 {
			return notify.TelegramRegistrationStatus{Code: 422, Message: "invalid"}
		}
		return notify.TelegramRegistrationStatus{Code: 500, Message: "oops"}
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

func TestRunTelegramSetupWizard_CentralizedMissingServerID_ExitsOnEscWithoutSkipping(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return nil, errors.New("detect failed")
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form := focus.(*tview.Form)
		if form.GetButtonIndex("Check") != -1 {
			t.Fatalf("expected no Check button without Server ID")
		}
		if form.GetButtonIndex("Skip") != -1 {
			t.Fatalf("expected no Skip button without Server ID")
		}
		if form.GetButtonIndex("Continue") == -1 {
			t.Fatalf("expected Continue button without Server ID")
		}

		capture := app.GetInputCapture()
		if capture == nil {
			t.Fatalf("expected input capture to be set")
		}
		capture(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=false")
	}
	if result.Verified {
		t.Fatalf("expected Verified=false")
	}
	if result.CheckAttempts != 0 {
		t.Fatalf("CheckAttempts=%d, want 0", result.CheckAttempts)
	}
	if result.ServerID != "" {
		t.Fatalf("ServerID=%q, want empty", result.ServerID)
	}
	if result.IdentityDetectError == "" {
		t.Fatalf("expected IdentityDetectError to be set")
	}
}

func TestRunTelegramSetupWizard_CentralizedMissingServerID_CanContinueButton(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: ""}, nil
	}
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form := focus.(*tview.Form)
		pressFormButton(t, form, "Continue")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=false")
	}
	if result.Verified {
		t.Fatalf("expected Verified=false")
	}
	if result.CheckAttempts != 0 {
		t.Fatalf("CheckAttempts=%d, want 0", result.CheckAttempts)
	}
}

func TestRunTelegramSetupWizard_CentralizedEscSkipsWhenNotVerified(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: "123456"}, nil
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

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: "123456"}, nil
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

	telegramSetupLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: "999888777"}, nil
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

		pressFormButton(t, form, "Check") // should be ignored while checking=true
		pressFormButton(t, form, "Skip")  // closes the wizard

		pending() // simulate late completion after closing
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
