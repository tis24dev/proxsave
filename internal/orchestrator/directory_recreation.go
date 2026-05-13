// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"errors"
	"fmt"

	"github.com/tis24dev/proxsave/internal/logging"
)

var (
	storageCfgPath   = "/etc/pve/storage.cfg"
	datastoreCfgPath = "/etc/proxmox-backup/datastore.cfg"
	zpoolCachePath   = "/etc/zfs/zpool.cache"
)

// RecreateStorageDirectories parses storage.cfg and recreates storage directories (PVE)
func RecreateStorageDirectories(logger *logging.Logger) error {
	entries, err := loadPVEStorageEntries(storageCfgPath, logger)
	if err != nil {
		return err
	}

	directoriesCreated := 0
	var errs []error
	for _, entry := range entries {
		if err := createPVEStorageStructure(entry.Path, entry.Type, logger); err != nil {
			logger.Warning("Failed to create storage structure for %s: %v", entry.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", entry.Name, err))
			continue
		}

		directoriesCreated++
		logger.Debug("Created storage structure: %s (%s) at %s", entry.Name, entry.Type, entry.Path)
	}

	if directoriesCreated > 0 {
		logger.Info("Recreated %d storage directory structures", directoriesCreated)
	}

	return errors.Join(errs...)
}

// RecreateDatastoreDirectories parses datastore.cfg and recreates datastore directories (PBS)
func RecreateDatastoreDirectories(logger *logging.Logger) error {
	entries, err := loadPBSDatastoreEntries(datastoreCfgPath, logger)
	if err != nil {
		return err
	}

	directoriesCreated := 0
	for _, entry := range entries {
		created, err := createPBSDatastoreStructure(entry.Path, entry.Name, logger)
		if err != nil {
			logger.Warning("Failed to create datastore structure for %s: %v", entry.Name, err)
			continue
		}
		if created {
			directoriesCreated++
			logger.Debug("Created datastore structure: %s at %s", entry.Name, entry.Path)
		}
	}

	if directoriesCreated > 0 {
		logger.Info("Recreated %d datastore directory structures", directoriesCreated)
	}

	return nil
}

// RecreateDirectoriesFromConfig recreates storage/datastore directories based on system type
func RecreateDirectoriesFromConfig(systemType SystemType, logger *logging.Logger) error {
	logger.Info("Recreating directory structures from configuration...")

	var errs []error
	ran := false

	if systemType.SupportsPVE() {
		ran = true
		if err := RecreateStorageDirectories(logger); err != nil {
			errs = append(errs, fmt.Errorf("recreate PVE storage directories: %w", err))
		}
	}
	if systemType.SupportsPBS() {
		ran = true
		if err := RecreateDatastoreDirectories(logger); err != nil {
			errs = append(errs, fmt.Errorf("recreate PBS datastore directories: %w", err))
		}
	}
	if !ran {
		logger.Debug("Unknown system type, skipping directory recreation")
	}

	return errors.Join(errs...)
}
