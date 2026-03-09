package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathWithinRootFS_AllowsMissingTailAfterSafeSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "safe-target"), 0o755); err != nil {
		t.Fatalf("mkdir safe-target: %v", err)
	}
	if err := os.Symlink("safe-target", filepath.Join(root, "safe-link")); err != nil {
		t.Fatalf("create safe symlink: %v", err)
	}

	resolved, err := resolvePathWithinRootFS(osFS{}, root, filepath.Join("safe-link", "missing", "file.txt"))
	if err != nil {
		t.Fatalf("resolvePathWithinRootFS returned error: %v", err)
	}

	want := filepath.Join(root, "safe-target", "missing", "file.txt")
	if resolved != want {
		t.Fatalf("resolved path = %q, want %q", resolved, want)
	}
}

func TestResolvePathWithinRootFS_RejectsBrokenIntermediateSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape-link")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}

	_, err := resolvePathWithinRootFS(osFS{}, root, filepath.Join("escape-link", "missing", "file.txt"))
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("expected escape rejection, got %v", err)
	}
}

func TestResolvePathWithinRootFS_RejectsSymlinkLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatalf("create loop symlink: %v", err)
	}

	_, err := resolvePathWithinRootFS(osFS{}, root, filepath.Join("loop", "file.txt"))
	if err == nil || !strings.Contains(err.Error(), "too many symlink resolutions") {
		t.Fatalf("expected symlink loop rejection, got %v", err)
	}
}

func TestResolvePathWithinRootFS_ClassifiesPathComponentNotDirectoryAsOperational(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "deep")
	if err := os.WriteFile(blocker, []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, err := resolvePathWithinRootFS(osFS{}, root, filepath.Join("deep", "nested", "file.txt"))
	if err == nil {
		t.Fatal("expected error for non-directory path component")
	}
	if !isPathOperationalError(err) {
		t.Fatalf("expected operational error classification, got %v", err)
	}
	if isPathSecurityError(err) {
		t.Fatalf("expected non-security error classification, got %v", err)
	}
}

func TestResolvePathWithinRootFS_ClassifiesPermissionDeniedAsOperational(t *testing.T) {
	fsys := NewFakeFS()
	root := filepath.Join(string(os.PathSeparator), "restore-root")
	if err := fsys.AddDir(root); err != nil {
		t.Fatalf("add root dir: %v", err)
	}
	if err := fsys.AddDir(filepath.Join(root, "subdir")); err != nil {
		t.Fatalf("add subdir: %v", err)
	}
	fsys.StatErrors[filepath.Clean(filepath.Join(root, "subdir", "file.txt"))] = os.ErrPermission

	_, err := resolvePathWithinRootFS(fsys, root, filepath.Join("subdir", "file.txt"))
	if err == nil {
		t.Fatal("expected permission error")
	}
	if !isPathOperationalError(err) {
		t.Fatalf("expected operational error classification, got %v", err)
	}
	if isPathSecurityError(err) {
		t.Fatalf("expected non-security error classification, got %v", err)
	}
}

func TestResolvePathWithinRootFS_AllowsAbsoluteSymlinkTargetViaLexicalRoot(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-root")
	linkRoot := filepath.Join(parent, "link-root")
	if err := os.MkdirAll(filepath.Join(realRoot, "etc-real"), 0o755); err != nil {
		t.Fatalf("mkdir real root: %v", err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatalf("create root symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(linkRoot, "etc-real"), filepath.Join(realRoot, "etc")); err != nil {
		t.Fatalf("create nested symlink: %v", err)
	}

	resolved, err := resolvePathWithinRootFS(osFS{}, linkRoot, filepath.Join("etc", "missing", "file.txt"))
	if err != nil {
		t.Fatalf("resolvePathWithinRootFS returned error: %v", err)
	}

	want := filepath.Join(realRoot, "etc-real", "missing", "file.txt")
	if resolved != want {
		t.Fatalf("resolved path = %q, want %q", resolved, want)
	}
}
