package safefs

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

// WalkBounded is the FS_IO_TIMEOUT-bounded analogue of filepath.WalkDir. It walks
// the tree rooted at root depth-first, invoking fn for the root and every
// descendant, with three deliberate properties for safe operation over
// dead/stale mounts:
//
//   - Every blocking syscall is bounded: the root probe goes through Lstat and
//     each directory read through ReadDir, both with the supplied timeout, so no
//     single getdents/lstat can wedge the walk in an uninterruptible (D-state)
//     kernel call. ReadDir also absorbs the per-entry lstat that os.ReadDir
//     performs for filesystems reporting DT_UNKNOWN, so DirEntry.IsDir() in the
//     loop never triggers a further unbounded syscall. A bounded op that does not
//     return in time yields *TimeoutError (wrapping ErrTimeout); the worker
//     goroutine is abandoned (see runLimited). When timeout <= 0 every op degrades
//     to a direct syscall (legacy unbounded behaviour, no goroutine/limiter cost).
//
//   - Symlinks are never followed: descent is decided from DirEntry.IsDir(), which
//     is false for a symlink, so a symlink is reported to fn as a leaf and never
//     traversed. This also makes the walk loop-free.
//
//   - Read/timeout errors are reported, not fatal by default: when a directory
//     cannot be read, fn is called a second time as fn(dir, d, err), exactly like
//     filepath.WalkDir. Returning nil (or fs.SkipDir) skips that subtree and the
//     walk continues; any other error aborts. fs.SkipDir and fs.SkipAll are
//     honoured.
//
// WalkBounded itself never mutates the filesystem. A mutating callback must bound
// its own syscalls (Lchown/Chmod with the same timeout) so a per-entry write
// cannot wedge after a mount goes stale mid-walk.
func WalkBounded(ctx context.Context, root string, timeout time.Duration, fn fs.WalkDirFunc) error {
	info, err := Lstat(ctx, root, timeout)
	if err != nil {
		// Mirror filepath.WalkDir: report the root error to fn and let it decide.
		err = fn(root, nil, err)
		if err == fs.SkipDir || err == fs.SkipAll {
			return nil
		}
		return err
	}
	err = walkBounded(ctx, root, fs.FileInfoToDirEntry(info), timeout, fn)
	if err == fs.SkipDir || err == fs.SkipAll {
		return nil
	}
	return err
}

func walkBounded(ctx context.Context, path string, d fs.DirEntry, timeout time.Duration, fn fs.WalkDirFunc) error {
	if err := fn(path, d, nil); err != nil {
		if err == fs.SkipDir && d.IsDir() {
			return nil // skip this directory's contents, keep walking siblings
		}
		return err // SkipDir on a file, SkipAll, or a real error -> bubble up
	}
	if !d.IsDir() {
		return nil
	}

	entries, err := ReadDir(ctx, path, timeout)
	if err != nil {
		// Second-call convention: surface the read/timeout error against this dir.
		if cbErr := fn(path, d, err); cbErr != nil && cbErr != fs.SkipDir {
			return cbErr
		}
		return nil // fn chose to skip the unreadable/timed-out subtree
	}

	for _, entry := range entries {
		if ctx.Err() != nil {
			return normalizeContextErr(ctx, &TimeoutError{Op: "walk", Path: path, Timeout: effectiveTimeout(ctx, timeout)})
		}
		child := filepath.Join(path, entry.Name())
		if err := walkBounded(ctx, child, entry, timeout, fn); err != nil {
			if err == fs.SkipDir {
				break // SkipDir bubbling up from a file: skip the rest of THIS dir
			}
			return err // SkipAll or a real error aborts the whole walk
		}
	}
	return nil
}
