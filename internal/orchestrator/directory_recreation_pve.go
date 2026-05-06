// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
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

	createStorageSubdirs(basePath, subdirs, logger)
	return nil
}

func createStorageSubdirs(basePath string, subdirs []string, logger *logging.Logger) {
	for _, subdir := range subdirs {
		path := filepath.Join(basePath, subdir)
		if err := os.MkdirAll(path, 0750); err != nil {
			logger.Warning("Failed to create %s: %v", path, err)
		}
	}
}
