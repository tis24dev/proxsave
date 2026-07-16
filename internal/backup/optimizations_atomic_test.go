package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
