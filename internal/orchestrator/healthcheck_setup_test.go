package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	on := healthcheckSetupNow
	t.Cleanup(func() {
		healthcheckSetupFetch = of
		healthcheckSetupPing = op
		healthcheckSetupLoadSecret = osec
		healthcheckSetupNow = on
	})
	healthcheckSetupLoadSecret = func(baseDir string) string { return "s" }
	now := time.Unix(1_700_000_000, 0)
	healthcheckSetupNow = func() time.Time { return now }

	t.Run("success returns login + reachable + daemon diagnosed", func(t *testing.T) {
		gotInclude := false
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			gotInclude = includeLogin
			return health.CentralizedConfig{AliveURL: "https://a", BackupURL: "https://b", LoginURL: "MAGIC"}, nil
		}
		healthcheckSetupPing = func(ctx context.Context, aliveURL string) error { return nil }
		// Fresh, OK heartbeat on disk -> the daemon is alive and transmitting (CheckDaemonState
		// reads the real status file now, so seed one instead of stubbing the load).
		base := t.TempDir()
		if err := health.RecordPing(base, "self", health.KindHeartbeat, now.Unix(), true, nil); err != nil {
			t.Fatalf("seed heartbeat: %v", err)
		}
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", base, time.Minute)
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
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{}, health.ErrHCNotReady
		}
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", t.TempDir(), time.Minute)
		if !errors.Is(res.Err, health.ErrHCNotReady) || res.Reachable || res.LoginURL != "" {
			t.Fatalf("unexpected: %+v", res)
		}
	})

	t.Run("ping error keeps login, not reachable", func(t *testing.T) {
		healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, includeLogin bool) (health.CentralizedConfig, error) {
			return health.CentralizedConfig{AliveURL: "https://a", LoginURL: "MAGIC"}, nil
		}
		healthcheckSetupPing = func(ctx context.Context, aliveURL string) error { return errors.New("dial") }
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", t.TempDir(), time.Minute)
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

// seedDaemonInfoFor writes a fresh heartbeat and a DaemonInfo recording the running daemon's version
// under this process's LIVE pid (so the leaf pidAlive probe reads ProcessAlive=true and the /proc
// alignment probe is consulted). It records ExecPath at base/proxsave for realism; alignment itself
// is driven by the injected DaemonProcStale seam, not the record.
func seedDaemonInfoFor(t *testing.T, base string, now time.Time) {
	t.Helper()
	if err := health.RecordPing(base, "self", health.KindHeartbeat, now.Unix(), true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if err := health.WriteDaemonInfo(base, health.DaemonInfo{
		PID: os.Getpid(), ExecPath: filepath.Join(base, "proxsave"),
		Version: "1.2.3", Commit: "abcdef0", StartTS: now.Unix(),
	}); err != nil {
		t.Fatalf("seed daemon info: %v", err)
	}
}

// TestCheckHealthcheckConnectionPopulatesDaemonAlignment: the shared CheckDaemonState surfaces the
// running daemon's version (from the record) AND the /proc-derived binary alignment onto the check
// result, so the post-install classify can tell "behind" apart from a fresh/aligned daemon.
func TestCheckHealthcheckConnectionPopulatesDaemonAlignment(t *testing.T) {
	of, on, ops := healthcheckSetupFetch, healthcheckSetupNow, DaemonProcStale
	t.Cleanup(func() {
		healthcheckSetupFetch = of
		healthcheckSetupNow = on
		DaemonProcStale = ops
	})
	now := time.Unix(1_700_000_000, 0)
	healthcheckSetupNow = func() time.Time { return now }
	// Fail the fetch so the check returns right after the daemon-state read.
	healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, login bool) (health.CentralizedConfig, error) {
		return health.CentralizedConfig{}, errors.New("stub-fetch-down")
	}

	t.Run("behind: /proc reports the running binary was replaced", func(t *testing.T) {
		DaemonProcStale = func(pid int) (bool, bool) { return true, true }
		base := t.TempDir()
		seedDaemonInfoFor(t, base, now)
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", base, time.Minute)
		if !res.DaemonHaveInfo {
			t.Fatalf("DaemonHaveInfo must be true when a record was found")
		}
		if res.DaemonAligned {
			t.Fatalf("DaemonAligned must be false when /proc reports the binary replaced")
		}
		if res.DaemonStale == "" {
			t.Fatalf("DaemonStale must carry the not-aligned phrasing")
		}
		if res.DaemonVersion != "1.2.3" {
			t.Fatalf("DaemonVersion = %q, want 1.2.3", res.DaemonVersion)
		}
	})

	t.Run("aligned: /proc reports the running binary intact", func(t *testing.T) {
		DaemonProcStale = func(pid int) (bool, bool) { return false, true }
		base := t.TempDir()
		seedDaemonInfoFor(t, base, now)
		res := CheckHealthcheckConnection(context.Background(), "https://h", "id", base, time.Minute)
		if !res.DaemonHaveInfo || !res.DaemonAligned {
			t.Fatalf("aligned daemon: HaveInfo=%v Aligned=%v, want true/true", res.DaemonHaveInfo, res.DaemonAligned)
		}
		if res.DaemonStale != "" {
			t.Fatalf("DaemonStale must be empty when aligned, got %q", res.DaemonStale)
		}
	})
}

// TestClassifyHealthcheckBehind: a reachable host whose running daemon is on an OLDER binary than
// the one now on disk reads BEHIND, DISTINCT from the aligned "RUNNING, NOT REPORTING" headline,
// and takes precedence over the transmission-state switch.
func TestClassifyHealthcheckBehind(t *testing.T) {
	behind := HealthcheckCheckResult{
		Err: nil, Reachable: true, DaemonRead: true,
		Daemon:             health.Diagnosis{State: health.TxRunningNoReport, DaemonUp: true},
		DaemonHaveInfo:     true,
		DaemonAlignChecked: true, // a real comparison ran and mismatched -> genuinely behind
		DaemonAligned:      false,
	}
	st := ClassifyHealthcheckSetupResult(behind)
	if st.Keyword != "BEHIND" {
		t.Fatalf("keyword = %q, want BEHIND", st.Keyword)
	}
	if st.Level != HealthcheckSetupLevelWarn {
		t.Fatalf("level = %d, want Warn", st.Level)
	}
	if !st.Verified {
		t.Fatalf("reachable connection must still be Verified")
	}
	if !strings.Contains(st.Message, "restart") {
		t.Fatalf("message should mention restart, got %q", st.Message)
	}

	// With the SAME diagnosis but an aligned binary, the headline stays the plain
	// transmission state (distinct verdicts, no BEHIND).
	aligned := behind
	aligned.DaemonAligned = true
	if got := ClassifyHealthcheckSetupResult(aligned).Keyword; got != "RUNNING, NOT REPORTING" {
		t.Fatalf("aligned daemon keyword = %q, want RUNNING, NOT REPORTING", got)
	}
}

// TestClassifyHealthcheckBehindWithoutRecord: a record-less-but-stale daemon (DaemonHaveInfo=false,
// but alignment determined by the /proc fallback) now reads BEHIND. The gate no longer requires a
// record (DaemonHaveInfo), so the daemon that predates the identity-record feature is caught instead
// of flattening to the transmission-state headline.
func TestClassifyHealthcheckBehindWithoutRecord(t *testing.T) {
	behind := HealthcheckCheckResult{
		Err: nil, Reachable: true, DaemonRead: true,
		Daemon:             health.Diagnosis{State: health.TxRunningNoReport, DaemonUp: true},
		DaemonHaveInfo:     false, // no record: the /proc fallback determined alignment
		DaemonAlignChecked: true,
		DaemonAligned:      false,
	}
	st := ClassifyHealthcheckSetupResult(behind)
	if st.Keyword != "BEHIND" {
		t.Fatalf("record-less stale daemon keyword = %q, want BEHIND", st.Keyword)
	}
	if !strings.Contains(st.Message, "restart") {
		t.Fatalf("message should mention restart, got %q", st.Message)
	}

	// UNKNOWN alignment (AlignChecked=false) with no record must NOT read as behind.
	unknown := behind
	unknown.DaemonAlignChecked = false
	if got := ClassifyHealthcheckSetupResult(unknown).Keyword; got == "BEHIND" {
		t.Fatalf("UNKNOWN alignment must not read as BEHIND")
	}
}

// TestCheckHealthcheckConnectionProcStaleFallback: with NO identity record but a live pid and a wired
// DaemonProcStale, the check surfaces the record-independent staleness verdict onto the result so the
// classify can read BEHIND. This exercises the orchestrator seam wiring end-to-end.
func TestCheckHealthcheckConnectionProcStaleFallback(t *testing.T) {
	of, on, ops := healthcheckSetupFetch, healthcheckSetupNow, DaemonProcStale
	t.Cleanup(func() {
		healthcheckSetupFetch = of
		healthcheckSetupNow = on
		DaemonProcStale = ops
	})
	now := time.Unix(1_700_000_000, 0)
	healthcheckSetupNow = func() time.Time { return now }
	healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, login bool) (health.CentralizedConfig, error) {
		return health.CentralizedConfig{}, errors.New("stub-fetch-down") // return right after the daemon-state read
	}
	// Wire the /proc fallback to report the running binary stale.
	DaemonProcStale = func(pid int) (bool, bool) { return true, true }

	base := t.TempDir()
	if err := health.RecordPing(base, "self", health.KindHeartbeat, now.Unix(), true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	// A LIVE pid (this process) so the leaf pidAlive probe reads ProcessAlive=true and the /proc probe
	// is consulted.
	if err := health.WriteDaemonPID(base, os.Getpid()); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	// Deliberately NO WriteDaemonInfo: there is no record, so /proc alone decides alignment.

	// The fetch stub fails, so the check returns right after the daemon-state read (res.Err set); we
	// assert on the Daemon* alignment fields the fallback populated. The record-less classify BEHIND
	// is covered by TestClassifyHealthcheckBehindWithoutRecord.
	res := CheckHealthcheckConnection(context.Background(), "https://h", "id", base, time.Minute)
	if res.DaemonHaveInfo {
		t.Fatalf("DaemonHaveInfo must be false with no record")
	}
	if !res.DaemonAlignChecked {
		t.Fatalf("DaemonAlignChecked must be true: the /proc fallback returned a verdict")
	}
	if res.DaemonAligned {
		t.Fatalf("DaemonAligned must be false when the /proc fallback reports stale")
	}
	if res.DaemonStale == "" {
		t.Fatalf("DaemonStale must carry the /proc fallback phrasing")
	}
}
