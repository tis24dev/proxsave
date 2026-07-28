package orchestrator

import (
	"bytes"
	"context"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
)

const (
	hcPortalURL   = "https://hc.proxsave.dev/accounts/login/"
	hcPortalLogin = "1414274709917575@proxsave.dev"
)

func boolPtr(v bool) *bool { return &v }

// TestHealthchecksSectionPortalFallback pins WHEN the section is allowed to carry the
// portal address + sign-in identity to the epilogue. The rule is narrow on purpose: only
// an explicit password_set=true from the server. An absent flag means the server could not
// confirm the state, and a false one means the operator has no password yet, so in both
// cases telling them to "sign in with the password you set" would send them to a page they
// cannot get past.
func TestHealthchecksSectionPortalFallback(t *testing.T) {
	cases := []struct {
		name        string
		passwordSet *bool
		wantCarried bool
	}{
		{"password confirmed set", boolPtr(true), true},
		{"password explicitly not set", boolPtr(false), false},
		{"server could not confirm", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			ch := hcTransmittingChannel(&config.Config{
				HealthcheckEnabled: true, HealthcheckMode: "centralized",
			}, &buf)
			ch.mintPortal = func(context.Context, string, string, string) (health.CentralizedConfig, error) {
				return health.CentralizedConfig{
					PortalURL:   hcPortalURL,
					PortalLogin: hcPortalLogin,
					PasswordSet: tc.passwordSet,
				}, nil
			}

			stats := &BackupStats{}
			if err := ch.Notify(context.Background(), stats); err != nil {
				t.Fatalf("Notify err: %v", err)
			}
			got := stats.HealthcheckPortalURL != "" || stats.HealthcheckPortalLogin != ""
			if got != tc.wantCarried {
				t.Fatalf("carried=%t want %t (url=%q login=%q)",
					got, tc.wantCarried, stats.HealthcheckPortalURL, stats.HealthcheckPortalLogin)
			}
			if tc.wantCarried {
				if stats.HealthcheckPortalURL != hcPortalURL {
					t.Fatalf("portal url=%q want %q", stats.HealthcheckPortalURL, hcPortalURL)
				}
				if stats.HealthcheckPortalLogin != hcPortalLogin {
					t.Fatalf("portal login=%q want %q", stats.HealthcheckPortalLogin, hcPortalLogin)
				}
			}
		})
	}
}

// TestHealthchecksSectionPortalFallbackGates pins that the fallback goes through the SAME
// trust gates as the magic-link: a sign-in page on a foreign registrable domain is a
// phishing target and must be dropped, and an identity carrying an escape sequence must be
// dropped whole. Each half fails independently so one bad value cannot hide the other.
func TestHealthchecksSectionPortalFallbackGates(t *testing.T) {
	t.Run("foreign portal domain is dropped", func(t *testing.T) {
		var buf bytes.Buffer
		ch := hcTransmittingChannel(&config.Config{
			HealthcheckEnabled: true, HealthcheckMode: "centralized",
		}, &buf)
		ch.mintPortal = func(context.Context, string, string, string) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{
				PortalURL:   "https://hc.evil.example/accounts/login/",
				PortalLogin: hcPortalLogin,
				PasswordSet: boolPtr(true),
			}, nil
		}

		stats := &BackupStats{}
		_ = ch.Notify(context.Background(), stats)
		if stats.HealthcheckPortalURL != "" {
			t.Fatalf("a foreign sign-in page must never reach root, got %q", stats.HealthcheckPortalURL)
		}
		if stats.HealthcheckPortalLogin != hcPortalLogin {
			t.Fatalf("the identity must survive a rejected url, got %q", stats.HealthcheckPortalLogin)
		}
	})

	t.Run("hostile identity is dropped", func(t *testing.T) {
		var buf bytes.Buffer
		ch := hcTransmittingChannel(&config.Config{
			HealthcheckEnabled: true, HealthcheckMode: "centralized",
		}, &buf)
		ch.mintPortal = func(context.Context, string, string, string) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{
				PortalURL:   hcPortalURL,
				PortalLogin: "ops@example.com\x1b[2K",
				PasswordSet: boolPtr(true),
			}, nil
		}

		stats := &BackupStats{}
		_ = ch.Notify(context.Background(), stats)
		if stats.HealthcheckPortalLogin != "" {
			t.Fatalf("a hostile identity must be dropped, got %q", stats.HealthcheckPortalLogin)
		}
		if stats.HealthcheckPortalURL != hcPortalURL {
			t.Fatalf("the address must survive a rejected identity, got %q", stats.HealthcheckPortalURL)
		}
	})
}

// TestHealthchecksSectionCapturedLinkSkipsFetch pins that a link the relay already
// captured this run short-circuits the network call entirely, so the fallback is never
// even asked for while a usable magic-link exists.
func TestHealthchecksSectionCapturedLinkSkipsFetch(t *testing.T) {
	var buf bytes.Buffer
	ch := hcTransmittingChannel(&config.Config{
		HealthcheckEnabled: true, HealthcheckMode: "centralized",
	}, &buf)
	ch.mintPortal = func(context.Context, string, string, string) (health.CentralizedConfig, error) {
		t.Fatal("a captured link must not trigger a fetch")
		return health.CentralizedConfig{}, nil
	}

	captured := "https://hc.proxsave.dev/l/abc123"
	stats := &BackupStats{HealthcheckLink: captured}
	if err := ch.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify err: %v", err)
	}
	if stats.HealthcheckLink != captured {
		t.Fatalf("link=%q want the captured one", stats.HealthcheckLink)
	}
	if stats.HealthcheckPortalURL != "" || stats.HealthcheckPortalLogin != "" {
		t.Fatalf("no fallback must be recorded, url=%q login=%q",
			stats.HealthcheckPortalURL, stats.HealthcheckPortalLogin)
	}
}

// TestClassifyHealthcheckSetupPortalFallback pins the same password_set rule on the
// install/dashboard screen path, which reaches the front-ends through a different struct.
func TestClassifyHealthcheckSetupPortalFallback(t *testing.T) {
	t.Run("carried on a confirmed password", func(t *testing.T) {
		st := ClassifyHealthcheckSetupResult(HealthcheckCheckResult{
			PortalURL:   hcPortalURL,
			PortalLogin: hcPortalLogin,
			PasswordSet: boolPtr(true),
			Reachable:   true,
		})
		if st.PortalURL != hcPortalURL || st.PortalLogin != hcPortalLogin {
			t.Fatalf("want the portal fallback, url=%q login=%q", st.PortalURL, st.PortalLogin)
		}
	})

	for _, tc := range []struct {
		name        string
		passwordSet *bool
	}{
		{"not set", boolPtr(false)},
		{"unconfirmed", nil},
	} {
		t.Run("suppressed when "+tc.name, func(t *testing.T) {
			st := ClassifyHealthcheckSetupResult(HealthcheckCheckResult{
				PortalURL:   hcPortalURL,
				PortalLogin: hcPortalLogin,
				PasswordSet: tc.passwordSet,
				Reachable:   true,
			})
			if st.PortalURL != "" || st.PortalLogin != "" {
				t.Fatalf("nothing must be carried, url=%q login=%q", st.PortalURL, st.PortalLogin)
			}
		})
	}

	t.Run("self mode never carries a portal", func(t *testing.T) {
		st := ClassifyHealthcheckSelfResult(HealthcheckCheckResult{
			PortalURL:   hcPortalURL,
			PortalLogin: hcPortalLogin,
			PasswordSet: boolPtr(true),
			Reachable:   true,
		})
		if st.PortalURL != "" || st.PortalLogin != "" {
			t.Fatalf("self mode has no central portal, url=%q login=%q", st.PortalURL, st.PortalLogin)
		}
	})
}
