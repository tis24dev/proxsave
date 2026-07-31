package serverbot

import "testing"

// TestTrustedPortalFallback pins the shared admission rule for the portal fallback,
// the one both producers now go through (ClassifyHealthcheckSetupResult for the setup
// screens, storePortalFallback for the run epilogue). The tri-state password flag is
// the point: only an EXPLICIT true admits anything, because a nil flag means the
// server could not tell us and an operator who never set a password must not be sent
// to a sign-in page. The two halves are then gated independently.
func TestTrustedPortalFallback(t *testing.T) {
	const host = "https://bot.proxsave.dev"
	const portal = "https://hc.proxsave.dev/projects/"
	const login = "ops@example.com"

	yes, no := true, false

	tests := []struct {
		name        string
		portalURL   string
		portalLogin string
		passwordSet *bool
		wantURL     string
		wantLogin   string
	}{
		{
			name:      "confirmed password admits both halves",
			portalURL: portal, portalLogin: login, passwordSet: &yes,
			wantURL: portal, wantLogin: login,
		},
		{
			name:      "an unconfirmed flag admits nothing",
			portalURL: portal, portalLogin: login, passwordSet: nil,
		},
		{
			name:      "an explicit false admits nothing",
			portalURL: portal, portalLogin: login, passwordSet: &no,
		},
		{
			name:      "a foreign sign-in page is dropped, the identity survives",
			portalURL: "https://evil.example.com/projects/", portalLogin: login, passwordSet: &yes,
			wantLogin: login,
		},
		{
			name:      "an unsanitizable identity is dropped, the address survives",
			portalURL: portal, portalLogin: "ops@example.com\x1b[31m", passwordSet: &yes,
			wantURL: portal,
		},
		{
			name:        "empty inputs stay empty even when confirmed",
			passwordSet: &yes,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotLogin := TrustedPortalFallback(tt.portalURL, tt.portalLogin, tt.passwordSet, host)
			if gotURL != tt.wantURL {
				t.Errorf("url = %q, want %q", gotURL, tt.wantURL)
			}
			if gotLogin != tt.wantLogin {
				t.Errorf("login = %q, want %q", gotLogin, tt.wantLogin)
			}
		})
	}
}

// TestTrustedPortalFallbackMatchesItsParts pins the helper as EXACTLY the composition
// of the two gates it replaced, so extracting it cannot have loosened either one.
func TestTrustedPortalFallbackMatchesItsParts(t *testing.T) {
	const host = "https://bot.proxsave.dev"
	confirmed := true

	for _, raw := range []struct{ url, login string }{
		{"https://hc.proxsave.dev/projects/", "ops@example.com"},
		{"https://evil.example.com/", "ops@example.com"},
		{"https://hc.proxsave.dev/projects/", "ops@example.com\n"},
		{"not a url", "not-an-url"},
		{"", ""},
	} {
		gotURL, gotLogin := TrustedPortalFallback(raw.url, raw.login, &confirmed, host)
		if want := TrustedLoginURL(raw.url, host); gotURL != want {
			t.Errorf("url for %q = %q, want TrustedLoginURL result %q", raw.url, gotURL, want)
		}
		if want := SanitizePortalLogin(raw.login); gotLogin != want {
			t.Errorf("login for %q = %q, want SanitizePortalLogin result %q", raw.login, gotLogin, want)
		}
	}
}
