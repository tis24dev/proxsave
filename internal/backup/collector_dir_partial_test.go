package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// A single unreadable file must not abort the whole directory copy: the other
// files are still collected, FilesFailed reflects the one failure, and the
// directory is recorded as a partial (StatusFailed), not StatusCollected.
func TestSafeCopyDirContinuesPastFileError(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempDir, types.ProxmoxVE, false)
	collector.systemManifest = make(map[string]ManifestEntry)
	collector.recordSystemManifest = true

	// Fail the write of the middle file only; other files copy normally.
	origOpenFile := osOpenFile
	t.Cleanup(func() { osOpenFile = origOpenFile })
	osOpenFile = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
		if strings.HasSuffix(name, "b.txt") {
			return nil, errors.New("injected write failure")
		}
		return os.OpenFile(name, flag, perm)
	}

	dest := filepath.Join(tempDir, "etc", "mydir")
	if err := collector.safeCopyDir(context.Background(), src, dest, "mydir"); err != nil {
		t.Fatalf("safeCopyDir must not abort on a single file error, got: %v", err)
	}

	// The two good files were collected despite the middle failure.
	for _, name := range []string{"a.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Fatalf("expected %s collected: %v", name, err)
		}
	}
	if collector.stats.FilesFailed != 1 {
		t.Fatalf("FilesFailed=%d; want 1", collector.stats.FilesFailed)
	}
	entry, ok := collector.systemManifest["etc/mydir"]
	if !ok {
		t.Fatalf("directory manifest entry missing; keys=%v", manifestKeys(collector.systemManifest))
	}
	if entry.Status != StatusFailed {
		t.Fatalf("dir status=%q; want %q (partial)", entry.Status, StatusFailed)
	}
}

// ctx cancellation must still abort the walk (not silently continue).
func TestSafeCopyDirCtxCancelAborts(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempDir, types.ProxmoxVE, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := collector.safeCopyDir(ctx, src, filepath.Join(tempDir, "dest"), "d"); err == nil {
		t.Fatalf("cancelled ctx must abort safeCopyDir")
	}
}

// The per-file ctx re-check inside the walk callback must turn a cancellation
// that arrives DURING a file copy into a hard abort. Here the LAST file visited
// both fails to open and cancels the context; with the re-check present,
// safeCopyDir returns context.Canceled, and without it the error would be
// swallowed (failedCount++/return nil), the walk would finish normally, and
// safeCopyDir would return nil. Making the cancelling file the last one visited
// keeps this test pinning the per-file re-check specifically: a non-last file
// would let the next iteration's top-of-callback ctx guard mask a deleted
// re-check by aborting anyway.
func TestSafeCopyDirPerFileCtxReCheckAbortsMidWalk(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	collector := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tempDir, types.ProxmoxVE, false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The last file both fails its write and cancels the context mid-walk.
	origOpenFile := osOpenFile
	t.Cleanup(func() { osOpenFile = origOpenFile })
	osOpenFile = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
		if strings.HasSuffix(name, "c.txt") {
			cancel()
			return nil, errors.New("injected write failure")
		}
		return os.OpenFile(name, flag, perm)
	}

	dest := filepath.Join(tempDir, "etc", "mydir")
	err := collector.safeCopyDir(ctx, src, dest, "mydir")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-walk cancellation must hard-abort with context.Canceled, got: %v", err)
	}
	// The earlier files were collected before the cancelling failure.
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, statErr := os.Stat(filepath.Join(dest, name)); statErr != nil {
			t.Fatalf("expected %s collected before cancel: %v", name, statErr)
		}
	}
}

func manifestKeys(m map[string]ManifestEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
