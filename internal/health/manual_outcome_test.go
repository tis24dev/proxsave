package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManualOutcomeRoundTrip: a written handoff loads back identically (rid, ts, exit code).
func TestManualOutcomeRoundTrip(t *testing.T) {
	base := t.TempDir()
	if err := WriteManualOutcome(base, "rid-x", 4242, 7); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	mo, err := LoadManualOutcome(base)
	if err != nil {
		t.Fatalf("LoadManualOutcome: %v", err)
	}
	if mo.RID != "rid-x" || mo.TS != 4242 || mo.ExitCode != 7 {
		t.Fatalf("LoadManualOutcome = %+v, want {rid-x 4242 7}", mo)
	}
}

// TestLoadManualOutcomeMissingAndEmpty: a missing file AND a zero-byte file both yield the zero
// value with a nil error (the tolerant "nothing handed off" path).
func TestLoadManualOutcomeMissingAndEmpty(t *testing.T) {
	base := t.TempDir()

	mo, err := LoadManualOutcome(base)
	if err != nil {
		t.Fatalf("LoadManualOutcome on missing file: unexpected error %v", err)
	}
	if mo != (ManualOutcome{}) {
		t.Fatalf("missing file should be zero value, got %+v", mo)
	}

	path := ManualOutcomePath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty outcome file: %v", err)
	}
	mo, err = LoadManualOutcome(base)
	if err != nil {
		t.Fatalf("LoadManualOutcome on empty file: unexpected error %v", err)
	}
	if mo != (ManualOutcome{}) {
		t.Fatalf("empty file should be zero value, got %+v", mo)
	}
}

// TestLoadManualOutcomeMalformed: garbage content is an error, and the returned value is the zero
// value (never a half-parsed struct).
func TestLoadManualOutcomeMalformed(t *testing.T) {
	base := t.TempDir()
	path := ManualOutcomePath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("write bad outcome file: %v", err)
	}
	mo, err := LoadManualOutcome(base)
	if err == nil {
		t.Fatalf("LoadManualOutcome on malformed JSON should error")
	}
	if mo != (ManualOutcome{}) {
		t.Fatalf("malformed file should still return the zero value, got %+v", mo)
	}
}

// TestRemoveManualOutcome: Remove deletes an existing handoff and is a no-op (nil) when missing.
func TestRemoveManualOutcome(t *testing.T) {
	base := t.TempDir()
	if err := WriteManualOutcome(base, "rid-y", 1, 0); err != nil {
		t.Fatalf("WriteManualOutcome: %v", err)
	}
	if err := RemoveManualOutcome(base); err != nil {
		t.Fatalf("RemoveManualOutcome (present): %v", err)
	}
	if _, statErr := os.Stat(ManualOutcomePath(base)); !os.IsNotExist(statErr) {
		t.Fatalf("file should be gone after Remove, stat err = %v", statErr)
	}
	// Idempotent: removing an already-missing file is not an error.
	if err := RemoveManualOutcome(base); err != nil {
		t.Fatalf("RemoveManualOutcome (missing) should be nil, got %v", err)
	}
	mo, err := LoadManualOutcome(base)
	if err != nil || mo != (ManualOutcome{}) {
		t.Fatalf("after Remove, Load = (%+v, %v), want zero/nil", mo, err)
	}
}
