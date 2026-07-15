package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestFetchCentralizedConfigWithChecks: the additive Checks map is parsed and the updates
// ping URL is reachable via CheckKeyUpdates.
func TestFetchCentralizedConfigWithChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://x/ping/a","backup_ping_url":"https://x/ping/b","project_code":"p","checks":{"updates":"https://x/ping/u"}}`)
	}))
	defer srv.Close()
	cfg, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false)
	if err != nil {
		t.Fatalf("FetchCentralizedConfig: %v", err)
	}
	if cfg.Checks[CheckKeyUpdates] != "https://x/ping/u" {
		t.Fatalf("checks.updates = %q, want the updates ping url", cfg.Checks[CheckKeyUpdates])
	}
}

// TestFetchCentralizedConfigWithoutChecksStillOK: an OLD server that omits "checks" parses
// fine (backward-compat) and simply resolves no updates URL (nil map).
func TestFetchCentralizedConfigWithoutChecksStillOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://x/ping/a","backup_ping_url":"https://x/ping/b","project_code":"p"}`)
	}))
	defer srv.Close()
	cfg, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false)
	if err != nil {
		t.Fatalf("FetchCentralizedConfig: %v", err)
	}
	if cfg.Checks != nil {
		t.Fatalf("absent checks must leave the map nil, got %+v", cfg.Checks)
	}
	if _, ok := cfg.Checks[CheckKeyUpdates]; ok {
		t.Fatalf("absent checks must not resolve an updates url")
	}
}

// TestFetchCentralizedConfigChecksDoNotRelaxCompleteness: "checks" is additive and must NOT
// substitute for the two required alive/backup keys - the completeness check is unchanged.
func TestFetchCentralizedConfigChecksDoNotRelaxCompleteness(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"","backup_ping_url":"","checks":{"updates":"https://x/ping/u"}}`)
	}))
	defer srv.Close()
	if _, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false); err == nil {
		t.Fatalf("checks must not substitute for the required alive/backup keys")
	}
}

// TestFetchWithChannelsSendsQuery is the Fase-2C CLIENT contract for the ?channels
// provisioning hint: FetchCentralizedConfigWithChannels sends the authoritative enabled set
// (comma-joined), an EMPTY (non-nil) slice sends the "none" sentinel, and the plain
// FetchCentralizedConfig sends NO channels key at all (install-wizard/Phase-7 callers can't
// touch the authoritative set). It also proves the returned additive Checks map round-trips
// updates + notify-<ch>.
func TestFetchWithChannelsSendsQuery(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://x/ping/a","backup_ping_url":"https://x/ping/b","project_code":"p","checks":{"updates":"u","notify-email":"n"}}`)
	}))
	defer srv.Close()

	// A populated non-nil slice -> channels=email,telegram (comma-joined, order preserved).
	cfg, err := FetchCentralizedConfigWithChannels(context.Background(), srv.Client(), srv.URL, "1", "s", false, []string{"email", "telegram"})
	if err != nil {
		t.Fatalf("FetchCentralizedConfigWithChannels: %v", err)
	}
	if got := gotQuery.Get("channels"); got != "email,telegram" {
		t.Fatalf("channels = %q, want email,telegram", got)
	}
	// The returned additive Checks map round-trips (updates + notify-email).
	if cfg.Checks[CheckKeyUpdates] != "u" {
		t.Fatalf("checks.updates = %q, want u", cfg.Checks[CheckKeyUpdates])
	}
	if cfg.Checks[CheckKeyNotify("email")] != "n" {
		t.Fatalf("checks.notify-email = %q, want n", cfg.Checks[CheckKeyNotify("email")])
	}

	// An EMPTY (non-nil) slice -> the explicit "none" sentinel (pause all), never empty value.
	if _, err := FetchCentralizedConfigWithChannels(context.Background(), srv.Client(), srv.URL, "1", "s", false, []string{}); err != nil {
		t.Fatalf("FetchCentralizedConfigWithChannels(empty): %v", err)
	}
	if got := gotQuery.Get("channels"); got != "none" {
		t.Fatalf("empty non-nil slice channels = %q, want the none sentinel", got)
	}

	// The plain FetchCentralizedConfig (nil channels) -> NO channels key at all.
	if _, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false); err != nil {
		t.Fatalf("FetchCentralizedConfig: %v", err)
	}
	if gotQuery.Has("channels") {
		t.Fatalf("plain fetch must not send ?channels, got %q", gotQuery.Get("channels"))
	}
}
