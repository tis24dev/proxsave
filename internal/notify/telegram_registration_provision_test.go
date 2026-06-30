package notify

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/version"
)

// provisionTestSecret is a realistic per-server relay secret matching the server
// format (lowercase alnum groups joined by hyphens) and longer than
// secretMinRegister, so the logger registers and masks it.
const provisionTestSecret = "3h64-dyi8-q3d6-wcm5"

// provisionCapture records what the routed stub server observed. The handshake
// is fully synchronous (the client blocks on each request), so the test reads
// these fields only after the call under test returns, mirroring the existing
// plain-variable capture pattern used elsewhere in this package.
type provisionCapture struct {
	getChatIDHits   int
	confirmHits     int
	version         string
	provisionHeader string
	confirmAuth     string
	confirmBody     string
}

// newProvisionTestLogger returns a debug logger writing into the returned buffer
// so tests can assert on (and against) the captured log output.
func newProvisionTestLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	return logger, buf
}

// routingSecretServer returns a stub that routes the get-chat-id handshake and
// the phase-2 confirm-secret POST, recording what it observed into capture.
// get-chat-id answers 200 with the given JSON body; confirm-secret answers with
// confirmStatus (use http.StatusOK for the happy path).
func routingSecretServer(t *testing.T, body string, confirmStatus int, capture *provisionCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/get-chat-id":
			capture.getChatIDHits++
			capture.version = r.Header.Get("X-Proxsave-Version")
			capture.provisionHeader = r.Header.Get("X-Proxsave-Provision")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		case "/api/confirm-secret":
			b, _ := io.ReadAll(r.Body)
			capture.confirmHits++
			capture.confirmAuth = r.Header.Get("X-Server-Auth")
			capture.confirmBody = string(b)
			w.WriteHeader(confirmStatus)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestCheckTelegramRegistrationAndProvisionPersistsSecretAndMasksLog(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", baseDir, logger)

	if status.Code != 200 || status.Error != nil {
		t.Fatalf("status = %+v, want Code=200 Error=nil", status)
	}

	loaded, err := identity.LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret error: %v", err)
	}
	if loaded != provisionTestSecret {
		t.Fatalf("persisted secret = %q, want %q", loaded, provisionTestSecret)
	}

	logged := buf.String()
	if !strings.Contains(logged, "bodyPreview") {
		t.Fatalf("expected the body-preview line to be logged, got:\n%s", logged)
	}
	if strings.Contains(logged, provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into log output:\n%s", logged)
	}
	if !strings.Contains(logged, logging.MaskSecret(provisionTestSecret)) {
		t.Fatalf("masked secret not found in log output:\n%s", logged)
	}

	if capture.version != version.String() {
		t.Fatalf("X-Proxsave-Version = %q, want %q", capture.version, version.String())
	}
	if capture.provisionHeader != "1" {
		t.Fatalf("X-Proxsave-Provision = %q, want %q", capture.provisionHeader, "1")
	}
	if capture.confirmHits != 1 {
		t.Fatalf("expected exactly 1 confirm-secret hit, got %d", capture.confirmHits)
	}
}

func TestCheckTelegramRegistrationAndProvisionNoSecretNoPersist(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", baseDir, logger)

	if status.Code != 200 {
		t.Fatalf("status.Code = %d, want 200", status.Code)
	}
	loaded, err := identity.LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret error: %v", err)
	}
	if loaded != "" {
		t.Fatalf("expected no persisted secret, got %q", loaded)
	}
	if _, err := os.Stat(identity.NotifySecretPath(baseDir)); !os.IsNotExist(err) {
		t.Fatalf("expected notify-secret file to be absent, got err=%v", err)
	}
	// No token -> nothing to confirm.
	if capture.confirmHits != 0 {
		t.Fatalf("expected no confirm-secret hit when no token issued, got %d", capture.confirmHits)
	}
}

func TestCheckTelegramRegistrationAndProvisionEmptyBaseDir(t *testing.T) {
	logger, _ := newProvisionTestLogger()

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", "", logger)

	if status.Code != 200 {
		t.Fatalf("status.Code = %d, want 200", status.Code)
	}
	if loaded, _ := identity.LoadNotifySecret(""); loaded != "" {
		t.Fatalf("expected no persisted secret for empty baseDir, got %q", loaded)
	}
	// Empty baseDir short-circuits before persist/confirm.
	if capture.confirmHits != 0 {
		t.Fatalf("expected no confirm-secret hit for empty baseDir, got %d", capture.confirmHits)
	}
}

func TestCheckTelegramRegistrationAndProvisionPersistFailureKeepsStatus(t *testing.T) {
	logger, buf := newProvisionTestLogger()

	// Make PersistNotifySecret's os.MkdirAll(baseDir/identity) fail by placing a
	// regular file where the parent directory would have to be created.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	baseDir := filepath.Join(blocker, "sub")

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", baseDir, logger)

	if status.Code != 200 || status.Error != nil {
		t.Fatalf("status = %+v, want Code=200 Error=nil despite persist failure", status)
	}
	if loaded, _ := identity.LoadNotifySecret(baseDir); loaded != "" {
		t.Fatalf("expected no persisted secret after failure, got %q", loaded)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into log output:\n%s", buf.String())
	}
	// Persist failed -> we must NOT confirm (server re-issues next run).
	if capture.confirmHits != 0 {
		t.Fatalf("expected no confirm-secret hit when persist failed, got %d", capture.confirmHits)
	}
}

// TestCheckTelegramRegistrationAndProvisionOverwritesAndConfirms verifies the
// adopt-on-token-present contract: a freshly issued token OVERWRITES any secret
// already on disk (no idempotent skip) and is confirmed back to the server.
func TestCheckTelegramRegistrationAndProvisionOverwritesAndConfirms(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()

	const seeded = "abcd-efgh-ijkl-mnop"
	if err := identity.PersistNotifySecret(context.Background(), baseDir, seeded, logger); err != nil {
		t.Fatalf("seed PersistNotifySecret: %v", err)
	}

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", baseDir, logger)

	if status.Code != 200 {
		t.Fatalf("status.Code = %d, want 200", status.Code)
	}
	loaded, err := identity.LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret error: %v", err)
	}
	// The seeded secret MUST be clobbered (opposite of the old idempotent test).
	if loaded != provisionTestSecret {
		t.Fatalf("re-issued secret was not adopted: got %q, want %q", loaded, provisionTestSecret)
	}
	if capture.confirmHits != 1 {
		t.Fatalf("expected exactly 1 confirm-secret hit, got %d", capture.confirmHits)
	}
	if capture.confirmAuth != provisionTestSecret {
		t.Fatalf("confirm X-Server-Auth = %q, want %q", capture.confirmAuth, provisionTestSecret)
	}
	if !strings.Contains(capture.confirmBody, `"server_id":"server-123"`) {
		t.Fatalf("confirm body = %q, want server_id server-123", capture.confirmBody)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into log output:\n%s", buf.String())
	}
}

// TestCheckTelegramRegistrationAndProvisionConfirmNonFatalOn403 verifies a 403
// from confirm-secret is non-fatal: the status stays 200, the issued secret is
// persisted, and nothing panics or leaks.
func TestCheckTelegramRegistrationAndProvisionConfirmNonFatalOn403(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusForbidden, &capture)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", baseDir, logger)

	if status.Code != 200 || status.Error != nil {
		t.Fatalf("status = %+v, want Code=200 Error=nil despite confirm 403", status)
	}
	loaded, err := identity.LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret error: %v", err)
	}
	if loaded != provisionTestSecret {
		t.Fatalf("persisted secret = %q, want %q", loaded, provisionTestSecret)
	}
	if capture.confirmHits != 1 {
		t.Fatalf("expected exactly 1 confirm-secret hit, got %d", capture.confirmHits)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into log output:\n%s", buf.String())
	}
}

// TestCheckTelegramRegistrationStatusCheckMasksSecretNoPersist guards the latent
// body-preview leak through the unchanged wrapper: even the legacy status-only
// path registers the secret with the logger (so it is masked) and never persists
// it, sends NO provision-intent header, and never confirms.
func TestCheckTelegramRegistrationStatusCheckMasksSecretNoPersist(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	untouched := t.TempDir()

	var capture provisionCapture
	server := routingSecretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, http.StatusOK, &capture)
	defer server.Close()

	status := CheckTelegramRegistration(context.Background(), server.URL, "server-123", logger)

	if status.Code != 200 {
		t.Fatalf("status.Code = %d, want 200", status.Code)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into log output on the status-only path:\n%s", buf.String())
	}
	if loaded, _ := identity.LoadNotifySecret(untouched); loaded != "" {
		t.Fatalf("status-only path must not persist anything, got %q", loaded)
	}
	// The bare status probe must not carry provision-intent and must not confirm.
	if capture.provisionHeader != "" {
		t.Fatalf("status probe sent X-Proxsave-Provision = %q, want empty", capture.provisionHeader)
	}
	if capture.confirmHits != 0 {
		t.Fatalf("status probe must not confirm, got %d hits", capture.confirmHits)
	}
}
