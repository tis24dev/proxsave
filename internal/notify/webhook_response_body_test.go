package notify

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// An endpoint's answer was printed raw in the visible line of every failed attempt:
// a proxy page split each WARNING over many lines with its escape and bidi bytes, the
// read had no limit (an 8 MiB answer made an 8 MiB WARNING), and the 5xx line said
// "will retry" on the last attempt too. The lines now name the status and the attempt;
// the answer is read up to 8 KiB and shown only at DEBUG, on one line, cut to 200.
func TestWebhookFailureLinesNameTheStatusNotTheAnswer(t *testing.T) {
	page := "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n" +
		"<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n" +
		"\x1b[2J\u202e" + strings.Repeat("<!-- padding -->\n", 512*1024)

	for _, tc := range []struct {
		status int
		lines  []string
	}{
		{http.StatusBadGateway, []string{
			"WARNING  ⚠️ Server error (HTTP 502), attempt 1/2\n",
			"WARNING  ⚠️ Server error (HTTP 502), attempt 2/2\n",
		}},
		{http.StatusMethodNotAllowed, []string{
			"WARNING  ⚠️ Unexpected status (HTTP 405), attempt 1/2\n",
			"WARNING  ⚠️ Unexpected status (HTTP 405), attempt 2/2\n",
		}},
		{http.StatusBadRequest, []string{"ERROR    ❌ Bad request (HTTP 400)\n"}},
		{http.StatusForbidden, []string{"ERROR    ❌ Forbidden (HTTP 403)\n"}},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(page))
		}))
		cfg := config.WebhookConfig{
			Enabled:       true,
			DefaultFormat: "generic",
			Timeout:       30,
			MaxRetries:    1,
			RetryDelay:    1,
			Endpoints: []config.WebhookEndpoint{{
				Name:   "ops",
				URL:    server.URL,
				Format: "generic",
				Method: "POST",
				Auth:   config.WebhookAuth{Type: "none"},
			}},
		}
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)
		notifier, err := NewWebhookNotifier(&cfg, logger)
		if err != nil {
			t.Fatalf("NewWebhookNotifier: %v", err)
		}
		sendErr := notifier.sendToEndpoint(context.Background(), cfg.Endpoints[0], createTestNotificationData())
		server.Close()
		if sendErr == nil {
			t.Fatalf("HTTP %d: nil error, want the failure", tc.status)
		}

		log := out.String()
		for _, want := range tc.lines {
			if !strings.Contains(log, want) {
				t.Fatalf("HTTP %d: no line %q; log:\n%.3000s", tc.status, want, log)
			}
		}
		for _, raw := range []string{"\x1b", "\u202e", "\r", "\n<head>"} {
			if strings.Contains(log, raw) || strings.Contains(sendErr.Error(), raw) {
				t.Fatalf("HTTP %d: raw answer (%q) in the log or the error %q; log:\n%.3000s", tc.status, raw, sendErr, log)
			}
		}
		if len(log) > 16<<10 {
			t.Fatalf("HTTP %d: %d bytes of log for one failed endpoint, the answer was not bounded", tc.status, len(log))
		}
		for line := range strings.SplitSeq(strings.TrimSuffix(log, "\n"), "\n") {
			if !strings.HasPrefix(line, "[") {
				t.Fatalf("HTTP %d: the answer split a line, %q has no prefix", tc.status, line)
			}
		}
	}
}

// The DEBUG excerpt masked the endpoint URL whole, but an error page echoes the
// requested path alone, and a webhook URL often carries its token in a path segment;
// query values and header values can be credentials too.
func TestWebhookAnswerExcerptMasksTheEndpointCredentials(t *testing.T) {
	const pathToken = "TokenSegmentAbcdef9"
	const queryValue = "QueryValue12345"
	const headerValue = "HeaderValue98765"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("The requested URL " + r.URL.Path + "?" + r.URL.RawQuery +
			" was not found (X-Key: " + r.Header.Get("X-Key") + ")"))
	}))
	defer server.Close()

	cfg := config.WebhookConfig{
		Enabled:       true,
		DefaultFormat: "generic",
		Timeout:       30,
		RetryDelay:    1,
		Endpoints: []config.WebhookEndpoint{{
			Name:    "ops",
			URL:     server.URL + "/api/webhooks/123456/" + pathToken + "?key=" + queryValue,
			Format:  "generic",
			Method:  "POST",
			Headers: map[string]string{"X-Key": headerValue},
			Auth:    config.WebhookAuth{Type: "none"},
		}},
	}
	var out bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&out)
	notifier, err := NewWebhookNotifier(&cfg, logger)
	if err != nil {
		t.Fatalf("NewWebhookNotifier: %v", err)
	}
	if err := notifier.sendToEndpoint(context.Background(), cfg.Endpoints[0], createTestNotificationData()); err == nil {
		t.Fatalf("nil error, want the 404")
	}
	log := out.String()
	if !strings.Contains(log, "Response body (") {
		t.Fatalf("no DEBUG line with the answer; log:\n%s", log)
	}
	for _, secret := range []string{pathToken, queryValue, headerValue} {
		if strings.Contains(log, secret) {
			t.Fatalf("endpoint credential %q in the log:\n%s", secret, log)
		}
	}
}
