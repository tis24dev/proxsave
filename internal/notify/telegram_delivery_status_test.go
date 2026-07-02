package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func dsTestLogger() *logging.Logger {
	l := logging.New(types.LogLevelDebug, false)
	l.SetOutput(io.Discard)
	return l
}

func dsJSONResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// notifyID "" -> zero jitter, so these unit tests are fast and deterministic.
func dsFastPollCfg() deliveryPollConfig {
	return deliveryPollConfig{Enabled: true, Timeout: 300 * time.Millisecond, Interval: 2 * time.Millisecond, InitialDelay: 0}
}

func TestPollDeliveryStatusDelivered(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/notify/status" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		if req.Header.Get("X-Server-Auth") != "sek" {
			t.Fatalf("missing/incorrect X-Server-Auth: %q", req.Header.Get("X-Server-Auth"))
		}
		if strings.Contains(req.URL.Host, "api.telegram.org") {
			t.Fatalf("must never contact api.telegram.org")
		}
		return dsJSONResp(200, `{"state":"delivered","telegram_message_id":4242,"attempts":1}`), nil
	})}
	ds := pollTelegramDeliveryStatus(context.Background(), client, "https://c.test", "srv", "sek", "", dsFastPollCfg(), dsTestLogger())
	if ds.State != "delivered" || ds.MessageID != 4242 {
		t.Fatalf("got %+v, want delivered/4242", ds)
	}
}

func TestPollDeliveryStatusFailed(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return dsJSONResp(200, `{"state":"failed","reason":"http_403","attempts":1}`), nil
	})}
	ds := pollTelegramDeliveryStatus(context.Background(), client, "https://c.test", "srv", "sek", "", dsFastPollCfg(), dsTestLogger())
	if ds.State != "failed" || ds.Reason != "http_403" {
		t.Fatalf("got %+v, want failed/http_403", ds)
	}
}

func TestPollDeliveryStatusPendingTimesOutAsQueued(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return dsJSONResp(200, `{"state":"pending","attempts":0}`), nil
	})}
	cfg := deliveryPollConfig{Enabled: true, Timeout: 40 * time.Millisecond, Interval: 5 * time.Millisecond, InitialDelay: 0}
	ds := pollTelegramDeliveryStatus(context.Background(), client, "https://c.test", "srv", "sek", "", cfg, dsTestLogger())
	if ds.State != "pending" {
		t.Fatalf("got %+v, want pending (queued)", ds)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("expected multiple polls before timeout, got %d", calls)
	}
}

func TestPollDeliveryStatus404IsUnknownAndDoesNotRetry(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return dsJSONResp(404, `{"error":"NOTIFY_NOT_FOUND"}`), nil
	})}
	ds := pollTelegramDeliveryStatus(context.Background(), client, "https://c.test", "srv", "sek", "", dsFastPollCfg(), dsTestLogger())
	if ds.State != "unknown" {
		t.Fatalf("got %+v, want unknown", ds)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("404 must not be retried, got %d calls", got)
	}
}

func TestPollDeliveryStatus401IsUnknownAndDoesNotReprovision(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return dsJSONResp(401, `{"error":"AUTH_REQUIRED"}`), nil
	})}
	ds := pollTelegramDeliveryStatus(context.Background(), client, "https://c.test", "srv", "stale", "", dsFastPollCfg(), dsTestLogger())
	if ds.State != "unknown" {
		t.Fatalf("got %+v, want unknown", ds)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("401 must stop immediately, got %d calls", got)
	}
}

func TestPollDeliveryStatusTransientErrorRetriesThenDelivers(t *testing.T) {
	var calls int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return nil, errors.New("connection refused") // transient
		}
		return dsJSONResp(200, `{"state":"delivered","telegram_message_id":7}`), nil
	})}
	ds := pollTelegramDeliveryStatus(context.Background(), client, "https://c.test", "srv", "sek", "", dsFastPollCfg(), dsTestLogger())
	if ds.State != "delivered" || ds.MessageID != 7 {
		t.Fatalf("got %+v, want delivered/7", ds)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("expected a retry after the transient error, got %d", calls)
	}
}

func TestPollDeliveryStatusRespectsContextCancel(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return dsJSONResp(200, `{"state":"pending"}`), nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	cfg := deliveryPollConfig{Enabled: true, Timeout: time.Second, Interval: 5 * time.Millisecond, InitialDelay: 10 * time.Millisecond}
	ds := pollTelegramDeliveryStatus(ctx, client, "https://c.test", "srv", "sek", "notify-x", cfg, dsTestLogger())
	if ds.State != "unknown" {
		t.Fatalf("got %+v, want unknown on cancel", ds)
	}
}

// --- Send() integration: relay 202 + poll ---

func dsRelayNotifier(t *testing.T, client *http.Client, confirm bool) *TelegramNotifier {
	t.Helper()
	n, err := NewTelegramNotifier(TelegramConfig{
		Enabled:         true,
		Mode:            TelegramModeCentralized,
		ServerAPIHost:   "https://c.test",
		ServerID:        "server-123",
		NotifySecret:    "sek",
		ConfirmDelivery: confirm,
		ConfirmTimeout:  300 * time.Millisecond,
		ConfirmInterval: 2 * time.Millisecond,
	}, dsTestLogger())
	if err != nil {
		t.Fatalf("notifier: %v", err)
	}
	n.client = client
	return n
}

func TestSendRelay202ThenPollDelivered(t *testing.T) {
	var notifyID string
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/notify":
			notifyID = req.Header.Get("X-Notify-Id")
			if notifyID == "" {
				t.Fatalf("relay POST missing X-Notify-Id")
			}
			return dsJSONResp(202, `{"status":"accepted","notify_id":"`+notifyID+`"}`), nil
		case "/api/notify/status":
			if req.URL.Query().Get("notify_id") != notifyID {
				t.Fatalf("status poll notify_id=%q, want %q", req.URL.Query().Get("notify_id"), notifyID)
			}
			return dsJSONResp(200, `{"state":"delivered","telegram_message_id":99}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, nil
		}
	})}
	result, err := dsRelayNotifier(t, client, true).Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !result.Success {
		t.Fatalf("want Success=true")
	}
	if result.Metadata["relay_accepted"] != true {
		t.Fatalf("relay_accepted=%v", result.Metadata["relay_accepted"])
	}
	if result.Metadata["telegram_state"] != "delivered" {
		t.Fatalf("telegram_state=%v", result.Metadata["telegram_state"])
	}
	if result.Metadata["telegram_message_id"] != int64(99) {
		t.Fatalf("telegram_message_id=%v", result.Metadata["telegram_message_id"])
	}
}

func TestSendRelay202PollFailedKeepsSuccessTrue(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/notify":
			return dsJSONResp(202, `{"status":"accepted"}`), nil
		case "/api/notify/status":
			return dsJSONResp(200, `{"state":"failed","reason":"http_403"}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, nil
		}
	})}
	result, err := dsRelayNotifier(t, client, true).Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// A Telegram delivery failure must NOT flip the notification result: server
	// acceptance is the success signal.
	if !result.Success {
		t.Fatalf("Success must stay true even when Telegram delivery failed")
	}
	if result.Metadata["telegram_state"] != "failed" {
		t.Fatalf("telegram_state=%v, want failed", result.Metadata["telegram_state"])
	}
	if result.Metadata["telegram_reason"] != "http_403" {
		t.Fatalf("telegram_reason=%v", result.Metadata["telegram_reason"])
	}
}

func TestSendRelay202NoPollWhenConfirmDisabled(t *testing.T) {
	var statusCalls int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/notify":
			return dsJSONResp(202, `{"status":"accepted"}`), nil
		case "/api/notify/status":
			atomic.AddInt32(&statusCalls, 1)
			return dsJSONResp(200, `{"state":"delivered"}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, nil
		}
	})}
	result, err := dsRelayNotifier(t, client, false).Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !result.Success {
		t.Fatalf("want Success=true")
	}
	if got := atomic.LoadInt32(&statusCalls); got != 0 {
		t.Fatalf("ConfirmDelivery=false must NOT poll, got %d status calls", got)
	}
	if result.Metadata["telegram_state"] != "pending" {
		t.Fatalf("telegram_state=%v, want pending", result.Metadata["telegram_state"])
	}
}

func TestSendRelay200SyncLegacySkipsPoll(t *testing.T) {
	var statusCalls int32
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/notify":
			return dsJSONResp(200, `{"status":"ok"}`), nil // sync legacy (killswitch off)
		case "/api/notify/status":
			atomic.AddInt32(&statusCalls, 1)
			return dsJSONResp(200, `{"state":"delivered"}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, nil
		}
	})}
	result, err := dsRelayNotifier(t, client, true).Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := atomic.LoadInt32(&statusCalls); got != 0 {
		t.Fatalf("HTTP 200 (sync legacy) must NOT poll, got %d status calls", got)
	}
	if result.Metadata["telegram_state"] != "delivered" {
		t.Fatalf("telegram_state=%v, want delivered", result.Metadata["telegram_state"])
	}
}
