package notify

import (
	"bytes"
	"context"
	"io"
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

func TestEmailNotifierBuildEmailMessage_AttachesLogWhenConfigured(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "backup.log")
	if err := os.WriteFile(logPath, []byte("log contents"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliverySendmail,
		From:           "no-reply@proxmox.example.com",
		AttachLogFile:  true,
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}

	data := createTestNotificationData()
	data.LogFilePath = logPath

	emailMessage, toHeader := notifier.buildEmailMessage("admin@example.com", "subject", "<b>html</b>", "text", data)
	if toHeader != "admin@example.com" {
		t.Fatalf("toHeader=%q want %q", toHeader, "admin@example.com")
	}
	if !strings.Contains(emailMessage, "Content-Type: multipart/mixed") {
		t.Fatalf("expected multipart/mixed email, got:\n%s", emailMessage)
	}
	if !strings.Contains(emailMessage, "Content-Disposition: attachment") {
		t.Fatalf("expected attachment, got:\n%s", emailMessage)
	}
	if !strings.Contains(emailMessage, "name=\"backup.log\"") {
		t.Fatalf("expected attachment filename backup.log, got:\n%s", emailMessage)
	}
}

func TestEmailNotifierBuildEmailMessage_FallsBackWhenLogUnreadable(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	notifier, err := NewEmailNotifier(EmailConfig{
		Enabled:        true,
		DeliveryMethod: EmailDeliverySendmail,
		From:           "no-reply@proxmox.example.com",
		AttachLogFile:  true,
	}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error = %v", err)
	}

	data := createTestNotificationData()
	data.LogFilePath = filepath.Join(t.TempDir(), "missing.log")

	emailMessage, _ := notifier.buildEmailMessage("admin@example.com", "subject", "<b>html</b>", "text", data)
	if !strings.Contains(emailMessage, "Content-Type: multipart/alternative") {
		t.Fatalf("expected multipart/alternative fallback, got:\n%s", emailMessage)
	}
}

func TestEmailNotifierIsMTAServiceActive_SystemctlMissing(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	t.Setenv("PATH", t.TempDir())
	active, msg := notifier.isMTAServiceActive(context.Background())
	if active {
		t.Fatalf("expected active=false when systemctl missing, got true (%s)", msg)
	}
	if msg != "systemctl not available" {
		t.Fatalf("msg=%q want %q", msg, "systemctl not available")
	}
}

func TestEmailNotifierIsMTAServiceActive_ServiceDetected(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	dir := t.TempDir()
	writeCmd(t, dir, "systemctl", "#!/bin/sh\nset -eu\nif [ \"$1\" = \"is-active\" ] && [ \"$2\" = \"postfix\" ]; then exit 0; fi\nexit 3\n")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

	active, service := notifier.isMTAServiceActive(context.Background())
	if !active || service != "postfix" {
		t.Fatalf("isMTAServiceActive()=(%v,%q) want (true,\"postfix\")", active, service)
	}
}

func TestEmailNotifierCheckRelayHostConfigured_Variants(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logger)
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	origPath := postfixMainCFPath
	t.Cleanup(func() { postfixMainCFPath = origPath })

	t.Run("missing file", func(t *testing.T) {
		postfixMainCFPath = filepath.Join(t.TempDir(), "missing.cf")
		ok, reason := notifier.checkRelayHostConfigured(context.Background())
		if ok || reason != "main.cf not found" {
			t.Fatalf("checkRelayHostConfigured()=(%v,%q) want (false,%q)", ok, reason, "main.cf not found")
		}
	})

	t.Run("unreadable (is dir)", func(t *testing.T) {
		postfixMainCFPath = t.TempDir()
		ok, reason := notifier.checkRelayHostConfigured(context.Background())
		if ok || reason != "cannot read config" {
			t.Fatalf("checkRelayHostConfigured()=(%v,%q) want (false,%q)", ok, reason, "cannot read config")
		}
	})

	t.Run("relayhost empty", func(t *testing.T) {
		dir := t.TempDir()
		postfixMainCFPath = filepath.Join(dir, "main.cf")
		if err := os.WriteFile(postfixMainCFPath, []byte("relayhost = []\n"), 0o600); err != nil {
			t.Fatalf("write main.cf: %v", err)
		}
		ok, reason := notifier.checkRelayHostConfigured(context.Background())
		if ok || reason != "no relay host" {
			t.Fatalf("checkRelayHostConfigured()=(%v,%q) want (false,%q)", ok, reason, "no relay host")
		}
	})

	t.Run("relayhost set", func(t *testing.T) {
		dir := t.TempDir()
		postfixMainCFPath = filepath.Join(dir, "main.cf")
		if err := os.WriteFile(postfixMainCFPath, []byte("relayhost = smtp.example.com:587\n"), 0o600); err != nil {
			t.Fatalf("write main.cf: %v", err)
		}
		ok, host := notifier.checkRelayHostConfigured(context.Background())
		if !ok || host != "smtp.example.com:587" {
			t.Fatalf("checkRelayHostConfigured()=(%v,%q) want (true,%q)", ok, host, "smtp.example.com:587")
		}
	})
}
