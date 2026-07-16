package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	atomicGeteuid   = os.Geteuid
	atomicFileChown = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
	atomicFileChmod = func(f *os.File, perm os.FileMode) error { return f.Chmod(perm) }
	atomicFileSync  = func(f *os.File) error { return f.Sync() }
)

type uidGid struct {
	uid int
	gid int
	ok  bool
}

func uidGidFromFileInfo(info os.FileInfo) uidGid {
	if info == nil {
		return uidGid{}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return uidGid{}
	}
	return uidGid{uid: int(st.Uid), gid: int(st.Gid), ok: true}
}

func modeBits(mode os.FileMode) os.FileMode {
	return mode & 0o7777
}

func findNearestExistingDirMeta(dir string) (uidGid, os.FileMode) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return uidGid{}, 0o755
	}

	candidate := dir
	for {
		info, err := restoreFS.Stat(candidate)
		if err == nil && info != nil && info.IsDir() {
			inheritMode := modeBits(info.Mode())
			if inheritMode == 0 {
				inheritMode = 0o755
			}
			return uidGidFromFileInfo(info), inheritMode
		}

		parent := filepath.Dir(candidate)
		if parent == candidate || parent == "." || parent == "" {
			break
		}
		candidate = parent
	}

	return uidGid{}, 0o755
}

func ensureDirExistsWithInheritedMeta(dir string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." || dir == string(os.PathSeparator) {
		return nil
	}

	if info, err := restoreFS.Stat(dir); err == nil {
		if info != nil && info.IsDir() {
			return nil
		}
		return fmt.Errorf("path exists but is not a directory: %s", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", dir, err)
	}

	existing := ""
	candidate := dir
	for {
		info, err := restoreFS.Stat(candidate)
		if err == nil && info != nil {
			if info.IsDir() {
				existing = candidate
				break
			}
			return fmt.Errorf("path exists but is not a directory: %s", candidate)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", candidate, err)
		}
		parent := filepath.Dir(candidate)
		if parent == candidate || parent == "." || parent == "" {
			break
		}
		candidate = parent
	}
	if existing == "" {
		existing = "."
	}

	var toCreate []string
	cur := dir
	for cur != existing && cur != "" && cur != "." {
		toCreate = append([]string{cur}, toCreate...)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}

	for _, p := range toCreate {
		if info, err := restoreFS.Stat(p); err == nil {
			if info != nil && info.IsDir() {
				continue
			}
			return fmt.Errorf("path exists but is not a directory: %s", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", p, err)
		}

		owner, perm := findNearestExistingDirMeta(filepath.Dir(p))
		if err := restoreFS.MkdirAll(p, perm); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}

		f, err := restoreFS.Open(p)
		if err != nil {
			return fmt.Errorf("open dir %s: %w", p, err)
		}

		if atomicGeteuid() == 0 && owner.ok {
			if err := atomicFileChown(f, owner.uid, owner.gid); err != nil {
				_ = f.Close()
				return fmt.Errorf("chown dir %s: %w", p, err)
			}
		}
		if err := atomicFileChmod(f, perm); err != nil {
			_ = f.Close()
			return fmt.Errorf("chmod dir %s: %w", p, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close dir %s: %w", p, err)
		}
	}
	return nil
}

func desiredOwnershipForAtomicWrite(destPath string) uidGid {
	destPath = filepath.Clean(strings.TrimSpace(destPath))
	if destPath == "" || destPath == "." {
		return uidGid{}
	}

	parent := filepath.Dir(destPath)
	parentOwner := uidGid{}
	if info, err := restoreFS.Stat(parent); err == nil && info != nil && info.IsDir() {
		parentOwner = uidGidFromFileInfo(info)
	}

	if info, err := restoreFS.Stat(destPath); err == nil && info != nil && !info.IsDir() {
		existing := uidGidFromFileInfo(info)
		if !existing.ok {
			if parentOwner.ok {
				return uidGid{uid: 0, gid: parentOwner.gid, ok: true}
			}
			return uidGid{}
		}

		if parentOwner.ok && existing.gid == 0 && parentOwner.gid != 0 {
			return uidGid{uid: existing.uid, gid: parentOwner.gid, ok: true}
		}
		return existing
	}

	if parentOwner.ok {
		return uidGid{uid: 0, gid: parentOwner.gid, ok: true}
	}
	return uidGid{}
}

// syncDir fsyncs a directory so a rename within it becomes durable. Filesystems
// that do not support directory fsync (EINVAL/ENOTSUP) are tolerated.
func syncDir(dir string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		dir = "."
	}

	df, err := restoreFS.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}

	syncErr := atomicFileSync(df)
	closeErr := df.Close()
	if syncErr != nil {
		if errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) {
			return closeErr
		}
		return fmt.Errorf("fsync dir %s: %w", dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close dir %s: %w", dir, closeErr)
	}
	return nil
}

// prepareAtomicTempFile is phase 1 of an atomic write: it writes data to a sibling
// temp file of path with the final ownership/permissions applied and flushed, but
// does NOT rename it into place, so no live file is touched yet. On any error before
// the temp is ready it removes the temp. It returns the temp path, the cleaned
// destination path, and the parent directory for commitAtomicTempFile (phase 2).
func prepareAtomicTempFile(path string, data []byte, perm os.FileMode) (tmpPath, cleanPath, dir string, err error) {
	cleanPath = filepath.Clean(strings.TrimSpace(path))
	if cleanPath == "" || cleanPath == "." {
		return "", "", "", fmt.Errorf("invalid path")
	}
	perm = modeBits(perm)
	if perm == 0 {
		perm = 0o644
	}

	dir = filepath.Dir(cleanPath)
	if err := ensureDirExistsWithInheritedMeta(dir); err != nil {
		return "", "", "", err
	}

	owner := desiredOwnershipForAtomicWrite(cleanPath)

	tmpPath = fmt.Sprintf("%s.proxsave.tmp.%d", cleanPath, nowRestore().UnixNano())
	f, err := restoreFS.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", "", err
	}

	writeErr := func() error {
		if len(data) == 0 {
			return nil
		}
		_, werr := f.Write(data)
		return werr
	}()
	if writeErr == nil {
		if atomicGeteuid() == 0 && owner.ok {
			if cerr := atomicFileChown(f, owner.uid, owner.gid); cerr != nil {
				writeErr = cerr
			}
		}
		if writeErr == nil {
			if cerr := atomicFileChmod(f, perm); cerr != nil {
				writeErr = cerr
			}
		}
		if writeErr == nil {
			if serr := atomicFileSync(f); serr != nil {
				writeErr = serr
			}
		}
	}

	closeErr := f.Close()
	if writeErr != nil {
		_ = restoreFS.Remove(tmpPath)
		return "", "", "", writeErr
	}
	if closeErr != nil {
		_ = restoreFS.Remove(tmpPath)
		return "", "", "", closeErr
	}
	return tmpPath, cleanPath, dir, nil
}

// commitAtomicTempFile is phase 2 of an atomic write: it renames a prepared temp
// file into its final destination and fsyncs the parent directory. The returned
// committed flag lets a batch caller roll back precisely: committed==false means the
// rename failed and the destination is untouched (the temp was removed); committed==
// true with a non-nil error means the rename succeeded (the file is live) but the
// directory fsync failed.
func commitAtomicTempFile(tmpPath, cleanPath, dir string) (committed bool, err error) {
	if err := restoreFS.Rename(tmpPath, cleanPath); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return false, err
	}
	if err := syncDir(dir); err != nil {
		return true, err
	}
	return true, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmpPath, cleanPath, dir, err := prepareAtomicTempFile(path, data, perm)
	if err != nil {
		return err
	}
	_, err = commitAtomicTempFile(tmpPath, cleanPath, dir)
	return err
}

// atomicFileWrite is one entry of a writeFilesAtomic batch. original holds the
// current on-disk bytes used to roll the file back if a later file in the batch
// fails to commit. An empty original (the read-back representation of an absent or
// empty file) rolls back to an empty file, which is acceptable for the account DB.
type atomicFileWrite struct {
	path     string
	data     []byte
	original []byte
	perm     os.FileMode
}

// writeFilesAtomic writes a set of files all-or-nothing as far as is achievable
// without a journal. Phase 1 prepares every temp file; if any preparation fails no
// destination is touched, so the common disk-full / read-only / IO failures leave
// every live file unchanged. Phase 2 renames each temp into place; if a commit fails
// the already-committed files are rolled back to their originals. Residual window: a
// crash or power loss BETWEEN renames cannot be made atomic here (that needs a
// journal / recovery marker) — the window is narrowed to the cheap renames because
// all data is already fsynced and closed in phase 1.
func writeFilesAtomic(writes []atomicFileWrite) error {
	type prepared struct {
		tmpPath   string
		cleanPath string
		dir       string
		w         atomicFileWrite
	}

	staged := make([]prepared, 0, len(writes))
	// Phase 1: prepare all temps. No destination file is touched yet.
	for _, w := range writes {
		tmpPath, cleanPath, dir, err := prepareAtomicTempFile(w.path, w.data, w.perm)
		if err != nil {
			for _, s := range staged {
				_ = restoreFS.Remove(s.tmpPath)
			}
			return fmt.Errorf("prepare %s: %w", w.path, err)
		}
		staged = append(staged, prepared{tmpPath: tmpPath, cleanPath: cleanPath, dir: dir, w: w})
	}

	// Phase 2: commit (rename) each in order; roll back on failure.
	for i, s := range staged {
		committed, err := commitAtomicTempFile(s.tmpPath, s.cleanPath, s.dir)
		if err == nil {
			continue
		}

		// Roll back the files already made live. When committed is true the rename of
		// index i succeeded (only the dir fsync failed) so file i is live and must be
		// reverted too; when false the rename failed and index i is untouched (its
		// temp was already removed by commitAtomicTempFile).
		last := i - 1
		if committed {
			last = i
		}
		var rollbackFailed []string
		for j := last; j >= 0; j-- {
			if rbErr := writeFileAtomic(staged[j].cleanPath, staged[j].w.original, staged[j].w.perm); rbErr != nil {
				rollbackFailed = append(rollbackFailed, staged[j].cleanPath)
			}
		}
		// Remove the not-yet-committed temps (index i's temp is already gone).
		for k := i + 1; k < len(staged); k++ {
			_ = restoreFS.Remove(staged[k].tmpPath)
		}

		if len(rollbackFailed) > 0 {
			// Typed as ErrRestoreInconsistentState so the restore workflow ABORTS (non-zero
			// outcome) instead of downgrading this to "completed with warnings" (F06-08). Both
			// the sentinel and the underlying commit error are wrapped for errors.Is.
			return fmt.Errorf("CRITICAL: commit of %s failed and rollback of %v also failed; the on-disk file set may be inconsistent and manual recovery is required: %w: %w", s.cleanPath, rollbackFailed, ErrRestoreInconsistentState, err)
		}
		return fmt.Errorf("commit %s failed; already-written files were rolled back to their originals: %w", s.cleanPath, err)
	}
	return nil
}
