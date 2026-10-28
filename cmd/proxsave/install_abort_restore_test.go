package main

import (
	"os"
	"path/filepath"
	"testing"
)

// An install the operator aborts after the wizard wrote backup.env puts the file back
// as it was: rewritten when it existed, removed when the install created it.
func TestInstallConfigSnapshotRestoresBackupEnvOnAbort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(path, []byte("ENCRYPT_ARCHIVE=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := takeInstallConfigSnapshot(path)
	if err := os.WriteFile(path, []byte("ENCRYPT_ARCHIVE=true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snap.restore(nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(path)
	st, _ := os.Stat(path)
	if err != nil || string(got) != "ENCRYPT_ARCHIVE=false\n" || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup.env = %q mode %v (err %v), want the file as it was before the install", got, st.Mode().Perm(), err)
	}

	fresh := filepath.Join(dir, "fresh.env")
	snap = takeInstallConfigSnapshot(fresh)
	if err := os.WriteFile(fresh, []byte("SCHEDULER_MODE=daemon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snap.restore(nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Fatalf("a backup.env the aborted install created must be removed, stat err = %v", err)
	}
}
