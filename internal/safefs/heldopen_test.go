package safefs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A file this process holds open is reported; one it closed, and one it never opened,
// are not.
func TestHeldOpenInSeesThisProcessDescriptors(t *testing.T) {
	dir := t.TempDir()
	open, err := os.Create(filepath.Join(dir, "open.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	closed, err := os.Create(filepath.Join(dir, "closed.log"))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := os.WriteFile(filepath.Join(dir, "never.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	held, err := HeldOpenIn(context.Background(), dir, 5*time.Second)
	if err != nil {
		t.Fatalf("HeldOpenIn: %v", err)
	}
	if !held["open.log"] || held["closed.log"] || held["never.log"] {
		t.Fatalf("held = %v, want only open.log", held)
	}
}

// The folder is matched through its symbolic link too, since the kernel reports the
// resolved path.
func TestHeldOpenInFollowsALinkedFolder(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(real, "open.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	held, err := HeldOpenIn(context.Background(), link, 5*time.Second)
	if err != nil {
		t.Fatalf("HeldOpenIn: %v", err)
	}
	if !held["open.log"] {
		t.Fatalf("held = %v, want open.log through the link", held)
	}
}

func TestHeldOpenInFailsWithoutProc(t *testing.T) {
	saved := procRoot
	procRoot = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { procRoot = saved })
	if _, err := HeldOpenIn(context.Background(), t.TempDir(), 5*time.Second); err == nil {
		t.Fatal("no /proc, no error: a log could be deleted unchecked")
	}
}
