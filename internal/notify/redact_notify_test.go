package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestGotifySendRedactsTokenOnTransportError (H11) verifies the Gotify token is
// not leaked into the transport-error returned by Send, in either its raw or
// URL-encoded form (it rides in the URL query, so *url.Error carries it encoded).
func TestGotifySendRedactsTokenOnTransportError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close() // closed -> client.Do fails with connection refused

	const token = "tok+en/val=secret123"
	notifier, err := NewGotifyNotifier(GotifyConfig{Enabled: true, ServerURL: serverURL, Token: token}, logger)
	if err != nil {
		t.Fatalf("new gotify: %v", err)
	}
	notifier.client = &http.Client{Timeout: 2 * time.Second}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected a transport failure, got success=%v err=%v", result.Success, result.Error)
	}

	msg := result.Error.Error()
	if strings.Contains(msg, token) {
		t.Fatalf("raw token leaked in error: %q", msg)
	}
	if enc := url.QueryEscape(token); strings.Contains(msg, enc) {
		t.Fatalf("URL-encoded token leaked in error: %q", msg)
	}
}
