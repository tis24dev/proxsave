package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDaemonInfoRoundTrip: a written record reads back field-for-field, and no stray ".tmp" is left
// after the atomic write.
func TestDaemonInfoRoundTrip(t *testing.T) {
	base := t.TempDir()
	want := DaemonInfo{
		PID:      1234,
		ExecPath: "/usr/local/bin/proxsave",
		Binary:   BinaryIdentity{Path: "/usr/local/bin/proxsave", SHA256: "deadbeef", Size: 42, MTime: 1000},
		Version:  "1.2.3",
		Commit:   "abc123",
		StartTS:  1700000000,
	}
	if err := WriteDaemonInfo(base, want); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	got, found, err := ReadDaemonInfo(base)
	if err != nil {
		t.Fatalf("ReadDaemonInfo: %v", err)
	}
	if !found {
		t.Fatalf("ReadDaemonInfo found = false, want true")
	}
	if got != want {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
	if _, statErr := os.Stat(DaemonInfoPath(base) + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("stray .tmp left after write, stat err = %v", statErr)
	}
}

// TestReadDaemonInfoMissing: a missing file yields (zero, false, nil).
func TestReadDaemonInfoMissing(t *testing.T) {
	info, found, err := ReadDaemonInfo(t.TempDir())
	if err != nil {
		t.Fatalf("ReadDaemonInfo(missing): unexpected error %v", err)
	}
	if found || info != (DaemonInfo{}) {
		t.Fatalf("ReadDaemonInfo(missing) = (%+v, %v), want (zero, false)", info, found)
	}
}

// TestReadDaemonInfoEmpty: an empty file is tolerated like a missing one -> (zero, false, nil).
func TestReadDaemonInfoEmpty(t *testing.T) {
	base := t.TempDir()
	path := DaemonInfoPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	info, found, err := ReadDaemonInfo(base)
	if err != nil {
		t.Fatalf("ReadDaemonInfo(empty): unexpected error %v", err)
	}
	if found || info != (DaemonInfo{}) {
		t.Fatalf("ReadDaemonInfo(empty) = (%+v, %v), want (zero, false)", info, found)
	}
}

// TestReadDaemonInfoGarbage: malformed JSON is an error, and the returned record is the zero value.
func TestReadDaemonInfoGarbage(t *testing.T) {
	base := t.TempDir()
	path := DaemonInfoPath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	info, found, err := ReadDaemonInfo(base)
	if err == nil {
		t.Fatalf("ReadDaemonInfo(garbage) should error")
	}
	if found || info != (DaemonInfo{}) {
		t.Fatalf("ReadDaemonInfo(garbage) = (%+v, %v), want (zero, false)", info, found)
	}
}

// TestRemoveDaemonInfoIdempotent: Remove deletes an existing record and is a no-op (nil) when
// missing.
func TestRemoveDaemonInfoIdempotent(t *testing.T) {
	base := t.TempDir()
	if err := WriteDaemonInfo(base, DaemonInfo{PID: 7}); err != nil {
		t.Fatalf("WriteDaemonInfo: %v", err)
	}
	if err := RemoveDaemonInfo(base); err != nil {
		t.Fatalf("RemoveDaemonInfo (present): %v", err)
	}
	if _, statErr := os.Stat(DaemonInfoPath(base)); !os.IsNotExist(statErr) {
		t.Fatalf("info file should be gone after Remove, stat err = %v", statErr)
	}
	if err := RemoveDaemonInfo(base); err != nil {
		t.Fatalf("RemoveDaemonInfo (missing) should be nil, got %v", err)
	}
}
