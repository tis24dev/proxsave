package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Without ownership the local cloud copies take their source's mode; a copy whose
// source mode cannot be read gets 0600, never rclone's 0644, and a symlink in the
// destination (a share other hosts write to) is not followed by the root chmod.
func TestCloudLocalModesFailClosedAndRefuseSymlinks(t *testing.T) {
	cs, _, archive, root := newLocalFormStore(t, "", false)
	dest := filepath.Join(root, filepath.Base(archive))
	if err := os.WriteFile(dest, []byte("copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dest+".sha256"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archive); err != nil { // source mode unreadable
		t.Fatal(err)
	}

	cs.setLocalModesFromSource(context.Background(), archive, dest)

	if st, err := os.Stat(dest); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("archive copy mode = %v (%v), want 0600 when the source mode is unknown", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(outside); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target mode = %v (%v), want 0644 untouched", st.Mode().Perm(), err)
	}
}
