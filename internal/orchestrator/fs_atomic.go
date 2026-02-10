package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

	owner, perm := findNearestExistingDirMeta(filepath.Dir(dir))
	if err := restoreFS.MkdirAll(dir, perm); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	f, err := restoreFS.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer f.Close()

	if os.Geteuid() == 0 && owner.ok {
		if err := f.Chown(owner.uid, owner.gid); err != nil {
			return fmt.Errorf("chown dir %s: %w", dir, err)
		}
	}
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod dir %s: %w", dir, err)
	}
	return nil
}

func desiredOwnershipForAtomicWrite(destPath string) uidGid {
	destPath = filepath.Clean(strings.TrimSpace(destPath))
	if destPath == "" || destPath == "." {
		return uidGid{}
	}

	if info, err := restoreFS.Stat(destPath); err == nil && info != nil && !info.IsDir() {
		return uidGidFromFileInfo(info)
	}

	parent := filepath.Dir(destPath)
	if info, err := restoreFS.Stat(parent); err == nil && info != nil && info.IsDir() {
		parentOwner := uidGidFromFileInfo(info)
		if parentOwner.ok {
			return uidGid{uid: 0, gid: parentOwner.gid, ok: true}
		}
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

	writeErr := func() error {
		if len(data) == 0 {
			return nil
		}
		_, err := f.Write(data)
		return err
	}()
	if writeErr == nil {
		if os.Geteuid() == 0 && owner.ok {
			if err := f.Chown(owner.uid, owner.gid); err != nil {
				writeErr = err
			}
		}
		if writeErr == nil {
			if err := f.Chmod(perm); err != nil {
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
	return nil
}
