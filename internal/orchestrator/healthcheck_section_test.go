package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// newHCTestChannel builds a HealthchecksChannel with NIL seams (loadSecret/mintLink);
// each test wires only the seams its branch needs, so an unexpected call is a bug.
func newHCTestChannel(cfg *config.Config, buf *bytes.Buffer) *HealthchecksChannel {
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(buf)
	return &HealthchecksChannel{cfg: cfg, logger: logger}
}

func TestHealthchecksSectionSelfMode(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "self"}, &buf)
	ch.loadSecret = func(string) (string, error) { t.Fatal("self mode must not read the secret"); return "", nil }
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
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("no-secret must not fetch")
		return "", nil
	}

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "not-configured" {
		t.Fatalf("status=%q want not-configured", stats.HealthcheckStatus)
	}
	if !strings.Contains(buf.String(), "not configured") {
		t.Fatalf("out=%q", buf.String())
	}
}

func TestHealthchecksSectionActiveReusesCapturedLink(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link must NOT trigger a mint")
		return "", nil
	}

	stats := &BackupStats{HealthcheckLink: "https://hc/accounts/check_token/u/CAP/"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "active" {
		t.Fatalf("status=%q want active", stats.HealthcheckStatus)
	}
	out := buf.String()
	if !strings.Contains(out, "active") || !strings.Contains(out, "https://hc/accounts/check_token/u/CAP/") {
		t.Fatalf("out=%q", out)
	}
}

func TestHealthchecksSectionActiveMintsWhenNoCapture(t *testing.T) {
	var buf bytes.Buffer
	ch := newHCTestChannel(&config.Config{
		HealthcheckEnabled:    true,
		HealthcheckMode:       "centralized",
		TelegramServerAPIHost: "https://bot",
		ServerID:              "srv1",
	}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
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
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.mintLink = func(context.Context, string, string, string) (string, error) { return "", errors.New("bot down") }

	stats := &BackupStats{}
	_ = ch.Notify(context.Background(), stats)
	// active is derived from the on-disk secret, INDEPENDENT of the mint result.
	if stats.HealthcheckStatus != "active" {
		t.Fatalf("status=%q want active", stats.HealthcheckStatus)
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
	ch := newHCTestChannel(&config.Config{HealthcheckEnabled: true, HealthcheckMode: "centralized"}, &buf)
	ch.loadSecret = func(string) (string, error) { return "sekret", nil }
	ch.mintLink = func(context.Context, string, string, string) (string, error) {
		t.Fatal("a captured link (even hostile) must NOT trigger a mint")
		return "", nil
	}

	// captured RAW link is hostile (raw space) -> SanitizeLoginURL rejects -> no line.
	stats := &BackupStats{HealthcheckLink: "https://hc/ x"}
	_ = ch.Notify(context.Background(), stats)
	if stats.HealthcheckStatus != "active" {
		t.Fatalf("status=%q want active", stats.HealthcheckStatus)
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
