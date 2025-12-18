package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func writeCaptureScript(t *testing.T, name, captureEnvVar string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := `#!/bin/sh
set -eu
out="${` + captureEnvVar + `:-}"
if [ -z "$out" ]; then
  echo "` + captureEnvVar + ` not set" >&2
  exit 2
fi
cat > "$out"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestEmailNotifier_RelayNoFallback_ReturnsError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	// Force relay failure.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"temporary"}`))
	}))
	defer server.Close()

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:          true,
		DeliveryMethod:   EmailDeliveryRelay,
		FallbackSendmail: false,
		Recipient:        "admin@example.com",
		From:             "no-reply@proxmox.example.com",
		CloudRelayConfig: CloudRelayConfig{
			WorkerURL:   server.URL,
			WorkerToken: "token",
			HMACSecret:  "secret",
			Timeout:     5,
			MaxRetries:  0,
			RetryDelay:  0,
		},
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected Success=false when relay fails and fallback is disabled")
	}
	if result.Error == nil {
		t.Fatalf("expected Error to be set")
	}
	if result.Method != "email-relay" {
		t.Fatalf("expected Method=email-relay, got %q", result.Method)
	}
}

func TestEmailNotifier_SendPMF_AllowsMissingRecipientAndInvokesForwarder(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	capturePath := filepath.Join(t.TempDir(), "pmf_capture.txt")
	t.Setenv("PMF_CAPTURE_PATH", capturePath)

	pmfScriptPath := writeCaptureScript(t, "proxmox-mail-forward", "PMF_CAPTURE_PATH")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(pmfScriptPath)+string(os.PathListSeparator)+origPath)

	origCandidates := pmfLookPathCandidates
	pmfLookPathCandidates = []string{"proxmox-mail-forward"}
	t.Cleanup(func() { pmfLookPathCandidates = origCandidates })

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliveryPMF,
		Recipient:      "", // force auto-detect attempt; failure should not block PMF
		From:           "no-reply@proxmox.example.com",
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true, got false (err=%v)", result.Error)
	}
	if result.Method != "email-pmf" {
		t.Fatalf("expected Method=email-pmf, got %q", result.Method)
	}

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read pmf capture: %v", err)
	}
	msg := string(got)
	if !strings.Contains(msg, "To: root\n") {
		t.Fatalf("expected To: root header when recipient is missing, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Subject: =?UTF-8?B?") {
		t.Fatalf("expected encoded Subject header, got:\n%s", msg)
	}
}

func TestEmailNotifier_RelayFallback_UsesPMFOnly(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	// Force relay failure.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"temporary"}`))
	}))
	defer server.Close()

	capturePath := filepath.Join(t.TempDir(), "pmf_capture.txt")
	t.Setenv("PMF_CAPTURE_PATH", capturePath)

	pmfScriptPath := writeCaptureScript(t, "proxmox-mail-forward", "PMF_CAPTURE_PATH")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(pmfScriptPath)+string(os.PathListSeparator)+origPath)

	origCandidates := pmfLookPathCandidates
	pmfLookPathCandidates = []string{"proxmox-mail-forward"}
	t.Cleanup(func() { pmfLookPathCandidates = origCandidates })

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:          true,
		DeliveryMethod:   EmailDeliveryRelay,
		FallbackSendmail: true, // historical name; now means fallback to PMF
		Recipient:        "admin@example.com",
		From:             "no-reply@proxmox.example.com",
		CloudRelayConfig: CloudRelayConfig{
			WorkerURL:   server.URL,
			WorkerToken: "token",
			HMACSecret:  "secret",
			Timeout:     5,
			MaxRetries:  0,
			RetryDelay:  0,
		},
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}

	result, err := notifier.Send(context.Background(), createTestNotificationData())
	if err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected Success=true due to PMF fallback, got false (err=%v)", result.Error)
	}
	if !result.UsedFallback {
		t.Fatalf("expected UsedFallback=true")
	}
	if result.Method != "email-pmf-fallback" {
		t.Fatalf("expected Method=email-pmf-fallback, got %q", result.Method)
	}
	if result.Error == nil {
		t.Fatalf("expected original relay error preserved in result.Error")
	}

	if callCount != 1 {
		t.Fatalf("expected relay to be attempted once, got %d", callCount)
	}

	got, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read pmf capture: %v", err)
	}
	if !strings.Contains(string(got), "To: admin@example.com\n") {
		t.Fatalf("expected To: admin@example.com header in PMF message")
	}
}
