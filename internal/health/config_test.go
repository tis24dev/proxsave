package health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCentralizedConfigSuccess(t *testing.T) {
	var gotAuth, gotVer, gotSID, gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Server-Auth")
		gotVer = r.Header.Get("X-Proxsave-Version")
		gotSID = r.URL.Query().Get("server_id")
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://hc.proxsave.dev/ping/a","backup_ping_url":"https://hc.proxsave.dev/ping/b","project_code":"proj1"}`)
	}))
	defer srv.Close()

	cfg, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "123456789012", "sekret-token", false)
	if err != nil {
		t.Fatalf("FetchCentralizedConfig: %v", err)
	}
	if cfg.AliveURL != "https://hc.proxsave.dev/ping/a" || cfg.BackupURL != "https://hc.proxsave.dev/ping/b" {
		t.Fatalf("unexpected ping urls: %+v", cfg)
	}
	if cfg.Mode != "centralized" || cfg.ProjectCode != "proj1" {
		t.Fatalf("unexpected cfg: %+v", cfg)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/healthcheck/config" {
		t.Fatalf("request was %s %s, want GET /api/healthcheck/config", gotMethod, gotPath)
	}
	if gotAuth != "sekret-token" {
		t.Fatalf("X-Server-Auth = %q, want the raw secret", gotAuth)
	}
	if gotVer == "" {
		t.Fatalf("X-Proxsave-Version must be set")
	}
	if gotSID != "123456789012" {
		t.Fatalf("server_id = %q", gotSID)
	}
}

func TestFetchCentralizedConfigIncludeLogin(t *testing.T) {
	var gotLogin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLogin = r.URL.Query().Get("login")
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://x/ping/a","backup_ping_url":"https://x/ping/b","project_code":"p","login_url":"https://hc/accounts/check_token/u/tok/"}`)
	}))
	defer srv.Close()

	// includeLogin=true -> request carries ?login=1 and LoginURL is parsed.
	cfg, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", true)
	if err != nil {
		t.Fatalf("FetchCentralizedConfig: %v", err)
	}
	if gotLogin != "1" {
		t.Fatalf("login query = %q, want 1", gotLogin)
	}
	if cfg.LoginURL != "https://hc/accounts/check_token/u/tok/" {
		t.Fatalf("LoginURL = %q", cfg.LoginURL)
	}
}

func TestFetchCentralizedConfigNoLoginByDefault(t *testing.T) {
	var gotLogin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLogin = r.URL.Query().Get("login")
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://x/ping/a","backup_ping_url":"https://x/ping/b","project_code":"p"}`)
	}))
	defer srv.Close()

	cfg, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false)
	if err != nil {
		t.Fatalf("FetchCentralizedConfig: %v", err)
	}
	if gotLogin != "" {
		t.Fatalf("daemon poll must not send ?login, got %q", gotLogin)
	}
	if cfg.LoginURL != "" {
		t.Fatalf("LoginURL should be empty when not requested, got %q", cfg.LoginURL)
	}
}

func TestFetchCentralizedConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"auth 401", 401, `{"error":"AUTH_INVALID"}`, ErrHCAuth},
		{"auth 403", 403, `{"error":"AUTH_INVALID"}`, ErrHCAuth},
		{"unknown 404", 404, `{"error":"SERVER_UNKNOWN"}`, ErrHCUnknown},
		{"disabled 503", 503, `{"error":"HC_DISABLED"}`, ErrHCDisabled},
		{"not ready 503", 503, `{"error":"HC_NOT_READY"}`, ErrHCNotReady},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFetchCentralizedConfigIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"","backup_ping_url":""}`)
	}))
	defer srv.Close()
	if _, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false); err == nil {
		t.Fatalf("expected error on incomplete response")
	}
}

func TestFetchCentralizedConfigGenericStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	_, err := FetchCentralizedConfig(context.Background(), srv.Client(), srv.URL, "1", "s", false)
	if err == nil {
		t.Fatalf("expected error on HTTP 500")
	}
	for _, sentinel := range []error{ErrHCAuth, ErrHCUnknown, ErrHCNotReady, ErrHCDisabled} {
		if errors.Is(err, sentinel) {
			t.Fatalf("500 should be a generic error, not %v", sentinel)
		}
	}
}
