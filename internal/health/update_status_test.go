package health

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRecordUpdateRoundTrip: an update write round-trips (Ping/Available/Latest/Mode) and
// leaves a pre-existing heartbeat intact (shared file, read-modify-write).
func TestRecordUpdateRoundTrip(t *testing.T) {
	base := t.TempDir()
	if err := RecordPing(base, "self", KindHeartbeat, 500, true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if err := RecordUpdate(base, "self", 1234, true, "v9.9.9", true, nil); err != nil {
		t.Fatalf("RecordUpdate: %v", err)
	}
	st := mustLoad(t, base)
	if st.Update == nil {
		t.Fatalf("Update record missing")
	}
	if st.Update.Ping.TS != 1234 || !st.Update.Ping.OK {
		t.Fatalf("Update.Ping = %+v, want TS=1234 OK=true", st.Update.Ping)
	}
	if !st.Update.Available || st.Update.Latest != "v9.9.9" {
		t.Fatalf("Update semantics = %+v, want Available=true Latest=v9.9.9", st.Update)
	}
	if st.Update.Ping.Err != "" || st.Update.Ping.Reason != "" {
		t.Fatalf("ok update must have empty Err/Reason, got %+v", st.Update.Ping)
	}
	if st.Record(KindHeartbeat) == nil || st.Record(KindHeartbeat).TS != 500 {
		t.Fatalf("heartbeat must survive the update write, got %+v", st.Record(KindHeartbeat))
	}
	if st.Mode != "self" {
		t.Fatalf("mode = %q, want self", st.Mode)
	}
}

// TestRecordUpdateNoURLReason: a report that failed with ErrNoUpdatesURL is stored as
// Reason=no_url with an empty Err, and the Available SIGNAL persists regardless.
func TestRecordUpdateNoURLReason(t *testing.T) {
	base := t.TempDir()
	if err := RecordUpdate(base, "centralized", 10, true, "v2", false, ErrNoUpdatesURL); err != nil {
		t.Fatalf("RecordUpdate no-url: %v", err)
	}
	up := mustLoad(t, base).Update
	if up == nil || up.Ping.OK {
		t.Fatalf("no-url update should be OK=false, got %+v", up)
	}
	if up.Ping.Reason != ReasonNoURL {
		t.Fatalf("Reason = %q, want %q", up.Ping.Reason, ReasonNoURL)
	}
	if up.Ping.Err != "" {
		t.Fatalf("no-url update must not populate Err, got %q", up.Ping.Err)
	}
	if !up.Available {
		t.Fatalf("Available must persist regardless of transmission, got %+v", up)
	}
}

// TestRecordUpdateRealErrPopulatesErr: a genuine transmit error keeps Reason empty and
// stores the (already redacted) error text.
func TestRecordUpdateRealErrPopulatesErr(t *testing.T) {
	base := t.TempDir()
	if err := RecordUpdate(base, "self", 20, false, "", false, errors.New("healthcheck updates: HTTP 500")); err != nil {
		t.Fatalf("RecordUpdate real err: %v", err)
	}
	up := mustLoad(t, base).Update
	if up == nil || up.Ping.Reason != "" || up.Ping.Err != "healthcheck updates: HTTP 500" {
		t.Fatalf("real error should be Reason='' + Err set, got %+v", up)
	}
}

// TestLoadStatusToleratesOldFileWithoutUpdate: a status file written by an OLDER daemon (no
// "update" key) must load cleanly with Update==nil, and a later RecordUpdate adds the record
// without disturbing the pre-existing heartbeat.
func TestLoadStatusToleratesOldFileWithoutUpdate(t *testing.T) {
	base := t.TempDir()
	path := StatusPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity: %v", err)
	}
	old := `{"mode":"centralized","heartbeat":{"ts":777,"ok":true}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("write old status: %v", err)
	}
	st := mustLoad(t, base)
	if st.Update != nil {
		t.Fatalf("old file must yield Update==nil, got %+v", st.Update)
	}
	if st.Record(KindHeartbeat) == nil || st.Record(KindHeartbeat).TS != 777 {
		t.Fatalf("old heartbeat must load, got %+v", st.Record(KindHeartbeat))
	}
	if err := RecordUpdate(base, "centralized", 888, false, "v1", true, nil); err != nil {
		t.Fatalf("RecordUpdate over old file: %v", err)
	}
	st = mustLoad(t, base)
	if st.Update == nil || st.Update.Ping.TS != 888 || st.Update.Available {
		t.Fatalf("update not added over old file: %+v", st.Update)
	}
	if st.Record(KindHeartbeat) == nil || st.Record(KindHeartbeat).TS != 777 {
		t.Fatalf("heartbeat must be preserved across the update write, got %+v", st.Record(KindHeartbeat))
	}
}
