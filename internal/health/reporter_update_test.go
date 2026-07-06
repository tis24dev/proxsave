package health

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReportUpdatePings0vs1: available=false hits /0 (check stays UP), available=true hits
// /1 (check goes DOWN so alerts fire). The stub http.Client is the same shape as the other
// reporter tests (capture struct from reporter_test.go).
func TestReportUpdatePings0vs1(t *testing.T) {
	cap := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "OK")
	}))
	defer srv.Close()
	rep := NewReporter(Config{Client: srv.Client(), UpdatesURL: srv.URL + "/ping/updates"})
	if !rep.HasUpdatesURL() {
		t.Fatalf("HasUpdatesURL should be true")
	}

	if err := rep.ReportUpdate(context.Background(), false); err != nil {
		t.Fatalf("ReportUpdate(false): %v", err)
	}
	if g := cap.get(); g.method != http.MethodPost || g.path != "/ping/updates/0" {
		t.Fatalf("up-to-date hit %s %s, want POST /ping/updates/0", g.method, g.path)
	}

	if err := rep.ReportUpdate(context.Background(), true); err != nil {
		t.Fatalf("ReportUpdate(true): %v", err)
	}
	if g := cap.get(); g.path != "/ping/updates/1" {
		t.Fatalf("available hit path %q, want /ping/updates/1", g.path)
	}
}

// TestReportUpdateNoURL: an unconfigured updates URL returns ErrNoUpdatesURL and never
// transmits (symmetric with the alive/backup no-url guards).
func TestReportUpdateNoURL(t *testing.T) {
	rep := NewReporter(Config{})
	if rep.HasUpdatesURL() {
		t.Fatalf("empty reporter must report no updates URL")
	}
	if err := rep.ReportUpdate(context.Background(), true); err != ErrNoUpdatesURL {
		t.Fatalf("ReportUpdate err = %v, want ErrNoUpdatesURL", err)
	}
}
