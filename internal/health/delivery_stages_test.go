package health

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The relay read logs its transport stages under the caller's operation, so a run log can tell
// "not sent" from "no answer".
func TestFetchDeliveryStatusLogsTheStagesUnderTheCallersOperation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, relayDeliveryBody)
	}))
	defer srv.Close()
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	if _, err := FetchDeliveryStatus(context.Background(), srv.Client(), srv.URL, "123456789012", "sekret-token", logger, "notifications init"); err != nil {
		t.Fatalf("FetchDeliveryStatus: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"notifications init: url=" + srv.URL + "/api/healthcheck/delivery-status",
		"notifications init: connected",
		"notifications init: request written",
		"notifications init: response http=200 elapsed=",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{"sekret-token", "123456789012", "proj1"} {
		if strings.Contains(out, leak) {
			t.Fatalf("the relay read leaked %q:\n%s", leak, out)
		}
	}
}

// An answer that is not a 200 logs an excerpt of its body, with the per-server secret masked:
// "the relay answered, and said this".
func TestFetchDeliveryStatusLogsTheBodyOfARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"reader down","echo":"sekret-token"}`)
	}))
	defer srv.Close()
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	if _, err := FetchDeliveryStatus(context.Background(), srv.Client(), srv.URL, "1", "sekret-token", logger, "op"); err == nil {
		t.Fatal("a 503 must be an error")
	}
	out := buf.String()
	if !strings.Contains(out, "op: response http=503") || !strings.Contains(out, `op: response body=`) || !strings.Contains(out, "reader down") {
		t.Fatalf("missing the refusal and its body in:\n%s", out)
	}
	if strings.Contains(out, "sekret-token") {
		t.Fatalf("the body excerpt leaked the secret:\n%s", out)
	}
}
