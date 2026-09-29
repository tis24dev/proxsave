package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// stagingWriteError marks a failure to write into the working directory, as opposed to
// a failure to read the source: the source was readable, the staging tree was not
// writable (full, quota, I/O error).
type stagingWriteError struct {
	err error
}

func (e *stagingWriteError) Error() string { return e.err.Error() }
func (e *stagingWriteError) Unwrap() error { return e.err }

func isStagingWriteError(err error) bool {
	var target *stagingWriteError
	return errors.As(err, &target)
}

// NoteStagingWriteError records a write into the working directory that failed for
// lack of space. The first one stops the collection: every later write fails the same
// way, and the archive used to go on with the file that hit the limit truncated and the
// ones after it empty (measured on a 300M tmpfs: 17 empty files, SSH host keys among
// them, in a run that ended with warnings only). It logs where the space ran out, once.
func (c *Collector) NoteStagingWriteError(err error) {
	if err == nil || !errors.Is(err, syscall.ENOSPC) {
		return
	}
	c.stagingSpaceMu.Lock()
	defer c.stagingSpaceMu.Unlock()
	if c.stagingNoSpace != nil {
		return
	}
	c.stagingNoSpace = fmt.Errorf("no space left in the working directory: %w", err)
	root := filepath.Dir(c.tempDir)
	fsType, size := stagingFilesystemFacts(root)
	c.logger.Error("Working directory - no space left on %s (%s, %s): collection stopped, no archive written", root, fsType, size)
}

// StagingNoSpaceErr returns the error that stopped the collection for lack of space in
// the working directory, or nil.
func (c *Collector) StagingNoSpaceErr() error {
	c.stagingSpaceMu.Lock()
	defer c.stagingSpaceMu.Unlock()
	return c.stagingNoSpace
}

// stagingFilesystemFacts names the filesystem holding path and its size, for the line
// that reports a full working directory ("tmpfs", "300.0 MiB").
func stagingFilesystemFacts(path string) (fsType, size string) {
	fsType, size = "unknown filesystem", "unknown size"
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err == nil {
		total, _, _ := safefs.SpaceUsageFromStatfs(st)
		size = FormatBytes(total)
	}
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fsType, size
	}
	best := -1
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mount := fields[1]
		if mount == "/" || path == mount || strings.HasPrefix(path, mount+"/") {
			if len(mount) >= best {
				best = len(mount)
				fsType = fields[2]
			}
		}
	}
	return fsType, size
}
