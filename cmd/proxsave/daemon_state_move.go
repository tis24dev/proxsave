package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

// removeLegacyDaemonCopies deletes identity/.daemon.pid and identity/.daemon_info.json once
// daemon_state/ has the same file. The daemon writes those two copies only on the start that
// moved its files out of identity/, for the previous release, which restarts the daemon during
// --upgrade and then polls identity/ to confirm the restart. Once this release's what's-new check
// runs, that verification is over. It runs on every check and is a no-op once identity/ is clean.
// A copy is kept while daemon_state/ lacks the file, so a daemon an older release still runs is
// never stripped of its pid file.
//
// Best-effort: debugf receives the DEBUG lines (nil keeps it silent, for the dashboard, which has
// no visible log).
func removeLegacyDaemonCopies(baseDir string, debugf func(format string, args ...any)) {
	if strings.TrimSpace(baseDir) == "" {
		return
	}
	if debugf == nil {
		debugf = func(string, ...any) {}
	}
	pairs := []struct{ legacy, current string }{
		{health.LegacyDaemonPIDPath(baseDir), health.DaemonPIDPath(baseDir)},
		{health.LegacyDaemonInfoPath(baseDir), health.DaemonInfoPath(baseDir)},
	}
	for _, p := range pairs {
		name := filepath.Base(p.legacy)
		if _, err := os.Lstat(p.legacy); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				debugf("daemon state: remove %s from identity/ failed error=%v", name, err)
			}
			continue
		}
		if _, err := os.Lstat(p.current); err != nil {
			debugf("daemon state: kept %s in identity/, daemon_state/ does not have it", name)
			continue
		}
		if err := os.Remove(p.legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
			debugf("daemon state: remove %s from identity/ failed error=%v", name, err)
			continue
		}
		debugf("daemon state: removed %s from identity/, daemon_state/ has it", name)
	}
}
