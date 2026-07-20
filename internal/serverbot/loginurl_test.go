package serverbot

import "testing"

// Absorbs the invariant cases from the two former sanitizers (notify.isSafePortalLink
// and orchestrator.sanitizeLoginURL) into the single canonical one.
func TestSanitizeLoginURL(t *testing.T) {
	for _, ok := range []string{
		"https://hc.proxsave.dev/accounts/check_token/u/ABC123/",
		"http://x/y",
	} {
		if got := SanitizeLoginURL(ok); got != ok {
			t.Errorf("clean URL rejected: %q -> %q", ok, got)
		}
	}

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"ftp scheme", "ftp://evil/x"},
		{"javascript scheme", "javascript:alert(1)"},
		{"file scheme", "file:///etc/passwd"},
		{"no scheme", "hc.proxsave.dev/x"},
		{"ansi escape", "https://x/" + string(rune(0x1b)) + "[2J"},
		{"del", "https://x/" + string(rune(0x7f))},
		{"c1 control embedded", "https://x/" + string(rune(0x85)) + "/tok"},
		{"bidi override", "https://x/" + string(rune(0x202e))},
		{"internal space", "https://x/ y"},
		{"empty", ""},
	} {
		if got := SanitizeLoginURL(tc.in); got != "" {
			t.Errorf("%s: expected reject, got %q", tc.name, got)
		}
	}

	// TrimSpace-inside: surrounding whitespace is trimmed, then accepted (the benign
	// nuance vs the old notify sanitizer, which rejected it). The anti-escape core is
	// unchanged: only leading/trailing unicode.IsSpace is removed.
	if got := SanitizeLoginURL("  https://x/y  "); got != "https://x/y" {
		t.Errorf("trim-then-accept: got %q, want https://x/y", got)
	}
}
