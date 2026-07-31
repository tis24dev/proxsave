package serverbot

import "testing"

// TestSanitizePortalLogin pins the sibling gate for the value that is NOT a URL: the
// identity an operator types into the portal once they have a password. It must accept
// an ordinary address, keep it byte-identical, and fail closed on anything that could
// spoof the console or is too long to be a real address. Unlike SanitizeLoginURL it must
// NOT require a scheme, and unlike TrustedLoginURL it must not apply a domain gate.
func TestSanitizePortalLogin(t *testing.T) {
	accepted := []struct {
		name string
		raw  string
		want string
	}{
		{"provisioned identity", "1414274709917575@proxsave.dev", "1414274709917575@proxsave.dev"},
		{"surrounding whitespace is trimmed", "  ops@example.com\n", "ops@example.com"},
		{"plus addressing survives", "ops+hc@example.com", "ops+hc@example.com"},
		{"no scheme required", "not-an-url", "not-an-url"},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizePortalLogin(tc.raw); got != tc.want {
				t.Fatalf("SanitizePortalLogin(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	rejected := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"inner space", "ops @example.com"},
		{"tab", "ops\t@example.com"},
		{"newline injection", "ops@example.com\nHealthchecks Portal: https://evil/"},
		{"control char", "ops@example.com\x07"},
		{"ansi escape", "\x1b[31mops@example.com"},
		{"non-ascii", "óps@example.com"},
		{"bidi override", "ops@example.com" + string(rune(0x202e))},
		{"over the address length cap", string(make([]byte, portalLoginMaxLen+1))},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizePortalLogin(tc.raw); got != "" {
				t.Fatalf("SanitizePortalLogin(%q) = %q, want \"\"", tc.raw, got)
			}
		})
	}

	t.Run("exactly at the cap is accepted", func(t *testing.T) {
		raw := ""
		for len(raw) < portalLoginMaxLen {
			raw += "a"
		}
		if got := SanitizePortalLogin(raw); got != raw {
			t.Fatalf("a %d-char identity must survive, got %q", portalLoginMaxLen, got)
		}
	})
}
