package orchestrator

import (
	"strings"
	"testing"
)

// TestClassifyTelegramSetupSkip pins DISTINCT copy per Telegram skip verdict, fixing the
// dashboard TWIN bug where every non-eligible state collapsed to one generic line.
func TestClassifyTelegramSetupSkip(t *testing.T) {
	cases := []struct {
		name        string
		res         TelegramSetupBootstrap
		wantKeyword string
		wantLevel   HealthcheckSetupLevel
		wantSubstr  string
	}{
		{"disabled", TelegramSetupBootstrap{Eligibility: TelegramSetupSkipDisabled}, "NOT ENABLED", HealthcheckSetupLevelWarn, "not enabled"},
		{"config-error", TelegramSetupBootstrap{Eligibility: TelegramSetupSkipConfigError, ConfigError: "boom"}, "CONFIG ERROR", HealthcheckSetupLevelError, "configuration could not be loaded"},
		{"personal-mode", TelegramSetupBootstrap{Eligibility: TelegramSetupSkipPersonalMode}, "PERSONAL MODE", HealthcheckSetupLevelWarn, "personal-bot mode"},
		{"identity-error", TelegramSetupBootstrap{Eligibility: TelegramSetupSkipIdentityUnavailable, IdentityDetectError: "read failed"}, "IDENTITY ERROR", HealthcheckSetupLevelWarn, "could not be read"},
		{"no-identity", TelegramSetupBootstrap{Eligibility: TelegramSetupSkipIdentityUnavailable}, "NO IDENTITY", HealthcheckSetupLevelWarn, "No server identity"},
		{"unknown-default", TelegramSetupBootstrap{}, "NOT CONFIGURED", HealthcheckSetupLevelWarn, "not configured"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			st := ClassifyTelegramSetupSkip(tt.res)
			if st.Keyword != tt.wantKeyword {
				t.Fatalf("Keyword=%q, want %q", st.Keyword, tt.wantKeyword)
			}
			if st.Level != tt.wantLevel {
				t.Fatalf("Level=%d, want %d", st.Level, tt.wantLevel)
			}
			if !strings.Contains(st.Message, tt.wantSubstr) {
				t.Fatalf("Message=%q, want substring %q", st.Message, tt.wantSubstr)
			}
		})
	}
}

// TestClassifyTelegramSetupSkip_Distinct locks that the two identity causes (detect error
// vs no ServerID) and the mode/config verdicts never render identically.
func TestClassifyTelegramSetupSkip_Distinct(t *testing.T) {
	states := []TelegramSetupBootstrap{
		{Eligibility: TelegramSetupSkipDisabled},
		{Eligibility: TelegramSetupSkipConfigError},
		{Eligibility: TelegramSetupSkipPersonalMode},
		{Eligibility: TelegramSetupSkipIdentityUnavailable, IdentityDetectError: "x"},
		{Eligibility: TelegramSetupSkipIdentityUnavailable},
	}
	seen := map[string]bool{}
	for _, s := range states {
		m := ClassifyTelegramSetupSkip(s).Message
		if seen[m] {
			t.Fatalf("duplicate skip message: %q", m)
		}
		seen[m] = true
	}
}
