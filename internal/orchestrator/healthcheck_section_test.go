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
		return health.Status{
			Heartbeat:   &health.PingRecord{TS: hcNow.Add(-30 * time.Second).Unix(), OK: true},
			RunFinished: &health.PingRecord{TS: hcNow.Add(-2 * time.Minute).Unix(), OK: true},
		}, nil
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
	if !strings.Contains(out, checkGlyph) || !strings.Contains(out, "transmitting to the monitor") {
		t.Fatalf("want success glyph + transmitting line, out=%q", out)
	}
	if !strings.Contains(out, "heartbeat") || !strings.Contains(out, "last backup outcome") {
		t.Fatalf("want heartbeat + last-outcome ages, out=%q", out)
	}
	// magic-link still works (reuse-captured)
	if !strings.Contains(out, "https://hc/accounts/check_token/u/CAP/") {
		t.Fatalf("captured link must still be displayed, out=%q", out)
	}
}

func TestHealthchecksSectionTransmittingNoOutcome(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		// heartbeat only, no run outcome recorded yet
		return health.Status{Heartbeat: &health.PingRecord{TS: hcNow.Add(-1 * time.Minute).Unix(), OK: true}}, nil
	}
	// no captured link; mint is reachable but returns an error -> quiet info line
	ch.mintLink = func(context.Context, string, string, string) (string, error) { return "", errors.New("bot down") }

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, checkGlyph) || !strings.Contains(out, "heartbeat") {
		t.Fatalf("want success glyph + heartbeat age, out=%q", out)
	}
	if strings.Contains(out, "last backup outcome") {
		t.Fatalf("no outcome recorded -> must not mention last backup outcome, out=%q", out)
	}
}

func TestHealthchecksSectionHeartbeatStale(t *testing.T) {
	var buf bytes.Buffer
	// interval unset (0) -> default 5m -> stale after 10m; a 1h-old beat is stale.
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{Heartbeat: &health.PingRecord{TS: hcNow.Add(-1 * time.Hour).Unix(), OK: true}}, nil
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
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "heartbeat stale") {
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
		return health.Status{
			Heartbeat:   &health.PingRecord{TS: hcNow.Add(-30 * time.Second).Unix(), OK: true},
			RunHang:     &health.PingRecord{TS: hcNow.Add(-10 * time.Minute).Unix(), OK: true},                                                // older, ok
			RunFinished: &health.PingRecord{TS: hcNow.Add(-2 * time.Minute).Unix(), OK: false, Err: "healthcheck finish: connection refused"}, // newer, failed
		}, nil
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
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "NOT transmitted") {
		t.Fatalf("want warning glyph + failed line, out=%q", out)
	}
	if !strings.Contains(out, "connection refused") {
		t.Fatalf("want the redacted err text, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("transmit-failed must not print a success glyph, out=%q", out)
	}
}

func TestHealthchecksSectionHeartbeatFailed(t *testing.T) {
	// A FRESH heartbeat whose transmission FAILED (OK=false) means the monitor is
	// unreachable RIGHT NOW even though the daemon keeps beating; a superseding ok
	// backup outcome must NOT rescue it into a false success. This is the exact false
	// glyph the section exists to eliminate: fresh TS hides staleness, ok outcome hides
	// transmit-failed, so only the heartbeat OK flag catches it.
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.now = func() time.Time { return hcNow }
	ch.loadStatus = func(string) (health.Status, error) {
		return health.Status{
			// fresh (-2m, well within the default 10m stale window) but FAILED
			Heartbeat:   &health.PingRecord{TS: hcNow.Add(-2 * time.Minute).Unix(), OK: false, Err: "healthcheck alive: connection refused"},
			RunFinished: &health.PingRecord{TS: hcNow.Add(-1 * 24 * time.Hour).Unix(), OK: true}, // old ok outcome must not rescue
		}, nil
	}
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must not mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "heartbeat-failed" {
		t.Fatalf("status=%q want heartbeat-failed", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "heartbeat NOT transmitted") {
		t.Fatalf("want warning glyph + heartbeat-failed line, out=%q", out)
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
				return health.Status{Heartbeat: &health.PingRecord{TS: hcNow.Add(-tc.hbAge).Unix(), OK: true}}, nil
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
		return health.Status{
			Heartbeat:   &health.PingRecord{TS: hcNow.Add(-30 * time.Second).Unix(), OK: true},
			RunFinished: &health.PingRecord{TS: hcNow.Add(-20 * time.Minute).Unix(), OK: false, Err: "old failure"}, // older, failed
			RunHang:     &health.PingRecord{TS: hcNow.Add(-2 * time.Minute).Unix(), OK: true},                       // newer, ok
		}, nil
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

func TestHealthchecksSectionNoTransmission(t *testing.T) {
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
	if stats.HealthcheckStatus != "no-transmission" {
		t.Fatalf("status=%q want no-transmission", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, warnGlyph) || !strings.Contains(out, "no transmission recorded") {
		t.Fatalf("want warning glyph + no-transmission line, out=%q", out)
	}
	if strings.Contains(out, checkGlyph) {
		t.Fatalf("no-transmission must not print a success glyph, out=%q", out)
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
	if !strings.Contains(buf.String(), "https://hc/accounts/check_token/u/MINT/") {
		t.Fatalf("out=%q", buf.String())
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
	if strings.Contains(out, "Monitoring portal") {
		t.Fatalf("a failed mint must not print a portal line, got: %q", out)
	}
	if !strings.Contains(out, "not available this run") {
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

	// captured RAW link is hostile (raw space) -> SanitizeLoginURL rejects -> no line.
	stats := &BackupStats{HealthcheckLink: "https://hc/ x"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "transmitting" {
		t.Fatalf("status=%q want transmitting", stats.HealthcheckStatus)
	}
	if strings.Contains(buf.String(), "https://hc/ x") || strings.Contains(buf.String(), "Monitoring portal") {
		t.Fatalf("hostile link must be sanitized away at the display boundary, got: %q", buf.String())
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
