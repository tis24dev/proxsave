package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/identity"
)

// These tests reuse provisionTestSecret and newProvisionTestLogger from
// telegram_registration_provision_test.go (same package). The provision path now targets
// POST /api/relay/provision, NOT the legacy GET /api/get-chat-id.

type relayCapture struct {
	provisionHits int
	method        string
	path          string
	version       string
	serverIDBody  string
	authHeader    string // X-Server-Auth on the provision call (must stay empty)
	retryAfter    string // optional response Retry-After
	confirmHits   int
	confirmAuth   string
}

// relayProvisionServer routes POST /api/relay/provision (returning provisionStatus +
// provisionBody) and POST /api/confirm-secret (returning confirmStatus), capturing the
// method/path/version/body of the provision call for the wire-contract assertions.
func relayProvisionServer(t *testing.T, provisionStatus int, provisionBody string, confirmStatus int, capt *relayCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/relay/provision":
			capt.provisionHits++
			capt.method = r.Method
			capt.path = r.URL.Path
			capt.version = r.Header.Get("X-Proxsave-Version")
			capt.authHeader = r.Header.Get("X-Server-Auth")
			var body struct {
				ServerID string `json:"server_id"`
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &body)
			capt.serverIDBody = body.ServerID
			if capt.retryAfter != "" {
				w.Header().Set("Retry-After", capt.retryAfter)
			}
			w.WriteHeader(provisionStatus)
			_, _ = w.Write([]byte(provisionBody))
		case "/api/confirm-secret":
			capt.confirmHits++
			capt.confirmAuth = r.Header.Get("X-Server-Auth")
			w.WriteHeader(confirmStatus)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// Mandatory contract 1-5: POST, exact path, body carries server_id, X-Proxsave-Version
// sent (and no X-Server-Auth), a 201 with notify_secret persists AND confirms the token.
func TestProvisionRelaySecretPostsToRelayProvision(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"`+provisionTestSecret+`"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if err != nil || !provisioned {
		t.Fatalf("provisioned=%v err=%v, want true/nil", provisioned, err)
	}
	if capt.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", capt.method)
	}
	if capt.path != "/api/relay/provision" {
		t.Fatalf("path = %q, want /api/relay/provision", capt.path)
	}
	if capt.serverIDBody != "1234567890123456" {
		t.Fatalf("body server_id = %q, want 1234567890123456", capt.serverIDBody)
	}
	if capt.version == "" {
		t.Fatalf("X-Proxsave-Version header missing")
	}
	if capt.authHeader != "" {
		t.Fatalf("relay/provision must be unauthenticated, got X-Server-Auth=%q", capt.authHeader)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != provisionTestSecret {
		t.Fatalf("persisted secret = %q, want %q", loaded, provisionTestSecret)
	}
	if capt.confirmHits != 1 || capt.confirmAuth != provisionTestSecret {
		t.Fatalf("confirm hits=%d auth=%q, want 1/%q", capt.confirmHits, capt.confirmAuth, provisionTestSecret)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into logs:\n%s", buf.String())
	}
}

// Mandatory contract 6: a 200 already_provisioned does not try to read a non-existent
// token; nothing is persisted and confirm is not called.
func TestProvisionRelaySecretAlreadyProvisioned(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusOK, `{"status":"already_provisioned"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err != nil {
		t.Fatalf("already_provisioned: want (false,nil), got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("nothing must be persisted, got %q", loaded)
	}
	if capt.confirmHits != 0 {
		t.Fatalf("already_provisioned -> no confirm, got %d", capt.confirmHits)
	}
}

func TestProvisionRelaySecretRejectsMalformedAlreadyProvisioned200(t *testing.T) {
	for _, body := range []string{
		`not-json`,
		`{"status":"something_else"}`,
		`{}`,
	} {
		t.Run(body, func(t *testing.T) {
			logger, _ := newProvisionTestLogger()
			baseDir := t.TempDir()
			var capt relayCapture
			server := relayProvisionServer(
				t, http.StatusOK, body, http.StatusOK, &capt)
			defer server.Close()

			provisioned, err := ProvisionRelaySecret(
				context.Background(), server.URL,
				"1234567890123456", baseDir, logger)
			if provisioned || err == nil {
				t.Fatalf("malformed 200 must fail: got (%v,%v)", provisioned, err)
			}
			if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
				t.Fatalf("malformed 200 must persist nothing, got %q", loaded)
			}
		})
	}
}

// Mandatory contract 7: a 429 persists nothing and is a retryable failure.
func TestProvisionRelaySecretRateLimitedRetryable(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	capt := relayCapture{retryAfter: "7200"}
	server := relayProvisionServer(t, http.StatusTooManyRequests, `{"error":"RELAY_RATE_LIMITED"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("429 must be a retryable failure: got (%v,%v)", provisioned, err)
	}
	var limited *RelayProvisionRateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("429 error type = %T, want *RelayProvisionRateLimitError", err)
	}
	if limited.RetryAfter != 2*time.Hour {
		t.Fatalf("RetryAfter = %v, want 2h", limited.RetryAfter)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("429 must persist nothing, got %q", loaded)
	}
	if capt.confirmHits != 0 {
		t.Fatalf("429 -> no confirm, got %d", capt.confirmHits)
	}
}

func TestParseRelayProvisionRetryAfterClampsBeforeDurationOverflow(t *testing.T) {
	// 2^55 seconds multiplied by 1e9ns wraps a time.Duration to zero. The
	// parser must clamp in integer seconds before converting.
	const wrapsDurationToZero = "36028797018963968"
	if got := parseRelayProvisionRetryAfter(
		wrapsDurationToZero, time.Unix(0, 0),
	); got != relayProvisionMaxRetryAfter {
		t.Fatalf("huge Retry-After = %v, want clamp %v",
			got, relayProvisionMaxRetryAfter)
	}
}

func TestProvisionRelaySecretConfirmFailureIsNonFatal(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"`+provisionTestSecret+`"}`, http.StatusForbidden, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if err != nil || !provisioned {
		t.Fatalf("confirm 403 must stay provisioned: (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != provisionTestSecret {
		t.Fatalf("secret must persist despite confirm failure, got %q", loaded)
	}
	if capt.confirmHits != 1 {
		t.Fatalf("confirm hits = %d, want 1", capt.confirmHits)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into logs:\n%s", buf.String())
	}
}

func TestProvisionRelaySecret201NoTokenNoPersist(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("201 without token must fail: got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("nothing must be persisted, got %q", loaded)
	}
	if capt.confirmHits != 0 {
		t.Fatalf("no token -> no confirm, got %d", capt.confirmHits)
	}
}

func TestProvisionRelaySecretShortSecretRefused(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	// "abc" is 3 runes (< identity.NotifySecretMinLen) and would NOT be masked in logs.
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"abc"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("short secret must be refused: got (%v,%v)", provisioned, err)
	}
	if _, statErr := os.Stat(identity.NotifySecretPath(baseDir)); !os.IsNotExist(statErr) {
		t.Fatalf("secret file must be absent, got err=%v", statErr)
	}
	if capt.confirmHits != 0 {
		t.Fatalf("short secret -> no confirm, got %d", capt.confirmHits)
	}
}

func TestProvisionRelaySecretEmptyBaseDir(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"`+provisionTestSecret+`"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", "", logger)
	if provisioned || err == nil {
		t.Fatalf("empty baseDir must fail: got (%v,%v)", provisioned, err)
	}
	if capt.provisionHits != 0 {
		t.Fatalf("empty baseDir must not contact the server, got %d", capt.provisionHits)
	}
}

func TestProvisionRelaySecretAdoptsExistingSecretUnderLock(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	// A secret is already on disk (a concurrent minter won the race). The provisioner must
	// adopt it under the lock and NOT contact the server nor mint a competing secret.
	if err := identity.PersistNotifySecret(context.Background(), baseDir, provisionTestSecret, nil); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		t.Errorf("server must not be contacted when a secret is already present: %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err != nil {
		t.Fatalf("adopt path must return (false,nil), got (%v,%v)", provisioned, err)
	}
	if hits != 0 {
		t.Fatalf("server must not be hit under the adopt path, got %d", hits)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != provisionTestSecret {
		t.Fatalf("existing secret must be left untouched, got %q", loaded)
	}
}

// RelayProvisionRateLimitError.Error must mention the code and, only when present, the
// backoff; the nil receiver must still yield a non-empty string.
func TestRelayProvisionRateLimitErrorMessage(t *testing.T) {
	withRA := &RelayProvisionRateLimitError{RetryAfter: 90 * time.Second}
	if got := withRA.Error(); !strings.Contains(got, "429") || !strings.Contains(got, "1m30s") {
		t.Fatalf("Error() with RetryAfter = %q, want 429 + 1m30s", got)
	}
	noRA := &RelayProvisionRateLimitError{}
	if got := noRA.Error(); !strings.Contains(got, "429") || strings.Contains(got, "1m30s") {
		t.Fatalf("Error() without RetryAfter = %q, want plain 429", got)
	}
	var nilErr *RelayProvisionRateLimitError
	if got := nilErr.Error(); got == "" {
		t.Fatalf("nil receiver Error() must be non-empty")
	}
}

// Exercise every parseRelayProvisionRetryAfter branch: absent, non-positive seconds,
// unparseable, integer seconds, and the HTTP-date path (future, past, beyond the cap).
func TestParseRelayProvisionRetryAfterBranches(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	cases := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"empty", "", 0},
		{"whitespace", "   ", 0},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"garbage", "not-a-date", 0},
		{"seconds", "120", 2 * time.Minute},
		{"httpdate-future", now.Add(3 * time.Minute).Format(http.TimeFormat), 3 * time.Minute},
		{"httpdate-past", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"httpdate-beyond-max", now.Add(48 * time.Hour).Format(http.TimeFormat), relayProvisionMaxRetryAfter},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRelayProvisionRetryAfter(tc.raw, now); got != tc.want {
				t.Fatalf("parse(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// An unexpected status (e.g. 500) is a retryable failure whose error carries only the
// status code, never the untrusted response body.
func TestProvisionRelaySecretUnexpectedStatus(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusInternalServerError, `{"error":"boom-secret-detail"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("500 must fail: got (%v,%v)", provisioned, err)
	}
	if strings.Contains(err.Error(), "boom-secret-detail") {
		t.Fatalf("error leaked the response body: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error should carry the status code, got %v", err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("500 must persist nothing, got %q", loaded)
	}
}

// A 201 with a truncated JSON body fails the decode without persisting anything.
func TestProvisionRelaySecretBad201JSON(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("bad 201 JSON must fail: got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("bad 201 must persist nothing, got %q", loaded)
	}
}

// A transport failure (server closed before the call) is a retryable error and persists
// nothing.
func TestProvisionRelaySecretTransportError(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"`+provisionTestSecret+`"}`, http.StatusOK, &capt)
	url := server.URL
	server.Close() // connection refused on the next dial

	provisioned, err := ProvisionRelaySecret(context.Background(), url, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("transport error must fail: got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("transport error must persist nothing, got %q", loaded)
	}
}

// When baseDir is an existing regular file the identity lock cannot be created, so the
// provisioner fails before any network call.
func TestProvisionRelaySecretLockFailureOnFileBaseDir(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	fileBaseDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileBaseDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"`+provisionTestSecret+`"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", fileBaseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("file baseDir must fail at the lock step: got (%v,%v)", provisioned, err)
	}
	if capt.provisionHits != 0 {
		t.Fatalf("lock failure must not contact the server, got %d", capt.provisionHits)
	}
}

// A real read error on the under-lock re-check (the secret path is a directory, not a
// file) aborts before any network call, rather than being swallowed as "no secret".
func TestProvisionRelaySecretLoadErrorUnderLock(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	if err := os.MkdirAll(identity.NotifySecretPath(baseDir), 0o755); err != nil {
		t.Fatalf("make secret path a directory: %v", err)
	}
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusCreated, `{"notify_secret":"`+provisionTestSecret+`"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("a re-check load error must fail: got (%v,%v)", provisioned, err)
	}
	if capt.provisionHits != 0 {
		t.Fatalf("a load error must abort before contacting the server, got %d", capt.provisionHits)
	}
}
