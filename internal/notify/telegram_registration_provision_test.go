package notify

import (
	"bytes"
	"context"
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

// newProvisionTestLogger returns a debug logger writing into the returned buffer
// so tests can assert on (and against) the captured log output.
func newProvisionTestLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	return logger, buf
}

// secretServer returns a get-chat-id stub that answers 200 with the given JSON
// body and records the X-Proxsave-Version header it received.
func secretServer(t *testing.T, body string, captured *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			*captured = r.Header.Get("X-Proxsave-Version")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
}

func TestCheckTelegramRegistrationAndProvisionPersistsSecretAndMasksLog(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	baseDir := t.TempDir()

	var capturedVersion string
	server := secretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, &capturedVersion)
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

	if capturedVersion != version.String() {
		t.Fatalf("X-Proxsave-Version = %q, want %q", capturedVersion, version.String())
	}
}

func TestCheckTelegramRegistrationAndProvisionNoSecretNoPersist(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()

	server := secretServer(t, `{"chat_id":"123","status":200}`, nil)
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
}

func TestCheckTelegramRegistrationAndProvisionEmptyBaseDir(t *testing.T) {
	logger, _ := newProvisionTestLogger()

	server := secretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, nil)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", "", logger)

	if status.Code != 200 {
		t.Fatalf("status.Code = %d, want 200", status.Code)
	}
	if loaded, _ := identity.LoadNotifySecret(""); loaded != "" {
		t.Fatalf("expected no persisted secret for empty baseDir, got %q", loaded)
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

	server := secretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, nil)
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
}

func TestCheckTelegramRegistrationAndProvisionIdempotent(t *testing.T) {
	logger, _ := newProvisionTestLogger()
	baseDir := t.TempDir()

	const seeded = "abcd-efgh-ijkl-mnop"
	if err := identity.PersistNotifySecret(context.Background(), baseDir, seeded, logger); err != nil {
		t.Fatalf("seed PersistNotifySecret: %v", err)
	}

	server := secretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, nil)
	defer server.Close()

	status := CheckTelegramRegistrationAndProvision(context.Background(), server.URL, "server-123", baseDir, logger)

	if status.Code != 200 {
		t.Fatalf("status.Code = %d, want 200", status.Code)
	}
	loaded, err := identity.LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret error: %v", err)
	}
	if loaded != seeded {
		t.Fatalf("seeded secret was clobbered: got %q, want %q", loaded, seeded)
	}
}

// TestCheckTelegramRegistrationStatusCheckMasksSecretNoPersist guards the latent
// body-preview leak through the unchanged wrapper: even the legacy status-only
// path registers the secret with the logger (so it is masked) and never persists
// it, since no baseDir is threaded through that signature.
func TestCheckTelegramRegistrationStatusCheckMasksSecretNoPersist(t *testing.T) {
	logger, buf := newProvisionTestLogger()
	untouched := t.TempDir()

	server := secretServer(t, `{"chat_id":"123","notify_secret":"`+provisionTestSecret+`","status":200}`, nil)
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
}
