package serverbot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func clientWithRT(f rtFunc) *Client {
	return New("https://bot.example", &http.Client{Transport: f}, nil)
}

func TestNewNormalizesHostOnce(t *testing.T) {
	for _, in := range []string{"https://bot.example", "https://bot.example/", "https://bot.example///"} {
		if got := New(in, nil, nil).base; got != "https://bot.example" {
			t.Errorf("New(%q).base = %q, want https://bot.example", in, got)
		}
	}
}

func TestNewNilHTTPClientOwnsClientAndRefusesRedirect(t *testing.T) {
	c := New("h", nil, nil)
	if c.http == nil {
		t.Fatal("nil httpClient must yield a non-nil owned client")
	}
	// Must NOT reuse the shared global: setting CheckRedirect on http.DefaultClient
	// would mutate every other user of the default client process-wide (F11-03).
	if c.http == http.DefaultClient {
		t.Fatal("nil httpClient must NOT reuse the shared http.DefaultClient")
	}
	if c.http.CheckRedirect == nil {
		t.Fatal("owned client must refuse redirects (CheckRedirect set)")
	}
	if err := c.http.CheckRedirect(nil, []*http.Request{{}}); err == nil {
		t.Error("CheckRedirect must return an error to refuse the redirect")
	}
}

func TestDoHeaderMatrix(t *testing.T) {
	cases := []struct {
		name                                string
		req                                 Request
		wantAuth, wantProv, wantNID, wantCT bool
	}{
		{"bare get", Request{Method: "GET", Path: "/x"}, false, false, false, false},
		{"auth", Request{Path: "/x", Secret: "sek"}, true, false, false, false},
		{"provision", Request{Path: "/x", Provision: true}, false, true, false, false},
		{"notify id", Request{Path: "/x", NotifyID: "nid"}, false, false, true, false},
		{"body", Request{Method: "POST", Path: "/x", Body: map[string]string{"a": "b"}}, false, false, false, true},
		{"all", Request{Method: "POST", Path: "/x", Secret: "sek", Provision: true, NotifyID: "nid", Body: map[string]int{"n": 1}}, true, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			c := clientWithRT(func(r *http.Request) (*http.Response, error) {
				got = r.Header.Clone()
				return stubResp(200, ""), nil
			})
			if _, err := c.Do(context.Background(), tc.req); err != nil {
				t.Fatalf("Do: %v", err)
			}
			if len(got.Values(versionHeader)) == 0 {
				t.Error("X-Proxsave-Version must ALWAYS be set")
			}
			// Presence via Values (not Get()!=""): a header Set with an empty value is
			// still ON THE WIRE. Get() can't tell "omitted" from "present but empty",
			// which would hide a regression that unconditionally set X-Server-Auth="".
			if hasAuth := len(got.Values(serverAuthHeader)) > 0; hasAuth != tc.wantAuth {
				t.Errorf("X-Server-Auth present=%v, want %v", hasAuth, tc.wantAuth)
			}
			if tc.wantAuth && got.Get(serverAuthHeader) != tc.req.Secret {
				t.Errorf("X-Server-Auth = %q, want %q", got.Get(serverAuthHeader), tc.req.Secret)
			}
			if hasProv := len(got.Values(provisionHeader)) > 0; hasProv != tc.wantProv {
				t.Errorf("X-Proxsave-Provision present=%v, want %v", hasProv, tc.wantProv)
			}
			if tc.wantProv && got.Get(provisionHeader) != "1" {
				t.Errorf("X-Proxsave-Provision = %q, want 1", got.Get(provisionHeader))
			}
			if hasNID := len(got.Values(notifyIDHeader)) > 0; hasNID != tc.wantNID {
				t.Errorf("X-Notify-Id present=%v, want %v", hasNID, tc.wantNID)
			}
			if hasCT := len(got.Values("Content-Type")) > 0; hasCT != tc.wantCT {
				t.Errorf("Content-Type present=%v, want %v", hasCT, tc.wantCT)
			}
		})
	}
}

func TestDoEscapesQuery(t *testing.T) {
	var gotURL string
	c := clientWithRT(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return stubResp(200, ""), nil
	})
	q := url.Values{"server_id": {"a b&c"}, "login": {"1"}}
	if _, err := c.Do(context.Background(), Request{Path: "/api/x", Query: q}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !strings.Contains(gotURL, "server_id=a+b%26c") {
		t.Errorf("query not escaped: %s", gotURL)
	}
}

func TestDoTimeoutCoalesce(t *testing.T) {
	var deadline time.Time
	var ok bool
	c := clientWithRT(func(r *http.Request) (*http.Response, error) {
		deadline, ok = r.Context().Deadline()
		return stubResp(200, ""), nil
	})
	if _, err := c.Do(context.Background(), Request{Path: "/x"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !ok {
		t.Fatal("Timeout=0 must apply a default deadline")
	}
	if d := time.Until(deadline); d <= 4*time.Second || d > 5*time.Second {
		t.Errorf("default timeout ~5s, got %v", d)
	}
}

func TestDoMaxBytesCoalesce(t *testing.T) {
	big := strings.Repeat("a", 20000)
	c := clientWithRT(func(r *http.Request) (*http.Response, error) { return stubResp(200, big), nil })

	resp, err := c.Do(context.Background(), Request{Path: "/x"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(resp.Body) != 8192 {
		t.Errorf("default MaxBytes cap = %d, want 8192", len(resp.Body))
	}
	resp2, err := c.Do(context.Background(), Request{Path: "/x", MaxBytes: 100})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(resp2.Body) != 100 {
		t.Errorf("explicit MaxBytes cap = %d, want 100", len(resp2.Body))
	}
}

func TestDoNon2xxIsNotError(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409, 413, 422, 426, 500, 503} {
		code := code
		c := clientWithRT(func(r *http.Request) (*http.Response, error) { return stubResp(code, "b"), nil })
		resp, err := c.Do(context.Background(), Request{Path: "/x"})
		if err != nil {
			t.Fatalf("status %d must NOT be an error, got %v", code, err)
		}
		if resp.Status != code {
			t.Errorf("Status = %d, want %d", resp.Status, code)
		}
	}
}

func TestDoTransportErrorRedacted(t *testing.T) {
	secret := "supersecret-token-value"
	// inner error also carries the secret in text (contrived) to prove RedactSecrets.
	c := clientWithRT(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed; token=" + secret)
	})
	_, err := c.Do(context.Background(), Request{Path: "/api/x", Query: url.Values{"server_id": {"900000000001"}}, Secret: secret})
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T (%v)", err, err)
	}
	msg := te.Error()
	if strings.Contains(msg, "900000000001") {
		t.Errorf("server_id (URL) leaked: %s", msg)
	}
	if strings.Contains(msg, secret) {
		t.Errorf("secret leaked: %s", msg)
	}

	// Secret == "" (pre-auth get-chat-id): only the URL strip, still no server_id.
	c2 := clientWithRT(func(r *http.Request) (*http.Response, error) { return nil, errors.New("boom") })
	_, err2 := c2.Do(context.Background(), Request{Path: "/api/get-chat-id", Query: url.Values{"server_id": {"900000000001"}}})
	var te2 *TransportError
	if errors.As(err2, &te2) && strings.Contains(te2.Error(), "900000000001") {
		t.Errorf("server_id leaked on no-secret path: %s", te2.Error())
	}
}

func TestDoLoggerNeverEmitsBodyOrSecret(t *testing.T) {
	var buf bytes.Buffer
	lg := logging.New(types.LogLevelDebug, false)
	lg.SetOutput(&buf)
	c := New("https://bot.example", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return stubResp(200, "SENSITIVE-BODY-CONTENT"), nil
	})}, lg)
	// Include a Query carrying server_id: the debug line must log req.Path, NOT the
	// full endpoint (which would leak the server_id in the query into logs).
	if _, err := c.Do(context.Background(), Request{Method: "POST", Path: "/api/notify", Query: url.Values{"server_id": {"900000000001"}}, Secret: "SEKRET-abc", Body: map[string]string{"m": "hi"}}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "SEKRET-abc") {
		t.Errorf("logger leaked the secret: %s", out)
	}
	if strings.Contains(out, "SENSITIVE-BODY-CONTENT") {
		t.Errorf("logger leaked the response body: %s", out)
	}
	if strings.Contains(out, "900000000001") {
		t.Errorf("logger leaked the server_id (query): %s", out)
	}
	if !strings.Contains(out, "/api/notify") {
		t.Errorf("expected a debug line naming the path, got: %s", out)
	}
}
