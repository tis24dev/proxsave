package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression for the os.Root containment of the lock-file reads (resolves gosec
// G304 structurally, no #nosec): readLockFileContent reads the lock file through an
// *os.Root on its directory. Besides removing the variable-path sink, this genuinely
// hardens behavior - a lock path that is a symlink escaping the lock directory is
// refused instead of followed (which the previous os.ReadFile would have done).

func TestReadLockFileContent_ReadsRegularFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".backup.lock")
	want := []byte("pid=123\nhost=node1\ntime=2026-06-09T00:00:00Z\n")
	if err := os.WriteFile(p, want, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readLockFileContent(p)
	if err != nil {
		t.Fatalf("read regular lock file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestReadLockFileContent_MissingFileErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := readLockFileContent(p); err == nil {
		t.Fatalf("expected an error reading a missing lock file")
	}
}

// The security-relevant case: a lock path that is a symlink to a file OUTSIDE the
// lock directory must NOT be followed (os.Root confines the read to the directory).
func TestReadLockFileContent_RefusesSymlinkEscapingLockDir(t *testing.T) {
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret")
	if err := os.WriteFile(secret, []byte("TOP SECRET CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	lockDir := t.TempDir()
	lockPath := filepath.Join(lockDir, ".backup.lock")
	if err := os.Symlink(secret, lockPath); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	content, err := readLockFileContent(lockPath)
	if err == nil {
		t.Fatalf("os.Root must refuse a lock symlink escaping its directory; got content %q", content)
	}
	if strings.Contains(string(content), "TOP SECRET") {
		t.Fatalf("the outside file's content leaked through a symlinked lock path")
	}
}
