package installer

import (
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

func TestValidateHealthcheckInitialEmail(t *testing.T) {
	for v, want := range map[string]string{
		"ops@example.org":      "",
		" ops@example.org ":    "",
		"":                     "Required with ProxSave HC Server",
		"   ":                  "Required with ProxSave HC Server",
		"ops":                  "Not a usable email address",
		"@example.org":         "Not a usable email address",
		"a@b@example.org":      "Not a usable email address",
		"a@localhost":          "Not a usable email address",
		"a@.example.org":       "Not a usable email address",
		"a@example.org.":       "Not a usable email address",
		"a@example..org":       "Not a usable email address",
		"a b@example.org":      "Not a usable email address",
		"à@example.org":        "Not a usable email address",
		"ops$TEAM@example.org": "Not a usable email address",
		"ops@exa$mple.org":     "Not a usable email address",
	} {
		err := ValidateHealthcheckInitialEmail(v)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", v, got, want)
		}
	}
}

// Written with the centralized mode only; every other mode keeps the stored value (maintainer, 2026-11-13).
func TestApplyInstallDataWritesTheInitialEmailOnlyWhenCentralized(t *testing.T) {
	base := SetEnvValueInTemplate(config.DefaultEnvTemplate(), "HEALTHCHECK_ACCOUNT_INITIAL_EMAIL", "kept@example.org")
	for mode, want := range map[string]string{"centralized": "new@example.org", "self": "kept@example.org", "off": "kept@example.org"} {
		out, err := ApplyInstallData(base, &InstallWizardData{SchedulerMode: "daemon", HealthcheckMode: mode,
			HealthcheckAccountInitialEmail: "new@example.org"})
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if got := DeriveInstallWizardPrefill(out).HealthcheckAccountInitialEmail; got != want {
			t.Errorf("%s: stored %q, want %q", mode, got, want)
		}
		if !strings.Contains(out, "HEALTHCHECK_ACCOUNT_INITIAL_EMAIL="+want) {
			t.Errorf("%s: line missing", mode)
		}
	}
}
