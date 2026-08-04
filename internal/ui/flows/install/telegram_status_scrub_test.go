package install

import (
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// TestRunTelegramSetupScrubsTheRelayStatusMessage: LastStatusMessage exists to be
// written into the persisted install log, and it carries a string the relay chose. It
// used to be stored raw ("parity with tview"), so a relay that answered with terminal
// escapes had them land in a file that is later cat'd by whoever debugs the install.
// Scrubbing belongs at THIS write site rather than at the log site, because the field
// is what future consumers will reach for and none of them can be relied on to
// remember. Pinned here so a reader who sees a scrub at the log site cannot conclude
// the field itself may go back to raw.
func TestRunTelegramSetupScrubsTheRelayStatusMessage(t *testing.T) {
	d := newDriver(t)

	origBootstrap := telegramBuildBootstrap
	origCheck := telegramCheckRegistration
	t.Cleanup(func() {
		telegramBuildBootstrap = origBootstrap
		telegramCheckRegistration = origCheck
	})

	telegramBuildBootstrap = func(configPath, baseDir string) (orchestrator.TelegramSetupBootstrap, error) {
		return orchestrator.TelegramSetupBootstrap{
			Eligibility: orchestrator.TelegramSetupEligibleCentralized,
			ServerID:    "12345678",
		}, nil
	}
	// A hostile-but-plausible 409: a clear-screen sequence, a cursor jump, a bell and a
	// carriage return wrapped around otherwise ordinary copy.
	const hostile = "\x1b[2J\x1b[1;1Hnot\tlinked\r\nyet\x07"
	telegramCheckRegistration = func(ctx context.Context, host, serverID, baseDir string, logger *logging.Logger) notify.TelegramRegistrationResult {
		res := notify.TelegramRegistrationResult{}
		res.Status.Code = 409
		res.Status.Message = hostile
		return res
	}

	type outcome struct {
		res installer.TelegramSetupResult
		err error
	}
	resCh := make(chan outcome, 1)
	go func() {
		res, err := RunTelegramSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", false)
		resCh <- outcome{res, err}
	}()

	d.waitScreen("Telegram setup")
	d.keys("enter") // Check
	d.waitScreen("Telegram setup")
	d.keys("down enter") // Skip (409 is not verified, so the leave action is Skip)
	got := <-resCh
	if got.err != nil {
		t.Fatalf("RunTelegramSetup: %v", got.err)
	}
	if got.res.CheckAttempts != 1 || got.res.LastStatusCode != 409 {
		t.Fatalf("test setup: the check did not run as expected: %+v", got.res)
	}

	msg := got.res.LastStatusMessage
	if strings.ContainsAny(msg, "\x1b\x07\r\n\t") {
		t.Fatalf("LastStatusMessage still carries control bytes and would reach the install log: %q", msg)
	}
	if want := orchestrator.SanitizeTelegramSetupStatusMessage(hostile); msg != want {
		t.Fatalf("LastStatusMessage = %q, want the shared sanitizer's output %q", msg, want)
	}
	// The words survive: scrubbing must not cost the reader the reason.
	if !strings.Contains(msg, "not linked yet") {
		t.Fatalf("LastStatusMessage lost the relay's actual message: %q", msg)
	}
}
