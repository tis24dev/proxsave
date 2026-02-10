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
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
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
	for {
		if cur == existing || cur == "" || cur == "." {
			break
		}
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
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
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

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return fmt.Errorf("invalid path")
	}
	perm = modeBits(perm)
	if perm == 0 {
		perm = 0o644
	}

	dir := filepath.Dir(path)
	if err := ensureDirExistsWithInheritedMeta(dir); err != nil {
		return err
	}

	owner := desiredOwnershipForAtomicWrite(path)

	tmpPath := fmt.Sprintf("%s.proxsave.tmp.%d", path, nowRestore().UnixNano())
	f, err := restoreFS.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	syncDir := func(dir string) error {
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

	writeErr := func() error {
		if len(data) == 0 {
			return nil
		}
		_, err := f.Write(data)
		return err
	}()
	if writeErr == nil {
		if atomicGeteuid() == 0 && owner.ok {
			if err := atomicFileChown(f, owner.uid, owner.gid); err != nil {
				writeErr = err
			}
		}
		if writeErr == nil {
			if err := atomicFileChmod(f, perm); err != nil {
				writeErr = err
			}
		}
		if writeErr == nil {
			if err := atomicFileSync(f); err != nil {
				writeErr = err
			}
		}
	}

	closeErr := f.Close()
	if writeErr != nil {
		_ = restoreFS.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		_ = restoreFS.Remove(tmpPath)
		return closeErr
	}

	if err := restoreFS.Rename(tmpPath, path); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return err
	}

	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}
