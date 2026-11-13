package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
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

// A parent directory of the copy replaced by a symlink to a directory outside the CLOUD_REMOTE
// directory: the root chmod does not reach the file there (Lchmod only refused a symlink at the
// final component).
func TestCloudLocalModesStayInsideTheCloudDirectory(t *testing.T) {
	srcDir := t.TempDir()
	archive := filepath.Join(srcDir, "node-backup-20261003-100000.tar.zst")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "cloud")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, filepath.Base(archive))
	if err := os.WriteFile(victim, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub")); err != nil {
		t.Fatal(err)
	}
	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: root, CloudRemotePath: "sub"}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}

	cs.setLocalModesFromSource(context.Background(), archive, filepath.Join(root, "sub", filepath.Base(archive)))

	if st, err := os.Stat(victim); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("file outside the cloud directory: mode %v (%v), want 0644 untouched", st.Mode().Perm(), err)
	}
}
