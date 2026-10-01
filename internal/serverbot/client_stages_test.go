package serverbot

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func stageLogger() (*logging.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	lg := logging.New(types.LogLevelDebug, false)
	lg.SetOutput(&buf)
	return lg, &buf
}

// assertInOrder fails unless every want appears in out, each after the previous one.
func assertInOrder(t *testing.T, out string, wants ...string) {
	t.Helper()
	last := -1
	for _, want := range wants {
		i := strings.Index(out, want)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
		if i < last {
			t.Fatalf("%q is out of order in:\n%s", want, out)
		}
		last = i
	}
}

// A completed exchange logs every stage under the caller's operation, in the order they happen,
// and ends with the HTTP status and the elapsed time.
func TestDoLogsEveryStageOfACompletedExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	// localhost instead of 127.0.0.1: an IP literal skips the name lookup, and the dns stage
	// would never be exercised.
	base := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	lg, buf := stageLogger()

	resp, err := New(base, nil, lg).Do(context.Background(), Request{
		Path: "/api/healthcheck/delivery-status", Query: url.Values{"server_id": {"900000000001"}},
		Secret: "SEKRET-abc", LogOperation: "notifications init",
	})
	if err != nil || resp.Status != http.StatusOK {
		t.Fatalf("Do = %v, %v; want a 200", resp, err)
	}
	out := buf.String()
	assertInOrder(t, out,
		"notifications init: url="+base+"/api/healthcheck/delivery-status\n",
		"notifications init: dns ok addr=",
		"notifications init: connected",
		"notifications init: request written",
		"notifications init: response http=200 elapsed=",
	)
	for _, leak := range []string{"SEKRET-abc", "900000000001", `"ok"`} {
		if strings.Contains(out, leak) {
			t.Fatalf("stage lines leaked %q:\n%s", leak, out)
		}
	}
	if strings.Contains(out, "failed stage=") {
		t.Fatalf("a completed exchange logged a failure:\n%s", out)
	}
	if strings.Contains(out, "-> 200") {
		t.Fatalf("the old one-line summary duplicates the stage lines:\n%s", out)
	}
}

// A name that does not resolve fails at the dns stage: nothing was sent.
func TestDoLogsAFailedNameLookup(t *testing.T) {
	lg, buf := stageLogger()

	_, err := New("http://proxsave-stage-test.invalid", nil, lg).Do(context.Background(), Request{
		Path: "/api/x", Timeout: 3 * time.Second, LogOperation: "op",
	})
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T (%v)", err, err)
	}
	out := buf.String()
	assertInOrder(t, out, "op: url=http://proxsave-stage-test.invalid/api/x", "op: failed stage=dns error=")
	for _, never := range []string{"dns ok", "connected", "request written", "response http="} {
		if strings.Contains(out, never) {
			t.Fatalf("a failed lookup logged %q:\n%s", never, out)
		}
	}
}

// A refused connection fails at the connect stage: nothing was sent.
func TestDoLogsARefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	lg, buf := stageLogger()

	_, err = New("http://"+addr, nil, lg).Do(context.Background(), Request{Path: "/api/x", LogOperation: "op"})
	if err == nil {
		t.Fatal("Do on a closed port returned no error")
	}
	out := buf.String()
	assertInOrder(t, out, "op: url=http://"+addr+"/api/x", "op: failed stage=connect error=")
	for _, never := range []string{"connected", "request written", "response http="} {
		if strings.Contains(out, never) {
			t.Fatalf("a refused connection logged %q:\n%s", never, out)
		}
	}
}

// A request that was written but whose answer broke off fails at the response stage: the relay
// got it and did not answer in full. The body read error is the one reported.
func TestDoLogsAnAnswerThatBrokeOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nabc")
		_ = rw.Flush()
	}))
	defer srv.Close()
	lg, buf := stageLogger()

	_, err := New(srv.URL, nil, lg).Do(context.Background(), Request{Path: "/api/x", LogOperation: "op"})
	var te *TransportError
	if !errors.As(err, &te) || te.Op != "read" {
		t.Fatalf("want a read *TransportError, got %T (%v)", err, err)
	}
	out := buf.String()
	assertInOrder(t, out, "op: connected", "op: request written", "op: failed stage=response error=")
	if strings.Contains(out, "response http=") {
		t.Fatalf("a broken answer logged a completed response:\n%s", out)
	}
}

// The failure line carries the same redaction as the TransportError: no query, no secret.
func TestDoFailureLineIsRedacted(t *testing.T) {
	secret := "supersecret-token-value"
	lg, buf := stageLogger()
	c := New("https://bot.example", &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed; token=" + secret)
	})}, lg)

	_, _ = c.Do(context.Background(), Request{Path: "/api/x", Query: url.Values{"server_id": {"900000000001"}}, Secret: secret, LogOperation: "op"})

	out := buf.String()
	if !strings.Contains(out, "op: failed stage=") {
		t.Fatalf("missing the failure line in:\n%s", out)
	}
	for _, leak := range []string{secret, "900000000001"} {
		if strings.Contains(out, leak) {
			t.Fatalf("failure line leaked %q:\n%s", leak, out)
		}
	}
}

// A caller that names no operation still gets the stage lines, under "serverbot", with the method
// when it is not GET: what the old "serverbot: POST /api/notify -> 200" line said is all there.
func TestDoWithoutAnOperationLogsUnderServerbot(t *testing.T) {
	lg, buf := stageLogger()
	c := New("https://bot.example", &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(202, ""), nil
	})}, lg)

	if _, err := c.Do(context.Background(), Request{Method: http.MethodPost, Path: "/api/notify", Body: map[string]string{"m": "x"}}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	assertInOrder(t, buf.String(),
		"serverbot: url=https://bot.example/api/notify method=POST",
		"serverbot: response http=202 elapsed=",
	)
}

// The logged URL never carries credentials embedded in the host.
func TestDoLoggedURLHidesUserinfo(t *testing.T) {
	lg, buf := stageLogger()
	c := New("https://user:pa55word@bot.example", &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return stubResp(200, ""), nil
	})}, lg)

	_, _ = c.Do(context.Background(), Request{Path: "/api/x", LogOperation: "op"})

	if strings.Contains(buf.String(), "pa55word") {
		t.Fatalf("logged URL leaked the password:\n%s", buf.String())
	}
}

// Without a logger nothing is traced, and the result is the same.
func TestDoWithoutALoggerStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	resp, err := New(srv.URL, nil, nil).Do(context.Background(), Request{Path: "/x", LogOperation: "op"})
	if err != nil || resp.Status != http.StatusTeapot {
		t.Fatalf("Do = %v, %v; want 418", resp, err)
	}
}

// The failed stage is the first one the round trip did not complete.
func TestFailedStageNamesTheFirstIncompleteStage(t *testing.T) {
	cases := []struct {
		name string
		s    *stages
		want string
	}{
		{"lookup never finished", &stages{dnsStarted: true}, "dns"},
		{"lookup failed", &stages{dnsStarted: true, dnsDone: true, dnsErr: errors.New("nxdomain")}, "dns"},
		{"no connection", &stages{dnsStarted: true, dnsDone: true}, "connect"},
		{"no connection, no lookup", &stages{}, "connect"},
		{"connected, not written", &stages{connected: true}, "request"},
		{"write failed", &stages{connected: true, wrote: true, writeErr: errors.New("broken pipe")}, "request"},
		{"written", &stages{connected: true, wrote: true}, "response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.failedStage(); got != tc.want {
				t.Fatalf("failedStage = %q; want %q", got, tc.want)
			}
		})
	}
}

// A caller's prefix starts every stage line of the call and its detail ends the url= line, so
// the stages of one attempt read as that attempt's (the daemon's schedule poll).
func TestDoPrefixesEveryStageLineAndDetailsTheURLLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	base := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	lg, buf := stageLogger()

	if _, err := New(base, nil, lg).Do(context.Background(), Request{
		Path: "/api/healthcheck/config", Secret: "SEKRET-abc", LogOperation: "schedule",
		LogPrefix: "attempt=1/3", LogURLDetail: "frequency=weekly notify_on=warning channels=email",
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	assertInOrder(t, buf.String(),
		"schedule: attempt=1/3 url="+base+"/api/healthcheck/config frequency=weekly notify_on=warning channels=email\n",
		"schedule: attempt=1/3 dns ok addr=",
		"schedule: attempt=1/3 connected\n",
		"schedule: attempt=1/3 request written\n",
		"schedule: attempt=1/3 response http=200 elapsed=",
	)

	// A refused connection: the failed line carries the prefix too.
	lg, buf = stageLogger()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, err := New(deadURL, nil, lg).Do(context.Background(), Request{
		Path: "/api/healthcheck/config", LogOperation: "schedule", LogPrefix: "attempt=2/3",
	})
	var te *TransportError
	if !errors.As(err, &te) || te.Stage != "connect" {
		t.Fatalf("Do against a closed server = %v; want a TransportError with Stage connect", err)
	}
	assertInOrder(t, buf.String(), "schedule: attempt=2/3 url="+deadURL+"/api/healthcheck/config\n",
		"schedule: attempt=2/3 failed stage=connect error=")
}
