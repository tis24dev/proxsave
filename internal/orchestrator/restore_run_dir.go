package orchestrator

import (
	"fmt"
	"path/filepath"
	"sync"
)

// restoreRunBaseDir holds what a restore must keep past the reboot it recommends:
// the safety backup, the rollback archives, the restore and rollback logs, and what
// it hands to the operator (the NIC repair copy, deferred datastore definitions,
// network diagnostics). /tmp does not qualify: on Debian 13 (PVE 9, PBS 4) it is a
// tmpfs, emptied by that reboot.
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

// rollbackLogPath places the log of a rollback script in RestoreRunDir. The script
// can run after ProxSave has exited, and its log is what is left to read after the
// reboot the restore recommends. Marker and script stay in the work dir: they only
// matter inside the rollback window. The script creates the log 0600 itself.
func rollbackLogPath(name string) (string, error) {
	dir := RestoreRunDir()
	if err := restoreFS.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create rollback log directory: %w", err)
	}
	return filepath.Join(dir, name), nil
}
