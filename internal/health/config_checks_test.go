package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
