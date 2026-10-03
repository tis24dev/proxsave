// daemon_state_dir.go names the directory the daemon keeps its state in. BASE_DIR/identity
// holds the server identity and keys; the daemon's lock, pid, identity record, runtime record,
// abandon marker, healthcheck status, notify results and manual-outcome handoff live in
// BASE_DIR/daemon_state, root-only. Releases before 0.41.0 kept them in identity/; the daemon
// moves them at its start (cmd/proxsave) and every reader uses daemon_state/ only.
//
// Like its siblings this file stays logging-free and stdlib-only.

package health

import (
	"os"
	"path/filepath"
)

// DaemonStateDirName is the BASE_DIR entry that holds the daemon's state files.
const DaemonStateDirName = "daemon_state"

// DaemonStateDirPerm is the mode daemon_state/ is created with: the files in it are root's
// alone, the same 0700 as identity/.
const DaemonStateDirPerm os.FileMode = 0o700

// legacyIdentityDirName is the BASE_DIR entry where releases before 0.41.0 kept the daemon's
// state files, next to the server identity.
const legacyIdentityDirName = "identity"

// DaemonStateDir returns BASE_DIR/daemon_state, resolved with a plain Join so every writer and
// reader agrees on the byte-identical path.
func DaemonStateDir(baseDir string) string {
	return filepath.Join(baseDir, DaemonStateDirName)
}

// LegacyIdentityDir returns BASE_DIR/identity, where releases before 0.41.0 kept the daemon's
// state files. Only the daemon's one-time move and the copies an older release reads while it
// verifies the upgrade it just performed use it.
func LegacyIdentityDir(baseDir string) string {
	return filepath.Join(baseDir, legacyIdentityDirName)
}
