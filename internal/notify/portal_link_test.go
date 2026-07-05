package notify

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestIsSafePortalLink(t *testing.T) {
	ok := []string{
		"https://hc.proxsave.dev/accounts/check_token/u/tok/",
		"http://x/y",
	}
	bad := []string{
		"",
		"ftp://x",
		"javascript:alert(1)",
		"hc.proxsave.dev/x",                 // no scheme
		"https://x/" + string(rune(0x1b)),   // ANSI/CSI (ESC)
		"https://x/\n",                      // newline
		"https://x/\t",                      // tab
		"https://x/" + string(rune(0x7f)),   // DEL
		"https://x/" + string(rune(0x9b)),   // C1 CSI
		"https://x/" + string(rune(0x2028)), // line separator
		"https://x/" + string(rune(0x202e)), // bidi override
		"https://x/pa th",                   // raw space
	}
	for _, s := range ok {
		if !isSafePortalLink(s) {
			t.Errorf("should accept %q", s)
		}
	}
	for _, s := range bad {
		if isSafePortalLink(s) {
			t.Errorf("should reject %q", s)
		}
	}
}

// S3 dual-write: showPortalLink CAPTURES the RAW login_url (returned, for the S4
// healthchecks section via BackupStats.HealthcheckLink) AND still emits the portal
// line when the link is safe. The direct emission moves out in S4.
func TestShowPortalLinkCapturesAndEmits(t *testing.T) {
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

	// present + safe -> captured RAW AND emitted (dual-write).
	link := n.showPortalLink([]byte(`{"status":"accepted","login_url":"https://hc/accounts/check_token/u/MAGIC/"}`))
	if link != "https://hc/accounts/check_token/u/MAGIC/" {
		t.Fatalf("must capture the RAW magic-link, got %q", link)
	}
	if !strings.Contains(buf.String(), "https://hc/accounts/check_token/u/MAGIC/") {
		t.Fatalf("must still emit the magic-link (dual-write), got: %q", buf.String())
	}

	// absent (user already logged in -> server omits it) -> capture "" + emit nothing.
	buf.Reset()
	if link := n.showPortalLink([]byte(`{"status":"accepted"}`)); link != "" {
		t.Fatalf("absent login_url must capture empty, got %q", link)
	}
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("no login_url must show nothing, got: %q", buf.String())
	}

	// unsafe URL (a raw space, valid JSON but < 0x21) -> NOT emitted (isSafePortalLink
	// rejects) but still captured RAW; the S4 display boundary is the sole sanitizer
	// (serverbot.SanitizeLoginURL), which would then reject it.
	buf.Reset()
	if unsafe := n.showPortalLink([]byte(`{"login_url":"https://hc/ x"}`)); unsafe != "https://hc/ x" {
		t.Fatalf("unsafe link must still be captured RAW, got %q", unsafe)
	}
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("unsafe link must NOT be emitted, got: %q", buf.String())
	}

	// malformed body -> capture "" + emit nothing.
	buf.Reset()
	if link := n.showPortalLink([]byte(`not json`)); link != "" {
		t.Fatalf("malformed body must capture empty, got %q", link)
	}
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("malformed body must show nothing, got: %q", buf.String())
	}
}
