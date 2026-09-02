package orchestrator

import (
	"fmt"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/logging"
)

// pmxcfsWriteFile writes ONE file under /etc/pve. That path is pmxcfs, the
// cluster-replicated configuration filesystem: a write on one node propagates
// cluster-wide exactly the way a pvesh call does, because pvesh itself ends in a
// pmxcfs write. The staged-apply arms use this where the API surface has no usable
// endpoint (datacenter.cfg, vzdump.cron, guest conf creation) and as the fidelity
// net where the endpoint half-works (guest set rejected on a create-only key); see
// diagnostics/design-staged-apply-pmxcfs-2026-09-02.md, verified live on PVE 9.1.9.
//
// The mounted guard is the same rule restore_ha.go and restore_access_control_ui.go
// already enforce: with /etc/pve NOT mounted, a write here would land on the root
// filesystem as a shadow file that the real mount hides forever and the cluster
// never sees. Refusing is the only honest answer.
//
// No rollback capture here, on the maintainer's call (2026-09-02): the restore
// workflow's safety backup (createSafetyBackup) has already archived every file
// these writes can touch before any apply arm runs.
var (
	pmxcfsRoot      = "/etc/pve"
	pmxcfsIsMounted = isMounted
)

func pmxcfsWriteFile(logger *logging.Logger, relPath string, data []byte) error {
	mounted, err := pmxcfsIsMounted(pmxcfsRoot)
	if err != nil {
		return fmt.Errorf("check pmxcfs mount %s: %w", pmxcfsRoot, err)
	}
	if !mounted {
		return fmt.Errorf("%s is not mounted; refusing a shadow write on the root filesystem", pmxcfsRoot)
	}
	dest := filepath.Join(pmxcfsRoot, relPath)
	if err := restoreFS.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if err := restoreFS.WriteFile(dest, data, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	logger.Debug("pmxcfs write: %s (%d bytes)", dest, len(data))
	return nil
}
