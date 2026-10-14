package safefs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// procRoot is where HeldOpenIn reads the open descriptors of every process.
var procRoot = "/proc"

// SetProcRootForTest points HeldOpenIn at another process table and returns a restore
// func. Test-only (cross-package): tests run without root, and an unprivileged reader
// cannot read the descriptors of other users' processes, which HeldOpenIn refuses to
// skip. Not safe for concurrent use; restore before the test ends.
func SetProcRootForTest(root string) (restore func()) {
	prev := procRoot
	procRoot = root
	return func() { procRoot = prev }
}

// HeldOpenIn returns the names of the files directly inside dir that a process on this
// host holds open, read from the /proc/<pid>/fd links. A log another process is still
// writing must not be deleted: unlinked, the rest of its output goes nowhere. The links
// are compared as text, so no file behind them is touched and a descriptor on a dead
// mount cannot stall the scan; only dir itself is resolved, bounded like every other
// call here. A process, or a descriptor, that goes away during the scan is skipped. Any
// other failure to read one fails the whole scan: a process whose descriptors could not
// be read may hold a log open, so an answer that left it out is not an answer.
func HeldOpenIn(ctx context.Context, dir string, timeout time.Duration) (map[string]bool, error) {
	return Run(ctx, "held-open", dir, timeout, func() (map[string]bool, error) {
		folders := make(map[string]bool, 2)
		if abs, err := filepath.Abs(dir); err == nil {
			folders[filepath.Clean(abs)] = true
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			folders[filepath.Clean(resolved)] = true
		}
		procs, err := os.ReadDir(procRoot)
		if err != nil {
			return nil, err
		}
		held := make(map[string]bool)
		for _, proc := range procs {
			if !proc.IsDir() || !isAllDigits(proc.Name()) {
				continue
			}
			fdDir := filepath.Join(procRoot, proc.Name(), "fd")
			fds, err := os.ReadDir(fdDir)
			if err != nil {
				if goneDuringScan(err) {
					continue
				}
				return nil, err
			}
			for _, fd := range fds {
				target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
				if err != nil {
					if goneDuringScan(err) {
						continue
					}
					return nil, err
				}
				if !strings.HasPrefix(target, "/") {
					continue
				}
				target = strings.TrimSuffix(target, " (deleted)")
				if folders[filepath.Dir(target)] {
					held[filepath.Base(target)] = true
				}
			}
		}
		return held, nil
	})
}

// goneDuringScan reports an error that only says the process, or the descriptor, ended
// between the listing and the read: there is nothing left there to hold a log open.
func goneDuringScan(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
