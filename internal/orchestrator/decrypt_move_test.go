package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

type fakeFSRenameFail struct{ *FakeFS }

func (f fakeFSRenameFail) Rename(oldpath, newpath string) error {
	return os.ErrPermission
}

func TestMoveFileSafe_RenameSuccess(t *testing.T) {
	orig := restoreFS
	defer func() { restoreFS = orig }()
	restoreFS = osFS{}

	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	content := []byte("hello")
	if err := os.WriteFile(src, content, 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := moveFileSafe(src, dst); err != nil {
		t.Fatalf("moveFileSafe: %v", err)
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected src removed, got %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("dst content = %q, want %q", string(data), string(content))
	}
}

func TestMoveFileSafe_RenameFailsFallsBackToCopy(t *testing.T) {
	orig := restoreFS
	defer func() { restoreFS = orig }()
	fake := &fakeFSRenameFail{NewFakeFS()}
	restoreFS = fake

	src := "/src/file.txt"
	dst := "/dst/file.txt"
	content := []byte("data")
	if err := fake.MkdirAll("/dst", 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := fake.WriteFile(src, content, 0o640); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := moveFileSafe(src, dst); err != nil {
		t.Fatalf("moveFileSafe: %v", err)
	}

	if _, err := fake.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("expected src removed, got %v", err)
	}
	data, err := fake.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("dst content = %q, want %q", string(data), string(content))
	}
}
