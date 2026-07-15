package health

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRecordPingRoundTripEachKind writes one kind at a time and confirms that,
// after each write, LoadStatus returns exactly the record set, the other kinds
// stay nil until they are recorded, and earlier records accumulate rather than
// being clobbered. It also checks that TS/OK/Mode round-trip through the file.
func TestRecordPingRoundTripEachKind(t *testing.T) {
	base := t.TempDir()

	// Each step records one kind, then asserts which fields are (non-)nil so we
	// see accumulation kind by kind.
	if err := RecordPing(base, "centralized", KindHeartbeat, 1000, true, nil); err != nil {
		t.Fatalf("RecordPing heartbeat: %v", err)
	}
	st := mustLoad(t, base)
	assertRecord(t, "heartbeat", st.Record(KindHeartbeat), 1000, true, "")
	if st.Record(KindRunStarted) != nil || st.Record(KindRunFinished) != nil || st.Record(KindRunHang) != nil {
		t.Fatalf("only heartbeat should be set, got %+v", st)
	}
	if st.Mode != "centralized" {
		t.Fatalf("mode = %q, want centralized", st.Mode)
	}

	if err := RecordPing(base, "centralized", KindRunStarted, 2000, true, nil); err != nil {
		t.Fatalf("RecordPing start: %v", err)
	}
	st = mustLoad(t, base)
	assertRecord(t, "heartbeat", st.Record(KindHeartbeat), 1000, true, "") // still there
	assertRecord(t, "run_started", st.Record(KindRunStarted), 2000, true, "")
	if st.Record(KindRunFinished) != nil || st.Record(KindRunHang) != nil {
		t.Fatalf("finished/hang should still be nil, got %+v", st)
	}

	if err := RecordPing(base, "centralized", KindRunFinished, 3000, true, nil); err != nil {
		t.Fatalf("RecordPing finish: %v", err)
	}
	st = mustLoad(t, base)
	assertRecord(t, "run_finished", st.Record(KindRunFinished), 3000, true, "")
	if st.Record(KindHeartbeat) == nil || st.Record(KindRunStarted) == nil || st.Record(KindRunHang) != nil {
		t.Fatalf("finish step: unexpected field state %+v", st)
	}

	if err := RecordPing(base, "centralized", KindRunHang, 4000, false, errors.New("timed out")); err != nil {
		t.Fatalf("RecordPing hang: %v", err)
	}
	st = mustLoad(t, base)
	assertRecord(t, "run_hang", st.Record(KindRunHang), 4000, false, "timed out")
	if st.Record(KindHeartbeat) == nil || st.Record(KindRunStarted) == nil || st.Record(KindRunFinished) == nil {
		t.Fatalf("hang step: earlier records must persist, got %+v", st)
	}
}

// TestLoadStatusMissingFile: an untouched base dir has no file at all -> zero
// Status, nil error (the "nothing recorded yet" path).
func TestLoadStatusMissingFile(t *testing.T) {
	base := t.TempDir()
	st, err := LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus on missing file: unexpected error %v", err)
	}
	if !isZeroStatus(st) {
		t.Fatalf("LoadStatus on missing file should be zero, got %+v", st)
	}
}

// TestLoadStatusEmptyFile: a zero-byte file (e.g. an interrupted write) must be
// treated exactly like a missing file, not as malformed JSON.
func TestLoadStatusEmptyFile(t *testing.T) {
	base := t.TempDir()
	path := StatusPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty status file: %v", err)
	}
	st, err := LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus on empty file: unexpected error %v", err)
	}
	if !isZeroStatus(st) {
		t.Fatalf("LoadStatus on empty file should be zero, got %+v", st)
	}
}

// TestLoadStatusBadJSON: garbage content surfaces as an error, and the returned
// Status is the zero value so a tolerant caller renders "no data" rather than
// trusting a half-parsed struct.
func TestLoadStatusBadJSON(t *testing.T) {
	base := t.TempDir()
	path := StatusPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write bad status file: %v", err)
	}
	st, err := LoadStatus(base)
	if err == nil {
		t.Fatalf("LoadStatus on bad JSON should error")
	}
	if !isZeroStatus(st) {
		t.Fatalf("LoadStatus on bad JSON should return zero Status, got %+v", st)
	}
}

// TestErrPopulatedOnlyOnFailure: a nil pingErr leaves Err empty (OK path); a
// non-nil pingErr stores its text verbatim (the Reporter has already redacted it).
func TestErrPopulatedOnlyOnFailure(t *testing.T) {
	base := t.TempDir()

	if err := RecordPing(base, "self", KindHeartbeat, 10, true, nil); err != nil {
		t.Fatalf("RecordPing ok: %v", err)
	}
	if st := mustLoad(t, base); st.Record(KindHeartbeat) == nil || st.Record(KindHeartbeat).Err != "" {
		t.Fatalf("nil pingErr should leave Err empty, got %+v", st.Record(KindHeartbeat))
	}

	if err := RecordPing(base, "self", KindHeartbeat, 20, false, errors.New("healthcheck alive: dial tcp: refused")); err != nil {
		t.Fatalf("RecordPing fail: %v", err)
	}
	st := mustLoad(t, base)
	if st.Record(KindHeartbeat) == nil || st.Record(KindHeartbeat).OK {
		t.Fatalf("failed ping should record OK=false, got %+v", st.Record(KindHeartbeat))
	}
	if st.Record(KindHeartbeat).Err != "healthcheck alive: dial tcp: refused" {
		t.Fatalf("Err = %q, want the redacted ping error", st.Record(KindHeartbeat).Err)
	}
}

// TestNoURLErrorClassifiedAsReason: a ping that failed with ErrNoAliveURL/ErrNoBackupURL
// (no endpoint resolved) is recorded as OK=false with Reason=ReasonNoURL and an EMPTY
// Err, so the section can render a clean "not provisioned yet" line instead of leaking
// an internal error string. Any OTHER error keeps Reason empty and populates Err.
func TestNoURLErrorClassifiedAsReason(t *testing.T) {
	base := t.TempDir()

	if err := RecordPing(base, "centralized", KindHeartbeat, 10, false, ErrNoAliveURL); err != nil {
		t.Fatalf("RecordPing no-url: %v", err)
	}
	st := mustLoad(t, base)
	if st.Record(KindHeartbeat) == nil || st.Record(KindHeartbeat).OK {
		t.Fatalf("no-url ping should record OK=false, got %+v", st.Record(KindHeartbeat))
	}
	if st.Record(KindHeartbeat).Reason != ReasonNoURL {
		t.Fatalf("Reason = %q, want %q", st.Record(KindHeartbeat).Reason, ReasonNoURL)
	}
	if st.Record(KindHeartbeat).Err != "" {
		t.Fatalf("no-url ping must not populate Err, got %q", st.Record(KindHeartbeat).Err)
	}

	// ErrNoBackupURL classifies the same way.
	if err := RecordPing(base, "centralized", KindRunFinished, 20, false, ErrNoBackupURL); err != nil {
		t.Fatalf("RecordPing no-url backup: %v", err)
	}
	if rf := mustLoad(t, base).Record(KindRunFinished); rf == nil || rf.Reason != ReasonNoURL || rf.Err != "" {
		t.Fatalf("ErrNoBackupURL should be Reason=no_url + empty Err, got %+v", rf)
	}

	// A genuine error keeps Reason empty and fills Err.
	if err := RecordPing(base, "centralized", KindHeartbeat, 30, false, errors.New("healthcheck alive: HTTP 500")); err != nil {
		t.Fatalf("RecordPing real err: %v", err)
	}
	hb := mustLoad(t, base).Record(KindHeartbeat)
	if hb.Reason != "" || hb.Err != "healthcheck alive: HTTP 500" {
		t.Fatalf("real error should be Reason='' + Err set, got %+v", hb)
	}
}

// TestStatusPathShape pins the file location both siblings depend on.
func TestStatusPathShape(t *testing.T) {
	base := "/opt/proxsave"
	want := filepath.Join(base, "identity", ".healthcheck_status.json")
	if got := StatusPath(base); got != want {
		t.Fatalf("StatusPath = %q, want %q", got, want)
	}
}

// TestAtomicWriteLeavesNoTmp: the write goes through a ".tmp" sibling that is
// renamed into place, so the identity dir must hold only the final file
// afterwards (no leftover temp).
func TestAtomicWriteLeavesNoTmp(t *testing.T) {
	base := t.TempDir()
	if err := RecordPing(base, "centralized", KindRunFinished, 5000, true, nil); err != nil {
		t.Fatalf("RecordPing: %v", err)
	}
	dir := filepath.Dir(StatusPath(base))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read identity dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover temp file after atomic write: %s", e.Name())
		}
	}
	if _, err := os.Stat(StatusPath(base)); err != nil {
		t.Fatalf("final status file missing after write: %v", err)
	}
}

// --- helpers ---

func mustLoad(t *testing.T, base string) Status {
	t.Helper()
	st, err := LoadStatus(base)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	return st
}

func assertRecord(t *testing.T, name string, got *PingRecord, wantTS int64, wantOK bool, wantErr string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s record is nil, want set", name)
	}
	if got.TS != wantTS {
		t.Fatalf("%s TS = %d, want %d", name, got.TS, wantTS)
	}
	if got.OK != wantOK {
		t.Fatalf("%s OK = %v, want %v", name, got.OK, wantOK)
	}
	if got.Err != wantErr {
		t.Fatalf("%s Err = %q, want %q", name, got.Err, wantErr)
	}
}

func isZeroStatus(s Status) bool {
	return len(s.Records) == 0 && s.Update == nil && s.Mode == ""
}
