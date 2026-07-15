package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReporterHasCheckAndPing covers the dynamic-check surface added for per-notification
// checks: HasCheck reports resolution by name, Ping transmits to the named check with the
// given suffix + rid, and an unresolved name returns the generic ErrNoPingURL (which
// IsNoURLErr recognizes).
func TestReporterHasCheckAndPing(t *testing.T) {
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "OK")
	}))
	defer srv.Close()

	rep := NewReporter(Config{
		Client:   srv.Client(),
		AliveURL: "https://x/ping/a",
		Checks: map[string]string{
			CheckKeyUpdates:         "https://x/ping/u",
			CheckKeyNotify("email"): srv.URL + "/ping/n",
		},
	})

	if !rep.HasCheck(CheckKeyUpdates) {
		t.Fatalf("HasCheck(updates) should be true")
	}
	if !rep.HasCheck(CheckKeyNotify("email")) {
		t.Fatalf("HasCheck(notify-email) should be true")
	}
	if rep.HasCheck("nope") {
		t.Fatalf("HasCheck(nope) should be false")
	}

	// A resolved dynamic check pings base+suffix(+?rid): here /ping/n/1?rid=rid.
	if err := rep.Ping(context.Background(), CheckKeyNotify("email"), "/1", "rid", "", "lbl"); err != nil {
		t.Fatalf("Ping(notify-email): %v", err)
	}
	g := cap.get()
	if g.method != http.MethodPost || g.path != "/ping/n/1" {
		t.Fatalf("notify ping hit %s %s, want POST /ping/n/1", g.method, g.path)
	}
	if g.query != "rid=rid" {
		t.Fatalf("notify ping query %q, want rid=rid", g.query)
	}

	// An unresolved name returns ErrNoPingURL, recognized by IsNoURLErr.
	err := rep.Ping(context.Background(), "nope", "/1", "", "", "lbl")
	if err != ErrNoPingURL {
		t.Fatalf("Ping(nope) err = %v, want ErrNoPingURL", err)
	}
	if !IsNoURLErr(err) {
		t.Fatalf("IsNoURLErr(ErrNoPingURL) should be true")
	}
}

// TestNewReporterFoldsChecks: every Config.Checks entry becomes pingable (HasCheck true) and
// a trailing slash on a URL is normalized away so it joins cleanly with a suffix.
func TestNewReporterFoldsChecks(t *testing.T) {
	rep := NewReporter(Config{Checks: map[string]string{
		CheckKeyUpdates:         "https://x/ping/u/", // trailing slash must be trimmed
		CheckKeyNotify("email"): "https://x/ping/n",
	}})

	if !rep.HasCheck(CheckKeyUpdates) || !rep.HasCheck(CheckKeyNotify("email")) {
		t.Fatalf("Config.Checks entries must be pingable")
	}
	if got := rep.urls[CheckKeyUpdates]; got != "https://x/ping/u" {
		t.Fatalf("trailing slash not trimmed: url = %q, want https://x/ping/u", got)
	}
}
