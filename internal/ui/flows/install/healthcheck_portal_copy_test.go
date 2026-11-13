package install

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// The portal box as it was before the portal sign-in page existed (issue 347): a magic-link, and the
// sign-in page once a password is set. These renders must not change.
const (
	boxMagicLink = `╭──────────────────────────────────────────────────────╮
│ Your monitoring portal (single-use link, valid ~1h): │
│ https://hc.proxsave.dev/l/abc                        │
╰──────────────────────────────────────────────────────╯
Open it to set a password and configure alert channels.`
	boxPasswordFallback = `╭─────────────────────────────────────────╮
│ Your monitoring portal:                 │
│ https://hc.proxsave.dev/accounts/login/ │
│ Login: 1@proxsave.dev                   │
╰─────────────────────────────────────────╯
Sign in with the password you set.`
)

func TestPortalBoxOfTodayIsUnchanged(t *testing.T) {
	magic := ansi.Strip(buildHealthcheckPrompt(false, "https://hc.proxsave.dev/l/abc", "", "", "WORKING", "ok",
		orchestrator.HealthcheckSetupLevelOk, nil, nil, portalLinkCopy{}))
	if !strings.Contains(magic, boxMagicLink) {
		t.Fatalf("magic-link box changed:\n%s", magic)
	}
	fallback := ansi.Strip(buildHealthcheckPrompt(false, "", "https://hc.proxsave.dev/accounts/login/", "1@proxsave.dev",
		"WORKING", "ok", orchestrator.HealthcheckSetupLevelOk, nil, nil, portalLinkCopy{}))
	if !strings.Contains(fallback, boxPasswordFallback) {
		t.Fatalf("password fallback box changed:\n%s", fallback)
	}
}

// The operator owns the project (issue 347): the sign-in page, with the approved sign-in line.
func TestPortalBoxForTheSignInPage(t *testing.T) {
	page := "https://hc.proxsave.dev/accounts/login/"
	box := `╭─────────────────────────────────────────╮
│ Your monitoring portal:                 │
│ https://hc.proxsave.dev/accounts/login/ │
╰─────────────────────────────────────────╯
`
	for passwordSet, line := range map[bool]string{
		false: `Sign in: your email, then "Email Me a Link"`,
		true:  "Sign in: your email and password",
	} {
		v := ansi.Strip(buildHealthcheckPrompt(false, page, "", "", "WORKING", "ok", orchestrator.HealthcheckSetupLevelOk,
			nil, nil, portalLinkCopy{portalPage: true, passwordSet: passwordSet}))
		if !strings.Contains(v, box+line+"\n") || strings.Contains(v, "single-use") {
			t.Errorf("password=%t:\n%s", passwordSet, v)
		}
	}
}
