package wizard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func extractTelegramSetupViews(t *testing.T, root tview.Primitive) (*tview.TextView, *tview.TextView, *tview.Form) {
	t.Helper()

	layout, ok := root.(*tview.Flex)
	if !ok {
		t.Fatalf("expected root *tview.Flex, got %T", root)
	}

	var pages *tview.Pages
	for i := 0; i < layout.GetItemCount(); i++ {
		candidate, ok := layout.GetItem(i).(*tview.Pages)
		if !ok {
			continue
		}
		if pages != nil {
			t.Fatal("expected a single pages container in telegram setup layout")
		}
		pages = candidate
	}
	if pages == nil {
		t.Fatal("expected pages container in telegram setup layout")
	}

	_, bodyPrimitive := pages.GetFrontPage()
	body, ok := bodyPrimitive.(*tview.Flex)
	if !ok {
		t.Fatalf("expected body *tview.Flex, got %T", bodyPrimitive)
	}

	var serverIDView, statusView *tview.TextView
	var form *tview.Form
	for i := 0; i < body.GetItemCount(); i++ {
		switch item := body.GetItem(i).(type) {
		case *tview.TextView:
			switch strings.TrimSpace(item.GetTitle()) {
			case "Server ID":
				serverIDView = item
			case "Status":
				statusView = item
			}
		case *tview.Form:
			if strings.TrimSpace(item.GetTitle()) == "Actions" {
				form = item
			}
		}
	}

	if serverIDView == nil {
		t.Fatal("expected Server ID view in telegram setup body")
	}
	if statusView == nil {
		t.Fatal("expected Status view in telegram setup body")
	}
	if form == nil {
		t.Fatal("expected Actions form in telegram setup body")
	}

	return serverIDView, statusView, form
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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

func TestRunTelegramSetupWizard_PropagatesBootstrapError(t *testing.T) {
	stubTelegramSetupDeps(t)

	expectedErr := errors.New("bootstrap failed")
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{}, expectedErr
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		t.Fatalf("runner should not be called when bootstrap returns an error")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
	if result != (TelegramSetupResult{}) {
		t.Fatalf("expected empty result on bootstrap error, got %#v", result)
	}
}

func TestRunTelegramSetupWizard_PassesContextToRunner(t *testing.T) {
	stubTelegramSetupDeps(t)

	ctx := t.Context()
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupWizardRunner = func(gotCtx context.Context, app *tui.App, root, focus tview.Primitive) error {
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		form := focus.(*tview.Form)
		pressFormButton(t, form, "Skip")
		return nil
	}

	result, err := RunTelegramSetupWizard(ctx, t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=true")
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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

func TestRunTelegramSetupWizard_ShowsPersistedIdentityState(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		state := eligibleTelegramSetupBootstrap()
		state.IdentityPersisted = true
		return state, nil
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		serverIDView, _, form := extractTelegramSetupViews(t, root)
		text := serverIDView.GetText(true)
		if !strings.Contains(text, "persisted") {
			t.Fatalf("expected persisted identity state, got %q", text)
		}
		if strings.Contains(text, "not persisted") {
			t.Fatalf("did not expect non-persisted label, got %q", text)
		}

		pressFormButton(t, form, "Skip")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.IdentityPersisted {
		t.Fatalf("expected IdentityPersisted=true")
	}
	if !result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=true")
	}
}

func TestRunTelegramSetupWizard_EscapesBracketedServerIdentityValues(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		state := eligibleTelegramSetupBootstrap()
		state.ServerID = "srv[42]"
		state.IdentityFile = "/tmp/identity[prod].key"
		return state, nil
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		serverIDView, _, form := extractTelegramSetupViews(t, root)

		rawText := serverIDView.GetText(false)
		if !strings.Contains(rawText, tview.Escape("srv[42]")) {
			t.Fatalf("expected escaped server ID in raw text, got %q", rawText)
		}
		if !strings.Contains(rawText, tview.Escape("/tmp/identity[prod].key")) {
			t.Fatalf("expected escaped identity file in raw text, got %q", rawText)
		}

		plainText := serverIDView.GetText(true)
		if !strings.Contains(plainText, "srv[42]") {
			t.Fatalf("expected literal server ID in plain text, got %q", plainText)
		}
		if !strings.Contains(plainText, "/tmp/identity[prod].key") {
			t.Fatalf("expected literal identity file in plain text, got %q", plainText)
		}

		pressFormButton(t, form, "Skip")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=true")
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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

func TestRunTelegramSetupWizard_TruncatesLongFailureMessage(t *testing.T) {
	stubTelegramSetupDeps(t)

	longMessage := strings.Repeat("x", 320)
	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		return notify.TelegramRegistrationStatus{
			Code:    500,
			Message: "  " + longMessage + "  ",
		}
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		_, statusView, form := extractTelegramSetupViews(t, root)

		pressFormButton(t, form, "Check")

		text := statusView.GetText(true)
		if !strings.Contains(text, "...(truncated)") {
			t.Fatalf("expected truncated status, got %q", text)
		}
		if !strings.Contains(text, "Skip verification and complete pairing later.") {
			t.Fatalf("expected retry/skip hint, got %q", text)
		}
		if strings.Contains(text, longMessage) {
			t.Fatalf("expected long message to be truncated, got %q", text)
		}

		pressFormButton(t, form, "Skip")
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if result.Verified {
		t.Fatalf("expected Verified=false")
	}
	if result.CheckAttempts != 1 {
		t.Fatalf("CheckAttempts=%d, want 1", result.CheckAttempts)
	}
	if result.LastStatusCode != 500 {
		t.Fatalf("LastStatusCode=%d, want 500", result.LastStatusCode)
	}
	if result.LastStatusMessage != "  "+longMessage+"  " {
		t.Fatalf("LastStatusMessage=%q, want original message", result.LastStatusMessage)
	}
}

func TestRunTelegramSetupWizard_CentralizedEscSkipsWhenNotVerified(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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

func TestRunTelegramSetupWizard_CentralizedEscAfterVerificationDoesNotSkip(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupCheckRegistration = func(ctx context.Context, serverAPIHost, serverID string, logger *logging.Logger) notify.TelegramRegistrationStatus {
		return notify.TelegramRegistrationStatus{Code: 200, Message: "ok"}
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		_, _, form := extractTelegramSetupViews(t, root)

		pressFormButton(t, form, "Check")
		if form.GetButtonIndex("Continue") == -1 {
			t.Fatalf("expected Continue button after verification")
		}

		capture := app.GetInputCapture()
		if capture == nil {
			t.Fatalf("expected input capture to be set")
		}
		if got := capture(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); got != nil {
			t.Fatalf("expected ESC to be consumed, got %#v", got)
		}
		return nil
	}

	result, err := RunTelegramSetupWizard(context.Background(), t.TempDir(), "/fake/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunTelegramSetupWizard error: %v", err)
	}
	if !result.Verified {
		t.Fatalf("expected Verified=true")
	}
	if result.SkippedVerification {
		t.Fatalf("expected SkippedVerification=false after ESC on verified flow")
	}
	if result.CheckAttempts != 1 {
		t.Fatalf("CheckAttempts=%d, want 1", result.CheckAttempts)
	}
}

func TestRunTelegramSetupWizard_PropagatesRunnerError(t *testing.T) {
	stubTelegramSetupDeps(t)

	telegramSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return eligibleTelegramSetupBootstrap(), nil
	}
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
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

func TestTelegramSetupDefaultWrappers(t *testing.T) {
	done := make(chan struct{})
	telegramSetupGo(func() {
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telegramSetupGo")
	}

	app := tui.NewApp()
	app.SetScreen(tcell.NewSimulationScreen("UTF-8"))
	root := tview.NewBox()

	updateQueued := make(chan struct{})
	updateDone := make(chan struct{})
	go func() {
		close(updateQueued)
		telegramSetupQueueUpdateDraw(app, func() {
			close(updateDone)
			app.Stop()
		})
	}()
	<-updateQueued

	go func() {
		select {
		case <-updateDone:
			return
		case <-time.After(100 * time.Millisecond):
			app.Stop()
		}
	}()

	if err := telegramSetupWizardRunner(context.Background(), app, root, root); err != nil {
		t.Fatalf("telegramSetupWizardRunner error: %v", err)
	}

	select {
	case <-updateDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telegramSetupQueueUpdateDraw")
	}
}
