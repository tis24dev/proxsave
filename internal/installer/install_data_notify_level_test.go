package installer

import (
	"strings"
	"testing"
)

// The wizard's Notify level lands in NOTIFY_ON. Without a monitor (Healthchecks off, or the cron
// engine) any level below always has no effect, so the file gets always (maintainer decision B).
func TestApplyInstallDataNotifyLevel(t *testing.T) {
	tests := []struct {
		name      string
		scheduler string
		hcMode    string
		level     string
		want      string
	}{
		{"centralized failure", "daemon", "centralized", "failure", "failure"},
		{"centralized warning", "daemon", "centralized", "warning", "warning"},
		{"self always", "daemon", "self", "always", "always"},
		{"healthchecks off forces always", "daemon", "off", "failure", "always"},
		{"cron forces always", "cron", "centralized", "warning", "always"},
		{"no answer keeps the template value", "daemon", "centralized", "", "warning"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := &InstallWizardData{BaseDir: "/data", SchedulerMode: tc.scheduler, HealthcheckMode: tc.hcMode, NotifyOn: tc.level}
			result, err := ApplyInstallData("", data)
			if err != nil {
				t.Fatalf("ApplyInstallData: %v", err)
			}
			if got := ParseEnvTemplate(result)["NOTIFY_ON"]; got != tc.want {
				t.Fatalf("NOTIFY_ON = %q, want %q", got, tc.want)
			}
			if n := strings.Count(result, "\nNOTIFY_ON="); n != 1 {
				t.Fatalf("NOTIFY_ON assigned %d times, want once", n)
			}
		})
	}
}

// An Edit prefills the stored level; an absent or invalid one leaves the prefill empty, and the
// wizards then default to warning.
func TestDeriveInstallWizardPrefillNotifyLevel(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"NOTIFY_ON=failure\n", "failure"},
		{"NOTIFY_ON=always\n", "always"},
		{"NOTIFY_ON=WARNINGS\n", "warning"},
		{"NOTIFY_ON=warnig\n", ""},
		{"BACKUP_ENABLED=true\n", ""},
	} {
		if got := DeriveInstallWizardPrefill(tc.body).NotifyOn; got != tc.want {
			t.Errorf("%q: prefill NotifyOn = %q, want %q", tc.body, got, tc.want)
		}
	}
}
