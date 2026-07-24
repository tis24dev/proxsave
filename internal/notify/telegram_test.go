package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewTelegramNotifierValidation(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	_, err := NewTelegramNotifier(TelegramConfig{Enabled: true, Mode: "invalid"}, logger)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}

	_, err = NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "",
		ChatID:   "",
	}, logger)
	if err == nil {
		t.Fatal("expected error for missing personal credentials")
	}

	_, err = NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123:short",
		ChatID:   "abc",
	}, logger)
	if err == nil {
		t.Fatal("expected error for invalid token/chat formats")
	}

	_, err = NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "",
		ServerID:      "",
	}, logger)
	if err == nil {
		t.Fatal("expected error for missing centralized config")
	}

	_, err = NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error for valid config: %v", err)
	}
}

func TestTelegramName(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, _ := NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
	}, logger)

	if notifier.Name() != "Telegram" {
		t.Errorf("Name() = %q, want %q", notifier.Name(), "Telegram")
	}
}

func TestTelegramIsEnabled(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	// Test disabled
	notifier1, _ := NewTelegramNotifier(TelegramConfig{Enabled: false}, logger)
	if notifier1 != nil && notifier1.IsEnabled() {
		t.Error("IsEnabled() should return false when disabled")
	}

	// Test enabled
	notifier2, _ := NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
	}, logger)
	if !notifier2.IsEnabled() {
		t.Error("IsEnabled() should return true when enabled")
	}
}

func TestTelegramIsCritical(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, _ := NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
	}, logger)

	if notifier.IsCritical() {
		t.Error("IsCritical() should return false for Telegram notifier")
	}
}

func TestTelegramSendPersonal(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Host, "api.telegram.org") {
				t.Fatalf("unexpected host: %s", req.URL.Host)
			}
			if err := req.ParseForm(); err != nil {
				t.Fatalf("ParseForm error: %v", err)
			}
			if req.Form.Get("chat_id") != "123456" {
				t.Fatalf("chat_id missing or wrong: %s", req.Form.Get("chat_id"))
			}
			if req.Form.Get("text") == "" {
				t.Fatal("text payload missing")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
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
}

func TestTelegramBuildMessageIncludesUpdateInfo(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:  true,
		Mode:     TelegramModePersonal,
		BotToken: "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
		ChatID:   "123456",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error creating notifier: %v", err)
	}

	data := createTestNotificationData()
	data.NewVersionAvailable = true
	data.CurrentVersion = "0.0.0-dev"
	data.LatestVersion = "0.11.3"

	msg := notifier.buildMessage(data)

	if !strings.Contains(msg, "Update available") {
		t.Fatalf("expected message to contain update header, got: %s", msg)
	}
	if !strings.Contains(msg, "New version: 0.11.3 (current: 0.0.0-dev)") {
		t.Fatalf("expected message to contain version details, got: %s", msg)
	}
}

func TestTelegramSendCentralized(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.Host, "central.test"):
				body := `{"bot_token":"123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz","chat_id":"987654","status":200}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			case strings.Contains(req.URL.Host, "api.telegram.org"):
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
				}, nil
			default:
				t.Fatalf("unexpected host: %s", req.URL.Host)
				return nil, nil
			}
		}),
	}

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "https://central.test",
		ServerID:      "server-123",
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
}

func TestTelegramSendCentralizedRelay(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	const secret = "relay-secret-value-123456"
	var relayCalls int
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "api.telegram.org") {
				t.Fatalf("api.telegram.org must never be contacted in relay mode")
			}
			if req.Method != http.MethodPost || req.URL.Path != "/api/notify" {
				t.Fatalf("unexpected request: %s %s", req.Method, req.URL.Path)
			}
			if got := req.Header.Get("X-Server-Auth"); got != secret {
				t.Fatalf("missing/incorrect X-Server-Auth header: %q", got)
			}
			bodyBytes, _ := io.ReadAll(req.Body)
			var payload struct {
				ServerID string `json:"server_id"`
				Message  string `json:"message"`
			}
			if err := json.Unmarshal(bodyBytes, &payload); err != nil {
				t.Fatalf("invalid JSON body: %v", err)
			}
			if payload.ServerID != "server-123" {
				t.Fatalf("server_id = %q, want server-123", payload.ServerID)
			}
			if payload.Message == "" {
				t.Fatalf("message must not be empty")
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
		NotifySecret:  secret,
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

func TestTelegramRelayFallbackWhenNoSecret(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	var sawGetChatID, sawTelegram bool
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/notify" {
				t.Fatalf("relay must not be used when NotifySecret is empty")
			}
			switch {
			case strings.Contains(req.URL.Host, "central.test"):
				sawGetChatID = true
				body := `{"bot_token":"123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz","chat_id":"987654","status":200}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			case strings.Contains(req.URL.Host, "api.telegram.org"):
				sawTelegram = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
				}, nil
			default:
				t.Fatalf("unexpected host: %s", req.URL.Host)
				return nil, nil
			}
		}),
	}

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "https://central.test",
		ServerID:      "server-123",
		NotifySecret:  "",
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
	if !sawGetChatID || !sawTelegram {
		t.Fatalf("legacy path not exercised: getChatID=%v telegram=%v", sawGetChatID, sawTelegram)
	}
}

func TestTelegramRelayErrorRedactsSecret(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	const secret = "relay-secret-value-redaction-1234"

	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// Server echoes the secret in an error body; the client must scrub it.
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("internal error: " + secret)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "https://central.test",
		ServerID:      "server-123",
		NotifySecret:  secret,
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	notifier.client = client

	_, _, relayErr := notifier.sendViaRelay(context.Background(), "hello", "notify-red-1")
	if relayErr == nil {
		t.Fatalf("expected an error from the relay")
	}
	if strings.Contains(relayErr.Error(), secret) {
		t.Fatalf("error string leaked the NotifySecret: %q", relayErr.Error())
	}
}

func TestTelegramRelayParkedReturnsSentinel(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusGone,
				Body:       io.NopCloser(strings.NewReader(`{"error":"SERVER_PARKED"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	notifier, err := NewTelegramNotifier(TelegramConfig{
		Enabled:       true,
		Mode:          TelegramModeCentralized,
		ServerAPIHost: "https://central.test",
		ServerID:      "server-123",
		NotifySecret:  "relay-secret-value-parked-000001",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	notifier.client = client
	status, _, relayErr := notifier.sendViaRelay(context.Background(), "hello", "notify-parked-1")
	if status != http.StatusGone {
		t.Fatalf("status = %d, want 410", status)
	}
	if !errors.Is(relayErr, errRelayParked) {
		t.Fatalf("err = %v, want errRelayParked", relayErr)
	}
}
