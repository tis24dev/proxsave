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

// createPVEStorageStructure creates the directory structure for a PVE storage
func createPVEStorageStructure(basePath, storageType string, logger *logging.Logger) error {
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return fmt.Errorf("create base directory: %w", err)
	}

	subdirs, ok := pveStorageSubdirs[storageType]
	if !ok {
		logger.Debug("Storage type %s does not require subdirectories", storageType)
		return nil
	}

	if err := createStorageSubdirs(basePath, subdirs); err != nil {
		return fmt.Errorf("create storage subdirectories: %w", err)
	}
	return nil
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
