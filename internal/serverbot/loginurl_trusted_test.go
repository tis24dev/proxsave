package serverbot

import "testing"

// F11-05: the portal magic-link must live on the same registrable domain as the
// bot-server, else a compromised/MITM /api/notify response could surface a foreign
// phishing host to root.
func TestTrustedLoginURL(t *testing.T) {
	const server = "https://bot.proxsave.dev"

	// Same registrable domain (bot.proxsave.dev vs hc.proxsave.dev -> proxsave.dev): accept.
	okURL := "https://hc.proxsave.dev/accounts/check_token/u/ABC123/"
	if got := TrustedLoginURL(okURL, server); got != okURL {
		t.Errorf("same-domain link rejected: %q -> %q", okURL, got)
	}

	// Foreign host: reject.
	if got := TrustedLoginURL("https://phishing.evil/accounts/check_token/u/ABC/", server); got != "" {
		t.Errorf("foreign host accepted: got %q", got)
	}

	// A look-alike suffix that is NOT the same registrable domain: reject.
	if got := TrustedLoginURL("https://hc.proxsave.dev.evil.com/x", server); got != "" {
		t.Errorf("suffix-spoof host accepted: got %q", got)
	}

	// SanitizeLoginURL layer still applies (ANSI escape on the RIGHT domain -> ""): reject.
	ansi := "https://hc.proxsave.dev/" + string(rune(0x1b)) + "[2J"
	if got := TrustedLoginURL(ansi, server); got != "" {
		t.Errorf("unsanitized link accepted: got %q", got)
	}

	// Fail-closed inputs.
	if got := TrustedLoginURL("", server); got != "" {
		t.Errorf("empty raw: got %q", got)
	}
	if got := TrustedLoginURL(okURL, "\x00"); got != "" {
		// A serverAPIHost whose host cannot be derived must never accidentally match.
		t.Errorf("undecodable server host accepted: got %q", got)
	}
}
