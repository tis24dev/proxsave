package orchestrator

import (
	"path/filepath"
	"sync"
)

// restoreRunBaseDir holds what a restore must keep past the reboot it recommends:
// the safety backup, the rollback archives and the restore logs. /tmp does not
// qualify: on Debian 13 (PVE 9, PBS 4) it is a tmpfs, emptied by that reboot.
const restoreRunBaseDir = "/var/lib/proxsave/restore"

var (
	restoreRunDirMu   sync.Mutex
	restoreRunDirPath string
)

// RestoreRunDir returns the directory of this process's restore,
// <restoreRunBaseDir>/<ts>. The first call fixes <ts>: the restore session log
// (package main) calls it before the workflow starts, so the log and every file
// the workflow writes end up in the same directory. Writers create it 0700 and
// their files 0600; nothing ever removes it.
func RestoreRunDir() string {
	restoreRunDirMu.Lock()
	defer restoreRunDirMu.Unlock()
	if restoreRunDirPath == "" {
		restoreRunDirPath = filepath.Join(restoreRunBaseDir, nowRestore().Format("20060102_150405"))
	}
	return restoreRunDirPath
}
