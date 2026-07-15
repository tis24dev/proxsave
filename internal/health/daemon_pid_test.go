package health

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDaemonPIDRoundTrip: a written pid reads back as the same integer.
func TestDaemonPIDRoundTrip(t *testing.T) {
	base := t.TempDir()
	if err := WriteDaemonPID(base, 4321); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	pid, err := ReadDaemonPID(base)
	if err != nil {
		t.Fatalf("ReadDaemonPID: %v", err)
	}
	if pid != 4321 {
		t.Fatalf("ReadDaemonPID = %d, want 4321", pid)
	}
}

// TestReadDaemonPIDMissing: a missing file yields (0, nil) -- the normal "no daemon recorded"
// state a standalone run treats as "nothing to signal".
func TestReadDaemonPIDMissing(t *testing.T) {
	base := t.TempDir()
	pid, err := ReadDaemonPID(base)
	if err != nil {
		t.Fatalf("ReadDaemonPID on missing file: unexpected error %v", err)
	}
	if pid != 0 {
		t.Fatalf("ReadDaemonPID on missing file = %d, want 0", pid)
	}
}

// TestReadDaemonPIDGarbage: an empty or non-numeric file is an error (a present-but-garbage pid
// must not be mistaken for a safe zero).
func TestReadDaemonPIDGarbage(t *testing.T) {
	for _, content := range []string{"", "   ", "not-a-pid"} {
		base := t.TempDir()
		path := DaemonPIDPath(base)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir identity dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write pid file: %v", err)
		}
		if _, err := ReadDaemonPID(base); err == nil {
			t.Fatalf("ReadDaemonPID(%q) should error", content)
		}
	}
}

// TestRemoveDaemonPID: Remove deletes an existing pid file and is a no-op (nil) when missing.
func TestRemoveDaemonPID(t *testing.T) {
	base := t.TempDir()
	if err := WriteDaemonPID(base, 99); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	if err := RemoveDaemonPID(base); err != nil {
		t.Fatalf("RemoveDaemonPID (present): %v", err)
	}
	if _, statErr := os.Stat(DaemonPIDPath(base)); !os.IsNotExist(statErr) {
		t.Fatalf("pid file should be gone after Remove, stat err = %v", statErr)
	}
	if err := RemoveDaemonPID(base); err != nil {
		t.Fatalf("RemoveDaemonPID (missing) should be nil, got %v", err)
	}
	pid, err := ReadDaemonPID(base)
	if err != nil || pid != 0 {
		t.Fatalf("after Remove, Read = (%d, %v), want 0/nil", pid, err)
	}
}
