package safefs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// procRoot is where HeldOpenIn reads the open descriptors of every process; a seam for
// tests.
var procRoot = "/proc"

// HeldOpenIn returns the names of the files directly inside dir that a process on this
// host holds open, read from the /proc/<pid>/fd links. A log another process is still
// writing must not be deleted: unlinked, the rest of its output goes nowhere. The links
// are compared as text, so no file behind them is touched and a descriptor on a dead
// mount cannot stall the scan; only dir itself is resolved, bounded like every other
// call here. A process that exits during the scan, or whose descriptors cannot be read,
// is skipped.
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
				continue
			}
			for _, fd := range fds {
				target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
				if err != nil || !strings.HasPrefix(target, "/") {
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
