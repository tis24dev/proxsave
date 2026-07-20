package health

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestNotifyResultsRoundTrip: a written results file loads back identically (rid, ts, map).
func TestNotifyResultsRoundTrip(t *testing.T) {
	base := t.TempDir()
	want := map[string]string{"Email": "ok", "Telegram": "error"}
	if err := WriteNotifyResults(base, "rid1", 123, want); err != nil {
		t.Fatalf("WriteNotifyResults: %v", err)
	}
	nr, err := LoadNotifyResults(base)
	if err != nil {
		t.Fatalf("LoadNotifyResults: %v", err)
	}
	if nr.RID != "rid1" {
		t.Fatalf("RID = %q, want rid1", nr.RID)
	}
	if nr.TS != 123 {
		t.Fatalf("TS = %d, want 123", nr.TS)
	}
	if !reflect.DeepEqual(nr.Results, want) {
		t.Fatalf("Results = %#v, want %#v", nr.Results, want)
	}
}

// TestNotifyResultsNilWritesEmpty: a nil results map is persisted as an empty object, so
// Load returns a non-nil empty map with the rid preserved (the daemon's rid guard passes,
// but there is nothing to ping).
func TestNotifyResultsNilWritesEmpty(t *testing.T) {
	base := t.TempDir()
	if err := WriteNotifyResults(base, "rid2", 456, nil); err != nil {
		t.Fatalf("WriteNotifyResults nil: %v", err)
	}
	nr, err := LoadNotifyResults(base)
	if err != nil {
		t.Fatalf("LoadNotifyResults: %v", err)
	}
	if nr.RID != "rid2" {
		t.Fatalf("RID = %q, want rid2", nr.RID)
	}
	if nr.Results == nil {
		t.Fatalf("Results should be a non-nil empty map, got nil")
	}
	if len(nr.Results) != 0 {
		t.Fatalf("Results len = %d, want 0", len(nr.Results))
	}
}

// TestLoadNotifyResultsMissingAndEmpty: a missing file AND a zero-byte file both yield the
// zero value with a nil error (the tolerant "nothing recorded" path).
func TestLoadNotifyResultsMissingAndEmpty(t *testing.T) {
	base := t.TempDir()

	// Missing file.
	nr, err := LoadNotifyResults(base)
	if err != nil {
		t.Fatalf("LoadNotifyResults on missing file: unexpected error %v", err)
	}
	if nr.RID != "" || nr.TS != 0 || nr.Results != nil {
		t.Fatalf("missing file should be zero value, got %+v", nr)
	}

	// Zero-byte file at the results path (e.g. an interrupted write).
	path := NotifyResultsPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty results file: %v", err)
	}
	nr, err = LoadNotifyResults(base)
	if err != nil {
		t.Fatalf("LoadNotifyResults on empty file: unexpected error %v", err)
	}
	if nr.RID != "" || nr.TS != 0 || nr.Results != nil {
		t.Fatalf("empty file should be zero value, got %+v", nr)
	}
}

// TestLoadNotifyResultsMalformed: garbage content is an error, and the returned value is the
// zero value (never a half-parsed struct).
func TestLoadNotifyResultsMalformed(t *testing.T) {
	base := t.TempDir()
	path := NotifyResultsPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("write bad results file: %v", err)
	}
	nr, err := LoadNotifyResults(base)
	if err == nil {
		t.Fatalf("LoadNotifyResults on malformed JSON should error")
	}
	if nr.RID != "" || nr.TS != 0 || nr.Results != nil {
		t.Fatalf("malformed file should still return the zero value, got %+v", nr)
	}
}

// TestRecordNotifyPingStoresDown: RecordNotifyPing default-stores the Down signal alongside
// OK, maps a no-url error to ReasonNoURL, and rejects an empty kind without mutating the file.
func TestRecordNotifyPingStoresDown(t *testing.T) {
	base := t.TempDir()

	// A transmitted /1 ping: OK==true AND Down==true (orthogonal signals).
	if err := RecordNotifyPing(base, "self", "notify-email", 1000, true, true, nil); err != nil {
		t.Fatalf("RecordNotifyPing: %v", err)
	}
	st := mustLoad(t, base)
	rec := st.Record("notify-email")
	if rec == nil {
		t.Fatal("notify-email record is nil, want set")
	}
	if !rec.OK || !rec.Down {
		t.Fatalf("first ping: OK=%v Down=%v, want true/true", rec.OK, rec.Down)
	}
	if rec.Reason != "" {
		t.Fatalf("first ping should have no reason, got %q", rec.Reason)
	}

	// A no-url ping overwrites: OK==false, Down==true, Reason==ReasonNoURL.
	if err := RecordNotifyPing(base, "self", "notify-email", 2000, false, true, ErrNoPingURL); err != nil {
		t.Fatalf("RecordNotifyPing no-url: %v", err)
	}
	st = mustLoad(t, base)
	rec = st.Record("notify-email")
	if rec == nil {
		t.Fatal("notify-email record is nil after second write")
	}
	if rec.OK {
		t.Fatalf("no-url ping must record OK=false, got %+v", rec)
	}
	if !rec.Down {
		t.Fatalf("no-url ping must preserve Down=true, got %+v", rec)
	}
	if rec.Reason != ReasonNoURL {
		t.Fatalf("Reason = %q, want %q", rec.Reason, ReasonNoURL)
	}

	// An empty kind is a caller bug: it errors WITHOUT touching the file.
	before, _ := os.ReadFile(StatusPath(base))
	if err := RecordNotifyPing(base, "self", "", 3000, true, false, nil); err == nil {
		t.Fatal("empty kind must return an error")
	}
	after, _ := os.ReadFile(StatusPath(base))
	if string(before) != string(after) {
		t.Fatalf("empty kind must not mutate the status file")
	}
}
