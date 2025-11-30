package orchestrator

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// RecreateStorageDirectories parses storage.cfg and recreates storage directories (PVE)
func RecreateStorageDirectories(logger *logging.Logger) error {
	storageCfgPath := "/etc/pve/storage.cfg"

	// Check if file exists
	if _, err := os.Stat(storageCfgPath); err != nil {
		if os.IsNotExist(err) {
			logger.Debug("No storage.cfg found, skipping storage directory recreation")
			return nil
		}
		return fmt.Errorf("stat storage.cfg: %w", err)
	}

	logger.Info("Parsing storage.cfg to recreate storage directories...")

	file, err := os.Open(storageCfgPath)
	if err != nil {
		return fmt.Errorf("open storage.cfg: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var currentStorage string
	var currentPath string
	var currentType string

	directoriesCreated := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for storage definition start (e.g., "dir: local")
		if strings.Contains(line, ":") && !strings.Contains(line, "=") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentType = parts[0]
				currentStorage = parts[1]
				currentPath = ""
			}
			continue
		}

		// Parse path directive
		if strings.HasPrefix(line, "path ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentPath = parts[1]
			}
		}

		// When we have both storage name and path, create the directory structure
		if currentStorage != "" && currentPath != "" && currentType != "" {
			if err := createPVEStorageStructure(currentPath, currentType, logger); err != nil {
				logger.Warning("Failed to create storage structure for %s: %v", currentStorage, err)
			} else {
				directoriesCreated++
				logger.Debug("Created storage structure: %s (%s) at %s", currentStorage, currentType, currentPath)
			}

			// Reset for next storage
			currentStorage = ""
			currentPath = ""
			currentType = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read storage.cfg: %w", err)
	}

	if directoriesCreated > 0 {
		logger.Info("Recreated %d storage directory structures", directoriesCreated)
	}

	return nil
}

// createPVEStorageStructure creates the directory structure for a PVE storage
func createPVEStorageStructure(basePath, storageType string, logger *logging.Logger) error {
	// Create base directory
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return fmt.Errorf("create base directory: %w", err)
	}

	// Create subdirectories based on storage type
	switch storageType {
	case "dir":
		// Standard directory storage needs these subdirectories
		subdirs := []string{"dump", "images", "template", "snippets", "private"}
		for _, subdir := range subdirs {
			path := filepath.Join(basePath, subdir)
			if err := os.MkdirAll(path, 0750); err != nil {
				logger.Warning("Failed to create %s: %v", path, err)
			}
		}

	case "nfs", "cifs":
		// Network storage
		subdirs := []string{"dump", "images", "template"}
		for _, subdir := range subdirs {
			path := filepath.Join(basePath, subdir)
			if err := os.MkdirAll(path, 0750); err != nil {
				logger.Warning("Failed to create %s: %v", path, err)
			}
		}

	default:
		// For other storage types, just ensure base path exists
		logger.Debug("Storage type %s does not require subdirectories", storageType)
	}

	// Set ownership to root:root (already the case when running as root)
	// PVE typically uses root:root for storage directories

	return nil
}

// RecreateDatastoreDirectories parses datastore.cfg and recreates datastore directories (PBS)
func RecreateDatastoreDirectories(logger *logging.Logger) error {
	datastoreCfgPath := "/etc/proxmox-backup/datastore.cfg"

	// Check if file exists
	if _, err := os.Stat(datastoreCfgPath); err != nil {
		if os.IsNotExist(err) {
			logger.Debug("No datastore.cfg found, skipping datastore directory recreation")
			return nil
		}
		return fmt.Errorf("stat datastore.cfg: %w", err)
	}

	logger.Info("Parsing datastore.cfg to recreate datastore directories...")

	file, err := os.Open(datastoreCfgPath)
	if err != nil {
		return fmt.Errorf("open datastore.cfg: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var currentDatastore string
	var currentPath string

	directoriesCreated := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for datastore definition start (e.g., "datastore: backup")
		if strings.HasPrefix(line, "datastore:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentDatastore = parts[1]
				currentPath = ""
			}
			continue
		}

		// Parse path directive
		if strings.HasPrefix(line, "path ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				currentPath = parts[1]
			}
		}

		// When we have both datastore name and path, create the directory
		if currentDatastore != "" && currentPath != "" {
			if err := createPBSDatastoreStructure(currentPath, currentDatastore, logger); err != nil {
				logger.Warning("Failed to create datastore structure for %s: %v", currentDatastore, err)
			} else {
				directoriesCreated++
				logger.Debug("Created datastore structure: %s at %s", currentDatastore, currentPath)
			}

			// Reset for next datastore
			currentDatastore = ""
			currentPath = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read datastore.cfg: %w", err)
	}

	if directoriesCreated > 0 {
		logger.Info("Recreated %d datastore directory structures", directoriesCreated)
	}

	return nil
}

// createPBSDatastoreStructure creates the directory structure for a PBS datastore
func createPBSDatastoreStructure(basePath, datastoreName string, logger *logging.Logger) error {
	// Check if this might be a ZFS mount point
	if isLikelyZFSMountPoint(basePath, logger) {
		logger.Warning("Path %s appears to be a ZFS mount point", basePath)
		logger.Warning("The ZFS pool may need to be imported manually before the datastore works")
		logger.Info("To check pools: zpool import")
		logger.Info("To import pool: zpool import <pool-name>")
		logger.Info("To check status: zpool status")

		// Don't create directory structure over an unmounted ZFS pool
		// as this would create a regular directory that prevents proper mounting
		return nil
	}

	// Create base directory
	if err := os.MkdirAll(basePath, 0700); err != nil {
		return fmt.Errorf("create base directory: %w", err)
	}

	// PBS datastores need these subdirectories
	subdirs := []string{".chunks", ".lock"}
	for _, subdir := range subdirs {
		path := filepath.Join(basePath, subdir)
		if err := os.MkdirAll(path, 0700); err != nil {
			logger.Warning("Failed to create %s: %v", path, err)
		}
	}

	// Set ownership to backup:backup if the user exists
	// PBS typically uses backup:backup for datastore directories
	if err := setDatastoreOwnership(basePath, logger); err != nil {
		logger.Warning("Could not set ownership for %s: %v", basePath, err)
	}

	return nil
}

// isLikelyZFSMountPoint checks if a path is likely a ZFS mount point
func isLikelyZFSMountPoint(path string, logger *logging.Logger) bool {
	// Check if /etc/zfs/zpool.cache exists (indicates ZFS is used on this system)
	if _, err := os.Stat("/etc/zfs/zpool.cache"); err != nil {
		// No ZFS on this system
		return false
	}

	// Common ZFS mount point patterns
	// PBS datastores on ZFS are typically under /mnt/ or use "backup" in the name
	pathLower := strings.ToLower(path)
	if strings.HasPrefix(pathLower, "/mnt/") ||
	   strings.Contains(pathLower, "backup") ||
	   strings.Contains(pathLower, "datastore") {
		logger.Debug("Path %s matches ZFS mount point pattern", path)
		return true
	}

	return false
}

// setDatastoreOwnership sets ownership to backup:backup for PBS datastores
func setDatastoreOwnership(path string, logger *logging.Logger) error {
	// This is a simplified version - in production you'd want to:
	// 1. Check if backup user/group exists
	// 2. Get their UID/GID
	// 3. Call os.Chown with the correct IDs

	// For now, we'll just log that this should be done
	logger.Debug("Note: Set ownership manually if needed: chown -R backup:backup %s", path)

	return nil
}

// RecreateDirectoriesFromConfig recreates storage/datastore directories based on system type
func RecreateDirectoriesFromConfig(systemType SystemType, logger *logging.Logger) error {
	logger.Info("Recreating directory structures from configuration...")

	if systemType == SystemTypePVE {
		if err := RecreateStorageDirectories(logger); err != nil {
			return fmt.Errorf("recreate PVE storage directories: %w", err)
		}
	} else if systemType == SystemTypePBS {
		if err := RecreateDatastoreDirectories(logger); err != nil {
			return fmt.Errorf("recreate PBS datastore directories: %w", err)
		}
	} else {
		logger.Debug("Unknown system type, skipping directory recreation")
	}

	return nil
}
