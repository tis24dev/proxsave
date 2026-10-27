package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
)

// The wizard writes backup.env through a 0600 temp file renamed over it, so an install
// aborted after a write that changed nothing still leaves the file at 0600. The
// restore puts the snapshot's mode back in that case too.
func TestInstallConfigSnapshotRestoresTheModeOfAnUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.env")
	const content = "ENCRYPT_ARCHIVE=false\n"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	snap := takeInstallConfigSnapshot(path)
	if err := installer.WriteConfigFileAtomic(path, path+".tmp", content); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("the wizard's write left mode %v, the test needs 0600", st.Mode().Perm())
	}
	snap.restore(nil)
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("backup.env mode = %v (err %v), want the snapshot's 0640", st.Mode().Perm(), err)
	}
}

// The restore replaces backup.env through a temp file and a rename, like the write it
// undoes: written in place, a failure partway left a truncated file and the original
// only in memory. A restore that cannot write leaves the install's file whole.
func TestInstallConfigSnapshotRestoreReplacesTheFileAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(path, []byte("ENCRYPT_ARCHIVE=false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := takeInstallConfigSnapshot(path)
	if err := os.WriteFile(path, []byte("ENCRYPT_ARCHIVE=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inode := func() uint64 {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return st.Sys().(*syscall.Stat_t).Ino
	}
	before := inode()
	snap.restore(nil)
	if got, _ := os.ReadFile(path); string(got) != "ENCRYPT_ARCHIVE=false\n" {
		t.Fatalf("backup.env = %q, want the snapshot", got)
	}
	if inode() == before {
		t.Fatal("backup.env was rewritten in place, want a new file renamed over it")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d entries, want backup.env alone (no temp file left)", len(entries))
	}

	// The temp file cannot be created: the install's file stays whole.
	if err := os.WriteFile(path, []byte("ENCRYPT_ARCHIVE=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".tmp.restore", 0o700); err != nil {
		t.Fatal(err)
	}
	snap.restore(nil)
	if got, _ := os.ReadFile(path); string(got) != "ENCRYPT_ARCHIVE=true\n" {
		t.Fatalf("after a failed restore backup.env = %q, want the install's file whole", got)
	}
}
