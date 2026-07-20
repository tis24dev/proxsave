package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestBuildHealthcheckSetupBootstrapSelf covers the self-mode eligibility branch: a
// collected alive URL yields EligibleSelf WITHOUT any ServerID/secret, an empty alive
// URL yields SkipSelfMode, and the centralized path is unchanged.
func TestBuildHealthcheckSetupBootstrapSelf(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		serverID string
		secret   string
		want     HealthcheckSetupEligibility
	}{
		{
			"self with alive url -> eligible self (no identity)",
			&config.Config{HealthcheckEnabled: true, HealthcheckMode: "self", HealthcheckAliveURL: "https://hc-ping.com/a"},
			"", "", // no ServerID, no secret on purpose
			HealthcheckSetupEligibleSelf,
		},
		{
			"self without alive url -> skip",
			&config.Config{HealthcheckEnabled: true, HealthcheckMode: "self", HealthcheckAliveURL: ""},
			"1", "s",
			HealthcheckSetupSkipSelfMode,
		},
		{
			"centralized unchanged",
			&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized", ServerAPIHost: "https://h"},
			"123456789012", "sekret",
			HealthcheckSetupEligibleCentralized,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubHealthcheckBootstrap(t, tc.cfg, nil, tc.serverID, tc.secret)
			state, err := BuildHealthcheckSetupBootstrap("/cfg", "/base")
			if err != nil {
				t.Fatalf("bootstrap returned error: %v", err)
			}
			if state.Eligibility != tc.want {
				t.Fatalf("Eligibility = %d, want %d", state.Eligibility, tc.want)
			}
			if tc.want == HealthcheckSetupEligibleSelf {
				if state.HealthcheckAliveURL != tc.cfg.HealthcheckAliveURL {
					t.Fatalf("HealthcheckAliveURL = %q, want %q", state.HealthcheckAliveURL, tc.cfg.HealthcheckAliveURL)
				}
				if state.ServerID != "" {
					t.Fatalf("self mode must not require ServerID, got %q", state.ServerID)
				}
			}
		})
	}
}

// TestCheckAndClassifyHealthcheckSelf stubs the ping seam and asserts the full
// self-mode check -> classify mapping (reachable/unreachable/not-configured).
func TestCheckAndClassifyHealthcheckSelf(t *testing.T) {
	orig := healthcheckSetupPing
	t.Cleanup(func() { healthcheckSetupPing = orig })

	tests := []struct {
		name         string
		aliveURL     string
		pingErr      error
		wantKeyword  string
		wantLevel    HealthcheckSetupLevel
		wantVerified bool
		wantFatal    bool
	}{
		{"reachable", "https://hc-ping.com/a", nil, "REACHABLE", HealthcheckSetupLevelOk, true, false},
		{"unreachable", "https://hc-ping.com/a", errors.New("dial tcp: timeout"), "UNREACHABLE", HealthcheckSetupLevelWarn, false, false},
		{"not configured", "", nil, "NOT CONFIGURED", HealthcheckSetupLevelWarn, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pinged := false
			healthcheckSetupPing = func(ctx context.Context, aliveURL string) error {
				pinged = true
				if aliveURL != tc.aliveURL {
					t.Fatalf("ping called with %q, want %q", aliveURL, tc.aliveURL)
				}
				return tc.pingErr
			}
			res := CheckHealthcheckSelfConnection(context.Background(), tc.aliveURL)
			if tc.aliveURL == "" {
				if pinged {
					t.Fatalf("empty alive URL must NOT ping")
				}
				if !errors.Is(res.Err, ErrHealthcheckSelfNotConfigured) {
					t.Fatalf("empty alive URL: Err = %v, want ErrHealthcheckSelfNotConfigured", res.Err)
				}
			}
			st := ClassifyHealthcheckSelfResult(res)
			if st.Keyword != tc.wantKeyword {
				t.Errorf("Keyword = %q, want %q", st.Keyword, tc.wantKeyword)
			}
			if st.Level != tc.wantLevel {
				t.Errorf("Level = %d, want %d", st.Level, tc.wantLevel)
			}
			if st.Verified != tc.wantVerified {
				t.Errorf("Verified = %v, want %v", st.Verified, tc.wantVerified)
			}
			if st.Fatal != tc.wantFatal {
				t.Errorf("Fatal = %v, want %v (self errors are retryable, never fatal)", st.Fatal, tc.wantFatal)
			}
			if st.LoginURL != "" {
				t.Errorf("self mode must not carry a LoginURL, got %q", st.LoginURL)
			}
		})
	}
}
