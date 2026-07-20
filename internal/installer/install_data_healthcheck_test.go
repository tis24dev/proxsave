package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestApplyInstallDataHealthcheckMode covers the mode switch in ApplyInstallData:
// off/centralized/self write the right ENABLED+MODE, cron forces off, and an empty
// mode with the daemon keeps the backward-compat centralized-on default.
func TestApplyInstallDataHealthcheckMode(t *testing.T) {
	tests := []struct {
		name        string
		scheduler   string
		hcMode      string
		wantEnabled string // "" -> not asserted
		wantMode    string // "" -> not asserted
	}{
		{"daemon off", "daemon", "off", "false", "off"},
		{"daemon centralized", "daemon", "centralized", "true", "centralized"},
		{"daemon self", "daemon", "self", "true", "self"},
		{"daemon empty backward-compat", "daemon", "", "true", "centralized"},
		{"cron forces off", "cron", "centralized", "false", "off"},
		{"cron self forces off", "cron", "self", "false", "off"},
		{"cron empty", "cron", "", "false", "off"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := &InstallWizardData{
				BaseDir:         "/data",
				SchedulerMode:   tc.scheduler,
				HealthcheckMode: tc.hcMode,
			}
			result, err := ApplyInstallData("", data)
			if err != nil {
				t.Fatalf("ApplyInstallData: %v", err)
			}
			raw := ParseEnvTemplate(result)
			if tc.wantEnabled != "" && raw["HEALTHCHECK_ENABLED"] != tc.wantEnabled {
				t.Errorf("HEALTHCHECK_ENABLED = %q, want %q\n%s", raw["HEALTHCHECK_ENABLED"], tc.wantEnabled, result)
			}
			if tc.wantMode != "" && raw["HEALTHCHECK_MODE"] != tc.wantMode {
				t.Errorf("HEALTHCHECK_MODE = %q, want %q\n%s", raw["HEALTHCHECK_MODE"], tc.wantMode, result)
			}
		})
	}
}

// TestApplyInstallDataSelfClearsCentralizedCache proves switching to self mode wipes
// a stale centralized alive/backup cache so no stale server URL is pinged before the
// self params screen writes the user's own URLs.
func TestApplyInstallDataSelfClearsCentralizedCache(t *testing.T) {
	baseTemplate := strings.Join([]string{
		"HEALTHCHECK_ENABLED=true",
		"HEALTHCHECK_MODE=centralized",
		"HEALTHCHECK_ALIVE_URL=https://server/ping/stale-a",
		"HEALTHCHECK_BACKUP_URL=https://server/ping/stale-b",
		"",
	}, "\n")
	data := &InstallWizardData{BaseDir: "/data", SchedulerMode: "daemon", HealthcheckMode: "self"}
	result, err := ApplyInstallData(baseTemplate, data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	raw := ParseEnvTemplate(result)
	if raw["HEALTHCHECK_MODE"] != "self" || raw["HEALTHCHECK_ENABLED"] != "true" {
		t.Fatalf("self switch not applied: %+v", raw)
	}
	if raw["HEALTHCHECK_ALIVE_URL"] != "" || raw["HEALTHCHECK_BACKUP_URL"] != "" {
		t.Fatalf("stale centralized cache not cleared: alive=%q backup=%q", raw["HEALTHCHECK_ALIVE_URL"], raw["HEALTHCHECK_BACKUP_URL"])
	}
}

// TestApplyInstallDataClearsSelfURLsOnModeSwitch proves an EDIT away from self mode
// (to centralized or off) wipes the leftover self ALIVE/BACKUP ping URLs, so the
// daemon never falls back to a stale self URL as the centralized cache.
func TestApplyInstallDataClearsSelfURLsOnModeSwitch(t *testing.T) {
	// A prior self config: full user ping URLs stored in ALIVE/BACKUP.
	baseTemplate := strings.Join([]string{
		"HEALTHCHECK_ENABLED=true",
		"HEALTHCHECK_MODE=self",
		"HEALTHCHECK_ALIVE_URL=https://old-self/ping/x",
		"HEALTHCHECK_BACKUP_URL=https://old-self/ping/y",
		"",
	}, "\n")

	tests := []struct {
		name        string
		scheduler   string
		hcMode      string
		wantEnabled string
		wantMode    string // "" -> not asserted
	}{
		{"self -> centralized clears self urls", "daemon", "centralized", "true", "centralized"},
		{"self -> off clears self urls", "daemon", "off", "false", "off"},
		{"self -> cron-disabled clears self urls", "cron", "centralized", "false", "off"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := &InstallWizardData{BaseDir: "/data", SchedulerMode: tc.scheduler, HealthcheckMode: tc.hcMode}
			result, err := ApplyInstallData(baseTemplate, data)
			if err != nil {
				t.Fatalf("ApplyInstallData: %v", err)
			}
			raw := ParseEnvTemplate(result)
			if raw["HEALTHCHECK_ENABLED"] != tc.wantEnabled {
				t.Errorf("HEALTHCHECK_ENABLED = %q, want %q", raw["HEALTHCHECK_ENABLED"], tc.wantEnabled)
			}
			if tc.wantMode != "" && raw["HEALTHCHECK_MODE"] != tc.wantMode {
				t.Errorf("HEALTHCHECK_MODE = %q, want %q", raw["HEALTHCHECK_MODE"], tc.wantMode)
			}
			if raw["HEALTHCHECK_ALIVE_URL"] != "" || raw["HEALTHCHECK_BACKUP_URL"] != "" {
				t.Fatalf("stale self urls not cleared: alive=%q backup=%q", raw["HEALTHCHECK_ALIVE_URL"], raw["HEALTHCHECK_BACKUP_URL"])
			}
		})
	}
}

// F10-08: a self->self re-run must PRESERVE the user's ALIVE/BACKUP ping URLs (they are
// only cleared on a genuine mode change), so an abort on the params screen never leaves
// the monitor with an empty ping target. Also, the off branch now writes MODE=off.
func TestApplyInstallDataSelfToSelfPreservesURLs(t *testing.T) {
	baseTemplate := strings.Join([]string{
		"HEALTHCHECK_ENABLED=true",
		"HEALTHCHECK_MODE=self",
		"HEALTHCHECK_ALIVE_URL=https://hc-ping.com/self-a",
		"HEALTHCHECK_BACKUP_URL=https://hc-ping.com/self-b",
		"",
	}, "\n")
	data := &InstallWizardData{BaseDir: "/data", SchedulerMode: "daemon", HealthcheckMode: "self"}
	result, err := ApplyInstallData(baseTemplate, data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	raw := ParseEnvTemplate(result)
	if raw["HEALTHCHECK_ALIVE_URL"] != "https://hc-ping.com/self-a" || raw["HEALTHCHECK_BACKUP_URL"] != "https://hc-ping.com/self-b" {
		t.Fatalf("self->self must preserve URLs: alive=%q backup=%q", raw["HEALTHCHECK_ALIVE_URL"], raw["HEALTHCHECK_BACKUP_URL"])
	}
	if raw["HEALTHCHECK_MODE"] != "self" {
		t.Fatalf("mode must stay self, got %q", raw["HEALTHCHECK_MODE"])
	}
}

// TestApplyHealthcheckSelfParams writes the full ping URLs; empty optionals stay blank.
func TestApplyHealthcheckSelfParams(t *testing.T) {
	tmpl := config.DefaultEnvTemplate()
	p := HealthcheckSelfParams{
		AliveURL:       "https://hc-ping.com/alive-uuid",
		BackupURL:      "https://hc-ping.com/backup-uuid",
		UpdatesURL:     "https://hc-ping.com/updates-uuid",
		NotifyEmailURL: "https://hc-ping.com/email-uuid",
		// telegram/gotify/webhook intentionally left empty
	}
	result := ApplyHealthcheckSelfParams(tmpl, p)
	raw := ParseEnvTemplate(result)
	if raw["HEALTHCHECK_ALIVE_URL"] != p.AliveURL {
		t.Errorf("alive url = %q, want %q", raw["HEALTHCHECK_ALIVE_URL"], p.AliveURL)
	}
	if raw["HEALTHCHECK_BACKUP_URL"] != p.BackupURL {
		t.Errorf("backup url = %q, want %q", raw["HEALTHCHECK_BACKUP_URL"], p.BackupURL)
	}
	if raw["HEALTHCHECK_UPDATES_URL"] != p.UpdatesURL {
		t.Errorf("updates url = %q, want %q", raw["HEALTHCHECK_UPDATES_URL"], p.UpdatesURL)
	}
	if raw["HEALTHCHECK_NOTIFY_EMAIL_URL"] != p.NotifyEmailURL {
		t.Errorf("email notify url = %q, want %q", raw["HEALTHCHECK_NOTIFY_EMAIL_URL"], p.NotifyEmailURL)
	}
	for _, k := range []string{"HEALTHCHECK_NOTIFY_TELEGRAM_URL", "HEALTHCHECK_NOTIFY_GOTIFY_URL", "HEALTHCHECK_NOTIFY_WEBHOOK_URL"} {
		if raw[k] != "" {
			t.Errorf("%s = %q, want blank (empty optional)", k, raw[k])
		}
	}
}

// TestDeriveHealthcheckSelfParamsRoundTrip proves Apply then Derive returns the same values.
func TestDeriveHealthcheckSelfParamsRoundTrip(t *testing.T) {
	p := HealthcheckSelfParams{
		AliveURL:          "https://hc-ping.com/a",
		BackupURL:         "https://hc-ping.com/b",
		UpdatesURL:        "https://hc-ping.com/u",
		NotifyEmailURL:    "https://hc-ping.com/e",
		NotifyTelegramURL: "https://hc-ping.com/t",
		NotifyGotifyURL:   "https://hc-ping.com/g",
		NotifyWebhookURL:  "https://hc-ping.com/w",
	}
	result := ApplyHealthcheckSelfParams(config.DefaultEnvTemplate(), p)
	got := DeriveHealthcheckSelfParams(result)
	if got != p {
		t.Fatalf("round-trip mismatch:\ngot  %+v\nwant %+v", got, p)
	}
}

// TestSelfParamsMergedConfigParsesFullURLBranch confirms the merged config parses
// back and the daemon's selfURLs() full-URL branch (cfg.HealthcheckAliveURL /
// HealthcheckBackupURL non-empty -> returned verbatim) resolves the pasted URLs.
func TestSelfParamsMergedConfigParsesFullURLBranch(t *testing.T) {
	data := &InstallWizardData{BaseDir: "/data", SchedulerMode: "daemon", HealthcheckMode: "self"}
	tmpl, err := ApplyInstallData("", data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	p := HealthcheckSelfParams{
		AliveURL:   "https://hc-ping.com/alive-uuid",
		BackupURL:  "https://hc-ping.com/backup-uuid",
		UpdatesURL: "https://hc-ping.com/updates-uuid",
	}
	tmpl = ApplyHealthcheckSelfParams(tmpl, p)

	dir := t.TempDir()
	path := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(path, []byte(tmpl), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadConfigWithBaseDir(path, dir)
	if err != nil {
		t.Fatalf("LoadConfigWithBaseDir: %v", err)
	}
	if !cfg.HealthcheckEnabled || cfg.HealthcheckMode != "self" {
		t.Fatalf("parsed config not self-enabled: enabled=%v mode=%q", cfg.HealthcheckEnabled, cfg.HealthcheckMode)
	}
	// selfURLs() line 728-729 returns HealthcheckAliveURL/BackupURL verbatim when either is non-empty.
	if cfg.HealthcheckAliveURL != p.AliveURL || cfg.HealthcheckBackupURL != p.BackupURL {
		t.Fatalf("full-url branch not resolved: alive=%q backup=%q", cfg.HealthcheckAliveURL, cfg.HealthcheckBackupURL)
	}
	if cfg.HealthcheckUpdatesURL != p.UpdatesURL {
		t.Fatalf("updates full url not parsed: %q", cfg.HealthcheckUpdatesURL)
	}
}

// TestDeriveInstallWizardPrefillHealthcheckMode covers prefill derivation of the mode.
func TestDeriveInstallWizardPrefillHealthcheckMode(t *testing.T) {
	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{"disabled -> off", "HEALTHCHECK_ENABLED=false\nHEALTHCHECK_MODE=centralized\n", "off"},
		{"enabled centralized", "HEALTHCHECK_ENABLED=true\nHEALTHCHECK_MODE=centralized\n", "centralized"},
		{"enabled self", "HEALTHCHECK_ENABLED=true\nHEALTHCHECK_MODE=self\n", "self"},
		{"enabled no mode -> centralized", "HEALTHCHECK_ENABLED=true\n", "centralized"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveInstallWizardPrefill(tc.tmpl)
			if got.HealthcheckMode != tc.want {
				t.Fatalf("HealthcheckMode = %q, want %q", got.HealthcheckMode, tc.want)
			}
		})
	}
}
