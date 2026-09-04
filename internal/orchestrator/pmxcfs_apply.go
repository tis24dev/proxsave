package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	pmxcfsCloseRoot = func(root *os.Root) error { return root.Close() }
)

func pmxcfsWriteFile(logger *logging.Logger, relPath string, data []byte) error {
	cleanPath, err := cleanPmxcfsRelativePath(relPath)
	if err != nil {
		return err
	}
	if isRealRestoreFS(restoreFS) {
		return pmxcfsWriteFileAtRoot(logger, cleanPath, data)
	}

	mounted, err := pmxcfsIsMounted(pmxcfsRoot)
	if err != nil {
		return fmt.Errorf("check pmxcfs mount %s: %w", pmxcfsRoot, err)
	}
	if !mounted {
		return fmt.Errorf("%s is not mounted; refusing a shadow write on the root filesystem", pmxcfsRoot)
	}
	dest := filepath.Join(pmxcfsRoot, cleanPath)
	if err := restoreFS.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if err := restoreFS.WriteFile(dest, data, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	logger.Debug("pmxcfs write: %s (%d bytes)", dest, len(data))
	return nil
}

func cleanPmxcfsRelativePath(relPath string) (string, error) {
	if relPath == "" || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("invalid pmxcfs path %q: expected a relative path below %s", relPath, pmxcfsRoot)
	}
	cleanPath := filepath.Clean(relPath)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid pmxcfs path %q: expected a relative path below %s", relPath, pmxcfsRoot)
	}
	return cleanPath, nil
}

func pmxcfsWriteFileAtRoot(logger *logging.Logger, relPath string, data []byte) (retErr error) {
	root, err := os.OpenRoot(pmxcfsRoot)
	if err != nil {
		return fmt.Errorf("open pmxcfs root %s: %w", pmxcfsRoot, err)
	}
	defer func() {
		if closeErr := pmxcfsCloseRoot(root); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close pmxcfs root %s: %w", pmxcfsRoot, closeErr))
		}
	}()

	mounted, err := pmxcfsIsMounted(pmxcfsRoot)
	if err != nil {
		return fmt.Errorf("check pmxcfs mount %s: %w", pmxcfsRoot, err)
	}
	if !mounted {
		return fmt.Errorf("%s is not mounted; refusing a shadow write on the root filesystem", pmxcfsRoot)
	}

	openedInfo, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("stat opened pmxcfs root %s: %w", pmxcfsRoot, err)
	}
	currentInfo, err := os.Stat(pmxcfsRoot)
	if err != nil {
		return fmt.Errorf("stat current pmxcfs root %s: %w", pmxcfsRoot, err)
	}
	if !os.SameFile(openedInfo, currentInfo) {
		return fmt.Errorf("pmxcfs root %s changed while preparing write; refusing to continue", pmxcfsRoot)
	}

	dir := filepath.Dir(relPath)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Join(pmxcfsRoot, dir), err)
		}
	}
	dest := filepath.Join(pmxcfsRoot, relPath)
	if err := root.WriteFile(relPath, data, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	logger.Debug("pmxcfs write: %s (%d bytes)", dest, len(data))
	return nil
}
