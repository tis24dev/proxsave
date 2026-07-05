package notify

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// S4: showPortalLink is capture-only. It returns the RAW login_url (which flows to
// BackupStats.HealthcheckLink) and NO LONGER emits -- the orchestrator's Healthchecks
// section is the sole sanitize+display boundary. isSafePortalLink was deleted; its
// cases now live in serverbot's SanitizeLoginURL tests.
func TestShowPortalLinkCapturesNotEmit(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	n, err := NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
	}, logger)
	if err != nil || n == nil {
		t.Fatalf("notifier construction: %v", err)
	}

	// present -> captured RAW, and NOT emitted.
	link := n.showPortalLink([]byte(`{"status":"accepted","login_url":"https://hc/accounts/check_token/u/MAGIC/"}`))
	if link != "https://hc/accounts/check_token/u/MAGIC/" {
		t.Fatalf("must capture the RAW magic-link, got %q", link)
	}
	if strings.Contains(buf.String(), "MAGIC") || strings.Contains(buf.String(), "portal") {
		t.Fatalf("capture-only: must NOT emit the link, got: %q", buf.String())
	}

	// absent (server omits it after first login) -> "".
	if link := n.showPortalLink([]byte(`{"status":"accepted"}`)); link != "" {
		t.Fatalf("absent login_url must capture empty, got %q", link)
	}

	// unsafe URL (a raw space) is still captured RAW; the section's SanitizeLoginURL is
	// the boundary that rejects it at display.
	if unsafe := n.showPortalLink([]byte(`{"login_url":"https://hc/ x"}`)); unsafe != "https://hc/ x" {
		t.Fatalf("unsafe link must still be captured RAW, got %q", unsafe)
	}

	// malformed body -> "".
	if link := n.showPortalLink([]byte(`not json`)); link != "" {
		t.Fatalf("malformed body must capture empty, got %q", link)
	}
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("capture-only path must never emit, got: %q", buf.String())
	}
}
