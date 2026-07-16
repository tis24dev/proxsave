package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A rewrite that fails at the rename step must leave the staged original intact
// (atomicity): the in-place os.Root.WriteFile version truncates before it can fail.
func TestAtomicRootRewrite_RenameFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	const original = "line1\r\nline2\r\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	orig := prefilterRootRename
	t.Cleanup(func() { prefilterRootRename = orig })
	prefilterRootRename = func(r *os.Root, oldname, newname string) error {
		return errors.New("rename boom")
	}

	if _, err := normalizeTextFile(root, "a.txt"); err == nil {
		t.Fatal("want error from failed rewrite, got nil")
	}
	got, rerr := os.ReadFile(filepath.Join(dir, "a.txt"))
	if rerr != nil {
		t.Fatalf("read back: %v", rerr)
	}
	if string(got) != original {
		t.Fatalf("original modified: got %q want %q", string(got), original)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

// The atomic rewrite must preserve the file's existing mode. The old in-place
// root.WriteFile left an existing file's permissions untouched; a fresh temp+rename
// would otherwise impose its own creation mode and widen/narrow the optimized file.
func TestAtomicRootRewrite_PreservesOriginalMode(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "priv.conf")
	const original = "a\r\nb\r\n"
	if err := os.WriteFile(fp, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(fp, 0o600); err != nil { // WriteFile perm is umask-subject
		t.Fatalf("chmod: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	changed, err := normalizeTextFile(root, "priv.conf")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if !changed {
		t.Fatal("expected the file to be normalized")
	}
	info, _ := os.Stat(fp)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: got %o want 600", info.Mode().Perm())
	}
	if got, _ := os.ReadFile(fp); string(got) != "a\nb\n" {
		t.Fatalf("content = %q", string(got))
	}
}

// The atomic rewrite must preserve the source file's uid/gid. The old in-place
// root.WriteFile kept the same inode (owner intact); a temp+rename creates a fresh
// inode owned by the backup process, so the fix must chown it back to the source.
func TestAtomicRootRewrite_PreservesUidGid(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "owned.log")
	if err := os.WriteFile(fp, []byte("p\r\nq\r\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fi, err := os.Lstat(fp)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	wantUID, wantGID := int(st.Uid), int(st.Gid)

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()

	gotUID, gotGID := -1, -1
	orig := prefilterFileChown
	t.Cleanup(func() { prefilterFileChown = orig })
	prefilterFileChown = func(f *os.File, uid, gid int) error {
		gotUID, gotGID = uid, gid
		return nil
	}

	if _, err := normalizeTextFile(root, "owned.log"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if gotUID != wantUID || gotGID != wantGID {
		t.Fatalf("chown args = (%d,%d); want source (%d,%d)", gotUID, gotGID, wantUID, wantGID)
	}
}

// A per-file rewrite error must be surfaced as a Warning and the walk must continue
// best-effort (returns nil), leaving the original untouched. The pre-fix code
// discarded the error (err == nil && c), logging nothing.
func TestPrefilterFiles_RewriteErrorWarnsAndContinues(t *testing.T) {
	dir := t.TempDir()
	const original = "x\r\ny\r\n"
	fp := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(fp, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	orig := prefilterRootRename
	t.Cleanup(func() { prefilterRootRename = orig })
	prefilterRootRename = func(r *os.Root, oldname, newname string) error {
		return errors.New("rename boom")
	}

	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)

	if _, err := prefilterFiles(context.Background(), logger, dir, 0); err != nil {
		t.Fatalf("prefilterFiles should continue best-effort, got err %v", err)
	}
	if !strings.Contains(buf.String(), "failed to optimize") {
		t.Fatalf("expected warning about failed optimize, log:\n%s", buf.String())
	}
	got, _ := os.ReadFile(fp)
	if string(got) != original {
		t.Fatalf("original modified: %q", string(got))
	}
}
