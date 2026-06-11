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
	if err := validateRecreationPath(basePath); err != nil {
		return false, fmt.Errorf("unsafe storage path %q from storage.cfg: %w", basePath, err)
	}
	if shouldSkipUnmountedStorageMount(basePath, pveStoragePathHasData(basePath), logger) {
		return false, nil
	}

	changed := false
	if _, err := os.Stat(basePath); os.IsNotExist(err) {
		changed = true
	}
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return false, fmt.Errorf("create base directory: %w", err)
	}

	subdirs, ok := pveStorageSubdirs[storageType]
	if !ok {
		logger.Debug("Storage type %s does not require subdirectories", storageType)
		return changed, nil
	}

	subdirsCreated, err := createStorageSubdirs(basePath, subdirs)
	if err != nil {
		return false, fmt.Errorf("create storage subdirectories: %w", err)
	}
	return changed || subdirsCreated, nil
}

// pveStoragePathHasData reports whether basePath already contains entries, i.e.
// it is the live storage rather than an empty/not-yet-mounted mountpoint.
func pveStoragePathHasData(basePath string) bool {
	has, err := dirHasAnyEntry(basePath)
	return err == nil && has
}

func createStorageSubdirs(basePath string, subdirs []string) (bool, error) {
	var errs []error
	created := false
	for _, subdir := range subdirs {
		path := filepath.Join(basePath, subdir)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			created = true
		}
		if err := os.MkdirAll(path, 0750); err != nil {
			errs = append(errs, fmt.Errorf("create %s: %w", path, err))
		}
	}
	return created, errors.Join(errs...)
}
