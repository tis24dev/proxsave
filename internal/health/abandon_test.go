package health

import (
	"os"
	"testing"
)

// TestAbandonMarkerRoundTrip pins the three states the daemon distinguishes: no marker (the
// normal case), a marker it wrote itself, and a marker it has cleared after a run completed.
func TestAbandonMarkerRoundTrip(t *testing.T) {
	base := t.TempDir()

	if rec, err := ReadAbandon(base); err != nil || rec != nil {
		t.Fatalf("no marker must read as (nil, nil), got (%+v, %v)", rec, err)
	}
	if err := ClearAbandon(base); err != nil {
		t.Fatalf("clearing an absent marker must be a no-op: %v", err)
	}

	if err := WriteAbandon(base, AbandonRecord{PID: 4242, RID: "abc", TS: 1700000000}); err != nil {
		t.Fatalf("WriteAbandon: %v", err)
	}
	rec, err := ReadAbandon(base)
	if err != nil {
		t.Fatalf("ReadAbandon: %v", err)
	}
	if rec == nil || rec.PID != 4242 || rec.RID != "abc" || rec.TS != 1700000000 {
		t.Fatalf("marker round-trip lost data: %+v", rec)
	}
	if _, err := os.Stat(AbandonPath(base) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("the atomic write must leave no .tmp behind (err=%v)", err)
	}

	if err := ClearAbandon(base); err != nil {
		t.Fatalf("ClearAbandon: %v", err)
	}
	if rec, err := ReadAbandon(base); err != nil || rec != nil {
		t.Fatalf("a cleared marker must read as (nil, nil), got (%+v, %v)", rec, err)
	}
}

// TestAbandonMarkerPresenceIsTheSignal guards the tolerance that matters: the daemon reads
// this file to decide whether to keep reporting the service-alive check DOWN. Unreadable
// CONTENTS must never be mistaken for "no abandon happened" -- that would silently re-green a
// host whose backups are dead, which is the whole failure this marker exists to prevent.
func TestAbandonMarkerPresenceIsTheSignal(t *testing.T) {
	base := t.TempDir()
	if err := WriteAbandon(base, AbandonRecord{PID: 1}); err != nil {
		t.Fatalf("WriteAbandon: %v", err)
	}
	if err := os.WriteFile(AbandonPath(base), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt the marker: %v", err)
	}
	rec, err := ReadAbandon(base)
	if err != nil {
		t.Fatalf("a corrupt marker must not be an error: %v", err)
	}
	if rec == nil {
		t.Fatal("a corrupt marker must still count as an abandon, not as its absence")
	}
	if rec.PID != 0 {
		t.Fatalf("unparseable contents must degrade to the zero record, got %+v", rec)
	}
}
