package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestNewGotifyNotifierValidation(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	_, err := NewGotifyNotifier(GotifyConfig{Enabled: true, ServerURL: "", Token: ""}, logger)
	if err == nil {
		t.Fatal("expected error for missing ServerURL/Token when enabled")
	}

	notifier, err := NewGotifyNotifier(GotifyConfig{
		Enabled:   true,
		ServerURL: "https://gotify.example",
		Token:     "token",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notifier.config.PrioritySuccess != 2 || notifier.config.PriorityWarning != 5 || notifier.config.PriorityFailure != 8 {
		t.Fatalf("default priorities not set correctly: %+v", notifier.config)
	}
}

func TestGotifySendDisabled(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, err := NewGotifyNotifier(GotifyConfig{Enabled: false}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false when disabled")
	}
}

func TestGotifyName(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, _ := NewGotifyNotifier(GotifyConfig{Enabled: false}, logger)

	if notifier.Name() != "Gotify" {
		t.Errorf("Name() = %q, want %q", notifier.Name(), "Gotify")
	}
}

func TestGotifyIsEnabled(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	// Test disabled
	notifier1, _ := NewGotifyNotifier(GotifyConfig{Enabled: false}, logger)
	if notifier1.IsEnabled() {
		t.Error("IsEnabled() should return false when disabled")
	}

	// Test enabled
	notifier2, _ := NewGotifyNotifier(GotifyConfig{
		Enabled:   true,
		ServerURL: "https://gotify.example",
		Token:     "token",
	}, logger)
	if !notifier2.IsEnabled() {
		t.Error("IsEnabled() should return true when enabled")
	}
}

func TestGotifyIsCritical(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, _ := NewGotifyNotifier(GotifyConfig{Enabled: false}, logger)

	if notifier.IsCritical() {
		t.Error("IsCritical() should return false for Gotify notifier")
	}
}

func TestGotifySendSuccessAndFailure(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	bodySeen := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/message") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		payload := gotifyMessage{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		bodySeen = payload.Title != "" && payload.Message != "" && payload.Priority > 0
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier, err := NewGotifyNotifier(GotifyConfig{
		Enabled:   true,
		ServerURL: server.URL,
		Token:     "token123",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	notifier.client = server.Client()

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	if !result.Success || !bodySeen {
		t.Fatalf("expected success and body to be sent, got success=%v bodySeen=%v", result.Success, bodySeen)
	}

	// Now force server to return 500 to trigger failure path.
	serverFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("fail"))
	}))
	defer serverFail.Close()

	notifierFail, _ := NewGotifyNotifier(GotifyConfig{
		Enabled:   true,
		ServerURL: serverFail.URL,
		Token:     "token123",
	}, logger)
	notifierFail.client = serverFail.Client()

	failResult, err := notifierFail.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("expected nil error on failure path, got %v", err)
	}
	if failResult.Success {
		t.Fatalf("expected Success=false when server returns error")
	}
	if status, ok := failResult.Metadata["status_code"]; !ok || status.(int) != http.StatusInternalServerError {
		t.Fatalf("expected status_code metadata with 500, got %+v", failResult.Metadata)
	}
}
