package safefs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxSymlinkHops caps the symlink chain length ReadFileInRoot will follow,
// mirroring the kernel MAXSYMLINKS limit so a symlink loop terminates.
const maxSymlinkHops = 40

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

// ReadFileInRoot reads absPath interpreted relative to root, following symlink
// chains but confining every resolved path inside root. It exists because os.Root
// does NOT re-anchor an absolute symlink target: os.Root.Open on a component whose
// target is an absolute path (for example /etc/ceph/ceph.conf -> /etc/pve/ceph.conf
// on a Proxmox host) is refused with "path escapes from parent", so a bare
// os.OpenRoot(root).Open(rel) cannot read such a file. This helper instead resolves
// the WHOLE-PATH symlink at each hop by hand with Lstat/Readlink and re-anchors an
// absolute target back under root, so /etc/ceph/ceph.conf resolves to
// root/etc/pve/ceph.conf and a hostile target like /etc/shadow re-anchors to
// root/etc/shadow, never the real host file.
//
// Scope and containment, precisely:
//   - It re-anchors a symlink whose whole current path resolves to a symlink (the
//     Proxmox /etc/ceph/ceph.conf case). An INTERMEDIATE directory component that is
//     itself an absolute symlink is not re-anchored: os.Root.Lstat refuses it with
//     "path escapes from parent", so such a layout fails closed (safe, but not read).
//   - A relative "../" target is lexically collapsed and re-anchored under root by
//     filepath.Clean (leading ".." above root are dropped), so it stays confined;
//     os.Root is the syscall-level backstop, not the primary guard for that case.
//
// Use it to read a system file under a SYSTEM_ROOT_PREFIX when the file may be an
// absolute symlink into another prefixed subtree. It is read-only and never
// touches anything outside root. This is a structural G304 remedy (os.Root), not a
// suppression, and is distinct from OpenFileUnderRoot which roots at the parent
// directory and guards only the final component.
func ReadFileInRoot(root, absPath string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()

	rel := strings.TrimPrefix(filepath.Clean("/"+absPath), "/")
	if rel == "" || rel == "." {
		return nil, fmt.Errorf("read in root %q: empty relative path", root)
	}

	for hop := 0; hop < maxSymlinkHops; hop++ {
		info, lerr := r.Lstat(rel)
		if lerr != nil {
			return nil, lerr
		}
		if info.Mode()&os.ModeSymlink == 0 {
			f, oerr := r.Open(rel)
			if oerr != nil {
				return nil, oerr
			}
			defer func() { _ = f.Close() }()
			return io.ReadAll(f)
		}
		target, rlerr := r.Readlink(rel)
		if rlerr != nil {
			return nil, rlerr
		}
		if filepath.IsAbs(target) {
			rel = strings.TrimPrefix(filepath.Clean(target), "/")
		} else {
			rel = strings.TrimPrefix(filepath.Clean("/"+filepath.Join(filepath.Dir(rel), target)), "/")
		}
		if rel == "" || rel == "." {
			return nil, fmt.Errorf("read in root %q: symlink resolves to root", root)
		}
	}
	return nil, fmt.Errorf("read in root %q: too many symlink hops resolving %q", root, absPath)
}
