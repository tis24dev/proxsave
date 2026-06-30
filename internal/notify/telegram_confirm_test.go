package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/version"
)

// TestConfirmTelegramRelaySecretPostsTokenAndVersion verifies the phase-2 POST
// carries the token in X-Server-Auth, the version header, and a JSON body with
// the server_id, returns nil on 200, and never logs the plaintext token.
func TestConfirmTelegramRelaySecretPostsTokenAndVersion(t *testing.T) {
	logger, buf := newProvisionTestLogger()

	var gotAuth, gotVersion, gotBody, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("X-Server-Auth")
		gotVersion = r.Header.Get("X-Proxsave-Version")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	err := confirmTelegramRelaySecret(context.Background(), http.DefaultClient, server.URL, "server-123", provisionTestSecret, logger)
	if err != nil {
		t.Fatalf("confirmTelegramRelaySecret error = %v, want nil", err)
	}
	if gotPath != "/api/confirm-secret" {
		t.Fatalf("path = %q, want /api/confirm-secret", gotPath)
	}
	if gotAuth != provisionTestSecret {
		t.Fatalf("X-Server-Auth = %q, want %q", gotAuth, provisionTestSecret)
	}
	if gotVersion != version.String() {
		t.Fatalf("X-Proxsave-Version = %q, want %q", gotVersion, version.String())
	}
	if !strings.Contains(gotBody, `"server_id":"server-123"`) {
		t.Fatalf("body = %q, want server_id server-123", gotBody)
	}
	if strings.Contains(buf.String(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into log output:\n%s", buf.String())
	}
}

// TestConfirmTelegramRelaySecretNonFatalOnReject verifies a rejecting status
// yields a non-nil error (the caller treats it as non-fatal) without panicking.
func TestConfirmTelegramRelaySecretNonFatalOnReject(t *testing.T) {
	logger, _ := newProvisionTestLogger()

	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		client := &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: code,
					Body:       io.NopCloser(strings.NewReader(`{"error":"nope"}`)),
					Header:     make(http.Header),
				}, nil
			}),
		}
		err := confirmTelegramRelaySecret(context.Background(), client, "https://central.test", "server-123", provisionTestSecret, logger)
		if err == nil {
			t.Fatalf("expected error for HTTP %d, got nil", code)
		}
	}
}

// TestConfirmTelegramRelaySecretRedactsSecretInError verifies a transport error
// that echoes the secret is masked before being returned.
func TestConfirmTelegramRelaySecretRedactsSecretInError(t *testing.T) {
	logger, _ := newProvisionTestLogger()

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial failed leaking %s in the message", provisionTestSecret)
		}),
	}
	err := confirmTelegramRelaySecret(context.Background(), client, "https://central.test", "server-123", provisionTestSecret, logger)
	if err == nil {
		t.Fatalf("expected a transport error, got nil")
	}
	if strings.Contains(err.Error(), provisionTestSecret) {
		t.Fatalf("plaintext secret leaked into returned error: %v", err)
	}
	if !strings.Contains(err.Error(), logging.MaskSecret(provisionTestSecret)) {
		t.Fatalf("masked secret not found in returned error: %v", err)
	}
}

// TestConfirmTelegramRelaySecretEmptyInputs verifies empty secret or serverID
// short-circuits with an error and never issues an HTTP request.
func TestConfirmTelegramRelaySecretEmptyInputs(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			t.Fatalf("no HTTP request must be made for empty inputs; got %s", req.URL.Path)
			return nil, nil
		}),
	}

	if err := confirmTelegramRelaySecret(context.Background(), client, "https://central.test", "server-123", "", logger); err == nil {
		t.Fatalf("expected error for empty secret, got nil")
	}
	if err := confirmTelegramRelaySecret(context.Background(), client, "https://central.test", "", provisionTestSecret, logger); err == nil {
		t.Fatalf("expected error for empty serverID, got nil")
	}
}
