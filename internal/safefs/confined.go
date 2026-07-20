package safefs

import (
	"io"
	"os"
	"path/filepath"
)

// OpenFileUnderRoot opens path through an *os.Root on its parent directory,
// confining the open at the syscall level: the path is no longer a raw variable
// sink, and a final component that is an absolute symlink or one escaping the
// parent directory is refused (resolving gosec G304 structurally, not with a
// suppression). This mirrors identity.readFileUnderRoot and
// checks.readLockFileContent. The returned *os.File is backed by an independent
// descriptor, so the root is closed before returning while the file stays open.
//
// It is NOT a drop-in os.OpenFile: two behaviors differ by design. (1) It needs
// read access to the parent directory, since os.OpenRoot opens it, not just
// execute/search. (2) It refuses an absolute or escaping-symlink final component
// that os.OpenFile would have followed; a relative symlink staying within the
// directory is still followed. A missing directory or file still surfaces as
// os.ErrNotExist. Use it only for a path whose final component is a regular file
// in a directory the caller may read.
func OpenFileUnderRoot(path string, flag int, perm os.FileMode) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.OpenFile(filepath.Base(path), flag, perm)
}

// ReadFileUnderRoot reads the whole file at path through an *os.Root on its parent
// directory (see OpenFileUnderRoot for the confinement and its two intentional
// differences from os.ReadFile).
func ReadFileUnderRoot(path string) ([]byte, error) {
	f, err := OpenFileUnderRoot(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}
