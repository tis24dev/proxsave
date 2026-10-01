package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
)

// daemonStateMovedNames are the files releases before 0.41.0 kept in BASE_DIR/identity and this
// release keeps in BASE_DIR/daemon_state. They are the historical names on purpose: the move is
// about what an older release left behind, whatever the files are called later.
var daemonStateMovedNames = []string{
	daemonLockFileName,
	".daemon.pid",
	".daemon_info.json",
	".daemon_runtime.json",
	".daemon_abandoned.json",
	".healthcheck_status.json",
	".notify_results.json",
	".manual_backup_outcome.json",
}

// moveDaemonStateFromIdentity moves the daemon's files, and the ".corrupt" quarantine copy of
// each, from identity/ to daemon_state/. Only the daemon calls it, at its start, while it holds
// the single-instance lock, so no other daemon writes either directory meanwhile. A file
// daemon_state/ does not have yet is renamed into it (both live under BASE_DIR); a file
// daemon_state/ already has is the newer one, so the identity/ copy is deleted. Every failure is
// a DEBUG line and nothing more: a file left in identity/ is simply not read again.
//
// It reports whether at least one file was moved or removed from identity/, which is how the
// daemon knows this is its first start after an upgrade from a release that reads identity/.
func moveDaemonStateFromIdentity(baseDir string) bool {
	identityDir := health.LegacyIdentityDir(baseDir)
	stateDir := health.DaemonStateDir(baseDir)
	moved := false
	for _, name := range daemonStateMovedNames {
		for _, entry := range []string{name, name + ".corrupt"} {
			if moveDaemonStateEntry(identityDir, stateDir, entry) {
				moved = true
			}
		}
	}
	return moved
}

// moveDaemonStateEntry moves one entry and reports whether identity/ lost it.
func moveDaemonStateEntry(identityDir, stateDir, name string) bool {
	src := filepath.Join(identityDir, name)
	if _, err := os.Lstat(src); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logging.Debug("daemon state: move %s failed error=%v", name, err)
		}
		return false
	}
	dst := filepath.Join(stateDir, name)
	_, err := os.Lstat(dst)
	switch {
	case err == nil:
		if rmErr := os.Remove(src); rmErr != nil {
			logging.Debug("daemon state: move %s failed error=%v", name, rmErr)
			return false
		}
		logging.Debug("daemon state: removed %s from identity/, daemon_state/ already has it", name)
		return true
	case !errors.Is(err, fs.ErrNotExist):
		logging.Debug("daemon state: move %s failed error=%v", name, err)
		return false
	}
	if err := os.Rename(src, dst); err != nil {
		logging.Debug("daemon state: move %s failed error=%v", name, err)
		return false
	}
	logging.Debug("daemon state: moved %s from identity/ to daemon_state/", name)
	return true
}
