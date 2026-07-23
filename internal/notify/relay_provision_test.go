package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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

// Mandatory contract 7: a 429 persists nothing and is a retryable failure.
func TestProvisionRelaySecretRateLimitedRetryable(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capt relayCapture
	server := relayProvisionServer(t, http.StatusTooManyRequests, `{"error":"RELAY_RATE_LIMITED"}`, http.StatusOK, &capt)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "1234567890123456", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("429 must be a retryable failure: got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("429 must persist nothing, got %q", loaded)
	}
	if capt.confirmHits != 0 {
		t.Fatalf("429 -> no confirm, got %d", capt.confirmHits)
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
