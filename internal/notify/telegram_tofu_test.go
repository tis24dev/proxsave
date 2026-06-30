package notify

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestSendCentralizedTOFUProvisionsThenRelays verifies the one-time provisioning:
// get-chat-id rides a fresh per-server secret back, the client persists it into
// the immutable identity file, and delivers THIS run through the relay (never the
// bot token to api.telegram.org).
func TestSendCentralizedTOFUProvisionsThenRelays(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()
	baseDir := t.TempDir()

	const secret = "3h64-dyi8-q3d6-wcm5"
	const botToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	var getChatIDCalls, relayCalls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "api.telegram.org") {
				t.Fatalf("api.telegram.org must never be contacted once a relay secret is provisioned")
			}
			switch req.URL.Path {
			case "/api/get-chat-id":
				getChatIDCalls++
				body := `{"chat_id":"123","bot_token":"` + botToken + `","notify_secret":"` + secret + `","status":200}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			case "/api/notify":
				if got := req.Header.Get("X-Server-Auth"); got != secret {
					t.Fatalf("relay X-Server-Auth = %q, want %q", got, secret)
				}
				relayCalls++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
					Header:     make(http.Header),
				}, nil
			default:
				t.Fatalf("unexpected request path: %s", req.URL.Path)
				return nil, nil
			}
		}),
	}

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "https://central.test",
		ServerID:      "server-123",
		BaseDir:       baseDir,
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	notifier.client = client

	result, err := notifier.Send(context.Background(), data)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if getChatIDCalls != 1 {
		t.Fatalf("expected exactly 1 get-chat-id call, got %d", getChatIDCalls)
	}
	if relayCalls != 1 {
		t.Fatalf("expected exactly 1 relay call, got %d", relayCalls)
	}

	persisted, err := identity.LoadNotifySecret(baseDir)
	if err != nil {
		t.Fatalf("LoadNotifySecret() error = %v", err)
	}
	if persisted != secret {
		t.Fatalf("persisted secret = %q, want %q", persisted, secret)
	}
}

// TestSendCentralizedUsesPersistedSecretWithoutFetch verifies that once a secret
// is persisted, Send adopts it lazily and goes straight to the relay, never
// hitting get-chat-id.
func TestSendCentralizedUsesPersistedSecretWithoutFetch(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()
	baseDir := t.TempDir()

	const secret = "abcd-efgh-ijkl-mnop"
	if err := identity.PersistNotifySecret(context.Background(), baseDir, secret, logger); err != nil {
		t.Fatalf("PersistNotifySecret() error = %v", err)
	}

	var relayCalls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "api.telegram.org") {
				t.Fatalf("api.telegram.org must never be contacted in relay mode")
			}
			if req.URL.Path == "/api/get-chat-id" {
				t.Fatalf("get-chat-id must not be fetched once a secret is persisted")
			}
			if req.URL.Path != "/api/notify" {
				t.Fatalf("unexpected request path: %s", req.URL.Path)
			}
			if got := req.Header.Get("X-Server-Auth"); got != secret {
				t.Fatalf("relay X-Server-Auth = %q, want %q", got, secret)
			}
			relayCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "https://central.test",
		ServerID:      "server-123",
		BaseDir:       baseDir,
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	notifier.client = client

	result, err := notifier.Send(context.Background(), data)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if relayCalls != 1 {
		t.Fatalf("expected exactly 1 relay call, got %d", relayCalls)
	}
}
