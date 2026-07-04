package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
)

func stubHealthcheckBootstrap(t *testing.T, cfg *config.Config, cfgErr error, serverID string, secret string) {
	t.Helper()
	oc, oi, os := healthcheckSetupLoadConfig, healthcheckSetupIdentityDetect, healthcheckSetupLoadSecret
	t.Cleanup(func() {
		healthcheckSetupLoadConfig = oc
		healthcheckSetupIdentityDetect = oi
		healthcheckSetupLoadSecret = os
	})
	healthcheckSetupLoadConfig = func(configPath, baseDir string) (*config.Config, error) { return cfg, cfgErr }
	healthcheckSetupIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: serverID}, nil
	}
	healthcheckSetupLoadSecret = func(baseDir string) string { return secret }
}

func TestBuildHealthcheckSetupBootstrap(t *testing.T) {
	daemonCfg := func() *config.Config {
		return &config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized", TelegramServerAPIHost: "https://h"}
	}
	tests := []struct {
		name     string
		cfg      *config.Config
		cfgErr   error
		serverID string
		secret   string
		want     HealthcheckSetupEligibility
	}{
		{"eligible", daemonCfg(), nil, "123456789012", "sekret", HealthcheckSetupEligibleCentralized},
		{"cron/disabled", &config.Config{HealthcheckEnabled: false}, nil, "1", "s", HealthcheckSetupSkipDisabled},
		{"self mode", &config.Config{HealthcheckEnabled: true, HealthcheckMode: "self"}, nil, "1", "s", HealthcheckSetupSkipSelfMode},
		{"no server id", daemonCfg(), nil, "", "s", HealthcheckSetupSkipIdentityUnavailable},
		{"no secret", daemonCfg(), nil, "1", "", HealthcheckSetupSkipIdentityUnavailable},
		{"config error", nil, errors.New("boom"), "1", "s", HealthcheckSetupSkipConfigError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubHealthcheckBootstrap(t, tc.cfg, tc.cfgErr, tc.serverID, tc.secret)
			state, err := BuildHealthcheckSetupBootstrap("/cfg", "/base")
			if err != nil {
				t.Fatalf("bootstrap returned error (must be non-blocking): %v", err)
			}
			if state.Eligibility != tc.want {
				t.Fatalf("Eligibility = %d, want %d", state.Eligibility, tc.want)
			}
			if tc.want == HealthcheckSetupEligibleCentralized {
				if state.ServerID == "" || !state.HasSecret || state.ServerAPIHost == "" {
					t.Fatalf("eligible state incomplete: %+v", state)
				}
			}
		})
	}
}

func TestClassifyHealthcheckSetupResult(t *testing.T) {
	tests := []struct {
		name         string
		res          HealthcheckCheckResult
		wantVerified bool
		wantFatal    bool
		wantLogin    string
	}{
		{"verified", HealthcheckCheckResult{Err: nil, Reachable: true, LoginURL: "https://hc/L"}, true, false, "https://hc/L"},
		{"ready but not reachable -> retry", HealthcheckCheckResult{Err: nil, Reachable: false, LoginURL: "https://hc/L"}, false, false, "https://hc/L"},
		{"auth fatal", HealthcheckCheckResult{Err: health.ErrHCAuth}, false, true, ""},
		{"unknown fatal", HealthcheckCheckResult{Err: health.ErrHCUnknown}, false, true, ""},
		{"disabled fatal", HealthcheckCheckResult{Err: health.ErrHCDisabled}, false, true, ""},
		{"not ready retry", HealthcheckCheckResult{Err: health.ErrHCNotReady}, false, false, ""},
		{"network retry keeps login", HealthcheckCheckResult{Err: errors.New("dial"), LoginURL: "https://hc/L2"}, false, false, "https://hc/L2"},
		{"hostile ansi login dropped", HealthcheckCheckResult{Err: nil, Reachable: true, LoginURL: "https://hc/\x1b[2Jx"}, true, false, ""},
		{"non-https login dropped", HealthcheckCheckResult{Err: nil, Reachable: true, LoginURL: "ftp://evil/x"}, true, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := ClassifyHealthcheckSetupResult(tc.res)
			if st.Verified != tc.wantVerified || st.Fatal != tc.wantFatal {
				t.Fatalf("verified=%v fatal=%v, want %v/%v", st.Verified, st.Fatal, tc.wantVerified, tc.wantFatal)
			}
			if st.LoginURL != tc.wantLogin {
				t.Fatalf("LoginURL = %q, want %q", st.LoginURL, tc.wantLogin)
			}
			if st.Message == "" {
				t.Fatalf("Message must never be empty")
			}
		})
	}
}

func TestCheckHealthcheckConnection(t *testing.T) {
	of, op, osec := healthcheckSetupFetch, healthcheckSetupPing, healthcheckSetupLoadSecret
	t.Cleanup(func() {
		healthcheckSetupFetch = of
		healthcheckSetupPing = op
		healthcheckSetupLoadSecret = osec
	})
	healthcheckSetupLoadSecret = func(baseDir string) string { return "s" }

	t.Run("success returns login + reachable", func(t *testing.T) {
		gotInclude := false
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			gotInclude = includeLogin
			return health.CentralizedConfig{AliveURL: "https://a", BackupURL: "https://b", LoginURL: "MAGIC"}, nil
		}
		healthcheckSetupPing = func(ctx context.Context, aliveURL string) error { return nil }
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", "/base")
		if !gotInclude {
			t.Fatal("the install check must request the login (includeLogin=true)")
		}
		if res.Err != nil || !res.Reachable || res.LoginURL != "MAGIC" {
			t.Fatalf("unexpected: %+v", res)
		}
	})

	t.Run("fetch error propagates, no login", func(t *testing.T) {
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{}, health.ErrHCNotReady
		}
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", "/base")
		if !errors.Is(res.Err, health.ErrHCNotReady) || res.Reachable || res.LoginURL != "" {
			t.Fatalf("unexpected: %+v", res)
		}
	})

	t.Run("ping error keeps login, not reachable", func(t *testing.T) {
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{AliveURL: "https://a", LoginURL: "MAGIC"}, nil
		}
		healthcheckSetupPing = func(ctx context.Context, aliveURL string) error { return errors.New("dial") }
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", "/base")
		if res.Err == nil || res.Reachable || res.LoginURL != "MAGIC" {
			t.Fatalf("unexpected: %+v", res)
		}
	})
}
