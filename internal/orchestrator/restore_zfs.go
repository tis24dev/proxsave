// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"bufio"
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

var restoreGlob = filepath.Glob

// checkZFSPoolsAfterRestore checks if ZFS pools need to be imported after restore
func checkZFSPoolsAfterRestore(logger *logging.Logger) error {
	if _, err := restoreCmd.Run(context.Background(), "which", "zpool"); err != nil {
		// zpool utility not available -> no ZFS tooling installed
		return nil
	}

	logger.Info("Checking ZFS pool status...")

	configuredPools := detectConfiguredZFSPools()
	importablePools, importOutput, importErr := detectImportableZFSPools()

	logConfiguredZFSPools(logger, configuredPools)
	logImportableZFSPools(logger, importablePools, importOutput, importErr)

	if len(importablePools) == 0 {
		logNoImportableZFSPools(logger, configuredPools)
		return nil
	}

	logManualZFSImportInstructions(logger, importablePools)
	return nil
}

func logConfiguredZFSPools(logger *logging.Logger, configuredPools []string) {
	if len(configuredPools) == 0 {
		return
	}
	logger.Warning("Found %d ZFS pool(s) configured for automatic import:", len(configuredPools))
	for _, pool := range configuredPools {
		logger.Warning("  - %s", pool)
	}
	logger.Info("")
}

func logImportableZFSPools(logger *logging.Logger, importablePools []string, importOutput string, importErr error) {
	if importErr != nil {
		logger.Warning("`zpool import` command returned an error: %v", importErr)
		if strings.TrimSpace(importOutput) != "" {
			logger.Warning("`zpool import` output:\n%s", importOutput)
		}
		return
	}
	if len(importablePools) > 0 {
		logger.Warning("`zpool import` reports pools waiting to be imported:")
		for _, pool := range importablePools {
			logger.Warning("  - %s", pool)
		}
		logger.Info("")
	}
}

func logNoImportableZFSPools(logger *logging.Logger, configuredPools []string) {
	logger.Info("`zpool import` did not report pools waiting for import.")
	if len(configuredPools) == 0 {
		return
	}
	logger.Info("")
	for _, pool := range configuredPools {
		if _, err := restoreCmd.Run(context.Background(), "zpool", "status", pool); err == nil {
			logger.Info("Pool %s is already imported (no manual action needed)", pool)
		} else {
			logger.Warning("Systemd expects pool %s, but `zpool import` and `zpool status` did not report it. Check disk visibility and pool status.", pool)
		}
	}
}

func logManualZFSImportInstructions(logger *logging.Logger, importablePools []string) {
	logger.Info("⚠ IMPORTANT: ZFS pools may need manual import after restore!")
	logger.Info("  Before rebooting, run these commands:")
	logger.Info("  1. Check available pools:  zpool import")
	for _, pool := range importablePools {
		logger.Info("  2. Import pool manually:   zpool import %s", pool)
	}
	logger.Info("  3. Verify pool status:     zpool status")
	logger.Info("")
	logger.Info("  If pools fail to import, check:")
	logger.Info("  - journalctl -u zfs-import@<pool-name>.service oppure import@<pool-name>.service")
	logger.Info("  - zpool import -d /dev/disk/by-id")
	logger.Info("")
}

func detectConfiguredZFSPools() []string {
	pools := make(map[string]struct{})
	addConfiguredZFSPoolsFromDirs(pools)
	addConfiguredZFSPoolsFromGlobPatterns(pools)
	return sortedPoolNames(pools)
}

func addConfiguredZFSPoolsFromDirs(pools map[string]struct{}) {
	directories := []string{
		"/etc/systemd/system/zfs-import.target.wants",
		"/etc/systemd/system/multi-user.target.wants",
	}

	for _, dir := range directories {
		entries, err := restoreFS.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if pool := parsePoolNameFromUnit(entry.Name()); pool != "" {
				pools[pool] = struct{}{}
			}
		}
	}
}

func addConfiguredZFSPoolsFromGlobPatterns(pools map[string]struct{}) {
	globPatterns := []string{
		"/etc/systemd/system/zfs-import@*.service",
		"/etc/systemd/system/import@*.service",
	}

	for _, pattern := range globPatterns {
		matches, err := restoreGlob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if pool := parsePoolNameFromUnit(filepath.Base(match)); pool != "" {
				pools[pool] = struct{}{}
			}
		}
	}
}

func sortedPoolNames(pools map[string]struct{}) []string {
	var poolNames []string
	for pool := range pools {
		poolNames = append(poolNames, pool)
	}
	sort.Strings(poolNames)
	return poolNames
}

func parsePoolNameFromUnit(unitName string) string {
	switch {
	case strings.HasPrefix(unitName, "zfs-import@") && strings.HasSuffix(unitName, ".service"):
		pool := strings.TrimPrefix(unitName, "zfs-import@")
		return strings.TrimSuffix(pool, ".service")
	case strings.HasPrefix(unitName, "import@") && strings.HasSuffix(unitName, ".service"):
		pool := strings.TrimPrefix(unitName, "import@")
		return strings.TrimSuffix(pool, ".service")
	default:
		return ""
	}
}

func detectImportableZFSPools() ([]string, string, error) {
	output, err := restoreCmd.Run(context.Background(), "zpool", "import")
	poolNames := parseZpoolImportOutput(string(output))
	if err != nil {
		return poolNames, string(output), err
	}
	return poolNames, string(output), nil
}

func parseZpoolImportOutput(output string) []string {
	var pools []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(strings.ToLower(line), "pool:") {
			pool := strings.TrimSpace(line[len("pool:"):])
			if pool != "" {
				pools = append(pools, pool)
			}
		}
	}
	return pools
}

func combinePoolNames(a, b []string) []string {
	merged := make(map[string]struct{})
	for _, pool := range a {
		merged[pool] = struct{}{}
	}
	for _, pool := range b {
		merged[pool] = struct{}{}
	}

	if len(merged) == 0 {
		return nil
	}

	names := make([]string, 0, len(merged))
	for pool := range merged {
		names = append(names, pool)
	}
	sort.Strings(names)
	return names
}
