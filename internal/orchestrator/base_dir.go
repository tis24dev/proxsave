package orchestrator

import (
	"path/filepath"
	"strings"
)

// fallbackBaseDir is BASE_DIR when none is known, the same fallback exportDestRoot uses
// for the export directory.
const fallbackBaseDir = "/opt/proxsave"

// SetBaseDir places the host state ProxSave keeps between runs under BASE_DIR: the
// restore run directories (<BASE_DIR>/restore) and the mount guards (<BASE_DIR>/guards).
// ProxSave writes only under BASE_DIR and /tmp/proxsave. main calls it once, right after
// parsing the arguments and before any mode reads either path; an empty baseDir falls
// back to fallbackBaseDir.
func SetBaseDir(baseDir string) {
	base := strings.TrimSpace(baseDir)
	if base == "" {
		base = fallbackBaseDir
	}
	base = filepath.Clean(base)

	restoreRunDirMu.Lock()
	restoreRunBaseDir = filepath.Join(base, "restore")
	restoreRunDirMu.Unlock()

	mountGuardBaseDir = filepath.Join(base, "guards")
}
