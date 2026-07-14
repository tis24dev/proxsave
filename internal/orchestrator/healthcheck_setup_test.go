package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

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
		return &config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized", ServerAPIHost: "https://h"}
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
		{"c1 control login dropped", HealthcheckCheckResult{Err: nil, Reachable: true, LoginURL: "https://hc/x"}, true, false, ""},
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
	ols, on := healthcheckSetupLoadStatus, healthcheckSetupNow
	t.Cleanup(func() {
		healthcheckSetupFetch = of
		healthcheckSetupPing = op
		healthcheckSetupLoadSecret = osec
		healthcheckSetupLoadStatus = ols
		healthcheckSetupNow = on
	})
	healthcheckSetupLoadSecret = func(baseDir string) string { return "s" }
	// Default: no daemon status file yet (tolerant zero read) -> DaemonRead true, down.
	healthcheckSetupLoadStatus = func(baseDir string) (health.Status, error) { return health.Status{}, nil }
	now := time.Unix(1_700_000_000, 0)
	healthcheckSetupNow = func() time.Time { return now }

	t.Run("success returns login + reachable + daemon diagnosed", func(t *testing.T) {
		gotInclude := false
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			gotInclude = includeLogin
			return health.CentralizedConfig{AliveURL: "https://a", BackupURL: "https://b", LoginURL: "MAGIC"}, nil
		}
		healthcheckSetupPing = func(ctx context.Context, aliveURL string) error { return nil }
		// Fresh, OK heartbeat -> the daemon is alive and transmitting.
		healthcheckSetupLoadStatus = func(baseDir string) (health.Status, error) {
			return health.Status{Heartbeat: &health.PingRecord{TS: now.Unix(), OK: true}}, nil
		}
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", "/base", time.Minute)
		if !gotInclude {
			t.Fatal("the install check must request the login (includeLogin=true)")
		}
		if res.Err != nil || !res.Reachable || res.LoginURL != "MAGIC" {
			t.Fatalf("unexpected: %+v", res)
		}
		if !res.DaemonRead || res.Daemon.State != health.TxTransmitting {
			t.Fatalf("daemon must be diagnosed as transmitting: read=%v state=%q", res.DaemonRead, res.Daemon.State)
		}
	})

	t.Run("fetch error propagates, no login", func(t *testing.T) {
		healthcheckSetupLoadStatus = func(baseDir string) (health.Status, error) { return health.Status{}, nil }
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{}, health.ErrHCNotReady
		}
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", "/base", time.Minute)
		if !errors.Is(res.Err, health.ErrHCNotReady) || res.Reachable || res.LoginURL != "" {
			t.Fatalf("unexpected: %+v", res)
		}
	})

	t.Run("ping error keeps login, not reachable", func(t *testing.T) {
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{AliveURL: "https://a", LoginURL: "MAGIC"}, nil
		}
		healthcheckSetupPing = func(ctx context.Context, aliveURL string) error { return errors.New("dial") }
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", "/base", time.Minute)
		if res.Err == nil || res.Reachable || res.LoginURL != "MAGIC" {
			t.Fatalf("unexpected: %+v", res)
		}
	})
}

// TestClassifyHealthcheckDaemonState pins the new headline semantics: with the
// connection reachable, the status keyword IS the real daemon state (mirroring the run),
// and only a live transmitting daemon reads WORKING (green/Ok level).
func TestClassifyHealthcheckDaemonState(t *testing.T) {
	reachable := func(d health.Diagnosis) HealthcheckCheckResult {
		return HealthcheckCheckResult{Err: nil, Reachable: true, DaemonRead: true, Daemon: d}
	}
	cases := []struct {
		name        string
		res         HealthcheckCheckResult
		wantKeyword string
		wantLevel   HealthcheckSetupLevel
	}{
		{"working", reachable(health.Diagnosis{State: health.TxTransmitting, DaemonUp: true}), "WORKING", HealthcheckSetupLevelOk},
		{"not installed", reachable(health.Diagnosis{State: health.TxNotInstalled}), "NOT INSTALLED", HealthcheckSetupLevelWarn},
		{"not active", reachable(health.Diagnosis{State: health.TxNotActive}), "NOT RUNNING", HealthcheckSetupLevelWarn},
		{"running not reporting", reachable(health.Diagnosis{State: health.TxRunningNoReport, DaemonUp: true}), "RUNNING, NOT REPORTING", HealthcheckSetupLevelWarn},
		{"daemon down", reachable(health.Diagnosis{State: health.TxNoHeartbeat}), "NOT RUNNING", HealthcheckSetupLevelWarn},
		{"stale", reachable(health.Diagnosis{State: health.TxStale, HbAge: time.Hour}), "STALE", HealthcheckSetupLevelWarn},
		{"transmit failed", reachable(health.Diagnosis{State: health.TxTransmitFailed, DaemonUp: true, Err: "500"}), "TRANSMIT FAILED", HealthcheckSetupLevelWarn},
		{"status unreadable", HealthcheckCheckResult{Err: nil, Reachable: true, DaemonRead: false}, "STATUS UNREADABLE", HealthcheckSetupLevelWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ClassifyHealthcheckSetupResult(tc.res)
			if st.Keyword != tc.wantKeyword || st.Level != tc.wantLevel {
				t.Fatalf("keyword=%q level=%d, want %q/%d", st.Keyword, st.Level, tc.wantKeyword, tc.wantLevel)
			}
			if !st.Verified { // reachable connection must still latch Continue
				t.Fatalf("reachable connection must be Verified, got %+v", st)
			}
			if st.Message == "" {
				t.Fatalf("Message must never be empty")
			}
		})
	}
}
