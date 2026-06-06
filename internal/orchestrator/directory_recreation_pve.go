// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/logging"
)

var pveStorageSubdirs = map[string][]string{
	"dir":  {"dump", "images", "template", "snippets", "private"},
	"nfs":  {"dump", "images", "template"},
	"cifs": {"dump", "images", "template"},
}

// createPVEStorageStructure creates the directory structure for a PVE storage.
// It returns true when ProxSave made filesystem changes for this storage path,
// and false (without error) when creation was safely skipped because the path
// looks like a dedicated/ZFS mount that is not yet mounted (see
// shouldSkipUnmountedStorageMount) — the same guard the PBS datastore path uses.
func createPVEStorageStructure(basePath, storageType string, logger *logging.Logger) (bool, error) {
	if shouldSkipUnmountedStorageMount(basePath, pveStoragePathHasData(basePath), logger) {
		return false, nil
	}

	if err := os.MkdirAll(basePath, 0750); err != nil {
		return false, fmt.Errorf("create base directory: %w", err)
	}

	subdirs, ok := pveStorageSubdirs[storageType]
	if !ok {
		logger.Debug("Storage type %s does not require subdirectories", storageType)
		return true, nil
	}

	if err := createStorageSubdirs(basePath, subdirs); err != nil {
		return false, fmt.Errorf("create storage subdirectories: %w", err)
	}
	return true, nil
}

// pveStoragePathHasData reports whether basePath already contains entries, i.e.
// it is the live storage rather than an empty/not-yet-mounted mountpoint.
func pveStoragePathHasData(basePath string) bool {
	has, err := dirHasAnyEntry(basePath)
	return err == nil && has
}

func createStorageSubdirs(basePath string, subdirs []string) error {
	var errs []error
	for _, subdir := range subdirs {
		path := filepath.Join(basePath, subdir)
		if err := os.MkdirAll(path, 0750); err != nil {
			errs = append(errs, fmt.Errorf("create %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}
