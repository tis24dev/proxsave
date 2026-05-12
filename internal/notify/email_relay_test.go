package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestGenerateHMACSignature(t *testing.T) {
	payload := []byte("payload")
	secret := "secret"

	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	expected := hex.EncodeToString(h.Sum(nil))

	if got := generateHMACSignature(payload, secret); got != expected {
		t.Fatalf("generateHMACSignature = %s, want %s", got, expected)
	}
}

func TestNormalizeRelayScriptVersion(t *testing.T) {
	tests := map[string]string{
		"":            "0.0.0",
		"dev":         "0.0.0",
		"v1.2.3":      "1.2.3",
		"1.2.3":       "1.2.3",
		"1.2":         "1.2.0",
		"0.0.0-dev":   "0.0.0",
		" 2.10.4+abc": "2.10.4",
	}

	for input, want := range tests {
		if got := normalizeRelayScriptVersion(input); got != want {
			t.Fatalf("normalizeRelayScriptVersion(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestSendViaCloudRelay_NormalizesScriptVersionHeader(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Script-Version")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	cfg := CloudRelayConfig{
		WorkerURL:   server.URL,
		WorkerToken: "token",
		HMACSecret:  "secret",
		Timeout:     5,
		MaxRetries:  0,
		RetryDelay:  0,
	}
	logger := logging.New(types.LogLevelDebug, false)
	err := sendViaCloudRelay(context.Background(), cfg, EmailRelayPayload{
		To:            "dest@test.invalid",
		Subject:       "subject",
		Report:        map[string]interface{}{"ok": true},
		Timestamp:     time.Now().Unix(),
		ServerMAC:     "00:11:22:33:44:55",
		ScriptVersion: "0.0.0-dev",
		ServerID:      "server-id",
	}, logger)
	if err != nil {
		t.Fatalf("sendViaCloudRelay() error = %v", err)
	}
	if gotHeader != "0.0.0" {
		t.Fatalf("X-Script-Version=%q, want 0.0.0", gotHeader)
	}
}

func TestIsQuotaLimit(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"per server quota exceeded", true},
		{"daily limit hit", true},
		{"write me on github", true},
		{"temporary rate limit", false},
		{"other message", false},
	}

	for _, tc := range cases {
		if got := isQuotaLimit(tc.input); got != tc.expected {
			t.Fatalf("isQuotaLimit(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestBuildReportAndStorageData(t *testing.T) {
	data := createTestNotificationData()
	data.SecondaryEnabled = true
	data.CloudEnabled = true
	data.EmailStatus = "ok"

	report := buildReportData(data)

	if report["status"] != data.Status.String() {
		t.Fatalf("status mismatch: %v", report["status"])
	}
	emojis, ok := report["emojis"].(map[string]interface{})
	if !ok || emojis["primary"] != GetStorageEmoji(data.LocalStatus) || emojis["cloud"] != GetStorageEmoji(data.CloudStatus) {
		t.Fatalf("emojis missing or incorrect: %+v", emojis)
	}

	storageSection, ok := report["storage"].(map[string]interface{})
	if !ok {
		t.Fatal("storage section missing")
	}
	if _, ok := storageSection["secondary"]; !ok {
		t.Fatal("secondary storage should be present when enabled")
	}

	logSummary, ok := report["log_summary"].(map[string]interface{})
	if !ok || logSummary["color"] != getLogSummaryColor(data.ErrorCount, data.WarningCount) {
		t.Fatalf("log_summary invalid: %+v", logSummary)
	}

	paths, ok := report["paths"].(map[string]interface{})
	if !ok || paths["cloud_display"] == "" {
		t.Fatalf("paths invalid: %+v", paths)
	}
}

func TestGetLogSummaryColor(t *testing.T) {
	cases := []struct {
		errors   int
		warnings int
		expected string
	}{
		{2, 0, "#dc3545"},
		{0, 3, "#ffc107"},
		{0, 0, "#28a745"},
	}

	for _, tc := range cases {
		if got := getLogSummaryColor(tc.errors, tc.warnings); got != tc.expected {
			t.Fatalf("getLogSummaryColor(%d,%d) = %s, want %s", tc.errors, tc.warnings, got, tc.expected)
		}
	}
}

func TestFormatCloudPathDisplay(t *testing.T) {
	if got := formatCloudPathDisplay(""); got != "Not configured" {
		t.Fatalf("formatCloudPathDisplay empty = %s, want Not configured", got)
	}
	if got := formatCloudPathDisplay("user@host:store"); got != "user@host:store" {
		t.Fatalf("formatCloudPathDisplay passthrough failed: %s", got)
	}
}

func TestSendViaCloudRelay_StatusHandling(t *testing.T) {
	type testCase struct {
		name              string
		statusCode        int
		body              string
		config            CloudRelayConfig
		expectErr         bool
		expectErrContains string
		expectCalls       int
	}

	baseConfig := CloudRelayConfig{
		WorkerToken: "token",
		HMACSecret:  "secret",
		Timeout:     5,
		MaxRetries:  0,
		RetryDelay:  0,
	}

	testData := createTestNotificationData()
	testData.ScriptVersion = "0.0.1"

	tests := []testCase{
		{name: "200-success", statusCode: 200, body: `{"success":true}`, config: baseConfig, expectErr: false, expectCalls: 1},
		{name: "200-success-false", statusCode: 200, body: `{"success":false,"error":"downstream email service failed"}`, config: baseConfig, expectErr: true, expectErrContains: "downstream email service failed", expectCalls: 1},
		{name: "200-empty-body", statusCode: 200, body: ``, config: baseConfig, expectErr: false, expectCalls: 1},
		{name: "200-invalid-json", statusCode: 200, body: `OK`, config: baseConfig, expectErr: false, expectCalls: 1},
		{name: "400-bad-request", statusCode: 400, body: `{"error":"bad payload"}`, config: baseConfig, expectErr: true, expectErrContains: "bad payload", expectCalls: 1},
		{name: "401-missing-signature-code", statusCode: 401, body: `{"code":"MISSING_SIGNATURE","error":"Missing signature"}`, config: baseConfig, expectErr: true, expectErrContains: "missing signature", expectCalls: 1},
		{name: "403-invalid-signature-code", statusCode: 403, body: `{"code":"INVALID_SIGNATURE","error":"Invalid signature"}`, config: baseConfig, expectErr: true, expectErrContains: "HMAC signature validation failed", expectCalls: 1},
		{name: "403-invalid-token-code", statusCode: 403, body: `{"code":"INVALID_TOKEN","error":"Invalid token"}`, config: baseConfig, expectErr: true, expectErrContains: "invalid token", expectCalls: 1},
		{name: "403-invalid-script-version-code", statusCode: 403, body: `{"code":"INVALID_SCRIPT_VERSION","error":"Missing or invalid script version"}`, config: baseConfig, expectErr: true, expectErrContains: "script version", expectCalls: 1},
		{name: "403-from-override-code", statusCode: 403, body: `{"code":"FROM_OVERRIDE_ATTEMPT","error":"From address override not allowed"}`, config: baseConfig, expectErr: true, expectErrContains: "from address override not allowed", expectCalls: 1},
		{name: "403-legacy-forbidden", statusCode: 403, body: `{"error":"Forbidden"}`, config: baseConfig, expectErr: true, expectErrContains: "invalid token", expectCalls: 1},
		{name: "429-quota", statusCode: 429, body: `{"message":"quota per server exceeded"}`, config: baseConfig, expectErr: true, expectErrContains: "rate limit exceeded", expectCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			cfg := tt.config
			cfg.WorkerURL = server.URL

			logger := logging.New(types.LogLevelDebug, false)
			err := sendViaCloudRelay(context.Background(), cfg, EmailRelayPayload{
				To:            "dest@test.invalid",
				Subject:       "subject",
				Report:        map[string]interface{}{"ok": true},
				Timestamp:     time.Now().Unix(),
				ServerMAC:     "00:11:22:33:44:55",
				ScriptVersion: testData.ScriptVersion,
				ServerID:      "server-id",
			}, logger)

			if tt.expectErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.expectErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.expectErrContains) {
					t.Fatalf("expected error containing %q, got %v", tt.expectErrContains, err)
				}
			}
			if callCount != tt.expectCalls {
				t.Fatalf("expected %d calls, got %d", tt.expectCalls, callCount)
			}
		})
	}
}

func TestSendViaCloudRelay_RetryOnServerError(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := CloudRelayConfig{
		WorkerURL:   server.URL,
		WorkerToken: "token",
		HMACSecret:  "secret",
		Timeout:     5,
		MaxRetries:  3,
		RetryDelay:  0,
	}

	logger := logging.New(types.LogLevelDebug, false)
	err := sendViaCloudRelay(context.Background(), cfg, EmailRelayPayload{
		To:            "dest@test.invalid",
		Subject:       "subject",
		Report:        map[string]interface{}{"ok": true},
		Timestamp:     time.Now().Unix(),
		ServerMAC:     "00:11:22:33:44:55",
		ScriptVersion: "0.0.1",
		ServerID:      "server-id",
	}, logger)

	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestSendViaCloudRelay_StopsRetryingWhenContextCanceled(t *testing.T) {
	var attempts int32
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			cancel()
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"temporary"}`))
	}))
	defer server.Close()

	cfg := CloudRelayConfig{
		WorkerURL:   server.URL,
		WorkerToken: "token",
		HMACSecret:  "secret",
		Timeout:     5,
		MaxRetries:  3,
		RetryDelay:  0,
	}

	logger := logging.New(types.LogLevelDebug, false)
	err := sendViaCloudRelay(ctx, cfg, EmailRelayPayload{
		To:            "dest@test.invalid",
		Subject:       "subject",
		Report:        map[string]interface{}{"ok": true},
		Timestamp:     time.Now().Unix(),
		ServerMAC:     "00:11:22:33:44:55",
		ScriptVersion: "0.0.1",
		ServerID:      "server-id",
	}, logger)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected 1 attempt after cancellation, got %d", got)
	}
}
