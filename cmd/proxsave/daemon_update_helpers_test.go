// Package main contains the proxsave command entrypoint.
package main

import (
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
)

// TestRecordUpdateWiresStatusFile checks the daemon-side recordUpdate persists the update
// verdict orthogonally to the transmission outcome: a nil pingErr records OK=true, while
// ErrNoUpdatesURL records OK=false with reason=no_url yet keeps the Available verdict.
func TestRecordUpdateWiresStatusFile(t *testing.T) {
	d := newTestDaemon(t, &fakeReporter{}, nil, time.Hour)
	d.cfg.HealthcheckMode = "self"

	d.recordUpdate(true, "v9.9.9", nil)
	st, err := health.LoadStatus(d.cfg.BaseDir)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if st.Update == nil || !st.Update.Available || st.Update.Latest != "v9.9.9" || !st.Update.Ping.OK {
		t.Fatalf("recordUpdate(nil err) -> %+v", st.Update)
	}
	if st.Mode != "self" {
		t.Fatalf("mode=%q want self", st.Mode)
	}

	d.recordUpdate(true, "v9.9.9", health.ErrNoUpdatesURL)
	st, _ = health.LoadStatus(d.cfg.BaseDir)
	if st.Update.Ping.OK || st.Update.Ping.Reason != health.ReasonNoURL || !st.Update.Available {
		t.Fatalf("recordUpdate(ErrNoUpdatesURL) must be OK=false reason=no_url, Available kept -> %+v", st.Update)
	}
}

// TestOrUnknownVersion covers the WARNING version renderer: blank/whitespace -> "unknown",
// non-blank returned verbatim (untrimmed).
func TestOrUnknownVersion(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "unknown"}, {"   ", "unknown"}, {"v1.2.3", "v1.2.3"}, {" v2 ", " v2 "},
	} {
		if got := orUnknownVersion(tc.in); got != tc.want {
			t.Errorf("orUnknownVersion(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// TestQuietUpdateLoggerNonNil guards the silenced-logger helper used to suppress
// checkForUpdates' per-tick WARNING.
func TestQuietUpdateLoggerNonNil(t *testing.T) {
	if quietUpdateLogger() == nil {
		t.Fatal("quietUpdateLogger() returned nil")
	}
}
