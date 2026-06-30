package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadNotifySecretContainmentRejectsSymlinkEscape verifies the os.Root-based
// read in LoadNotifySecret confines access to the identity directory: a
// .notify_secret that is a symlink escaping the directory is NOT followed, so the
// outside file's content is never returned. This documents the structural gosec
// G304 fix (no #nosec) and mirrors checks' lock-read containment audited test.
func TestLoadNotifySecretContainmentRejectsSymlinkEscape(t *testing.T) {
	baseDir := t.TempDir()

	// A valid-format secret sitting OUTSIDE the identity directory.
	outside := filepath.Join(baseDir, "outside_secret")
	if err := os.WriteFile(outside, []byte("aaaa-bbbb\n"), 0o600); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}

	identityDir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(identityDir, 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}

	// Point the secret file at a symlink that escapes the identity directory.
	link := filepath.Join(identityDir, notifySecretFileName)
	if err := os.Symlink(filepath.Join("..", "outside_secret"), link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	// Control: a raw os.ReadFile WOULD follow the symlink and read the outside
	// file; if it does not here, the host blocks symlinks and the test is moot.
	if raw, rerr := os.ReadFile(link); rerr != nil || string(raw) != "aaaa-bbbb\n" {
		t.Skipf("host does not follow the symlink via os.ReadFile (err=%v); containment test moot", rerr)
	}

	got, _ := LoadNotifySecret(baseDir)
	if got != "" {
		t.Fatalf("LoadNotifySecret followed an escaping symlink and leaked %q; want empty (containment broken)", got)
	}
}
