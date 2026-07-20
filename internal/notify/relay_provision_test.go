package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/identity"
)

// These tests reuse routingSecretServer, provisionCapture, provisionTestSecret and
// newProvisionTestLogger from telegram_registration_provision_test.go (same package).

func TestProvisionRelaySecretHappyPath(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capture provisionCapture
	server := routingSecretServer(t, `{"notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", baseDir, logger)
	if err != nil || !provisioned {
		t.Fatalf("provisioned=%v err=%v, want true/nil", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != provisionTestSecret {
		t.Fatalf("persisted secret = %q, want %q", loaded, provisionTestSecret)
	}
	if capture.provisionHeader != "1" {
		t.Fatalf("X-Proxsave-Provision = %q, want 1", capture.provisionHeader)
	}
	if capture.confirmHits != 1 {
		t.Fatalf("confirm hits = %d, want 1", capture.confirmHits)
	}
	if capture.confirmAuth != provisionTestSecret {
		t.Fatalf("confirm X-Server-Auth = %q, want %q", capture.confirmAuth, provisionTestSecret)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into logs:\n%s", buf.String())
	}
}

func TestProvisionRelaySecretConfirmFailureIsNonFatal(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capture provisionCapture
	server := routingSecretServer(t, `{"notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusForbidden, &capture)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", baseDir, logger)
	if err != nil || !provisioned {
		t.Fatalf("confirm 403 must stay provisioned: provisioned=%v err=%v", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != provisionTestSecret {
		t.Fatalf("secret must persist despite confirm failure, got %q", loaded)
	}
	if capture.confirmHits != 1 {
		t.Fatalf("confirm hits = %d, want 1", capture.confirmHits)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into logs:\n%s", buf.String())
	}
}

func TestProvisionRelaySecretNoTokenNoPersist(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capture provisionCapture
	server := routingSecretServer(t, `{"status":200}`, http.StatusOK, &capture)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", baseDir, logger)
	if provisioned || err != nil {
		t.Fatalf("no token: want (false,nil), got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("nothing must be persisted, got %q", loaded)
	}
	if capture.confirmHits != 0 {
		t.Fatalf("no token -> no confirm, got %d", capture.confirmHits)
	}
}

func TestProvisionRelaySecretShortSecretRefused(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	var capture provisionCapture
	// "abc" is 3 runes (< identity.NotifySecretMinLen) and would NOT be masked in logs.
	server := routingSecretServer(t, `{"notify_secret":"abc","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("short secret must be refused: got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("short secret must NOT be persisted, got %q", loaded)
	}
	if _, statErr := os.Stat(identity.NotifySecretPath(baseDir)); !os.IsNotExist(statErr) {
		t.Fatalf("secret file must be absent, got err=%v", statErr)
	}
	if capture.confirmHits != 0 {
		t.Fatalf("short secret -> no confirm, got %d", capture.confirmHits)
	}
}

func TestProvisionRelaySecretEmptyBaseDir(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	var capture provisionCapture
	server := routingSecretServer(t, `{"notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", "", logger)
	if provisioned || err == nil {
		t.Fatalf("empty baseDir must fail: got (%v,%v)", provisioned, err)
	}
	if capture.confirmHits != 0 {
		t.Fatalf("empty baseDir -> no confirm, got %d", capture.confirmHits)
	}
}

func TestProvisionRelaySecretAdoptsExistingSecretUnderLock(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	// A secret is already on disk (e.g. a concurrent minter won the race). The provisioner
	// must adopt it under the lock and NOT contact the server nor mint a competing secret.
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

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", baseDir, logger)
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

func TestProvisionRelaySecretNon200(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()
	confirmHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/get-chat-id":
			w.WriteHeader(http.StatusConflict) // 409: no relay secret issued (chat-less + server pre-change)
			_, _ = w.Write([]byte("SERVER_NON_ASSOCIATO"))
		case "/api/confirm-secret":
			confirmHits++
			t.Errorf("confirm must not be hit on a non-200 handshake")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provisioned, err := ProvisionRelaySecret(context.Background(), server.URL, "server-123", baseDir, logger)
	if provisioned || err == nil {
		t.Fatalf("409 must fail: got (%v,%v)", provisioned, err)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("nothing persisted on 409, got %q", loaded)
	}
	if confirmHits != 0 {
		t.Fatalf("no confirm on 409, got %d", confirmHits)
	}
}
