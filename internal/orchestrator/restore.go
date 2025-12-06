package orchestrator

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

var ErrRestoreAborted = errors.New("restore workflow aborted by user")

var (
	serviceStopTimeout        = 45 * time.Second
	serviceStartTimeout       = 30 * time.Second
	serviceVerifyTimeout      = 30 * time.Second
	serviceStatusCheckTimeout = 5 * time.Second
	servicePollInterval       = 500 * time.Millisecond
	serviceRetryDelay         = 500 * time.Millisecond
)

func RunRestoreWorkflow(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string) error {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}

	reader := bufio.NewReader(os.Stdin)
	candidate, prepared, err := prepareDecryptedBackup(ctx, reader, cfg, logger, version, false)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	destRoot := "/"
	logger.Info("Restore target: system root (/) — files will be written back to their original paths")

	// Detect system type
	systemType := restoreSystem.DetectCurrentSystem()
	logger.Info("Detected system type: %s", GetSystemTypeString(systemType))

	// Validate compatibility
	if err := ValidateCompatibility(candidate.Manifest); err != nil {
		logger.Warning("Compatibility check: %v", err)
		fmt.Println()
		fmt.Printf("⚠ %v\n", err)
		fmt.Println()
		fmt.Print("Do you want to continue anyway? This may cause system instability. (yes/no): ")

		response, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(response)) != "yes" {
			return fmt.Errorf("restore aborted due to incompatibility")
		}
	}

	// Analyze available categories in the backup
	logger.Info("Analyzing backup contents...")
	availableCategories, err := AnalyzeBackupCategories(prepared.ArchivePath, logger)
	if err != nil {
		logger.Warning("Could not analyze categories: %v", err)
		logger.Info("Falling back to full restore mode")
		return runFullRestore(ctx, reader, candidate, prepared, destRoot, logger)
	}

	// Show restore mode selection menu
	mode, err := restorePrompter.SelectRestoreMode(logger, systemType)
	if err != nil {
		if err.Error() == "user cancelled" {
			return ErrRestoreAborted
		}
		return err
	}

	// Determine selected categories based on mode
	var selectedCategories []Category
	if mode == RestoreModeCustom {
		// Interactive category selection
		selectedCategories, err = restorePrompter.SelectCategories(logger, availableCategories, systemType)
		if err != nil {
			if err.Error() == "user cancelled" {
				return ErrRestoreAborted
			}
			return err
		}
	} else {
		// Pre-defined mode (Full, Storage, Base)
		selectedCategories = GetCategoriesForMode(mode, systemType, availableCategories)
	}

	plan := PlanRestore(candidate.Manifest, selectedCategories, systemType, mode)

	// Cluster safety prompt: if backup proviene da cluster e vogliamo ripristinare pve_cluster, chiedi come procedere.
	clusterBackup := strings.EqualFold(strings.TrimSpace(candidate.Manifest.ClusterMode), "cluster")
	if plan.NeedsClusterRestore && clusterBackup {
		logger.Info("Backup marked as cluster node; enabling guarded restore options for pve_cluster")
		choice, promptErr := promptClusterRestoreMode(ctx, reader)
		if promptErr != nil {
			return promptErr
		}
		if choice == 0 {
			return ErrRestoreAborted
		}
		if choice == 1 {
			plan.ApplyClusterSafeMode(true)
			logger.Info("Selected SAFE cluster restore: /var/lib/pve-cluster will be exported only, not written to system")
		} else {
			plan.ApplyClusterSafeMode(false)
			logger.Warning("Selected RECOVERY cluster restore: full cluster database will be restored; ensure other nodes are isolated")
		}
	}

	// Create restore configuration
	restoreConfig := &SelectiveRestoreConfig{
		Mode:       mode,
		SystemType: systemType,
		Metadata:   candidate.Manifest,
	}
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.NormalCategories...)
	restoreConfig.SelectedCategories = append(restoreConfig.SelectedCategories, plan.ExportCategories...)

	// Show detailed restore plan
	ShowRestorePlan(logger, restoreConfig)

	// Confirm operation
	confirmed, err := restorePrompter.ConfirmRestore(logger)
	if err != nil {
		return err
	}
	if !confirmed {
		logger.Info("Restore operation cancelled by user")
		return ErrRestoreAborted
	}

	// Create safety backup of current configuration (only for categories that will write to system paths)
	var safetyBackup *SafetyBackupResult
	if len(plan.NormalCategories) > 0 {
		logger.Info("")
		safetyBackup, err = CreateSafetyBackup(logger, plan.NormalCategories, destRoot)
		if err != nil {
			logger.Warning("Failed to create safety backup: %v", err)
			fmt.Println()
			fmt.Print("Continue without safety backup? (yes/no): ")
			response, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(response)) != "yes" {
				return fmt.Errorf("restore aborted: safety backup failed")
			}
		} else {
			logger.Info("Safety backup location: %s", safetyBackup.BackupPath)
			logger.Info("You can restore from this backup if needed using: tar -xzf %s -C /", safetyBackup.BackupPath)
		}
	}

	// If we are restoring cluster database, stop PVE services and unmount /etc/pve before writing
	needsClusterRestore := plan.NeedsClusterRestore
	clusterServicesStopped := false
	pbsServicesStopped := false
	needsPBSServices := plan.NeedsPBSServices
	if needsClusterRestore {
		logger.Info("")
		logger.Info("Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve")
		if err := stopPVEClusterServices(ctx, logger); err != nil {
			return err
		}
		clusterServicesStopped = true
		defer func() {
			if err := startPVEClusterServices(ctx, logger); err != nil {
				logger.Warning("Failed to restart PVE services after restore: %v", err)
			}
		}()

		if err := unmountEtcPVE(ctx, logger); err != nil {
			logger.Warning("Could not unmount /etc/pve: %v", err)
		}
	}

	// For PBS restores, stop PBS services before applying configuration/datastore changes if relevant categories are selected
	if needsPBSServices {
		logger.Info("")
		logger.Info("Preparing PBS system for restore: stopping proxmox-backup services")
		if err := stopPBSServices(ctx, logger); err != nil {
			logger.Warning("Unable to stop PBS services automatically: %v", err)
			fmt.Println()
			fmt.Println("⚠ PBS services are still running. Continuing restore may leave proxmox-backup processes active.")
			logger.Info("Continuing restore without stopping PBS services")
		} else {
			pbsServicesStopped = true
			defer func() {
				if err := startPBSServices(ctx, logger); err != nil {
					logger.Warning("Failed to restart PBS services after restore: %v", err)
				}
			}()
		}
	}

	// Perform selective extraction for normal categories
	var detailedLogPath string
	if len(plan.NormalCategories) > 0 {
		logger.Info("")
		detailedLogPath, err = extractSelectiveArchive(ctx, prepared.ArchivePath, destRoot, plan.NormalCategories, mode, logger)
		if err != nil {
			logger.Error("Restore failed: %v", err)
			if safetyBackup != nil {
				logger.Info("You can rollback using the safety backup at: %s", safetyBackup.BackupPath)
			}
			return err
		}
	} else {
		logger.Info("")
		logger.Info("No system-path categories selected for restore (only export categories will be processed).")
	}

	// Handle export-only categories (/etc/pve) by extracting them to a separate directory
	exportLogPath := ""
	exportRoot := ""
	if len(plan.ExportCategories) > 0 {
		exportRoot = exportDestRoot(cfg.BaseDir)
		logger.Info("")
		logger.Info("Exporting /etc/pve contents to: %s", exportRoot)
		if err := restoreFS.MkdirAll(exportRoot, 0o755); err != nil {
			return fmt.Errorf("failed to create export directory %s: %w", exportRoot, err)
		}

		if exportLog, err := extractSelectiveArchive(ctx, prepared.ArchivePath, exportRoot, plan.ExportCategories, RestoreModeCustom, logger); err != nil {
			logger.Warning("Export of /etc/pve contents completed with errors: %v", err)
		} else {
			exportLogPath = exportLog
		}
	}

	// SAFE cluster mode: offer applying configs via pvesh without touching config.db
	if plan.ClusterSafeMode {
		if exportRoot == "" {
			logger.Warning("Cluster SAFE mode selected but export directory not available; skipping automatic pvesh apply")
		} else if err := runSafeClusterApply(ctx, reader, exportRoot, logger); err != nil {
			logger.Warning("Cluster SAFE apply completed with errors: %v", err)
		}
	}

	// Recreate directory structures from configuration files if relevant categories were restored
	logger.Info("")
	if shouldRecreateDirectories(systemType, plan.NormalCategories) {
		if err := RecreateDirectoriesFromConfig(systemType, logger); err != nil {
			logger.Warning("Failed to recreate directory structures: %v", err)
			logger.Warning("You may need to manually create storage/datastore directories")
		}
	} else {
		logger.Debug("Skipping datastore/storage directory recreation (category not selected)")
	}

	logger.Info("")
	logger.Info("Restore completed successfully.")
	logger.Info("Temporary decrypted bundle removed.")

	if detailedLogPath != "" {
		logger.Info("Detailed restore log: %s", detailedLogPath)
	}
	if exportLogPath != "" {
		logger.Info("Exported /etc/pve files are available at: %s", exportLogPath)
	}

	if safetyBackup != nil {
		logger.Info("Safety backup preserved at: %s", safetyBackup.BackupPath)
		logger.Info("Remove it manually if restore was successful: rm %s", safetyBackup.BackupPath)
	}

	logger.Info("")
	logger.Info("IMPORTANT: You may need to restart services for changes to take effect.")
	if systemType == SystemTypePVE {
		if needsClusterRestore && clusterServicesStopped {
			logger.Info("  PVE services were stopped/restarted during restore; verify status with: pvecm status")
		} else {
			logger.Info("  PVE services: systemctl restart pve-cluster pvedaemon pveproxy")
		}
	} else if systemType == SystemTypePBS {
		if pbsServicesStopped {
			logger.Info("  PBS services were stopped/restarted during restore; verify status with: systemctl status proxmox-backup proxmox-backup-proxy")
		} else {
			logger.Info("  PBS services: systemctl restart proxmox-backup-proxy proxmox-backup")
		}

		// Check ZFS pool status for PBS systems only when ZFS category was restored
		if hasCategoryID(plan.NormalCategories, "zfs") {
			logger.Info("")
			if err := checkZFSPoolsAfterRestore(logger); err != nil {
				logger.Warning("ZFS pool check: %v", err)
			}
		} else {
			logger.Debug("Skipping ZFS pool verification (ZFS category not selected)")
		}
	}

	logger.Info("")
	logger.Warning("⚠ SYSTEM REBOOT RECOMMENDED")
	logger.Info("Reboot the node (or at least restart networking and system services) to ensure all restored configurations take effect cleanly.")

	return nil
}

// checkZFSPoolsAfterRestore checks if ZFS pools need to be imported after restore
func checkZFSPoolsAfterRestore(logger *logging.Logger) error {
	if _, err := restoreCmd.Run(context.Background(), "which", "zpool"); err != nil {
		// zpool utility not available -> no ZFS tooling installed
		return nil
	}

	logger.Info("Checking ZFS pool status...")

	configuredPools := detectConfiguredZFSPools()
	importablePools, importOutput, importErr := detectImportableZFSPools()

	if len(configuredPools) > 0 {
		logger.Warning("Found %d ZFS pool(s) configured for automatic import:", len(configuredPools))
		for _, pool := range configuredPools {
			logger.Warning("  - %s", pool)
		}
		logger.Info("")
	}

	if importErr != nil {
		logger.Warning("`zpool import` command returned an error: %v", importErr)
		if strings.TrimSpace(importOutput) != "" {
			logger.Warning("`zpool import` output:\n%s", importOutput)
		}
	} else if len(importablePools) > 0 {
		logger.Warning("`zpool import` reports pools waiting to be imported:")
		for _, pool := range importablePools {
			logger.Warning("  - %s", pool)
		}
		logger.Info("")
	}

	if len(importablePools) == 0 {
		logger.Info("`zpool import` did not report pools waiting for import.")

		if len(configuredPools) > 0 {
			logger.Info("")
			for _, pool := range configuredPools {
				if _, err := restoreCmd.Run(context.Background(), "zpool", "status", pool); err == nil {
					logger.Info("Pool %s is already imported (no manual action needed)", pool)
				} else {
					logger.Warning("Systemd expects pool %s, but `zpool import` and `zpool status` did not report it. Check disk visibility and pool status.", pool)
				}
			}
		}
		return nil
	}

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

	return nil
}

func stopPVEClusterServices(ctx context.Context, logger *logging.Logger) error {
	services := []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"}
	for _, service := range services {
		if err := stopServiceWithRetries(ctx, logger, service); err != nil {
			return fmt.Errorf("failed to stop PVE services (%s): %w", service, err)
		}
	}
	return nil
}

func startPVEClusterServices(ctx context.Context, logger *logging.Logger) error {
	services := []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"}
	for _, service := range services {
		if err := startServiceWithRetries(ctx, logger, service); err != nil {
			return fmt.Errorf("failed to start PVE services (%s): %w", service, err)
		}
	}
	return nil
}

func stopPBSServices(ctx context.Context, logger *logging.Logger) error {
	if _, err := restoreCmd.Run(ctx, "which", "systemctl"); err != nil {
		return fmt.Errorf("systemctl not available: %w", err)
	}
	services := []string{"proxmox-backup-proxy", "proxmox-backup"}
	var failures []string
	for _, service := range services {
		if err := stopServiceWithRetries(ctx, logger, service); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", service, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func startPBSServices(ctx context.Context, logger *logging.Logger) error {
	if _, err := restoreCmd.Run(ctx, "which", "systemctl"); err != nil {
		return fmt.Errorf("systemctl not available: %w", err)
	}
	services := []string{"proxmox-backup", "proxmox-backup-proxy"}
	var failures []string
	for _, service := range services {
		if err := startServiceWithRetries(ctx, logger, service); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", service, err))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func unmountEtcPVE(ctx context.Context, logger *logging.Logger) error {
	output, err := restoreCmd.Run(ctx, "umount", "/etc/pve")
	msg := strings.TrimSpace(string(output))
	if err != nil {
		if strings.Contains(msg, "not mounted") {
			logger.Info("Skipping umount /etc/pve (already unmounted)")
			return nil
		}
		if msg != "" {
			return fmt.Errorf("umount /etc/pve failed: %s", msg)
		}
		return fmt.Errorf("umount /etc/pve failed: %w", err)
	}
	if msg != "" {
		logger.Debug("umount /etc/pve output: %s", msg)
	}
	return nil
}

func runCommandWithTimeout(ctx context.Context, logger *logging.Logger, timeout time.Duration, name string, args ...string) error {
	return execCommand(ctx, logger, timeout, name, args...)
}

func execCommand(ctx context.Context, logger *logging.Logger, timeout time.Duration, name string, args ...string) error {
	execCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	output, err := restoreCmd.Run(execCtx, name, args...)
	msg := strings.TrimSpace(string(output))
	if err != nil {
		if timeout > 0 && (errors.Is(execCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("%s %s timed out after %s", name, strings.Join(args, " "), timeout)
		}
		if msg != "" {
			return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), msg)
		}
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	if msg != "" && logger != nil {
		logger.Debug("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return nil
}

func stopServiceWithRetries(ctx context.Context, logger *logging.Logger, service string) error {
	attempts := []struct {
		description string
		args        []string
	}{
		{"stop (no-block)", []string{"stop", "--no-block", service}},
		{"stop (blocking)", []string{"stop", service}},
		{"aggressive stop", []string{"kill", "--signal=SIGTERM", "--kill-who=all", service}},
		{"force kill", []string{"kill", "--signal=SIGKILL", "--kill-who=all", service}},
	}

	var lastErr error
	for i, attempt := range attempts {
		if i > 0 {
			if err := sleepWithContext(ctx, serviceRetryDelay); err != nil {
				return err
			}
		}

		if logger != nil {
			logger.Debug("Attempting %s for %s (%d/%d)", attempt.description, service, i+1, len(attempts))
		}

		if err := runCommandWithTimeout(ctx, logger, serviceStopTimeout, "systemctl", attempt.args...); err != nil {
			lastErr = err
			continue
		}
		if err := waitForServiceInactive(ctx, logger, service, serviceVerifyTimeout); err != nil {
			lastErr = err
			continue
		}
		resetFailedService(ctx, logger, service)
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unable to stop %s", service)
	}
	return lastErr
}

func startServiceWithRetries(ctx context.Context, logger *logging.Logger, service string) error {
	attempts := []struct {
		description string
		args        []string
	}{
		{"start", []string{"start", service}},
		{"retry start", []string{"start", service}},
		{"aggressive restart", []string{"restart", service}},
	}

	var lastErr error
	for i, attempt := range attempts {
		if i > 0 {
			if err := sleepWithContext(ctx, serviceRetryDelay); err != nil {
				return err
			}
		}

		if logger != nil {
			logger.Debug("Attempting %s for %s (%d/%d)", attempt.description, service, i+1, len(attempts))
		}

		if err := runCommandWithTimeout(ctx, logger, serviceStartTimeout, "systemctl", attempt.args...); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unable to start %s", service)
	}
	return lastErr
}

func waitForServiceInactive(ctx context.Context, logger *logging.Logger, service string, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("%s still active after %s", service, timeout)
		}

		checkTimeout := minDuration(remaining, serviceStatusCheckTimeout)
		active, err := isServiceActive(ctx, service, checkTimeout)
		if err != nil {
			return err
		}
		if !active {
			if logger != nil {
				logger.Debug("%s stopped successfully", service)
			}
			return nil
		}

		wait := minDuration(remaining, servicePollInterval)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func resetFailedService(ctx context.Context, logger *logging.Logger, service string) {
	resetCtx, cancel := context.WithTimeout(ctx, serviceStatusCheckTimeout)
	defer cancel()

	if _, err := restoreCmd.Run(resetCtx, "systemctl", "reset-failed", service); err != nil {
		if logger != nil {
			logger.Debug("systemctl reset-failed %s ignored: %v", service, err)
		}
	}
}

func isServiceActive(ctx context.Context, service string, timeout time.Duration) (bool, error) {
	if timeout <= 0 {
		timeout = serviceStatusCheckTimeout
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := restoreCmd.Run(checkCtx, "systemctl", "is-active", service)
	msg := strings.TrimSpace(string(output))
	if err == nil {
		return true, nil
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return false, fmt.Errorf("systemctl is-active %s timed out after %s", service, timeout)
	}
	if msg == "" {
		msg = err.Error()
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "deactivating") || strings.Contains(lower, "activating") {
		return true, nil
	}
	if strings.Contains(lower, "inactive") || strings.Contains(lower, "failed") || strings.Contains(lower, "dead") {
		return false, nil
	}
	return false, fmt.Errorf("systemctl is-active %s failed: %s", service, msg)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func detectConfiguredZFSPools() []string {
	pools := make(map[string]struct{})

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

	globPatterns := []string{
		"/etc/systemd/system/zfs-import@*.service",
		"/etc/systemd/system/import@*.service",
	}

	for _, pattern := range globPatterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if pool := parsePoolNameFromUnit(filepath.Base(match)); pool != "" {
				pools[pool] = struct{}{}
			}
		}
	}

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

func shouldRecreateDirectories(systemType SystemType, categories []Category) bool {
	switch systemType {
	case SystemTypePVE:
		return hasCategoryID(categories, "storage_pve")
	case SystemTypePBS:
		return hasCategoryID(categories, "datastore_pbs")
	default:
		return false
	}
}

func hasCategoryID(categories []Category, id string) bool {
	for _, cat := range categories {
		if cat.ID == id {
			return true
		}
	}
	return false
}

// shouldStopPBSServices reports whether any selected categories belong to PBS-specific configuration
// and therefore require stopping PBS services before restore.
func shouldStopPBSServices(categories []Category) bool {
	for _, cat := range categories {
		if cat.Type == CategoryTypePBS {
			return true
		}
	}
	return false
}

func splitExportCategories(categories []Category) (normal []Category, export []Category) {
	for _, cat := range categories {
		if cat.ExportOnly {
			export = append(export, cat)
			continue
		}
		normal = append(normal, cat)
	}
	return normal, export
}

// redirectClusterCategoryToExport removes pve_cluster from normal categories and adds it to export-only list.
func redirectClusterCategoryToExport(normal []Category, export []Category) ([]Category, []Category) {
	filtered := make([]Category, 0, len(normal))
	for _, cat := range normal {
		if cat.ID == "pve_cluster" {
			export = append(export, cat)
			continue
		}
		filtered = append(filtered, cat)
	}
	return filtered, export
}

func exportDestRoot(baseDir string) string {
	base := strings.TrimSpace(baseDir)
	if base == "" {
		base = "/opt/proxsave"
	}
	return filepath.Join(base, fmt.Sprintf("pve-config-export-%s", nowRestore().Format("20060102-150405")))
}

// runFullRestore performs a full restore without selective options (fallback)
func runFullRestore(ctx context.Context, reader *bufio.Reader, candidate *decryptCandidate, prepared *preparedBundle, destRoot string, logger *logging.Logger) error {
	if err := confirmRestoreAction(ctx, reader, candidate, destRoot); err != nil {
		return err
	}

	if err := extractPlainArchive(ctx, prepared.ArchivePath, destRoot, logger); err != nil {
		return err
	}

	logger.Info("Restore completed successfully.")
	return nil
}

func confirmRestoreAction(ctx context.Context, reader *bufio.Reader, cand *decryptCandidate, dest string) error {
	manifest := cand.Manifest
	fmt.Println()
	fmt.Printf("Selected backup: %s (%s)\n", cand.DisplayBase, manifest.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Println("Restore destination: / (system root; original paths will be preserved)")
	fmt.Println("WARNING: This operation will overwrite configuration files on this system.")
	fmt.Println("Type RESTORE to proceed or 0 to cancel.")

	for {
		fmt.Print("Confirmation: ")
		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return err
		}
		switch strings.TrimSpace(input) {
		case "RESTORE":
			return nil
		case "0":
			return ErrRestoreAborted
		default:
			fmt.Println("Please type RESTORE to confirm or 0 to cancel.")
		}
	}
}

func extractPlainArchive(ctx context.Context, archivePath, destRoot string, logger *logging.Logger) error {
	if err := restoreFS.MkdirAll(destRoot, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	if destRoot == "/" && os.Geteuid() != 0 {
		return fmt.Errorf("restore to %s requires root privileges", destRoot)
	}

	logger.Info("Extracting archive %s into %s", filepath.Base(archivePath), destRoot)

	// Use native Go extraction to preserve atime/ctime from PAX headers
	if err := extractArchiveNative(ctx, archivePath, destRoot, logger, nil, RestoreModeFull, nil, ""); err != nil {
		return fmt.Errorf("archive extraction failed: %w", err)
	}

	return nil
}

// runSafeClusterApply applies selected cluster configs via pvesh without touching config.db.
// It operates on files extracted to exportRoot (e.g. exportDestRoot).
func runSafeClusterApply(ctx context.Context, reader *bufio.Reader, exportRoot string, logger *logging.Logger) error {
	if _, err := exec.LookPath("pvesh"); err != nil {
		logger.Warning("pvesh not found in PATH; skipping SAFE cluster apply")
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	currentNode, _ := os.Hostname()
	currentNode = shortHost(currentNode)

	logger.Info("")
	logger.Info("SAFE cluster restore: applying configs via pvesh (node=%s)", currentNode)

	vmEntries, vmErr := scanVMConfigs(exportRoot, currentNode)
	if vmErr != nil {
		logger.Warning("Failed to scan VM configs: %v", vmErr)
	}
	if len(vmEntries) > 0 {
		fmt.Println()
		fmt.Printf("Found %d VM/CT configs for node %s\n", len(vmEntries), currentNode)
		applyVMs, err := promptYesNo(ctx, reader, "Apply all VM/CT configs via pvesh?")
		if err != nil {
			return err
		}
		if applyVMs {
			applied, failed := applyVMConfigs(ctx, vmEntries, logger)
			logger.Info("VM/CT apply completed: ok=%d failed=%d", applied, failed)
		} else {
			logger.Info("Skipping VM/CT apply")
		}
	} else {
		logger.Info("No VM/CT configs found for node %s in export", currentNode)
	}

	// Storage configuration
	storageCfg := filepath.Join(exportRoot, "etc/pve/storage.cfg")
	if info, err := restoreFS.Stat(storageCfg); err == nil && !info.IsDir() {
		fmt.Println()
		fmt.Printf("Storage configuration found: %s\n", storageCfg)
		applyStorage, err := promptYesNo(ctx, reader, "Apply storage.cfg via pvesh?")
		if err != nil {
			return err
		}
		if applyStorage {
			applied, failed, err := applyStorageCfg(ctx, storageCfg, logger)
			if err != nil {
				logger.Warning("Storage apply encountered errors: %v", err)
			}
			logger.Info("Storage apply completed: ok=%d failed=%d", applied, failed)
		} else {
			logger.Info("Skipping storage.cfg apply")
		}
	} else {
		logger.Info("No storage.cfg found in export")
	}

	// Datacenter configuration
	dcCfg := filepath.Join(exportRoot, "etc/pve/datacenter.cfg")
	if info, err := restoreFS.Stat(dcCfg); err == nil && !info.IsDir() {
		fmt.Println()
		fmt.Printf("Datacenter configuration found: %s\n", dcCfg)
		applyDC, err := promptYesNo(ctx, reader, "Apply datacenter.cfg via pvesh?")
		if err != nil {
			return err
		}
		if applyDC {
			if err := runPvesh(ctx, logger, []string{"set", "/cluster/config", "-conf", dcCfg}); err != nil {
				logger.Warning("Failed to apply datacenter.cfg: %v", err)
			} else {
				logger.Info("datacenter.cfg applied successfully")
			}
		} else {
			logger.Info("Skipping datacenter.cfg apply")
		}
	} else {
		logger.Info("No datacenter.cfg found in export")
	}

	return nil
}

type vmEntry struct {
	VMID string
	Kind string // qemu | lxc
	Name string
	Path string
}

func scanVMConfigs(exportRoot, node string) ([]vmEntry, error) {
	var entries []vmEntry
	base := filepath.Join(exportRoot, "etc/pve/nodes", node)

	type dirSpec struct {
		kind string
		path string
	}

	dirs := []dirSpec{
		{kind: "qemu", path: filepath.Join(base, "qemu-server")},
		{kind: "lxc", path: filepath.Join(base, "lxc")},
	}

	for _, spec := range dirs {
		infos, err := restoreFS.ReadDir(spec.path)
		if err != nil {
			continue
		}
		for _, entry := range infos {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".conf") {
				continue
			}
			vmid := strings.TrimSuffix(name, ".conf")
			vmPath := filepath.Join(spec.path, name)
			vmName := readVMName(vmPath)
			entries = append(entries, vmEntry{
				VMID: vmid,
				Kind: spec.kind,
				Name: vmName,
				Path: vmPath,
			})
		}
	}

	return entries, nil
}

func readVMName(confPath string) string {
	data, err := restoreFS.ReadFile(confPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "name:"))
		}
		if strings.HasPrefix(t, "hostname:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "hostname:"))
		}
	}
	return ""
}

func applyVMConfigs(ctx context.Context, entries []vmEntry, logger *logging.Logger) (applied, failed int) {
	for _, vm := range entries {
		if err := ctx.Err(); err != nil {
			logger.Warning("VM apply aborted: %v", err)
			return applied, failed
		}
		target := fmt.Sprintf("/nodes/%s/%s/%s/config", detectNodeForVM(vm), vm.Kind, vm.VMID)
		args := []string{"set", target, "--filename", vm.Path}
		if err := runPvesh(ctx, logger, args); err != nil {
			logger.Warning("Failed to apply %s (vmid=%s kind=%s): %v", target, vm.VMID, vm.Kind, err)
			failed++
		} else {
			display := vm.VMID
			if vm.Name != "" {
				display = fmt.Sprintf("%s (%s)", vm.VMID, vm.Name)
			}
			logger.Info("Applied VM/CT config %s", display)
			applied++
		}
	}
	return applied, failed
}

func detectNodeForVM(vm vmEntry) string {
	host, _ := os.Hostname()
	host = shortHost(host)
	if host != "" {
		return host
	}
	return "localhost"
}

type storageBlock struct {
	ID   string
	data []string
}

func applyStorageCfg(ctx context.Context, cfgPath string, logger *logging.Logger) (applied, failed int, err error) {
	blocks, perr := parseStorageBlocks(cfgPath)
	if perr != nil {
		return 0, 0, perr
	}
	if len(blocks) == 0 {
		logger.Info("No storage definitions detected in storage.cfg")
		return 0, 0, nil
	}

	for _, blk := range blocks {
		tmp, tmpErr := restoreFS.CreateTemp("", fmt.Sprintf("pve-storage-%s-*.cfg", sanitizeID(blk.ID)))
		if tmpErr != nil {
			failed++
			continue
		}
		tmpName := tmp.Name()
		if _, werr := tmp.WriteString(strings.Join(blk.data, "\n") + "\n"); werr != nil {
			_ = tmp.Close()
			_ = restoreFS.Remove(tmpName)
			failed++
			continue
		}
		_ = tmp.Close()

		args := []string{"set", fmt.Sprintf("/cluster/storage/%s", blk.ID), "-conf", tmpName}
		if runErr := runPvesh(ctx, logger, args); runErr != nil {
			logger.Warning("Failed to apply storage %s: %v", blk.ID, runErr)
			failed++
		} else {
			logger.Info("Applied storage definition %s", blk.ID)
			applied++
		}

		_ = restoreFS.Remove(tmpName)

		if err := ctx.Err(); err != nil {
			return applied, failed, err
		}
	}

	return applied, failed, nil
}

func parseStorageBlocks(cfgPath string) ([]storageBlock, error) {
	data, err := restoreFS.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}

	var blocks []storageBlock
	var current *storageBlock

	flush := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "storage:") {
			flush()
			id := strings.TrimSpace(strings.TrimPrefix(trimmed, "storage:"))
			current = &storageBlock{ID: id, data: []string{line}}
			continue
		}
		if current != nil {
			current.data = append(current.data, line)
		}
	}
	flush()

	return blocks, nil
}

func runPvesh(ctx context.Context, logger *logging.Logger, args []string) error {
	output, err := restoreCmd.Run(ctx, "pvesh", args...)
	if len(output) > 0 {
		logger.Debug("pvesh %v output: %s", args, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("pvesh %v failed: %w", args, err)
	}
	return nil
}

func shortHost(host string) string {
	if idx := strings.Index(host, "."); idx > 0 {
		return host[:idx]
	}
	return host
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// promptClusterRestoreMode asks how to handle cluster database restore (safe export vs full recovery).
func promptClusterRestoreMode(ctx context.Context, reader *bufio.Reader) (int, error) {
	fmt.Println()
	fmt.Println("Cluster backup detected. Choose how to restore the cluster database:")
	fmt.Println("  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).")
	fmt.Println("  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.")
	fmt.Println("  [0] Exit")

	for {
		fmt.Print("Choice: ")
		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return 0, err
		}
		switch strings.TrimSpace(input) {
		case "1":
			return 1, nil
		case "2":
			return 2, nil
		case "0":
			return 0, nil
		default:
			fmt.Println("Please enter 1, 2, or 0.")
		}
	}
}

// extractSelectiveArchive extracts only files matching selected categories
func extractSelectiveArchive(ctx context.Context, archivePath, destRoot string, categories []Category, mode RestoreMode, logger *logging.Logger) (string, error) {
	if err := restoreFS.MkdirAll(destRoot, 0o755); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}

	if destRoot == "/" && os.Geteuid() != 0 {
		return "", fmt.Errorf("restore to %s requires root privileges", destRoot)
	}

	// Create detailed log directory
	logDir := "/tmp/proxsave"
	if err := restoreFS.MkdirAll(logDir, 0755); err != nil {
		logger.Warning("Could not create log directory: %v", err)
	}

	// Create detailed log file
	timestamp := nowRestore().Format("20060102_150405")
	logPath := filepath.Join(logDir, fmt.Sprintf("restore_%s.log", timestamp))
	logFile, err := restoreFS.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		logger.Warning("Could not create detailed log file: %v", err)
		logFile = nil
	} else {
		defer logFile.Close()
		logger.Info("Detailed restore log: %s", logPath)
	}

	logger.Info("Extracting selected categories from archive %s into %s", filepath.Base(archivePath), destRoot)

	// Use native Go extraction with category filter
	if err := extractArchiveNative(ctx, archivePath, destRoot, logger, categories, mode, logFile, logPath); err != nil {
		return logPath, err
	}

	return logPath, nil
}

// extractArchiveNative extracts TAR archives natively in Go, preserving all timestamps
// If categories is nil, all files are extracted. Otherwise, only files matching the categories are extracted.
func extractArchiveNative(ctx context.Context, archivePath, destRoot string, logger *logging.Logger, categories []Category, mode RestoreMode, logFile *os.File, logFilePath string) error {
	// Open the archive file
	file, err := restoreFS.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	// Create decompression reader based on file extension
	reader, err := createDecompressionReader(ctx, file, archivePath)
	if err != nil {
		return fmt.Errorf("create decompression reader: %w", err)
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}

	// Create TAR reader
	tarReader := tar.NewReader(reader)

	// Write log header if log file is available
	if logFile != nil {
		fmt.Fprintf(logFile, "=== PROXMOX RESTORE LOG ===\n")
		fmt.Fprintf(logFile, "Date: %s\n", nowRestore().Format("2006-01-02 15:04:05"))
		fmt.Fprintf(logFile, "Mode: %s\n", getModeName(mode))
		if len(categories) > 0 {
			fmt.Fprintf(logFile, "Selected categories: %d categories\n", len(categories))
			for _, cat := range categories {
				fmt.Fprintf(logFile, "  - %s (%s)\n", cat.Name, cat.ID)
			}
		} else {
			fmt.Fprintf(logFile, "Selected categories: ALL (full restore)\n")
		}
		fmt.Fprintf(logFile, "Archive: %s\n", filepath.Base(archivePath))
		fmt.Fprintf(logFile, "\n")
	}

	// Extract files (selective or full)
	filesExtracted := 0
	filesSkipped := 0
	filesFailed := 0
	selectiveMode := len(categories) > 0

	var restoredTemp, skippedTemp *os.File
	if logFile != nil {
		if tmp, err := restoreFS.CreateTemp("", "restored_entries_*.log"); err == nil {
			restoredTemp = tmp
			defer func() {
				tmp.Close()
				_ = restoreFS.Remove(tmp.Name())
			}()
		} else {
			logger.Warning("Could not create temporary file for restored entries: %v", err)
		}

		if tmp, err := restoreFS.CreateTemp("", "skipped_entries_*.log"); err == nil {
			skippedTemp = tmp
			defer func() {
				tmp.Close()
				_ = restoreFS.Remove(tmp.Name())
			}()
		} else {
			logger.Warning("Could not create temporary file for skipped entries: %v", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}

		// Check if file should be extracted (selective mode)
		if selectiveMode {
			shouldExtract := false
			for _, cat := range categories {
				if PathMatchesCategory(header.Name, cat) {
					shouldExtract = true
					break
				}
			}

			if !shouldExtract {
				filesSkipped++
				if skippedTemp != nil {
					fmt.Fprintf(skippedTemp, "SKIPPED: %s (does not match any selected category)\n", header.Name)
				}
				continue
			}
		}

		if err := extractTarEntry(tarReader, header, destRoot, logger); err != nil {
			logger.Warning("Failed to extract %s: %v", header.Name, err)
			filesFailed++
			continue
		}

		filesExtracted++
		if restoredTemp != nil {
			fmt.Fprintf(restoredTemp, "RESTORED: %s\n", header.Name)
		}
		if filesExtracted%100 == 0 {
			logger.Debug("Extracted %d files...", filesExtracted)
		}
	}

	// Write detailed log
	if logFile != nil {
		fmt.Fprintf(logFile, "=== FILES RESTORED ===\n")
		if restoredTemp != nil {
			if _, err := restoredTemp.Seek(0, 0); err == nil {
				if _, err := io.Copy(logFile, restoredTemp); err != nil {
					logger.Warning("Could not write restored entries to log: %v", err)
				}
			}
		}
		fmt.Fprintf(logFile, "\n")

		fmt.Fprintf(logFile, "=== FILES SKIPPED ===\n")
		if skippedTemp != nil {
			if _, err := skippedTemp.Seek(0, 0); err == nil {
				if _, err := io.Copy(logFile, skippedTemp); err != nil {
					logger.Warning("Could not write skipped entries to log: %v", err)
				}
			}
		}
		fmt.Fprintf(logFile, "\n")

		fmt.Fprintf(logFile, "=== SUMMARY ===\n")
		fmt.Fprintf(logFile, "Total files extracted: %d\n", filesExtracted)
		fmt.Fprintf(logFile, "Total files skipped: %d\n", filesSkipped)
		fmt.Fprintf(logFile, "Total files in archive: %d\n", filesExtracted+filesSkipped)
	}

	if filesFailed == 0 {
		if selectiveMode {
			logger.Info("Successfully restored all %d configuration files/directories", filesExtracted)
		} else {
			logger.Info("Successfully restored all %d files/directories", filesExtracted)
		}
	} else {
		logger.Warning("Restored %d files/directories; %d item(s) failed (see detailed log)", filesExtracted, filesFailed)
	}

	if filesSkipped > 0 {
		logger.Info("%d additional archive entries (logs, diagnostics, system defaults) were left unchanged on this system; see detailed log for details", filesSkipped)
	}

	if logFilePath != "" {
		logger.Info("Detailed restore log: %s", logFilePath)
	}

	return nil
}

// createDecompressionReader creates appropriate decompression reader based on file extension
func createDecompressionReader(ctx context.Context, file *os.File, archivePath string) (io.Reader, error) {
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz"):
		return gzip.NewReader(file)
	case strings.HasSuffix(archivePath, ".tar.xz"):
		return createXZReader(ctx, file)
	case strings.HasSuffix(archivePath, ".tar.zst") || strings.HasSuffix(archivePath, ".tar.zstd"):
		return createZstdReader(ctx, file)
	case strings.HasSuffix(archivePath, ".tar.bz2"):
		return createBzip2Reader(ctx, file)
	case strings.HasSuffix(archivePath, ".tar.lzma"):
		return createLzmaReader(ctx, file)
	case strings.HasSuffix(archivePath, ".tar"):
		return file, nil
	default:
		return nil, fmt.Errorf("unsupported archive format: %s", filepath.Base(archivePath))
	}
}

// createXZReader creates an XZ decompression reader using injectable command runner
func createXZReader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "xz", file, "-d", "-c")
}

// createZstdReader creates a Zstd decompression reader using injectable command runner
func createZstdReader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "zstd", file, "-d", "-c")
}

// createBzip2Reader creates a Bzip2 decompression reader using injectable command runner
func createBzip2Reader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "bzip2", file, "-d", "-c")
}

// createLzmaReader creates an LZMA decompression reader using injectable command runner
func createLzmaReader(ctx context.Context, file *os.File) (io.Reader, error) {
	return runRestoreCommandStream(ctx, "lzma", file, "-d", "-c")
}

// runRestoreCommandStream starts a command that reads from stdin and exposes stdout as a ReadCloser.
// It prefers an injectable streaming runner when available; otherwise falls back to exec.CommandContext.
func runRestoreCommandStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.Reader, error) {
	type streamingRunner interface {
		RunStream(ctx context.Context, name string, stdin io.Reader, args ...string) (io.ReadCloser, error)
	}
	if sr, ok := restoreCmd.(streamingRunner); ok && sr != nil {
		return sr.RunStream(ctx, name, stdin, args...)
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create %s pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		stdout.Close()
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	return &waitReadCloser{ReadCloser: stdout, wait: cmd.Wait}, nil
}

func sanitizeRestoreEntryTarget(destRoot, entryName string) (string, string, error) {
	cleanDestRoot := filepath.Clean(destRoot)
	if cleanDestRoot == "" {
		cleanDestRoot = string(os.PathSeparator)
	}

	name := strings.TrimSpace(entryName)
	if name == "" {
		return "", "", fmt.Errorf("empty archive entry name")
	}

	sanitized := path.Clean(name)
	for strings.HasPrefix(sanitized, string(os.PathSeparator)) {
		sanitized = strings.TrimPrefix(sanitized, string(os.PathSeparator))
	}

	if sanitized == "" || sanitized == "." {
		return "", "", fmt.Errorf("invalid archive entry name: %q", entryName)
	}

	if sanitized == ".." || strings.HasPrefix(sanitized, "../") || strings.Contains(sanitized, "/../") {
		return "", "", fmt.Errorf("illegal path: %s", entryName)
	}

	target, err := resolveAndCheckPath(cleanDestRoot, sanitized)
	if err != nil {
		return "", "", fmt.Errorf("illegal path: %s: %w", entryName, err)
	}
	return target, cleanDestRoot, nil
}

// extractTarEntry extracts a single TAR entry, preserving all attributes including atime/ctime
func extractTarEntry(tarReader *tar.Reader, header *tar.Header, destRoot string, logger *logging.Logger) error {
	target, cleanDestRoot, err := sanitizeRestoreEntryTarget(destRoot, header.Name)
	if err != nil {
		return err
	}

	// Hard guard: never write directly into /etc/pve when restoring to system root
	if cleanDestRoot == string(os.PathSeparator) && strings.HasPrefix(target, "/etc/pve") {
		logger.Warning("Skipping restore to %s (writes to /etc/pve are prohibited)", target)
		return nil
	}

	// Create parent directories
	if err := restoreFS.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return extractDirectory(target, header, logger)
	case tar.TypeReg:
		return extractRegularFile(tarReader, target, header, logger)
	case tar.TypeSymlink:
		return extractSymlink(target, header, cleanDestRoot, logger)
	case tar.TypeLink:
		return extractHardlink(target, header, cleanDestRoot, logger)
	default:
		logger.Debug("Skipping unsupported file type %d: %s", header.Typeflag, header.Name)
		return nil
	}
}

// extractDirectory creates a directory with proper permissions and timestamps
func extractDirectory(target string, header *tar.Header, logger *logging.Logger) error {
	if err := restoreFS.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Set ownership
	if err := os.Chown(target, header.Uid, header.Gid); err != nil {
		logger.Debug("Failed to chown directory %s: %v", target, err)
	}

	// Set permissions explicitly
	if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
		return fmt.Errorf("chmod directory: %w", err)
	}

	// Set timestamps (mtime, atime)
	if err := setTimestamps(target, header); err != nil {
		logger.Debug("Failed to set timestamps on directory %s: %v", target, err)
	}

	return nil
}

// extractRegularFile extracts a regular file with content and timestamps
func extractRegularFile(tarReader *tar.Reader, target string, header *tar.Header, logger *logging.Logger) error {
	// Remove existing file if it exists
	_ = restoreFS.Remove(target)

	// Create the file
	outFile, err := restoreFS.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer outFile.Close()

	// Copy content
	if _, err := io.Copy(outFile, tarReader); err != nil {
		return fmt.Errorf("write file content: %w", err)
	}

	// Close before setting attributes
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}

	// Set ownership
	if err := os.Chown(target, header.Uid, header.Gid); err != nil {
		logger.Debug("Failed to chown file %s: %v", target, err)
	}

	// Set permissions explicitly
	if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
		return fmt.Errorf("chmod file: %w", err)
	}

	// Set timestamps (mtime, atime, ctime via syscall)
	if err := setTimestamps(target, header); err != nil {
		logger.Debug("Failed to set timestamps on file %s: %v", target, err)
	}

	return nil
}

// extractSymlink creates a symbolic link
func extractSymlink(target string, header *tar.Header, destRoot string, logger *logging.Logger) error {
	linkTarget := header.Linkname

	// Reject absolute symlink targets immediately
	if filepath.IsAbs(linkTarget) {
		return fmt.Errorf("absolute symlink target not allowed: %s", linkTarget)
	}

	// Pre-validation: ensure the resolved target would stay within destRoot before creating the symlink
	relativeTarget, err := filepath.Rel(destRoot, target)
	if err != nil {
		return fmt.Errorf("determine relative path for symlink %s: %w", target, err)
	}
	if strings.HasPrefix(relativeTarget, ".."+string(os.PathSeparator)) || relativeTarget == ".." {
		return fmt.Errorf("sanitized symlink path escapes root: %s", target)
	}

	symlinkArchivePath := path.Clean(filepath.ToSlash(relativeTarget))
	symlinkArchiveDir := path.Dir(symlinkArchivePath)
	if symlinkArchiveDir == "." {
		symlinkArchiveDir = ""
	}
	potentialTarget := path.Join(symlinkArchiveDir, linkTarget)
	potentialTarget = filepath.FromSlash(potentialTarget)

	if _, err := resolveAndCheckPath(destRoot, potentialTarget); err != nil {
		return fmt.Errorf("symlink target escapes root before creation: %s -> %s: %w", header.Name, linkTarget, err)
	}

	// Remove existing file/link if it exists
	_ = restoreFS.Remove(target)

	// Create symlink
	if err := restoreFS.Symlink(linkTarget, target); err != nil {
		return fmt.Errorf("create symlink: %w", err)
	}

	// POST-CREATION VALIDATION: Verify the created symlink's target stays within destRoot
	actualTarget, err := restoreFS.Readlink(target)
	if err != nil {
		restoreFS.Remove(target) // Clean up
		return fmt.Errorf("read created symlink %s: %w", target, err)
	}

	// Resolve the symlink target relative to the symlink's directory
	symlinkDir := filepath.Dir(target)
	resolvedTarget := filepath.Join(symlinkDir, actualTarget)

	// Validate the resolved target stays within destRoot using absolute paths
	absDestRoot, err := filepath.Abs(destRoot)
	if err != nil {
		restoreFS.Remove(target)
		return fmt.Errorf("resolve destination root: %w", err)
	}

	absResolvedTarget, err := filepath.Abs(resolvedTarget)
	if err != nil {
		restoreFS.Remove(target)
		return fmt.Errorf("resolve symlink target: %w", err)
	}

	// Check if resolved target is within destRoot
	rel, err := filepath.Rel(absDestRoot, absResolvedTarget)
	if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		restoreFS.Remove(target)
		return fmt.Errorf("symlink target escapes root after creation: %s -> %s (resolves to %s)",
			header.Name, linkTarget, absResolvedTarget)
	}

	// Set ownership (on the symlink itself, not the target)
	if err := os.Lchown(target, header.Uid, header.Gid); err != nil {
		logger.Debug("Failed to lchown symlink %s: %v", target, err)
	}

	// Note: timestamps on symlinks are not typically preserved
	return nil
}

// extractHardlink creates a hard link
func extractHardlink(target string, header *tar.Header, destRoot string, logger *logging.Logger) error {
	// Validate hard link target
	linkName := header.Linkname

	// Reject absolute hard link targets immediately
	if filepath.IsAbs(linkName) {
		return fmt.Errorf("absolute hardlink target not allowed: %s", linkName)
	}

	// Validate the hard link target stays within extraction root
	if _, err := resolveAndCheckPath(destRoot, linkName); err != nil {
		return fmt.Errorf("hardlink target escapes root: %s -> %s: %w", header.Name, linkName, err)
	}

	linkTarget := filepath.Join(destRoot, linkName)

	// Remove existing file/link if it exists
	_ = restoreFS.Remove(target)

	// Create hard link
	if err := restoreFS.Link(linkTarget, target); err != nil {
		return fmt.Errorf("create hardlink: %w", err)
	}

	return nil
}

// setTimestamps sets atime, mtime, and attempts to set ctime via syscall
func setTimestamps(target string, header *tar.Header) error {
	// Convert times to Unix format
	atime := header.AccessTime
	mtime := header.ModTime

	// Use syscall.UtimesNano to set atime and mtime with nanosecond precision
	times := []syscall.Timespec{
		{Sec: atime.Unix(), Nsec: int64(atime.Nanosecond())},
		{Sec: mtime.Unix(), Nsec: int64(mtime.Nanosecond())},
	}

	if err := syscall.UtimesNano(target, times); err != nil {
		return fmt.Errorf("set atime/mtime: %w", err)
	}

	// Note: ctime (change time) cannot be set directly by user-space programs
	// It is automatically updated by the kernel when file metadata changes
	// The header.ChangeTime is stored in PAX but cannot be restored

	return nil
}

// getModeName returns a human-readable name for the restore mode
func getModeName(mode RestoreMode) string {
	switch mode {
	case RestoreModeFull:
		return "FULL restore (all files)"
	case RestoreModeStorage:
		return "STORAGE/DATASTORE only"
	case RestoreModeBase:
		return "SYSTEM BASE only"
	case RestoreModeCustom:
		return "CUSTOM selection"
	default:
		return "Unknown mode"
	}
}
