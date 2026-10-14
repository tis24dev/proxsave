package safefs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeProc builds a process table under a temp dir where process pid holds each of
// paths open as a descriptor link, the way /proc/<pid>/fd shows it, and points
// HeldOpenIn at it for the rest of the test.
func fakeProc(t *testing.T, pid int, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	fdDir := filepath.Join(root, strconv.Itoa(pid), "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		if err := os.Symlink(path, filepath.Join(fdDir, strconv.Itoa(i+3))); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(SetProcRootForTest(root))
	return root
}

// A file some process holds open is reported; one nobody holds, and one in another
// folder, are not. Links that are not paths (sockets, pipes) are ignored, and a
// deleted file is still reported by its name.
func TestHeldOpenInReportsTheFilesHeldInTheFolder(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	root := fakeProc(t, 4242,
		filepath.Join(dir, "open.log"),
		filepath.Join(dir, "gone.log")+" (deleted)",
		filepath.Join(other, "elsewhere.log"),
		"socket:[12345]",
	)
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	held, err := HeldOpenIn(context.Background(), dir, 5*time.Second)
	if err != nil {
		t.Fatalf("HeldOpenIn: %v", err)
	}
	if !held["open.log"] || !held["gone.log"] || held["elsewhere.log"] || len(held) != 2 {
		t.Fatalf("held = %v, want open.log and gone.log", held)
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
	fakeProc(t, 4242, filepath.Join(real, "open.log"))

	held, err := HeldOpenIn(context.Background(), link, 5*time.Second)
	if err != nil {
		t.Fatalf("HeldOpenIn: %v", err)
	}
	if !held["open.log"] {
		t.Fatalf("held = %v, want open.log through the link", held)
	}
}

// A process that ended between the listing and the read left nothing to hold: it is
// skipped and the scan answers.
func TestHeldOpenInSkipsAProcessThatEnded(t *testing.T) {
	dir := t.TempDir()
	root := fakeProc(t, 4242, filepath.Join(dir, "open.log"))
	if err := os.MkdirAll(filepath.Join(root, "4243"), 0o755); err != nil { // no fd dir: it exited
		t.Fatal(err)
	}
	held, err := HeldOpenIn(context.Background(), dir, 5*time.Second)
	if err != nil || !held["open.log"] {
		t.Fatalf("held=%v err=%v, want open.log and no error", held, err)
	}
}

// A process whose descriptors cannot be read may hold a log open: the scan fails
// instead of answering without it.
func TestHeldOpenInFailsOnAProcessItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	root := fakeProc(t, 4242)
	fdDir := filepath.Join(root, "4242", "fd")
	if err := os.Chmod(fdDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fdDir, 0o755) })
	if held, err := HeldOpenIn(context.Background(), t.TempDir(), 5*time.Second); err == nil {
		t.Fatalf("held=%v and no error, though a process could not be read", held)
	}
}

func TestHeldOpenInFailsWithoutProc(t *testing.T) {
	t.Cleanup(SetProcRootForTest(filepath.Join(t.TempDir(), "missing")))
	if _, err := HeldOpenIn(context.Background(), t.TempDir(), 5*time.Second); err == nil {
		t.Fatal("no /proc, no error: a log could be deleted unchecked")
	}
}

// On the real process table, run as root the way ProxSave runs, a file this process
// holds open is reported.
func TestHeldOpenInOnTheRealProcessTable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("reading every process's descriptors needs root")
	}
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "open.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	held, err := HeldOpenIn(context.Background(), dir, 5*time.Second)
	if err != nil || !held["open.log"] {
		t.Fatalf("held=%v err=%v, want open.log", held, err)
	}
}

// A folder that cannot be resolved could not be matched against the links: the scan
// fails rather than answer that nothing is held. A folder that does not exist holds
// nothing, and says so.
func TestHeldOpenInResolvingTheFolder(t *testing.T) {
	fakeProc(t, 4242)
	base := t.TempDir()
	loopA, loopB := filepath.Join(base, "a"), filepath.Join(base, "b")
	if err := os.Symlink(loopB, loopA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loopA, loopB); err != nil {
		t.Fatal(err)
	}
	if held, err := HeldOpenIn(context.Background(), loopA, 5*time.Second); err == nil {
		t.Fatalf("held=%v and no error for a folder that cannot be resolved", held)
	}
	held, err := HeldOpenIn(context.Background(), filepath.Join(base, "missing"), 5*time.Second)
	if err != nil || len(held) != 0 {
		t.Fatalf("held=%v err=%v for a missing folder, want nothing and no error", held, err)
	}
}
