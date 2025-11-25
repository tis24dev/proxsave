package notify

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
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
