package safefs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f1"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "f2"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestWalkBounded_VisitsRootThenDFS(t *testing.T) {
	root := mkTree(t)
	var visited []string
	err := WalkBounded(context.Background(), root, time.Second, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatalf("unexpected callback err for %s: %v", path, err)
		}
		visited = append(visited, path)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkBounded: %v", err)
	}
	want := []string{
		root,
		filepath.Join(root, "a"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a", "f2"),
		filepath.Join(root, "f1"),
	}
	if len(visited) != len(want) {
		t.Fatalf("visited %v; want %v", visited, want)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("visited[%d]=%s; want %s (full: %v)", i, visited[i], want[i], visited)
		}
	}
}

func TestWalkBounded_TimeoutZeroRunsInline(t *testing.T) {
	root := mkTree(t)
	count := 0
	err := WalkBounded(context.Background(), root, 0, func(string, fs.DirEntry, error) error {
		count++
		return nil
	})
	if err != nil || count != 5 {
		t.Fatalf("unbounded walk: err=%v count=%d, want 5", err, count)
	}
}

func TestWalkBounded_DoesNotFollowSymlinks(t *testing.T) {
	ext := t.TempDir()
	if err := os.WriteFile(filepath.Join(ext, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(ext, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	var visited []string
	var linkIsDir bool
	err := WalkBounded(context.Background(), root, time.Second, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatalf("unexpected err for %s: %v", path, err)
		}
		visited = append(visited, path)
		if path == link {
			linkIsDir = d.IsDir()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkBounded: %v", err)
	}
	if linkIsDir {
		t.Fatal("symlink must be reported as a leaf (IsDir=false)")
	}
	for _, p := range visited {
		if p == filepath.Join(link, "secret") {
			t.Fatalf("must not descend into a symlinked directory; visited %s", p)
		}
	}
}

func TestWalkBounded_RootLstatTimeoutReportedToCallback(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	calls := 0
	var gotErr error
	var gotEntry fs.DirEntry
	err := WalkBounded(ctx, root, 30*time.Second, func(path string, d fs.DirEntry, e error) error {
		calls++
		gotErr = e
		gotEntry = d
		return nil // skip
	})
	if err != nil {
		t.Fatalf("WalkBounded should swallow a root error the callback ignored, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d; want 1", calls)
	}
	if !errors.Is(gotErr, ErrTimeout) {
		t.Fatalf("callback err = %v; want ErrTimeout", gotErr)
	}
	if gotEntry != nil {
		t.Fatalf("root-error callback must pass a nil DirEntry, got %v", gotEntry)
	}
}

func TestWalkBounded_ReadDirTimeoutSkipsSubtree(t *testing.T) {
	prev := osReadDir
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "readdir completion", unblock, finished, func() { osReadDir = prev })
	osReadDir = func(string) ([]os.DirEntry, error) {
		<-unblock
		close(finished)
		return nil, nil
	}

	root := t.TempDir() // real dir: root Lstat (unstubbed) succeeds, ReadDir blocks
	var sawTimeout bool
	err := WalkBounded(context.Background(), root, 25*time.Millisecond, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			if errors.Is(e, ErrTimeout) {
				sawTimeout = true
			}
			return nil // skip the timed-out subtree
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkBounded should return nil when callback skips the timeout, got %v", err)
	}
	if !sawTimeout {
		t.Fatal("callback never received the ReadDir timeout error")
	}
}

func TestWalkBounded_ReadDirTimeoutAbortsWhenCallbackReturnsErr(t *testing.T) {
	prev := osReadDir
	unblock := make(chan struct{})
	finished := make(chan struct{})
	registerBlockedOpCleanup(t, "readdir completion", unblock, finished, func() { osReadDir = prev })
	osReadDir = func(string) ([]os.DirEntry, error) {
		<-unblock
		close(finished)
		return nil, nil
	}

	root := t.TempDir()
	err := WalkBounded(context.Background(), root, 25*time.Millisecond, func(path string, d fs.DirEntry, e error) error {
		return e // propagate -> abort
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("WalkBounded err = %v; want ErrTimeout", err)
	}
}

func TestWalkBounded_SkipDirSkipsContents(t *testing.T) {
	root := mkTree(t)
	skip := filepath.Join(root, "a")
	var visited []string
	err := WalkBounded(context.Background(), root, time.Second, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited = append(visited, path)
		if path == skip {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkBounded: %v", err)
	}
	for _, p := range visited {
		if p == filepath.Join(root, "a", "b") || p == filepath.Join(root, "a", "f2") {
			t.Fatalf("SkipDir must skip contents of %s; visited %s", skip, p)
		}
	}
	// Sibling f1 must still be visited.
	found := false
	for _, p := range visited {
		if p == filepath.Join(root, "f1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("SkipDir must not skip siblings; f1 missing from %v", visited)
	}
}

func TestWalkBounded_ContextCancelAborts(t *testing.T) {
	root := mkTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := WalkBounded(ctx, root, time.Second, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		cancel() // cancel during the first callback; the next loop iteration must abort
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WalkBounded err = %v; want context.Canceled", err)
	}
}
