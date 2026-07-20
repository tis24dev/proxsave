package sourceguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-commit hook must block a commit that stages a file containing a
// deceptive rune. This runs the real hook against a temp file staged in the
// repo index, then restores the index and worktree in Cleanup.
func TestPreCommitHookBlocksDeceptiveStagedFile(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	hook := filepath.Join(root, ".githooks", "pre-commit")
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("hook missing: %v", err)
	}

	// A .md temp file carrying a real U+202E (built from the escape). .md avoids
	// any Go package concern; the hook still scans it (a format rune is flagged
	// regardless of extension).
	rel := "zz_sourceguard_hooktest.md"
	abs := filepath.Join(root, rel)
	if err := os.WriteFile(abs, []byte("staged a\u202eb\n"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", root, "reset", "-q", "HEAD", rel).Run()
		_ = os.Remove(abs)
	})
	if err := exec.Command("git", "-C", root, "add", rel).Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}

	cmd := exec.Command("sh", hook)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("hook must block a deceptive staged file, but exited 0. output:\n%s", out)
	}
	if !strings.Contains(string(out), "U+202E") && !strings.Contains(string(out), "blocked") {
		t.Fatalf("hook output did not report the block:\n%s", out)
	}
}
