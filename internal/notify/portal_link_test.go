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
		"hc.proxsave.dev/x", // no scheme
		"https://x/\x1b[2J", // ANSI
		"https://x/\n",      // newline
		"https://x/\t",      // tab
		"https://x/\x7f",    // DEL
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

func TestShowPortalLink(t *testing.T) {
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

	// present + safe -> shown
	n.showPortalLink(strings.NewReader(`{"status":"accepted","login_url":"https://hc/accounts/check_token/u/MAGIC/"}`))
	if !strings.Contains(buf.String(), "https://hc/accounts/check_token/u/MAGIC/") {
		t.Fatalf("must show the magic-link, got: %q", buf.String())
	}

	// absent (user already logged in -> server omits it) -> nothing
	buf.Reset()
	n.showPortalLink(strings.NewReader(`{"status":"accepted"}`))
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("no login_url must show nothing, got: %q", buf.String())
	}

	// hostile ANSI in the link -> dropped
	buf.Reset()
	n.showPortalLink(strings.NewReader("{\"login_url\":\"https://hc/\x1b[2Jx\"}"))
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("ANSI link must be dropped, got: %q", buf.String())
	}

	// malformed body -> nothing
	buf.Reset()
	n.showPortalLink(strings.NewReader(`not json`))
	if strings.Contains(buf.String(), "portal") {
		t.Fatalf("malformed body must show nothing, got: %q", buf.String())
	}
}
