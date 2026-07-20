package orchestrator

import (
	"strings"
	"testing"
)

// TestClassifyHealthcheckSetupSkip pins DISTINCT user-facing copy for every skip verdict,
// so the dashboard/CLI no longer collapse them into one generic line. The centralized
// missing-secret state is A-aware.
func TestClassifyHealthcheckSetupSkip(t *testing.T) {
	cases := []struct {
		name        string
		res         HealthcheckSetupBootstrap
		wantKeyword string
		wantLevel   HealthcheckSetupLevel
		wantSubstr  string
	}{
		{"disabled", HealthcheckSetupBootstrap{Eligibility: HealthcheckSetupSkipDisabled}, "NOT ENABLED", HealthcheckSetupLevelWarn, "not enabled"},
		{"config-error", HealthcheckSetupBootstrap{Eligibility: HealthcheckSetupSkipConfigError, ConfigError: "boom"}, "CONFIG ERROR", HealthcheckSetupLevelError, "configuration could not be loaded"},
		{"self-no-url", HealthcheckSetupBootstrap{Eligibility: HealthcheckSetupSkipSelfMode}, "NOT CONFIGURED", HealthcheckSetupLevelWarn, "ping URL"},
		{"no-identity", HealthcheckSetupBootstrap{Eligibility: HealthcheckSetupSkipIdentityUnavailable, ServerID: ""}, "NO IDENTITY", HealthcheckSetupLevelWarn, "No server identity"},
		{"provisioning-missing-secret", HealthcheckSetupBootstrap{Eligibility: HealthcheckSetupSkipIdentityUnavailable, ServerID: "abcd1234", HasSecret: false}, "PROVISIONING", HealthcheckSetupLevelWarn, "provisioned automatically"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			st := ClassifyHealthcheckSetupSkip(tt.res)
			if st.Keyword != tt.wantKeyword {
				t.Fatalf("Keyword=%q, want %q", st.Keyword, tt.wantKeyword)
			}
			if st.Level != tt.wantLevel {
				t.Fatalf("Level=%d, want %d", st.Level, tt.wantLevel)
			}
			if !strings.Contains(st.Message, tt.wantSubstr) {
				t.Fatalf("Message=%q, want substring %q", st.Message, tt.wantSubstr)
			}
			if st.Message == "" {
				t.Fatal("Message must not be empty")
			}
		})
	}
}

// TestClassifyHealthcheckSetupSkip_MissingSecretIsAAware guards the Option-A copy: the
// centralized missing-secret state must NEVER tell the user to pair Telegram, and must
// promise automatic provisioning plus the "monitor unreachable if it persists" hint.
func TestClassifyHealthcheckSetupSkip_MissingSecretIsAAware(t *testing.T) {
	st := ClassifyHealthcheckSetupSkip(HealthcheckSetupBootstrap{
		Eligibility: HealthcheckSetupSkipIdentityUnavailable,
		ServerID:    "abcd1234",
	})
	low := strings.ToLower(st.Message)
	if strings.Contains(low, "telegram") || strings.Contains(low, "pair") {
		t.Fatalf("missing-secret copy must be A-aware (no Telegram/pairing), got %q", st.Message)
	}
	if !strings.Contains(low, "provisioned automatically") {
		t.Fatalf("missing-secret copy must promise auto provisioning, got %q", st.Message)
	}
	if !strings.Contains(low, "unreachable") {
		t.Fatalf("missing-secret copy must hint at monitor unreachability, got %q", st.Message)
	}
}

// TestClassifyHealthcheckSetupSkip_Distinct locks that no two skip verdicts render the
// same headline or copy (the whole point of the differentiation).
func TestClassifyHealthcheckSetupSkip_Distinct(t *testing.T) {
	states := []HealthcheckSetupBootstrap{
		{Eligibility: HealthcheckSetupSkipDisabled},
		{Eligibility: HealthcheckSetupSkipConfigError},
		{Eligibility: HealthcheckSetupSkipSelfMode},
		{Eligibility: HealthcheckSetupSkipIdentityUnavailable, ServerID: ""},
		{Eligibility: HealthcheckSetupSkipIdentityUnavailable, ServerID: "abcd1234"},
	}
	kw := map[string]bool{}
	msg := map[string]bool{}
	for _, s := range states {
		st := ClassifyHealthcheckSetupSkip(s)
		if kw[st.Keyword] {
			t.Fatalf("duplicate skip keyword: %q", st.Keyword)
		}
		kw[st.Keyword] = true
		if msg[st.Message] {
			t.Fatalf("duplicate skip message: %q", st.Message)
		}
		msg[st.Message] = true
	}
}
