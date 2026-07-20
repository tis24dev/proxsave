package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeHashFile must not follow a symlink at the hash path: a link planted there
// must be refused, leaving the link target untouched.
func TestWriteHashFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	c := &Checker{fsTimeout: 0}

	// Happy path: a plain hash file is written.
	plain := filepath.Join(dir, "plain.md5")
	if err := c.writeHashFile(context.Background(), plain, "deadbeef"); err != nil {
		t.Fatalf("writeHashFile plain: %v", err)
	}
	if b, err := os.ReadFile(plain); err != nil || string(b) != "deadbeef" {
		t.Fatalf("plain content = %q (err %v); want deadbeef", string(b), err)
	}

	// Symlink at the hash path pointing at a sentinel: the write must be refused
	// and the sentinel must stay intact (the link was not followed).
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("seed sentinel: %v", err)
	}
	link := filepath.Join(dir, "link.md5")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatalf("seed symlink: %v", err)
	}
	if err := c.writeHashFile(context.Background(), link, "beefdead"); err == nil {
		t.Fatal("writeHashFile through a symlink must error, not follow it")
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "SECRET" {
		t.Fatalf("sentinel content = %q (err %v); writeHashFile followed the symlink", string(b), err)
	}
}
