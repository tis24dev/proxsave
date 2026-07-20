package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRawStatus drops a verbatim JSON literal at the status path so the tests can
// exercise the on-disk legacy/new formats directly (bypassing writeStatus).
func writeRawStatus(t *testing.T, base, raw string) {
	t.Helper()
	path := StatusPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write raw status: %v", err)
	}
}

// TestStatusLegacyShimRoundTrip: a status file written in the pre-Fase-2 format (fixed
// kinds as top-level "heartbeat"/"run_finished"/"run_hang" keys, no "records" map) must load
// through UnmarshalJSON's migration into Records under the right KIND, with Update/Mode
// recovered too. A legacy key that is absent (run_started here) stays nil.
func TestStatusLegacyShimRoundTrip(t *testing.T) {
	base := t.TempDir()
	writeRawStatus(t, base, `{
		"mode": "centralized",
		"heartbeat":    {"ts": 100, "ok": true},
		"run_finished": {"ts": 200, "ok": false, "err": "boom"},
		"run_hang":     {"ts": 150, "ok": true},
		"update":       {"ping": {"ts": 300, "ok": true}, "available": true, "latest": "v2"}
	}`)

	st := mustLoad(t, base)
	assertRecord(t, "heartbeat", st.Record(KindHeartbeat), 100, true, "")
	assertRecord(t, "run_finished", st.Record(KindRunFinished), 200, false, "boom")
	assertRecord(t, "run_hang", st.Record(KindRunHang), 150, true, "")
	// run_started was absent from the legacy file -> stays nil (matches input).
	if st.Record(KindRunStarted) != nil {
		t.Fatalf("absent legacy run_started must stay nil, got %+v", st.Record(KindRunStarted))
	}
	if st.Update == nil || st.Update.Ping.TS != 300 || !st.Update.Available || st.Update.Latest != "v2" {
		t.Fatalf("update not recovered from legacy file, got %+v", st.Update)
	}
	if st.Mode != "centralized" {
		t.Fatalf("mode = %q, want centralized", st.Mode)
	}
}

// TestStatusShimFirstHeartbeatKeepsPriorOutcome is THE data-loss guard: an in-place daemon
// upgrade whose FIRST write is a heartbeat read-modify-write must NOT drop the last backup
// outcome + update verdict that a legacy file carried as top-level keys. RecordPing does a
// LoadStatus -> mutate -> writeStatus cycle; if UnmarshalJSON did not fold the legacy keys
// into Records, that reload would return them as nil and the write-back would erase them.
func TestStatusShimFirstHeartbeatKeepsPriorOutcome(t *testing.T) {
	base := t.TempDir()
	// Legacy file: a prior backup outcome + update verdict, NO heartbeat and NO records map.
	writeRawStatus(t, base, `{
		"mode": "centralized",
		"run_finished": {"ts": 200, "ok": true},
		"update":       {"ping": {"ts": 300, "ok": true}, "available": false, "latest": "v1"}
	}`)

	// The first heartbeat after upgrade: a read-modify-write over the legacy file.
	if err := RecordPing(base, "self", KindHeartbeat, 999, true, nil); err != nil {
		t.Fatalf("RecordPing heartbeat: %v", err)
	}

	st := mustLoad(t, base)
	// The new heartbeat landed...
	assertRecord(t, "heartbeat", st.Record(KindHeartbeat), 999, true, "")
	// ...AND the pre-existing outcome survived the RMW (this fails without the shim).
	if rf := st.Record(KindRunFinished); rf == nil || rf.TS != 200 || !rf.OK {
		t.Fatalf("legacy run_finished dropped by the first-heartbeat RMW, got %+v", rf)
	}
	if st.Update == nil || st.Update.Ping.TS != 300 || st.Update.Available {
		t.Fatalf("legacy update verdict dropped by the first-heartbeat RMW, got %+v", st.Update)
	}
}

// TestStatusNewFormatRoundTrip: RecordPing several kinds, including a dynamic notify-<ch>
// kind, and confirm they all persist into Records (and the file is written in the new
// "records" shape, not the legacy top-level keys).
func TestStatusNewFormatRoundTrip(t *testing.T) {
	base := t.TempDir()
	notifyKind := CheckKeyNotify("email") // "notify-email"

	for _, step := range []struct {
		kind string
		ts   int64
	}{
		{KindHeartbeat, 1},
		{KindRunFinished, 2},
		{notifyKind, 3},
	} {
		if err := RecordPing(base, "self", step.kind, step.ts, true, nil); err != nil {
			t.Fatalf("RecordPing %s: %v", step.kind, err)
		}
	}

	st := mustLoad(t, base)
	assertRecord(t, "heartbeat", st.Record(KindHeartbeat), 1, true, "")
	assertRecord(t, "run_finished", st.Record(KindRunFinished), 2, true, "")
	assertRecord(t, "notify-email", st.Record(notifyKind), 3, true, "")

	// The on-disk shape is the new "records" map, not the legacy top-level tags.
	data, err := os.ReadFile(StatusPath(base))
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	if !strings.Contains(string(data), `"records"`) {
		t.Fatalf("new-format file must carry a records map, got:\n%s", data)
	}
	if strings.Contains(string(data), `"run_finished"`) {
		t.Fatalf("new-format file must not write legacy top-level keys, got:\n%s", data)
	}
}

// TestStatusRecordsWinOverLegacy: when a file carries BOTH a records-map entry and a legacy
// top-level key for the same kind, the records-map value wins (new format is authoritative;
// the fold only fills kinds the map does not already carry).
func TestStatusRecordsWinOverLegacy(t *testing.T) {
	base := t.TempDir()
	writeRawStatus(t, base, `{
		"records":   {"heartbeat": {"ts": 999, "ok": true}},
		"heartbeat": {"ts": 111, "ok": false}
	}`)

	st := mustLoad(t, base)
	hb := st.Record(KindHeartbeat)
	if hb == nil || hb.TS != 999 || !hb.OK {
		t.Fatalf("records map must win over the legacy key, got %+v", hb)
	}
}

// TestRecordPingDynamicKind: a dynamic notify-<ch> kind is stored into Records without error
// (the old code errored on any kind outside a fixed switch; the new code default-stores).
func TestRecordPingDynamicKind(t *testing.T) {
	base := t.TempDir()
	if err := RecordPing(base, "self", "notify-email", 5, true, nil); err != nil {
		t.Fatalf("dynamic kind must not error, got %v", err)
	}
	rec := mustLoad(t, base).Record("notify-email")
	if rec == nil || rec.TS != 5 || !rec.OK {
		t.Fatalf("dynamic kind not stored, got %+v", rec)
	}
}

// TestRecordPingEmptyKindRejected: an empty kind is a caller bug -> it returns an error and
// must NOT create a fresh file nor mutate an existing one (the guard runs before any write).
func TestRecordPingEmptyKindRejected(t *testing.T) {
	// Fresh dir: empty kind must not create the file.
	base := t.TempDir()
	if err := RecordPing(base, "self", "", 5, true, nil); err == nil {
		t.Fatalf("empty kind should return an error")
	}
	if _, statErr := os.Stat(StatusPath(base)); !os.IsNotExist(statErr) {
		t.Fatalf("empty kind must not create the status file (stat err=%v)", statErr)
	}

	// Existing file: empty kind must not mutate it.
	if err := RecordPing(base, "self", KindHeartbeat, 7, true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	before, err := os.ReadFile(StatusPath(base))
	if err != nil {
		t.Fatalf("read seeded file: %v", err)
	}
	if err := RecordPing(base, "self", "", 8, true, nil); err == nil {
		t.Fatalf("empty kind over an existing file should return an error")
	}
	after, err := os.ReadFile(StatusPath(base))
	if err != nil {
		t.Fatalf("read file after rejected write: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("empty kind must not mutate the file:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestPingRecordDownOmitempty pins the byte-compat contract: Down=false (the common case)
// marshals WITHOUT a "down" field, so an old reader on a downgrade sees byte-identical
// records; Down=true emits it.
func TestPingRecordDownOmitempty(t *testing.T) {
	off, err := json.Marshal(PingRecord{TS: 1, OK: true})
	if err != nil {
		t.Fatalf("marshal Down=false: %v", err)
	}
	if strings.Contains(string(off), "down") {
		t.Fatalf("Down=false must omit the down field, got %s", off)
	}

	on, err := json.Marshal(PingRecord{TS: 1, OK: true, Down: true})
	if err != nil {
		t.Fatalf("marshal Down=true: %v", err)
	}
	if !strings.Contains(string(on), `"down":true`) {
		t.Fatalf("Down=true must emit the down field, got %s", on)
	}
}
