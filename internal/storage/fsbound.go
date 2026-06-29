package storage

import (
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// fsIoTimeout converts the configured FS_IO_TIMEOUT into a per-operation safefs
// budget shared by the storage backends. A non-positive value (the explicit
// FS_IO_TIMEOUT=0 opt-out, or an unset cfg) yields 0, which safefs treats as
// unbounded (legacy behaviour). It bounds individual filesystem syscalls on
// user-configured mount paths (BACKUP_PATH / SECONDARY_PATH / cloud local source)
// so a dead/stale mount cannot wedge the run in an uninterruptible (D) syscall.
func fsIoTimeout(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.FsIoTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(cfg.FsIoTimeoutSeconds) * time.Second
}
