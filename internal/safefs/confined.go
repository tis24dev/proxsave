package safefs

import (
	"io"
	"os"
	"path/filepath"
)

// OpenFileUnderRoot opens path through an *os.Root on its parent directory,
// confining the open at the syscall level: the path is no longer a raw variable
// sink and a symlink or ".." in the final component cannot escape the directory
// (resolving gosec G304 structurally, not with a suppression). This mirrors
// identity.readFileUnderRoot and checks.readLockFileContent. The returned *os.File
// is backed by an independent descriptor, so the root is closed before returning
// while the file stays open. A missing directory or file surfaces as
// os.ErrNotExist, matching os.OpenFile.
func OpenFileUnderRoot(path string, flag int, perm os.FileMode) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.OpenFile(filepath.Base(path), flag, perm)
}

// ReadFileUnderRoot reads the whole file at path through an *os.Root on its parent
// directory (see OpenFileUnderRoot), matching os.ReadFile semantics.
func ReadFileUnderRoot(path string) ([]byte, error) {
	f, err := OpenFileUnderRoot(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
