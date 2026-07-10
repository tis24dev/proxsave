package health

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A malformed status file must wrap ErrStatusParse so a writer can tell a
// recoverable corruption from a genuine read/permission error.
func TestLoadStatusParseErrorIsErrStatusParse(t *testing.T) {
	base := writeCorruptStatus(t)
	_, err := LoadStatus(base)
	if !errors.Is(err, ErrStatusParse) {
		t.Fatalf("parse failure should wrap ErrStatusParse, got %v", err)
	}
}

// A corrupt status file must NOT freeze the writers: the next write self-heals by
// quarantining the unreadable bytes and overwriting from a zero Status, and the
// corrupt-file hook fires so the daemon can debug-log the event.
func TestRecordPingSelfHealsCorruptStatus(t *testing.T) {
	base := writeCorruptStatus(t)
	path := StatusPath(base)

	var quarantined string
	orig := corruptStatusHook
	corruptStatusHook = func(p string) { quarantined = p }
	t.Cleanup(func() { corruptStatusHook = orig })

	if err := RecordPing(base, "self", KindHeartbeat, 1234, true, nil); err != nil {
		t.Fatalf("RecordPing on a corrupt file should self-heal, got %v", err)
	}

	st, err := LoadStatus(base)
	if err != nil {
		t.Fatalf("status should be valid after self-heal, got %v", err)
	}
	if rec := st.Record(KindHeartbeat); rec == nil || rec.TS != 1234 || !rec.OK {
		t.Fatalf("self-healed status missing the new record: %+v", st)
	}

	corruptPath := path + ".corrupt"
	if quarantined != corruptPath {
		t.Fatalf("hook quarantine path = %q, want %q", quarantined, corruptPath)
	}
	if data, err := os.ReadFile(corruptPath); err != nil || string(data) != "{not json" {
		t.Fatalf("quarantine sidecar missing/incorrect: data=%q err=%v", string(data), err)
	}
}

// RecordUpdate and RecordNotifyPing share loadStatusForWrite, so they must
// self-heal a corrupt file too (no freeze).
func TestRecordUpdateAndNotifySelfHealCorruptStatus(t *testing.T) {
	base := writeCorruptStatus(t)
	if err := RecordUpdate(base, "self", 10, true, "v9.9.9", true, nil); err != nil {
		t.Fatalf("RecordUpdate on a corrupt file should self-heal, got %v", err)
	}
	// The heartbeat write below reloads the (now valid) file and must not freeze.
	if err := RecordNotifyPing(base, "self", "notify-email", 20, true, false, nil); err != nil {
		t.Fatalf("RecordNotifyPing after self-heal should succeed, got %v", err)
	}
	st, err := LoadStatus(base)
	if err != nil {
		t.Fatalf("status should be valid after self-heal, got %v", err)
	}
	if st.Update == nil || st.Update.Latest != "v9.9.9" {
		t.Fatalf("update record lost after self-heal: %+v", st.Update)
	}
	if rec := st.Record("notify-email"); rec == nil || rec.TS != 20 {
		t.Fatalf("notify record missing after self-heal: %+v", st)
	}
}

func writeCorruptStatus(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	path := StatusPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt status file: %v", err)
	}
	return base
}
