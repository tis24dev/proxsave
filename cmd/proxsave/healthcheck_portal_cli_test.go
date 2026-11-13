package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// The --install --cli portal block as it was before the portal sign-in page existed (issue 347).
func TestPrintHealthcheckPortalOfTodayIsUnchanged(t *testing.T) {
	cases := []struct {
		name string
		st   orchestrator.HealthcheckSetupState
		want string
	}{
		{"magic-link", orchestrator.HealthcheckSetupState{LoginURL: "https://hc.proxsave.dev/l/abc"},
			"\nYour monitoring portal (single-use link, valid ~1h):\n  https://hc.proxsave.dev/l/abc\n" +
				"Open it to set a password and configure alert channels (email, etc.).\n\n"},
		{"password fallback", orchestrator.HealthcheckSetupState{PortalURL: "https://hc.proxsave.dev/accounts/login/",
			PortalLogin: "1@proxsave.dev"},
			"\nYour monitoring portal:\n  https://hc.proxsave.dev/accounts/login/\n" +
				"Sign in as 1@proxsave.dev with the password you set.\n\n"},
		{"nothing", orchestrator.HealthcheckSetupState{}, ""},
	}
	for _, c := range cases {
		if got := captureStdout(t, func() { printHealthcheckPortal(c.st) }); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", c.name, got, c.want)
		}
	}
}

// The operator owns the project (issue 347): the sign-in page, with the approved sign-in line.
func TestPrintHealthcheckPortalForTheSignInPage(t *testing.T) {
	page := "https://hc.proxsave.dev/accounts/login/"
	for passwordSet, line := range map[bool]string{
		false: `Sign in with your email: use "Email Me a Link" on the sign-in page.`,
		true:  "Sign in with your email and the password you set.",
	} {
		st := orchestrator.HealthcheckSetupState{LoginURL: page, LoginIsPortalPage: true, LoginPasswordSet: passwordSet}
		want := "\nYour monitoring portal:\n  " + page + "\n" + line + "\n\n"
		if got := captureStdout(t, func() { printHealthcheckPortal(st) }); got != want {
			t.Errorf("password=%t:\ngot  %q\nwant %q", passwordSet, got, want)
		}
	}
}
