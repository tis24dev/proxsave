// Package main contains the proxsave command entrypoint.
package main

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// TestLogMonitoringPortalFallback pins the second display state of the epilogue portal
// block: once the operator has their own portal password the server stops minting the
// magic-link and sends the plain sign-in page plus the identity instead, and the epilogue
// must show THAT rather than falling silent. The magic-link keeps precedence when both
// are present, because one click beats an address plus a password prompt.
func TestLogMonitoringPortalFallback(t *testing.T) {
	const (
		portalWording = "Healthchecks Portal:"
		loginWording  = "Healthchecks Login:"
		portal        = "https://hc.proxsave.dev/accounts/login/"
		login         = "1414274709917575@proxsave.dev"
	)

	t.Run("portal and identity are shown when no link was minted", func(t *testing.T) {
		out := captureDefaultInfo(t, func() {
			logMonitoringPortalLink(&orchestrator.BackupStats{
				HealthcheckPortalURL:   portal,
				HealthcheckPortalLogin: login,
			})
		})
		if !strings.Contains(out, portalWording) || !strings.Contains(out, portal) {
			t.Fatalf("want the portal address line, out=%q", out)
		}
		if !strings.Contains(out, loginWording) || !strings.Contains(out, login) {
			t.Fatalf("want the sign-in identity line, out=%q", out)
		}
	})

	t.Run("magic-link wins over the fallback", func(t *testing.T) {
		link := "https://hc.proxsave.dev/l/abc123"
		out := captureDefaultInfo(t, func() {
			logMonitoringPortalLink(&orchestrator.BackupStats{
				HealthcheckLink:        link,
				HealthcheckPortalURL:   portal,
				HealthcheckPortalLogin: login,
			})
		})
		if !strings.Contains(out, link) {
			t.Fatalf("want the magic-link, out=%q", out)
		}
		if strings.Contains(out, portal) || strings.Contains(out, loginWording) {
			t.Fatalf("the fallback must stay hidden while a link exists, out=%q", out)
		}
	})

	t.Run("address survives an unusable identity", func(t *testing.T) {
		out := captureDefaultInfo(t, func() {
			logMonitoringPortalLink(&orchestrator.BackupStats{
				HealthcheckPortalURL:   portal,
				HealthcheckPortalLogin: "ops@example.com\nHealthchecks Portal: https://evil/",
			})
		})
		if !strings.Contains(out, portal) {
			t.Fatalf("a bad identity must not suppress the address, out=%q", out)
		}
		if strings.Contains(out, loginWording) || strings.Contains(out, "evil") {
			t.Fatalf("the injected identity must be dropped whole, out=%q", out)
		}
	})

	hostile := []struct {
		name   string
		portal string
	}{
		{"raw space", "https://hc.proxsave.dev/accounts/login/ x"},
		{"javascript scheme", "javascript:alert(1)"},
		{"control char", "https://hc.proxsave.dev/\x07login/"},
	}
	for _, tc := range hostile {
		t.Run("hostile portal address stripped: "+tc.name, func(t *testing.T) {
			out := captureDefaultInfo(t, func() {
				logMonitoringPortalLink(&orchestrator.BackupStats{
					HealthcheckPortalURL:   tc.portal,
					HealthcheckPortalLogin: login,
				})
			})
			if strings.Contains(out, portalWording) || strings.Contains(out, loginWording) {
				t.Fatalf("a hostile address must print nothing at all, out=%q", out)
			}
		})
	}

	t.Run("nothing to show prints nothing", func(t *testing.T) {
		out := captureDefaultInfo(t, func() {
			logMonitoringPortalLink(&orchestrator.BackupStats{})
		})
		if strings.Contains(out, portalWording) || strings.Contains(out, loginWording) {
			t.Fatalf("an empty stats must print no portal block, out=%q", out)
		}
	})
}
