package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// Without ownership the local cloud copies take their source's mode; a copy whose
// source mode cannot be read gets 0600, never rclone's 0644, and a symlink in the
// destination (a share other hosts write to) is not followed by the root chmod. The mode
// not set on it is reported as the ownership branch reports its own; a clean set records
// nothing.
func TestCloudLocalModesFailClosedAndRefuseSymlinks(t *testing.T) {
	clean, cleanLog, cleanArchive, cleanRoot := newLocalFormStore(t, "", false)
	for _, suffix := range []string{"", ".sha256", ".metadata"} {
		if err := os.WriteFile(filepath.Join(cleanRoot, filepath.Base(cleanArchive)+suffix), []byte("copy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cleanIssues := &storeIssueRecorder{}
	clean.setLocalModesFromSource(context.Background(), cleanArchive, filepath.Join(cleanRoot, filepath.Base(cleanArchive)), cleanIssues)
	if got := cleanIssues.list(); len(got) != 0 || strings.Contains(cleanLog.String(), "Permissions failed") {
		t.Fatalf("clean set: issues %v, log:\n%s", got, cleanLog.String())
	}

	cs, buf, archive, root := newLocalFormStore(t, "", false)
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

	issues := &storeIssueRecorder{}
	cs.setLocalModesFromSource(context.Background(), archive, dest, issues)

	if st, err := os.Stat(dest); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("archive copy mode = %v (%v), want 0600 when the source mode is unknown", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(outside); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("symlink target mode = %v (%v), want 0644 untouched", st.Mode().Perm(), err)
	}
	if got := issues.list(); len(got) != 1 || got[0] != StoreIssuePermissionsNotSet {
		t.Fatalf("issues = %v, want permissions not set", got)
	}
	if want := "  Permissions failed: " + filepath.Base(dest) + ".sha256: "; !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
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

	issues := &storeIssueRecorder{}
	cs.setLocalModesFromSource(context.Background(), archive, filepath.Join(root, "sub", filepath.Base(archive)), issues)

	if st, err := os.Stat(victim); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("file outside the cloud directory: mode %v (%v), want 0644 untouched", st.Mode().Perm(), err)
	}
	if got := issues.list(); len(got) != 1 || got[0] != StoreIssuePermissionsNotSet {
		t.Fatalf("issues = %v, want permissions not set", got)
	}
}

// A FAT-family CLOUD_REMOTE directory has no Unix permissions: the copies are not
// chmodded and nothing is reported, where a chmod would fail. Another filesystem without
// ownership keeps the chmod and reports the copy whose mode could not be set.
func TestCloudLocalModesSkippedOnFAT(t *testing.T) {
	for _, tc := range []struct {
		fsType   FilesystemType
		skipped  bool
		wantMode os.FileMode
	}{
		{FilesystemFAT32, true, 0o644},
		{FilesystemFAT, true, 0o644},
		{FilesystemMSDOS, true, 0o644},
		{FilesystemExFAT, true, 0o644},
		{FilesystemCIFS, false, 0o600},
		{FilesystemNTFS, false, 0o600},
	} {
		t.Run(string(tc.fsType), func(t *testing.T) {
			cs, buf, archive, root := newLocalFormStore(t, "", false)
			cs.localFS.Type = tc.fsType
			dest := filepath.Join(root, filepath.Base(archive))
			if err := os.WriteFile(dest, []byte("copy"), 0o644); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			// A symlinked sidecar makes the chmod fail where it runs.
			if err := os.Symlink(outside, dest+".sha256"); err != nil {
				t.Fatal(err)
			}

			issues := &storeIssueRecorder{}
			cs.setLocalPermissions(context.Background(), archive, dest, issues)

			if st, err := os.Stat(dest); err != nil || st.Mode().Perm() != tc.wantMode {
				t.Fatalf("archive copy mode = %v (%v), want %o", st.Mode().Perm(), err, tc.wantMode)
			}
			got := issues.list()
			failed := strings.Contains(buf.String(), "Permissions failed")
			if tc.skipped {
				if len(got) != 0 || failed {
					t.Fatalf("FAT: issues %v, log:\n%s", got, buf.String())
				}
				return
			}
			if len(got) != 1 || got[0] != StoreIssuePermissionsNotSet || !failed {
				t.Fatalf("issues = %v, want permissions not set; log:\n%s", got, buf.String())
			}
		})
	}
}
