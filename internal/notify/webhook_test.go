package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Helper function to create a test NotificationData
func createTestNotificationData() *NotificationData {
	return &NotificationData{
		Status:         StatusSuccess,
		StatusMessage:  "Backup completed successfully",
		ExitCode:       0,
		Hostname:       "pbs1.tis24.it",
		ProxmoxType:    types.ProxmoxBS,
		ServerID:       "1320617378791208",
		ServerMAC:      "bc:24:11:41:0d:18",
		BackupDate:     time.Date(2025, 11, 11, 12, 0, 0, 0, time.UTC),
		BackupDuration: 2 * time.Minute,
		BackupFileName: "pbs1-backup-20251111.tar.xz",
		BackupSize:     7558144, // ~7.2 MiB
		BackupSizeHR:   "7.2 MiB",

		CompressionType:  "xz",
		CompressionLevel: 9,
		CompressionMode:  "ultra",
		CompressionRatio: 58.78,

		FilesIncluded: 1070,
		FilesMissing:  0,

		LocalStatus:            "ok",
		LocalStatusSummary:     "7/7 backups",
		LocalCount:             7,
		LocalFree:              "12.68 GB",
		LocalUsed:              "14.33 GB",
		LocalPercent:           "53.0%",
		LocalUsagePercent:      53.0,
		SecondaryEnabled:       true,
		SecondaryStatus:        "ok",
		SecondaryStatusSummary: "14/14 backups",
		SecondaryCount:         14,
		SecondaryFree:          "12.68 GB",
		SecondaryUsed:          "14.33 GB",
		SecondaryPercent:       "53.0%",
		SecondaryUsagePercent:  53.0,
		CloudEnabled:           false,
		CloudStatus:            "disabled",
		CloudStatusSummary:     "not configured",
		CloudCount:             0,

		ErrorCount:    0,
		WarningCount:  0,
		TotalIssues:   0,
		LogFilePath:   "/var/log/backup.log",
		LogCategories: []LogCategory{},

		ScriptVersion: "0.2.0",
	}
}

func TestNewWebhookNotifier(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	tests := []struct {
		name        string
		cfg         config.WebhookConfig
		expectError bool
	}{
		{
			name: "valid config with one endpoint",
			cfg: config.WebhookConfig{
				Enabled:       true,
				DefaultFormat: "generic",
				Timeout:       30,
				MaxRetries:    3,
				RetryDelay:    2,
				Endpoints: []config.WebhookEndpoint{
					{
						Name:   "test-webhook",
						URL:    "https://example.com/webhook",
						Format: "generic",
						Method: "POST",
						Auth:   config.WebhookAuth{Type: "none"},
					},
				},
			},
			expectError: false,
		},
		{
			name: "disabled config",
			cfg: config.WebhookConfig{
				Enabled: false,
			},
			expectError: false,
		},
		{
			name: "enabled but no endpoints",
			cfg: config.WebhookConfig{
				Enabled:   true,
				Endpoints: []config.WebhookEndpoint{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier, err := NewWebhookNotifier(&tt.cfg, logger)
			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if notifier == nil {
				t.Fatal("Expected notifier instance but got nil")
			}
		})
	}
}

func TestWebhookNotifier_Name(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, _ := NewWebhookNotifier(&config.WebhookConfig{Enabled: false}, logger)

	if notifier.Name() != "Webhook" {
		t.Errorf("Name() = %q, want %q", notifier.Name(), "Webhook")
	}
}

func TestWebhookNotifier_IsCritical(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	notifier, _ := NewWebhookNotifier(&config.WebhookConfig{Enabled: false}, logger)

	if notifier.IsCritical() {
		t.Error("IsCritical() should return false for webhook notifier")
	}
}

func TestWebhookNotifier_IsEnabled(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	tests := []struct {
		name           string
		cfg            config.WebhookConfig
		expectedResult bool
		expectError    bool
	}{
		{
			name: "enabled with endpoints",
			cfg: config.WebhookConfig{
				Enabled: true,
				Endpoints: []config.WebhookEndpoint{
					{Name: "test", URL: "https://example.com"},
				},
			},
			expectedResult: true,
		},
		{
			name: "disabled",
			cfg: config.WebhookConfig{
				Enabled: false,
				Endpoints: []config.WebhookEndpoint{
					{Name: "test", URL: "https://example.com"},
				},
			},
			expectedResult: false,
		},
		{
			name: "enabled but no endpoints",
			cfg: config.WebhookConfig{
				Enabled:   true,
				Endpoints: []config.WebhookEndpoint{},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier, err := NewWebhookNotifier(&tt.cfg, logger)
			if tt.expectError {
				if err == nil {
					t.Fatal("Expected error for invalid configuration")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			result := notifier.IsEnabled()
			if result != tt.expectedResult {
				t.Errorf("IsEnabled() = %v, want %v", result, tt.expectedResult)
			}
		})
	}
}

func TestWebhookNotifier_Send_Success(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("Expected POST method, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		// Read and verify body
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("Failed to unmarshal payload: %v", err)
		}

		// Respond with success
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		Timeout:       30,
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{
				Name:   "test-webhook",
				URL:    server.URL,
				Format: "generic",
				Method: "POST",
				Auth:   config.WebhookAuth{Type: "none"},
			},
		},
	}

	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create notifier: %v", err)
	}

	data := createTestNotificationData()
	result, err := notifier.Send(context.Background(), data)

	if err != nil {
		t.Errorf("Send() returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Send() returned nil result")
	}
	if !result.Success {
		t.Errorf("Send() result.Success = false, want true. Error: %s", result.Error)
	}
	if result.Method != "webhook" {
		t.Errorf("Send() result.Method = %s, want webhook", result.Method)
	}
}

func TestWebhookNotifier_Send_Retry(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	attempts := 0

	// Create mock server that fails first 2 times, then succeeds
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"temporary failure"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		Timeout:       30,
		MaxRetries:    3,
		RetryDelay:    1,
		Endpoints: []config.WebhookEndpoint{
			{
				Name:   "test-webhook",
				URL:    server.URL,
				Format: "generic",
				Method: "POST",
				Auth:   config.WebhookAuth{Type: "none"},
			},
		},
	}

	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create notifier: %v", err)
	}

	data := createTestNotificationData()
	result, err := notifier.Send(context.Background(), data)

	if err != nil {
		t.Errorf("Send() returned error: %v", err)
	}
	if !result.Success {
		t.Error("Send() should succeed after retries")
	}
	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestWebhookNotifier_Send_DisabledDoesNotPanic(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	cfg := config.WebhookConfig{Enabled: false}
	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false when disabled, got %+v", result)
	}
	if result.Error == nil {
		t.Fatalf("expected result.Error to be set when disabled")
	}
}

func TestWebhookNotifier_Send_PartialSuccess(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okServer.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{
				Name:   "bad",
				URL:    "ftp://example.com",
				Method: "POST",
				Auth:   config.WebhookAuth{Type: "none"},
			},
			{
				Name:   "good",
				URL:    okServer.URL,
				Method: "POST",
				Auth:   config.WebhookAuth{Type: "none"},
			},
		},
	}

	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true when at least one endpoint succeeds, got %+v", result)
	}
	if result.Error != nil {
		t.Fatalf("expected result.Error=nil on partial success, got %v", result.Error)
	}
}

func TestWebhookNotifier_Authentication_Bearer(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	expectedToken := "test-bearer-token-12345"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		expectedAuth := "Bearer " + expectedToken

		if authHeader != expectedAuth {
			t.Errorf("Authorization header = %s, want %s", authHeader, expectedAuth)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		Timeout:       30,
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{
				Name:   "test-webhook",
				URL:    server.URL,
				Format: "generic",
				Method: "POST",
				Auth: config.WebhookAuth{
					Type:  "bearer",
					Token: expectedToken,
				},
			},
		},
	}

	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create notifier: %v", err)
	}

	data := createTestNotificationData()
	result, err := notifier.Send(context.Background(), data)

	if err != nil {
		t.Errorf("Send() returned error: %v", err)
	}
	if !result.Success {
		t.Error("Send() should succeed with bearer token")
	}
}

func TestWebhookNotifier_Authentication_HMAC(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	secret := "test-hmac-secret"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signature := r.Header.Get("X-Signature")
		if signature == "" {
			t.Error("X-Signature header missing")
			w.WriteHeader(http.StatusForbidden)
			return
		}

		// Read body for verification
		body, _ := io.ReadAll(r.Body)
		expectedSignature := generateHMACSignature(body, secret)

		if signature != expectedSignature {
			t.Errorf("HMAC signature mismatch: got %s, want %s", signature, expectedSignature)
			w.WriteHeader(http.StatusForbidden)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		Timeout:       30,
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{
				Name:   "test-webhook",
				URL:    server.URL,
				Format: "generic",
				Method: "POST",
				Auth: config.WebhookAuth{
					Type:   "hmac",
					Secret: secret,
				},
			},
		},
	}

	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create notifier: %v", err)
	}

	data := createTestNotificationData()
	result, err := notifier.Send(context.Background(), data)

	if err != nil {
		t.Errorf("Send() returned error: %v", err)
	}
	if !result.Success {
		t.Error("Send() should succeed with HMAC authentication")
	}
}

func TestWebhookNotifier_Authentication_Basic(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Basic ") {
			t.Fatalf("expected Basic auth, got %q", authHeader)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{
				Name:   "basic",
				URL:    server.URL,
				Format: "generic",
				Method: "POST",
				Auth: config.WebhookAuth{
					Type: "basic",
					User: "user",
					Pass: "pass",
				},
			},
		},
	}

	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true, got %+v", result)
	}
}

func TestWebhookNotifier_Authentication_Errors(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	w, err := NewWebhookNotifier(&config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{Name: "x", URL: "https://example.com", Auth: config.WebhookAuth{Type: "none"}},
		},
	}, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "https://example.com", nil)

	if err := w.applyAuthentication(req, config.WebhookAuth{Type: "bearer", Token: ""}, []byte("x")); err == nil {
		t.Fatal("expected bearer empty token error")
	}
	if err := w.applyAuthentication(req, config.WebhookAuth{Type: "basic", User: "", Pass: "x"}, []byte("x")); err == nil {
		t.Fatal("expected basic empty user/pass error")
	}
	if err := w.applyAuthentication(req, config.WebhookAuth{Type: "hmac", Secret: ""}, []byte("x")); err == nil {
		t.Fatal("expected hmac empty secret error")
	}
	if err := w.applyAuthentication(req, config.WebhookAuth{Type: "unknown"}, []byte("x")); err == nil {
		t.Fatal("expected unknown auth type error")
	}

	if err := w.applyAuthentication(req, config.WebhookAuth{Type: ""}, []byte("x")); err != nil {
		t.Fatalf("expected no error for empty auth type, got %v", err)
	}
}

func TestWebhookNotifier_buildPayload_CoversFormats(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	notifier, err := NewWebhookNotifier(&config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		Endpoints: []config.WebhookEndpoint{
			{Name: "x", URL: "https://example.com"},
		},
	}, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier() error = %v", err)
	}

	data := createTestNotificationData()
	formats := []string{"discord", "slack", "teams", "generic", "unknown"}
	for _, format := range formats {
		format := format
		t.Run(format, func(t *testing.T) {
			payload, err := notifier.buildPayload(format, data)
			if err != nil {
				t.Fatalf("buildPayload(%q) error = %v", format, err)
			}
			if payload == nil {
				t.Fatalf("buildPayload(%q) returned nil payload", format)
			}
		})
	}
}

type failingReadCloser struct{}

func (failingReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingReadCloser) Close() error             { return nil }

func TestWebhookNotifier_sendToEndpoint_CoversErrorBranches(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	notifier, err := NewWebhookNotifier(&config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		MaxRetries:    0,
		Endpoints: []config.WebhookEndpoint{
			{Name: "x", URL: "https://example.com"},
		},
	}, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier() error = %v", err)
	}

	t.Run("invalid url parse", func(t *testing.T) {
		endpoint := config.WebhookEndpoint{Name: "bad", URL: "http://[::1", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for invalid URL")
		}
	})

	t.Run("invalid scheme", func(t *testing.T) {
		endpoint := config.WebhookEndpoint{Name: "bad", URL: "ftp://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for invalid scheme")
		}
	})

	t.Run("client do error", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "doerr", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for client.Do failure")
		}
	})

	t.Run("response read error", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       failingReadCloser{},
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "readerr", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for response body read failure")
		}
	})

	t.Run("http 400 no retry", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader("bad")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "400", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for HTTP 400")
		}
	})

	t.Run("http 401 no retry", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Body:       io.NopCloser(strings.NewReader("nope")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "401", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for HTTP 401")
		}
	})

	t.Run("http 403 no retry", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Body:       io.NopCloser(strings.NewReader("forbidden")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "403", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for HTTP 403")
		}
	})

	t.Run("http 404 no retry", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(strings.NewReader("missing")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "404", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for HTTP 404")
		}
	})

	t.Run("http 429 no sleep when no retries", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader("rate")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{Name: "429", URL: "https://example.com", Method: "POST"}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err == nil {
			t.Fatal("expected error for HTTP 429")
		}
	})

	t.Run("custom headers + GET omit body", func(t *testing.T) {
		notifier.client = &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet {
					t.Fatalf("expected GET, got %s", req.Method)
				}
				if ct := req.Header.Get("Content-Type"); ct != "" {
					t.Fatalf("expected no Content-Type for GET, got %q", ct)
				}
				if ua := req.Header.Get("User-Agent"); ua == "" {
					t.Fatalf("expected User-Agent to be set")
				}
				if got := req.Header.Get("X-Custom"); got != "ok" {
					t.Fatalf("expected X-Custom header, got %q", got)
				}
				if got := req.Header.Get("Host"); got != "" {
					t.Fatalf("expected Host header not to be set explicitly, got %q", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		}
		endpoint := config.WebhookEndpoint{
			Name:   "get",
			URL:    "https://example.com",
			Method: "GET",
			Headers: map[string]string{
				"":             "skip",
				"Content-Type": "blocked",
				"Host":         "blocked",
				"X-Custom":     "ok",
			},
		}
		if err := notifier.sendToEndpoint(context.Background(), endpoint, data); err != nil {
			t.Fatalf("expected success for GET endpoint, got %v", err)
		}
	})
}

func TestBuildDiscordPayload(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	payload, err := buildDiscordPayload(data, logger)
	if err != nil {
		t.Fatalf("buildDiscordPayload() error: %v", err)
	}

	// Verify payload structure
	embeds, ok := payload["embeds"].([]interface{})
	if !ok || len(embeds) == 0 {
		t.Fatal("Payload missing embeds array")
	}

	embed := embeds[0].(map[string]interface{})

	// Check required fields
	if _, ok := embed["title"]; !ok {
		t.Error("Embed missing title")
	}
	if _, ok := embed["description"]; !ok {
		t.Error("Embed missing description")
	}
	if _, ok := embed["color"]; !ok {
		t.Error("Embed missing color")
	}
	if _, ok := embed["fields"]; !ok {
		t.Error("Embed missing fields")
	}
	if _, ok := embed["timestamp"]; !ok {
		t.Error("Embed missing timestamp")
	}

	// Verify fields array
	fields := embed["fields"].([]map[string]interface{})
	if len(fields) < 5 {
		t.Errorf("Expected at least 5 fields, got %d", len(fields))
	}
}

func TestBuildSlackPayload(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	payload, err := buildSlackPayload(data, logger)
	if err != nil {
		t.Fatalf("buildSlackPayload() error: %v", err)
	}

	// Verify payload structure
	blocks, ok := payload["blocks"].([]interface{})
	if !ok || len(blocks) == 0 {
		t.Fatal("Payload missing blocks array")
	}

	// Check for header block
	headerBlock := blocks[0].(map[string]interface{})
	if headerBlock["type"] != "header" {
		t.Error("First block should be header type")
	}
}

func TestBuildTeamsPayload(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	payload, err := buildTeamsPayload(data, logger)
	if err != nil {
		t.Fatalf("buildTeamsPayload() error: %v", err)
	}

	// Verify payload structure
	if payload["type"] != "message" {
		t.Error("Payload type should be 'message'")
	}

	attachments, ok := payload["attachments"].([]interface{})
	if !ok || len(attachments) == 0 {
		t.Fatal("Payload missing attachments array")
	}

	attachment := attachments[0].(map[string]interface{})
	if attachment["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Error("Attachment contentType should be adaptive card")
	}

	content := attachment["content"].(map[string]interface{})
	if content["type"] != "AdaptiveCard" {
		t.Error("Content type should be AdaptiveCard")
	}
	if content["version"] != "1.5" {
		t.Error("AdaptiveCard version should be 1.5")
	}
}

func TestBuildGenericPayload(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	data := createTestNotificationData()

	payload, err := buildGenericPayload(data, logger)
	if err != nil {
		t.Fatalf("buildGenericPayload() error: %v", err)
	}

	// Verify top-level fields
	requiredFields := []string{"status", "hostname", "timestamp", "backup", "compression", "storage", "issues"}
	for _, field := range requiredFields {
		if _, ok := payload[field]; !ok {
			t.Errorf("Payload missing required field: %s", field)
		}
	}

	// Verify nested structures
	backup, ok := payload["backup"].(map[string]interface{})
	if !ok {
		t.Fatal("Backup field should be a map")
	}
	if _, ok := backup["size_bytes"]; !ok {
		t.Error("Backup missing size_bytes")
	}

	storage, ok := payload["storage"].(map[string]interface{})
	if !ok {
		t.Fatal("Storage field should be a map")
	}
	if _, ok := storage["local"]; !ok {
		t.Error("Storage missing local")
	}
}

func TestMaskURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "https://discord.com/api/webhooks/123456/abcdef",
			expected: "https://discord.com/***MASKED***",
		},
		{
			input:    "https://hooks.slack.com/services/T00/B00/xxx",
			expected: "https://hooks.slack.com/***MASKED***",
		},
		{
			input:    "http://example.com/webhook",
			expected: "http://example.com/***MASKED***",
		},
		{
			input:    "://bad",
			expected: "***INVALID_URL***",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := maskURL(tt.input)
			if result != tt.expected {
				t.Errorf("maskURL(%s) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMaskHeaderValue(t *testing.T) {
	tests := []struct {
		key      string
		value    string
		expected string
	}{
		{
			key:      "Authorization",
			value:    "Bearer abc123def456",
			expected: "Bear***MASKED***",
		},
		{
			key:      "X-API-Token",
			value:    "secret-token-12345",
			expected: "secr***MASKED***",
		},
		{
			key:      "X-API-Token",
			value:    "short",
			expected: "***MASKED***",
		},
		{
			key:      "Content-Type",
			value:    "application/json",
			expected: "application/json",
		},
		{
			key:      "User-Agent",
			value:    "proxsave/0.2.0",
			expected: "proxsave/0.2.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			result := maskHeaderValue(tt.key, tt.value)
			if !strings.Contains(result, "MASKED") && tt.expected != tt.value {
				t.Errorf("maskHeaderValue(%s, %s) = %s, expected masked value", tt.key, tt.value, result)
			}
			if tt.expected == tt.value && result != tt.expected {
				t.Errorf("maskHeaderValue(%s, %s) = %s, want %s", tt.key, tt.value, result, tt.expected)
			}
		})
	}
}
