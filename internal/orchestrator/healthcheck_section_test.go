package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// hcNow is a fixed clock for deterministic freshness/staleness assertions.
var hcNow = time.Unix(1_700_000_000, 0)

// checkGlyph / warnGlyph mirror the exact glyphs the sibling notification lines use
// (notification_adapter.go logTelegramOutcome): a plain check mark and a warning sign.
const (
	checkGlyph = "✓"
	warnGlyph  = "⚠️"
)

// newHCTestChannel builds a HealthchecksChannel with NIL seams
// (loadSecret/mintLink/loadStatus/now); each test wires only the seams its branch needs,
// so an unexpected call is a bug.
func newHCTestChannel(cfg *config.Config, buf *bytes.Buffer) *HealthchecksChannel {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(buf)
	return &HealthchecksChannel{cfg: cfg, logger: logger}
}

// hcTransmittingChannel wires a channel into the centralized "transmitting" state (secret
// present, fresh heartbeat, last outcome ok) so the magic-link tests can focus on the
// link seam. The caller sets ch.mintLink / stats.HealthcheckLink as needed.
func hcTransmittingChannel(cfg *config.Config, buf *bytes.Buffer) *HealthchecksChannel {
	ch := newHCTestChannel(cfg, buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Records: map[string]*health.PingRecord{
			health.KindHeartbeat:   {TS: hcNow.Add(-30 * time.Second).Unix(), OK: true},
			health.KindRunFinished: {TS: hcNow.Add(-2 * time.Minute).Unix(), OK: true},
		}}, nil
	}
	return ch
}

func TestHealthchecksSectionSelfMode(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "self"}, &buf)
	ch.loadSecret = func(string) (string, error) { t.Fatal("self mode must not read the secret"); return "", nil }
	ch.loadStatus = func(string) (health.Status, error) {
		t.Fatal("self mode must not read the status file")
		return health.Status{}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("self mode must not fetch")
		return "", nil
	}

	stats := &BackupStats{}
	if err := ch.Notify(context.Background(), stats); err != nil {
		t.Fatalf("Notify err: %v", err)
	}
	if stats.HealthcheckStatus != "self" {
		t.Fatalf("status=%q want self", stats.HealthcheckStatus)
	}
	if !strings.Contains(buf.String(), "self-hosted") {
		t.Fatalf("out=%q", buf.String())
	}
}

func TestHealthchecksSectionNotConfigured(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "", nil } // no secret on disk
	ch.loadStatus = func(string) (health.Status, error) {
		t.Fatal("no-secret must not read the status file")
		return health.Status{}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("no-secret must not fetch")
		return "", nil
	}

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "not-configured" {
		t.Fatalf("status=%q want not-configured", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, "not configured") {
		t.Fatalf("out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("not-configured must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionTransmitting(t *testing.T) {
	var buf bytes.Buffer
	ch := hcTransmittingChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	// captured link -> no mint
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must NOT trigger a mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, checkGlyph) || !strings.Contains(out, "transmitting") {
		t.Fatalf("want success glyph + transmitting line, out=%q", out)
	}
	if !strings.Contains(out, "last beat") {
		t.Fatalf("want last-beat age, out=%q", out)
	}
	// The captured link is PRESERVED on stats (carried RAW to the epilogue) but is NOT
	// displayed by this channel: the SOLE display boundary moved to logMonitoringPortalLink.
	if stats.HealthcheckLink != "https://hc/accounts/check_token/u/CAP/" {
		t.Fatalf("captured link must be preserved on stats, got %q", stats.HealthcheckLink)
	}
	if strings.Contains(out, "https://hc/accounts/check_token/u/CAP/") || strings.Contains(out, "Healthchecks Portal") {
		t.Fatalf("the channel must no longer display the link (moved to the epilogue), out=%q", out)
	}
}

func TestHealthchecksSectionTransmittingNoOutcome(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		// heartbeat only, no run outcome recorded yet
		return health.Status{Records: map[string]*health.PingRecord{health.KindHeartbeat: {TS: hcNow.Add(-1 * time.Minute).Unix(), OK: true}}}, nil
	}
	// no captured link; mint is reachable but returns an error -> quiet info line
	ch.mintLink = func(context.Context, string, string, string) (string, error) { return "", errors.New("bot down") }

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, checkGlyph) || !strings.Contains(out, "last beat") {
		t.Fatalf("want success glyph + last-beat age, out=%q", out)
	}
}

func TestHealthchecksSectionHeartbeatStale(t *testing.T) {
	var buf bytes.Buffer
	// interval unset (0) -> default 5m -> stale after 10m; a 1h-old beat is stale.
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Records: map[string]*health.PingRecord{health.KindHeartbeat: {TS: hcNow.Add(-1 * time.Hour).Unix(), OK: true}}}, nil
	}
	// captured link so no mint is attempted
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("stale branch with a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "stale" {
		t.Fatalf("status=%q want stale", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "daemon stale") {
		t.Fatalf("want warning glyph + stale line, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("stale must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionTransmitFailed(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Records: map[string]*health.PingRecord{
			health.KindHeartbeat:   {TS: hcNow.Add(-30 * time.Second).Unix(), OK: true},
			health.KindRunHang:     {TS: hcNow.Add(-10 * time.Minute).Unix(), OK: true},                                                // older, ok
			health.KindRunFinished: {TS: hcNow.Add(-2 * time.Minute).Unix(), OK: false, Err: "healthcheck finish: connection refused"}, // newer, failed
		}}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("failed branch with a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmit-failed" {
		t.Fatalf("status=%q want transmit-failed", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "not transmitted") {
		t.Fatalf("want warning glyph + failed line, out=%q", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("want the redacted err text, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("transmit-failed must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionUnreachable(t *testing.T) {
	// A FRESH heartbeat whose transmission FAILED with a real error (OK=false, no no_url
	// reason) means the monitor is unreachable RIGHT NOW even though the daemon keeps
	// beating; a superseding ok backup outcome must NOT rescue it into a false success.
	// This is the exact false glyph the section exists to eliminate: fresh TS hides
	// staleness, ok outcome hides transmit-failed, so only the heartbeat OK flag catches it.
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Records: map[string]*health.PingRecord{
			// fresh (-2m, well within the default 10m stale window) but FAILED (real error)
			health.KindHeartbeat:   {TS: hcNow.Add(-2 * time.Minute).Unix(), OK: false, Err: "healthcheck alive: connection refused"},
			health.KindRunFinished: {TS: hcNow.Add(-1 * 24 * time.Hour).Unix(), OK: true}, // old ok outcome must not rescue
		}}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "unreachable" {
		t.Fatalf("status=%q want unreachable", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "monitor unreachable") {
		t.Fatalf("want warning glyph + unreachable line, out=%q", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("want the redacted err text surfaced, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("a fresh-but-failed heartbeat must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionStaleThresholdPinnedToInterval(t *testing.T) {
	// Pin BOTH the config-driven threshold and the 2x multiplier: with an explicit 1m
	// interval, staleAfter is 2m, so a beat straddling that boundary must flip. A
	// mutation that hard-codes the threshold (e.g. return 10m) or drops the 2x factor
	// would break exactly one of these two sub-cases.
	cases := []struct {
		name       string
		hbAge      time.Duration
		wantStatus string
		wantGlyph  string
	}{
		// 1m30s < 2m -> fresh -> transmitting (success glyph)
		{name: "just under 2x", hbAge: 90 * time.Second, wantStatus: "transmitting", wantGlyph: checkGlyph},
		// 2m30s > 2m -> stale (warning glyph)
		{name: "just over 2x", hbAge: 150 * time.Second, wantStatus: "stale", wantGlyph: warnGlyph},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			ch := newHCTestChannel(&config.Config{
				HealthcheckEnabled:           true,
				HealthcheckMode:              "centralized",
				HealthcheckHeartbeatInterval: time.Minute, // explicit -> staleAfter 2m
			}, &buf)
			ch.loadSecret = func(string) (string, error) { return "sekret", nil }
			ch.now = func() time.Time { return hcNow }
			ch.loadStatus = func(string) (health.Status, error) {
				return health.Status{Records: map[string]*health.PingRecord{health.KindHeartbeat: {TS: hcNow.Add(-tc.hbAge).Unix(), OK: true}}}, nil
			}
			ch.mintLink = func(context.Context, string, string, string) (string, error) {
				t.Fatal("a captured link must not mint")
				return "", nil
			}

			stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
			_ = ch.Notify(context.Background(), stats)
			if stats.HealthcheckStatus != tc.wantStatus {
				t.Fatalf("hbAge=%s status=%q want %q", tc.hbAge, stats.HealthcheckStatus, tc.wantStatus)
			}
			if !strings.Contains(buf.String(), tc.wantGlyph) {
				t.Fatalf("hbAge=%s want glyph %q, out=%q", tc.hbAge, tc.wantGlyph, buf.String())
			}
		})
	}
}

func TestHealthchecksSectionNewerOutcomeWins(t *testing.T) {
	// An OLD failure superseded by a NEWER ok outcome must read as transmitting, proving
	// we pick the NEWER of RunFinished/RunHang rather than "any failure".
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Records: map[string]*health.PingRecord{
			health.KindHeartbeat:   {TS: hcNow.Add(-30 * time.Second).Unix(), OK: true},
			health.KindRunFinished: {TS: hcNow.Add(-20 * time.Minute).Unix(), OK: false, Err: "old failure"}, // older, failed
			health.KindRunHang:     {TS: hcNow.Add(-2 * time.Minute).Unix(), OK: true},                       // newer, ok
		}}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	out := buf.String()
	if strings.Contains(out, "old failure") || strings.Contains(out, "NOT transmitted") {
		t.Fatalf("a superseded old failure must not surface, out=%q", out)
	}
	if !strings.Contains(out, checkGlyph) {
		t.Fatalf("want success glyph, out=%q", out)
	}
}

// Empty status file = the daemon never recorded a beat = it is not running. Because the
// daemon records its FIRST beat immediately (even before a URL resolves), a missing
// heartbeat is an unambiguous "daemon not running" - NOT the old confusing "(daemon not
// running, or first run pending)" that guessed two causes on one line.
func TestHealthchecksSectionDaemonDown(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) { return health.Status{}, nil } // empty status file
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "daemon-down" {
		t.Fatalf("status=%q want daemon-down", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "daemon not running") {
		t.Fatalf("want warning glyph + daemon-not-running line, out=%q", out)
	}
	// The old confusing "or first run pending" hedge must be GONE.
	if strings.Contains(out, "first run pending") {
		t.Fatalf("must not hedge two causes on one line, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("daemon-down must not print a success glyph, out=%q", out)
	}
}

// A fresh beat whose Reason is no_url = the daemon is ALIVE but has no ping URL yet
// (pairing pending / server unreachable). This is a DISTINCT line from "daemon not
// running" and from "monitor unreachable"; it must not surface a raw error string.
func TestHealthchecksSectionNotProvisioned(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Records: map[string]*health.PingRecord{
			health.KindHeartbeat: {TS: hcNow.Add(-30 * time.Second).Unix(), OK: false, Reason: health.ReasonNoURL},
		}}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "not-provisioned" {
		t.Fatalf("status=%q want not-provisioned", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "not provisioned") {
		t.Fatalf("want warning glyph + not-provisioned line, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("not-provisioned must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionMintsWhenNoCapture(t *testing.T) {
	var buf bytes.Buffer
	ch := hcTransmittingChannel(&config.Config{
		HealthcheckEnabled: true,
		HealthcheckMode:    "centralized",
		ServerAPIHost:      "https://bot",
		ServerID:           "srv1",
	}, &buf)
	minted := 0
	ch.mintLink = func(_ context.Context, host, serverID, secret string) (string, error) {
		minted++
		if host != "https://bot" || serverID != "srv1" || secret != "sekret" {
			t.Fatalf("mint args host=%q id=%q secret=%q", host, serverID, secret)
		}
		return "https://hc/accounts/check_token/u/MINT/", nil
	}

	stats := &BackupStats{} // no captured link -> mint once
	_ = ch.Notify(context.Background(), stats)
	if minted != 1 {
		t.Fatalf("mint called %d times, want 1", minted)
	}
	// The MINTED link is STORED RAW on stats (carried to the epilogue) but NOT displayed
	// by this channel: the SOLE display boundary is logMonitoringPortalLink in cmd/proxsave.
	if stats.HealthcheckLink != "https://hc/accounts/check_token/u/MINT/" {
		t.Fatalf("minted link must be stored on stats, got %q", stats.HealthcheckLink)
	}
	if strings.Contains(buf.String(), "https://hc/accounts/check_token/u/MINT/") || strings.Contains(buf.String(), "Healthchecks Portal") {
		t.Fatalf("the channel must not display the minted link (moved to the epilogue), out=%q", buf.String())
	}
}

func TestHealthchecksSectionMintFailIsQuiet(t *testing.T) {
	var buf bytes.Buffer
	ch := hcTransmittingChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.mintLink = func(context.Context, string, string, string) (string, error) { return "", errors.New("bot down") }

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	// The transmission status is derived from the status file, INDEPENDENT of the mint.
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	out := buf.String()
	if strings.Contains(out, "Healthchecks Portal") {
		t.Fatalf("a failed mint must not print a portal line, got: %q", out)
	}
	if !strings.Contains(out, "portal link unavailable") {
		t.Fatalf("a failed mint must degrade to a quiet info line, got: %q", out)
	}
}

func TestHealthchecksSectionHostileLinkSanitizedAway(t *testing.T) {
	var buf bytes.Buffer
	ch := hcTransmittingChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link (even hostile) must NOT trigger a mint")
		return "", nil
	}

	// captured RAW link is hostile (raw space). This channel carries it RAW and does NOT
	// display it: the sanitize-away happens at the SOLE display boundary in the epilogue
	// (proven by TestLogMonitoringPortalLink in cmd/proxsave). Here we only assert the
	// channel never leaks the hostile link to its own output.
	stats := &BackupStats{HealthcheckLink: "https://hc/ x"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	if strings.Contains(buf.String(), "https://hc/ x") || strings.Contains(buf.String(), "Healthchecks Portal") {
		t.Fatalf("the channel must not display the hostile link, got: %q", buf.String())
	}
	// The raw link is carried through untouched for the epilogue to sanitize.
	if stats.HealthcheckLink != "https://hc/ x" {
		t.Fatalf("channel must carry the raw link untouched, got %q", stats.HealthcheckLink)
	}
}

func TestHealthchecksSectionStatusUnreadable(t *testing.T) {
	// A corrupt/unreadable status file is a DISTINCT condition: we can neither prove nor
	// disprove transmission, so it must NOT be reported as "daemon not running".
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{}, errors.New("parse healthcheck status: unexpected end of JSON input")
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "status-unreadable" {
		t.Fatalf("status=%q want status-unreadable", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "status file unreadable") {
		t.Fatalf("want warning glyph + status-unreadable line, out=%q", out)
	}
	if strings.Contains(out, "daemon is not running") {
		t.Fatalf("a corrupt file must NOT claim daemon-down, out=%q", out)
	}
	// The raw parse error (may carry the file path) must stay debug-only, not in the WARNING.
	if strings.Contains(out, "unexpected end of JSON") {
		t.Fatalf("raw error must be debug-only, not surfaced in the warning, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("status-unreadable must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionUnreadableButDaemonProbed(t *testing.T) {
	// When the status file is unreadable BUT systemd was probed, the section must report
	// the REAL systemd state (here: active -> "running, not reporting") so it agrees with
	// the run-init check instead of a bare "status file unreadable".
	orig := DaemonPresenceProbe
	t.Cleanup(func() { DaemonPresenceProbe = orig })
	DaemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}

	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{}, errors.New("parse healthcheck status: unexpected end of JSON input")
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) { return "", nil }

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "running-not-reporting" {
		t.Fatalf("status=%q want running-not-reporting", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "daemon running, not reporting") {
		t.Fatalf("want 'daemon running, not reporting' warning, out=%q", out)
	}
	if strings.Contains(out, "status file unreadable") {
		t.Fatalf("a probed systemd state must override the bare unreadable line, out=%q", out)
	}
}

func TestHealthchecksChannelNameMatchesEntryConst(t *testing.T) {
	// R7: Name() and the dispatchNotifications entry MUST be the same const, or the
	// section double-handles. Pin the invariant so a rename cannot silently break it.
	ch := &HealthchecksChannel{}
	if ch.Name() != healthchecksSectionName {
		t.Fatalf("Name()=%q != const %q", ch.Name(), healthchecksSectionName)
	}
}
