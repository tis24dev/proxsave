package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// capture records the last request a test server received.
type capture struct {
	mu     sync.Mutex
	method string
	path   string
	query  string
	body   string
	hits   int
}

func (c *capture) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	c.method = r.Method
	c.path = r.URL.Path
	c.query = r.URL.RawQuery
	c.body = string(b)
	c.hits++
}

func (c *capture) get() capture {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capture{method: c.method, path: c.path, query: c.query, body: c.body, hits: c.hits}
}

// newServer returns an httptest server that records requests and replies with the
// given status/body, plus a Reporter pointed at "<server>/ping/alive" and
// "<server>/ping/backup".
func newServer(t *testing.T, status int, sendLog bool) (*capture, *Reporter, func()) {
	t.Helper()
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "OK")
	}))
	rep := NewReporter(Config{
		Client:    srv.Client(),
		AliveURL:  srv.URL + "/ping/alive",
		BackupURL: srv.URL + "/ping/backup",
		SendLog:   sendLog,
	})
	return cap, rep, srv.Close
}

func TestHeartbeat(t *testing.T) {
	cap, rep, done := newServer(t, 200, true)
	defer done()
	if err := rep.Heartbeat(context.Background()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	g := cap.get()
	if g.method != http.MethodPost || g.path != "/ping/alive" {
		t.Fatalf("heartbeat hit %s %s, want POST /ping/alive", g.method, g.path)
	}
	if g.query != "" {
		t.Fatalf("heartbeat should carry no rid, got query %q", g.query)
	}
}

func TestRunStartedCarriesRid(t *testing.T) {
	cap, rep, done := newServer(t, 200, true)
	defer done()
	if err := rep.RunStarted(context.Background(), "rid-123"); err != nil {
		t.Fatalf("RunStarted: %v", err)
	}
	g := cap.get()
	if g.path != "/ping/backup/start" {
		t.Fatalf("start hit path %q, want /ping/backup/start", g.path)
	}
	if g.query != "rid=rid-123" {
		t.Fatalf("start query %q, want rid=rid-123", g.query)
	}
}

func TestRunFinishedOutcomeMapping(t *testing.T) {
	tests := []struct {
		name     string
		exit     int
		sendLog  bool
		wantPath string
		wantBody string
	}{
		{"success no body", 0, true, "/ping/backup/0", ""},
		{"warning with log", 1, true, "/ping/backup/1", "logtail-here"},
		{"backup error with log", 4, true, "/ping/backup/4", "logtail-here"},
		{"error but sendLog off", 4, false, "/ping/backup/4", ""},
		{"clamp over 255", 300, true, "/ping/backup/255", "logtail-here"},
		{"clamp negative", -1, true, "/ping/backup/255", "logtail-here"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cap, rep, done := newServer(t, 200, tc.sendLog)
			defer done()
			if err := rep.RunFinished(context.Background(), "rid-x", tc.exit, "logtail-here"); err != nil {
				t.Fatalf("RunFinished: %v", err)
			}
			g := cap.get()
			if g.path != tc.wantPath {
				t.Fatalf("path %q, want %q", g.path, tc.wantPath)
			}
			if g.body != tc.wantBody {
				t.Fatalf("body %q, want %q", g.body, tc.wantBody)
			}
			if g.query != "rid=rid-x" {
				t.Fatalf("query %q, want rid=rid-x", g.query)
			}
		})
	}
}

func TestRunHang(t *testing.T) {
	cap, rep, done := newServer(t, 200, true)
	defer done()
	if err := rep.RunHang(context.Background(), "rid-h", 90*time.Minute, "tail"); err != nil {
		t.Fatalf("RunHang: %v", err)
	}
	g := cap.get()
	if g.path != "/ping/backup/fail" {
		t.Fatalf("hang path %q, want /ping/backup/fail", g.path)
	}
	if !strings.HasPrefix(g.body, "timed out after ") {
		t.Fatalf("hang body %q, want a timeout message", g.body)
	}
	if !strings.Contains(g.body, "tail") {
		t.Fatalf("hang body should append the log tail, got %q", g.body)
	}
}

func TestTestPingUsesLogSuffix(t *testing.T) {
	cap, rep, done := newServer(t, 200, true)
	defer done()
	if err := rep.TestPing(context.Background(), rep.aliveURL); err != nil {
		t.Fatalf("TestPing: %v", err)
	}
	if g := cap.get(); g.path != "/ping/alive/log" {
		t.Fatalf("test ping path %q, want /ping/alive/log", g.path)
	}
}

func TestMissingURLs(t *testing.T) {
	rep := NewReporter(Config{})
	if err := rep.Heartbeat(context.Background()); err != ErrNoAliveURL {
		t.Fatalf("Heartbeat err = %v, want ErrNoAliveURL", err)
	}
	if err := rep.RunStarted(context.Background(), "r"); err != ErrNoBackupURL {
		t.Fatalf("RunStarted err = %v, want ErrNoBackupURL", err)
	}
	if err := rep.RunFinished(context.Background(), "r", 0, ""); err != ErrNoBackupURL {
		t.Fatalf("RunFinished err = %v, want ErrNoBackupURL", err)
	}
	if rep.HasAliveURL() || rep.HasBackupURL() {
		t.Fatalf("empty reporter should report no URLs")
	}
}

func TestNon2xxIsError(t *testing.T) {
	_, rep, done := newServer(t, 500, true)
	defer done()
	if err := rep.Heartbeat(context.Background()); err == nil {
		t.Fatalf("expected error on HTTP 500")
	} else if strings.Contains(err.Error(), "/ping/") {
		t.Fatalf("error must not leak the ping URL: %v", err)
	}
}

func TestBodyCappedToTail(t *testing.T) {
	cap, rep, done := newServer(t, 200, true)
	defer done()
	big := strings.Repeat("A", maxPingBody) + "TAIL_MARKER"
	if err := rep.RunFinished(context.Background(), "r", 4, big); err != nil {
		t.Fatalf("RunFinished: %v", err)
	}
	g := cap.get()
	if len(g.body) != maxPingBody {
		t.Fatalf("body len %d, want capped to %d", len(g.body), maxPingBody)
	}
	if !strings.HasSuffix(g.body, "TAIL_MARKER") {
		t.Fatalf("cap must keep the tail, got suffix %q", g.body[len(g.body)-16:])
	}
}

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewRunIDIsUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewRunID()
		if !uuidV4Re.MatchString(id) {
			t.Fatalf("NewRunID %q is not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewRunID collision: %q", id)
		}
		seen[id] = true
	}
}
