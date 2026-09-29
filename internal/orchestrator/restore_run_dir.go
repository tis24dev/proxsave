package orchestrator

import (
	"fmt"
	"path/filepath"
	"sync"
)

// restoreRunBaseDir holds what a restore must keep past the reboot it recommends:
// the safety backup, the rollback archives, the restore and rollback logs, and what
// it hands to the operator (the NIC repair copy, deferred datastore definitions,
// network diagnostics). It is <BASE_DIR>/restore, set by SetBaseDir. /tmp does not
// qualify: on Debian 13 (PVE 9, PBS 4) it is a tmpfs, emptied by that reboot.
var (
	restoreRunDirMu   sync.Mutex
	restoreRunBaseDir = filepath.Join(fallbackBaseDir, "restore")
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

// rollbackLogProbe is the line every rollback script runs right after it sets LOG. It
// creates the log 0600 in a subshell, so the umask of the rest of the script, and of
// what it runs (ifreload and its hooks), stays as it was. If the log cannot be created
// the script logs to /dev/null instead: the scripts run under `set -eu`, so the first
// failed write to the log would stop the rollback before it restored anything. That
// happens when RestoreRunDir's filesystem is full or read-only while the files the
// rollback restores are still writable, e.g. BASE_DIR (/opt) on its own filesystem.
const rollbackLogProbe = `if ! (umask 077 && : >> "$LOG") 2>/dev/null; then LOG=/dev/null; fi`

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
