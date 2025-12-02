package orchestrator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newDirTestLogger() *logging.Logger {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	return logger
}

func overridePath(t *testing.T, target *string, filename string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	prev := *target
	*target = path
	return path, func() {
		*target = prev
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRecreateStorageDirectoriesCreatesStructure(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "local")
	cfg := fmt.Sprintf("dir: local\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	if err := RecreateStorageDirectories(logger); err != nil {
		t.Fatalf("RecreateStorageDirectories error: %v", err)
	}

	for _, sub := range []string{"dump", "images", "template", "snippets", "private"} {
		if _, err := os.Stat(filepath.Join(baseDir, sub)); err != nil {
			t.Fatalf("expected subdir %s to exist: %v", sub, err)
		}
	}
}

func TestCreatePVEStorageStructureHandlesVariousTypes(t *testing.T) {
	logger := newDirTestLogger()
	baseNFS := filepath.Join(t.TempDir(), "nfs")
	if err := createPVEStorageStructure(baseNFS, "nfs", logger); err != nil {
		t.Fatalf("createPVEStorageStructure(nfs): %v", err)
	}
	for _, sub := range []string{"dump", "images", "template"} {
		if _, err := os.Stat(filepath.Join(baseNFS, sub)); err != nil {
			t.Fatalf("expected nfs subdir %s: %v", sub, err)
		}
	}

	baseOther := filepath.Join(t.TempDir(), "rbd")
	if err := createPVEStorageStructure(baseOther, "rbd", logger); err != nil {
		t.Fatalf("createPVEStorageStructure(rbd): %v", err)
	}
	if _, err := os.Stat(baseOther); err != nil {
		t.Fatalf("expected base directory for other type: %v", err)
	}
}

func TestRecreateDatastoreDirectoriesCreatesStructure(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "datastore1")
	cfg := fmt.Sprintf("datastore: backup\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	cachePath, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()
	// Ensure cache path does not exist to simulate non-ZFS environment
	if err := os.RemoveAll(cachePath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("cleanup cache path: %v", err)
	}

	if err := RecreateDatastoreDirectories(logger); err != nil {
		t.Fatalf("RecreateDatastoreDirectories error: %v", err)
	}

	for _, sub := range []string{".chunks", ".lock"} {
		if _, err := os.Stat(filepath.Join(baseDir, sub)); err != nil {
			t.Fatalf("expected datastore subdir %s: %v", sub, err)
		}
	}
}

func TestRecreateDatastoreDirectoriesSkipsZFSMountPoints(t *testing.T) {
	logger := newDirTestLogger()
	baseDir := filepath.Join(t.TempDir(), "backup-ds")
	cfg := fmt.Sprintf("datastore: ds\n    path %s\n", baseDir)
	cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
	defer restore()
	writeFile(t, cfgPath, cfg)

	cachePath, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer cacheRestore()
	writeFile(t, cachePath, "cache")

	if err := RecreateDatastoreDirectories(logger); err != nil {
		t.Fatalf("RecreateDatastoreDirectories zfs scenario: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, ".chunks")); !os.IsNotExist(err) {
		t.Fatalf("expected ZFS path to be skipped, got err=%v", err)
	}
}

func TestIsLikelyZFSMountPointRequiresCache(t *testing.T) {
	logger := newDirTestLogger()
	cachePath, restore := overridePath(t, &zpoolCachePath, "zpool.cache")
	defer restore()

	// Without cache file the function should return false even for matching paths.
	if isLikelyZFSMountPoint("/mnt/pbs", logger) {
		t.Fatalf("expected false when cache file is missing")
	}

	writeFile(t, cachePath, "cache")
	if !isLikelyZFSMountPoint("/mnt/pbs", logger) {
		t.Fatalf("expected true when cache exists and path matches patterns")
	}
}

func TestSetDatastoreOwnershipNoop(t *testing.T) {
	logger := newDirTestLogger()
	if err := setDatastoreOwnership(t.TempDir(), logger); err != nil {
		t.Fatalf("setDatastoreOwnership returned error: %v", err)
	}
}

func TestRecreateDirectoriesFromConfigRoutes(t *testing.T) {
	logger := newTestLogger()

	t.Run("PVE", func(t *testing.T) {
		baseDir := filepath.Join(t.TempDir(), "local")
		cfg := fmt.Sprintf("dir: local\n    path %s\n", baseDir)
		cfgPath, restore := overridePath(t, &storageCfgPath, "storage.cfg")
		t.Cleanup(restore)
		writeFile(t, cfgPath, cfg)

		if err := RecreateDirectoriesFromConfig(SystemTypePVE, logger); err != nil {
			t.Fatalf("RecreateDirectoriesFromConfig PVE: %v", err)
		}
		if _, err := os.Stat(filepath.Join(baseDir, "images")); err != nil {
			t.Fatalf("expected PVE directories to be created: %v", err)
		}
	})

	t.Run("PBS", func(t *testing.T) {
		baseDir := filepath.Join(t.TempDir(), "data")
		cfg := fmt.Sprintf("datastore: main\n    path %s\n", baseDir)
		cfgPath, restore := overridePath(t, &datastoreCfgPath, "datastore.cfg")
		t.Cleanup(restore)
		writeFile(t, cfgPath, cfg)

		cachePath, cacheRestore := overridePath(t, &zpoolCachePath, "zpool.cache")
		t.Cleanup(cacheRestore)
		if err := os.RemoveAll(cachePath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("cleanup cache path: %v", err)
		}

		if err := RecreateDirectoriesFromConfig(SystemTypePBS, logger); err != nil {
			t.Fatalf("RecreateDirectoriesFromConfig PBS: %v", err)
		}
		if _, err := os.Stat(filepath.Join(baseDir, ".chunks")); err != nil {
			t.Fatalf("expected PBS directories to be created: %v", err)
		}
	})

	t.Run("Unknown", func(t *testing.T) {
		if err := RecreateDirectoriesFromConfig(SystemTypeUnknown, logger); err != nil {
			t.Fatalf("RecreateDirectoriesFromConfig unknown: %v", err)
		}
	})
}
