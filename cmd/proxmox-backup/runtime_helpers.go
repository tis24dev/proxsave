package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/storage"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

type ExecInfo struct {
	ExecPath string
	ExecDir  string
	BaseDir  string
	HasBase  bool
}

var (
	execInfo     ExecInfo
	execInfoOnce sync.Once
)

func getExecInfo() ExecInfo {
	execInfoOnce.Do(func() {
		execInfo = detectExecInfo()
	})
	return execInfo
}

func detectExecInfo() ExecInfo {
	candidates := collectExecPathCandidates()

	execPath := ""
	seen := map[string]struct{}{}
	for _, cand := range candidates {
		clean := filepath.Clean(strings.TrimSpace(cand))
		if clean == "" {
			continue
		}

		if resolved, err := filepath.EvalSymlinks(clean); err == nil && strings.TrimSpace(resolved) != "" {
			clean = resolved
		}
		clean = filepath.Clean(clean)

		if _, dup := seen[clean]; dup {
			continue
		}
		seen[clean] = struct{}{}

		if info, err := os.Stat(clean); err == nil && info.Mode().IsRegular() {
			if info.Mode().Perm()&0o111 == 0 {
				logging.Debug("Skipping %s: lacks executable permissions", clean)
				continue
			}
			execPath = clean
			break
		}
	}

	if execPath == "" {
		warnExecPathMissing()
		return ExecInfo{}
	}

	execDir := filepath.Dir(execPath)
	dir := execDir
	originalDir := dir
	baseDir := ""

	for {
		if dir == "" || dir == "." || dir == string(filepath.Separator) {
			break
		}
		if info, err := os.Stat(filepath.Join(dir, "env")); err == nil && info.IsDir() {
			baseDir = dir
			break
		}
		if info, err := os.Stat(filepath.Join(dir, "script")); err == nil && info.IsDir() {
			baseDir = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if baseDir == "" {
		if parent := filepath.Dir(originalDir); parent != "" && parent != "." && parent != string(filepath.Separator) {
			baseDir = parent
		}
	}

	return ExecInfo{
		ExecPath: execPath,
		ExecDir:  execDir,
		BaseDir:  baseDir,
		HasBase:  baseDir != "",
	}
}

func detectBaseDir() (string, bool) {
	info := getExecInfo()
	return info.BaseDir, info.HasBase
}

func collectExecPathCandidates() []string {
	var candidates []string

	if path, err := os.Executable(); err == nil && strings.TrimSpace(path) != "" {
		candidates = append(candidates, path)
	} else if err != nil {
		logging.Debug("os.Executable failed: %v", err)
	}

	if resolved, err := filepath.EvalSymlinks("/proc/self/exe"); err == nil && strings.TrimSpace(resolved) != "" {
		candidates = append(candidates, resolved)
	} else if err != nil {
		logging.Debug("EvalSymlinks on /proc/self/exe failed: %v", err)
	}

	arg0 := strings.TrimSpace(os.Args[0])
	if arg0 != "" {
		if filepath.IsAbs(arg0) {
			candidates = append(candidates, arg0)
		} else if abs, err := filepath.Abs(arg0); err == nil {
			candidates = append(candidates, abs)
		} else {
			logging.Debug("Unable to convert argv[0] to absolute path: %v", err)
		}

		if found, err := exec.LookPath(arg0); err == nil && strings.TrimSpace(found) != "" {
			if abs, err := filepath.Abs(found); err == nil {
				candidates = append(candidates, abs)
			} else {
				candidates = append(candidates, found)
			}
		} else if err != nil {
			logging.Debug("exec.LookPath failed for %s: %v", arg0, err)
		}
	}

	return candidates
}

func warnExecPathMissing() {
	msg := "WARNING: Unable to determine proxmox-backup executable path; symlink and cron setup skipped"
	fmt.Fprintln(os.Stderr, msg)
	logging.Warning("%s", msg)
}

func checkInternetConnectivity(timeout time.Duration) error {
	type target struct{ network, addr string }
	targets := []target{
		{"tcp", "1.1.1.1:443"},
		{"tcp", "8.8.8.8:53"},
	}
	deadline := time.Now().Add(timeout)
	for _, t := range targets {
		d := net.Dialer{Timeout: time.Until(deadline)}
		if conn, err := d.Dial(t.network, t.addr); err == nil {
			_ = conn.Close()
			return nil
		}
	}
	return fmt.Errorf("no outbound connectivity (checked %d endpoints)", len(targets))
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

func logServerIdentityValues(serverID, mac string) {
	serverID = strings.TrimSpace(serverID)
	mac = strings.TrimSpace(mac)
	if serverID != "" {
		logging.Info("Server ID: %s", serverID)
	}
	if mac != "" {
		logging.Info("Server MAC Address: %s", mac)
	}
}

func resolveHostname() string {
	if path, err := exec.LookPath("hostname"); err == nil {
		if out, err := exec.Command(path, "-f").Output(); err == nil {
			if fqdn := strings.TrimSpace(string(out)); fqdn != "" {
				return fqdn
			}
		}
	}

	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return "unknown"
	}
	return host
}

func validateFutureFeatures(cfg *config.Config) error {
	if cfg.SecondaryEnabled && cfg.SecondaryPath == "" {
		return fmt.Errorf("secondary backup enabled but SECONDARY_PATH is empty")
	}
	if cfg.CloudEnabled && cfg.CloudRemote == "" {
		logging.Warning("Cloud backup enabled but CLOUD_REMOTE is empty – disabling cloud storage for this run")
		cfg.CloudEnabled = false
		cfg.CloudRemote = ""
		cfg.CloudLogPath = ""
	}
	if cfg.TelegramEnabled && cfg.TelegramBotType == "personal" {
		if cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
			return fmt.Errorf("telegram personal mode enabled but TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID missing")
		}
	}
	if cfg.MetricsEnabled && cfg.MetricsPath == "" {
		return fmt.Errorf("metrics enabled but METRICS_PATH is empty")
	}
	return nil
}

func detectFilesystemInfo(ctx context.Context, backend storage.Storage, path string, logger *logging.Logger) (*storage.FilesystemInfo, error) {
	if backend == nil || !backend.IsEnabled() {
		return nil, nil
	}

	fsInfo, err := backend.DetectFilesystem(ctx)
	if err != nil {
		if backend.IsCritical() {
			return nil, err
		}
		logger.Debug("WARNING: %s filesystem detection failed: %v", backend.Name(), err)
		return nil, nil
	}

	if !fsInfo.SupportsOwnership {
		logger.Warning("%s [%s] does not support ownership changes; chown/chmod will be skipped", path, fsInfo.Type)
	}

	return fsInfo, nil
}

func formatStorageLabel(path string, info *storage.FilesystemInfo) string {
	fsType := "unknown"
	if info != nil && info.Type != "" {
		fsType = string(info.Type)
	}
	return fmt.Sprintf("%s [%s]", path, fsType)
}

func formatDetailedFilesystemLabel(path string, info *storage.FilesystemInfo) string {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return "disabled"
	}
	if info == nil {
		return fmt.Sprintf("%s -> Filesystem: unknown (detection unavailable)", cleanPath)
	}

	ownership := "no ownership"
	if info.SupportsOwnership {
		ownership = "supports ownership"
	}

	network := ""
	if info.IsNetworkFS {
		network = " [network]"
	}

	mount := info.MountPoint
	if mount == "" {
		mount = "unknown"
	}

	return fmt.Sprintf("%s -> Filesystem: %s (%s)%s [mount: %s]",
		cleanPath,
		info.Type,
		ownership,
		network,
		mount,
	)
}

func fetchStorageStats(ctx context.Context, backend storage.Storage, logger *logging.Logger, label string) *storage.StorageStats {
	if ctx.Err() != nil || backend == nil || !backend.IsEnabled() {
		return nil
	}
	stats, err := backend.GetStats(ctx)
	if err != nil {
		logger.Debug("%s: unable to gather stats: %v", label, err)
		return nil
	}
	return stats
}

func formatStorageInitSummary(name string, cfg *config.Config, location storage.BackupLocation, stats *storage.StorageStats, backups []*types.BackupMetadata) string {
	retentionConfig := storage.NewRetentionConfigFromConfig(cfg, location)

	if stats == nil {
		reason := "unable to gather stats"
		if retentionConfig.Policy == "gfs" {
			return fmt.Sprintf("⚠ %s initialized with warnings (%s; GFS retention: daily=%d, weekly=%d, monthly=%d, yearly=%d)",
				name, reason, retentionConfig.Daily, retentionConfig.Weekly,
				retentionConfig.Monthly, retentionConfig.Yearly)
		}
		return fmt.Sprintf("⚠ %s initialized with warnings (%s; retention %s)", name, reason, formatBackupNoun(retentionConfig.MaxBackups))
	}

	if retentionConfig.Policy == "gfs" {
		result := fmt.Sprintf("✓ %s initialized (present %s)", name, formatBackupNoun(stats.TotalBackups))
		if stats.TotalBackups > 0 && backups != nil && len(backups) > 0 {
			classification := storage.ClassifyBackupsGFS(backups, retentionConfig)
			gfsStats := storage.GetRetentionStats(classification)

			total := stats.TotalBackups
			kept := total - gfsStats[storage.CategoryDelete]

			result += fmt.Sprintf("\n  Total: %d/-", total)
			result += fmt.Sprintf("\n  Daily: %d/%d", gfsStats[storage.CategoryDaily], retentionConfig.Daily)
			result += fmt.Sprintf("\n  Weekly: %d/%d", gfsStats[storage.CategoryWeekly], retentionConfig.Weekly)
			result += fmt.Sprintf("\n  Monthly: %d/%d", gfsStats[storage.CategoryMonthly], retentionConfig.Monthly)
			result += fmt.Sprintf("\n  Yearly: %d/%d", gfsStats[storage.CategoryYearly], retentionConfig.Yearly)
			result += fmt.Sprintf("\n  Kept (est.): %d, To delete (est.): %d", kept, gfsStats[storage.CategoryDelete])
		} else {
			result += fmt.Sprintf("\n  Daily: 0/%d, Weekly: 0/%d, Monthly: 0/%d, Yearly: 0/%d",
				retentionConfig.Daily, retentionConfig.Weekly,
				retentionConfig.Monthly, retentionConfig.Yearly)
		}
		return result
	}

	result := fmt.Sprintf("✓ %s initialized (present %s)", name, formatBackupNoun(stats.TotalBackups))
	result += fmt.Sprintf("\n  Policy: simple (keep %d newest)", retentionConfig.MaxBackups)
	return result
}

func logStorageInitSummary(summary string) {
	if summary == "" {
		return
	}
	for _, line := range strings.Split(summary, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "⚠") {
			logging.Warning("%s", line)
			continue
		}
		if strings.Contains(trimmed, "Kept (est.):") {
			logging.Debug("%s", line)
		} else {
			logging.Info("%s", line)
		}
	}
}

func formatBackupNoun(n int) string {
	if n == 1 {
		return "1 backup"
	}
	return fmt.Sprintf("%d backups", n)
}

func cleanupLegacyBashSymlinks(baseDir string, bootstrap *logging.BootstrapLogger) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		baseDir = "/opt/proxmox-backup"
	}

	legacyTargets := map[string]struct{}{}
	addLegacyDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		scriptDir := filepath.Join(dir, "script")
		if info, err := os.Stat(scriptDir); err != nil || !info.IsDir() {
			return
		}
		for _, name := range []string{
			"proxmox-backup.sh",
			"security-check.sh",
			"fix-permissions.sh",
			"proxmox-restore.sh",
		} {
			path := filepath.Join(scriptDir, name)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != "" {
				legacyTargets[resolved] = struct{}{}
			} else {
				legacyTargets[path] = struct{}{}
			}
		}
	}

	addLegacyDir(baseDir)
	if baseDir != "/opt/proxmox-backup" {
		addLegacyDir("/opt/proxmox-backup")
	}

	if len(legacyTargets) == 0 {
		return
	}

	searchDirs := []string{"/usr/local/bin", "/usr/bin"}

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				continue
			}

			target, err := os.Readlink(path)
			if err != nil {
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(dir, target)
			}
			resolved, err := filepath.EvalSymlinks(target)
			if err != nil {
				resolved = target
			}

			if _, ok := legacyTargets[resolved]; !ok {
				continue
			}

			if err := os.Remove(path); err != nil {
				bootstrap.Warning("WARNING: Failed to remove legacy symlink %s -> %s: %v", path, resolved, err)
			} else {
				bootstrap.Info("Removed legacy bash symlink: %s -> %s", path, resolved)
			}
		}
	}
}

func ensureGoSymlink(execPath string, bootstrap *logging.BootstrapLogger) {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		logBootstrapWarning(bootstrap, "WARNING: Unable to create proxmox-backup symlink: executable path is unknown")
		return
	}

	dest := "/usr/local/bin/proxmox-backup"
	if _, err := os.Lstat(dest); err == nil {
		if rmErr := os.Remove(dest); rmErr != nil {
			logBootstrapWarning(bootstrap, "WARNING: Failed to replace %s: remove failed: %v", dest, rmErr)
			return
		}
	} else if !os.IsNotExist(err) {
		logBootstrapWarning(bootstrap, "WARNING: Unable to inspect %s: %v", dest, err)
		return
	}

	if err := os.Symlink(execPath, dest); err != nil {
		logBootstrapWarning(bootstrap, "WARNING: Failed to create symlink %s -> %s: %v", dest, execPath, err)
		return
	}
}

func migrateLegacyCronEntries(ctx context.Context, baseDir, execPath string, bootstrap *logging.BootstrapLogger, cronSchedule string) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		baseDir = "/opt/proxmox-backup"
	}

	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		logBootstrapWarning(bootstrap, "WARNING: Unable to update cron entry: executable path is unknown")
		return
	}

	legacyPaths := []string{
		filepath.Join(baseDir, "script", "proxmox-backup.sh"),
		filepath.Join("/opt/proxmox-backup", "script", "proxmox-backup.sh"),
	}

	newCommandToken := "/usr/local/bin/proxmox-backup"
	if _, err := os.Stat(newCommandToken); err != nil {
		fallback := strings.TrimSpace(execPath)
		if fallback != "" {
			bootstrap.Info("Symlink %s not found, falling back to %s for cron entries", newCommandToken, fallback)
			newCommandToken = fallback
		} else {
			bootstrap.Warning("WARNING: Unable to locate Go binary for cron migration")
			return
		}
	}

	readCron := func() (string, error) {
		cmd := exec.CommandContext(ctx, "crontab", "-l")
		output, err := cmd.CombinedOutput()
		if err != nil {
			lower := strings.ToLower(string(output))
			if strings.Contains(lower, "no crontab for") {
				return "", nil
			}
			return "", fmt.Errorf("crontab -l failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return string(output), nil
	}

	writeCron := func(content string) error {
		cmd := exec.CommandContext(ctx, "crontab", "-")
		cmd.Stdin = strings.NewReader(content)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("crontab update failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	current, err := readCron()
	if err != nil {
		bootstrap.Warning("WARNING: Unable to inspect existing cron entries: %v", err)
		return
	}

	normalized := strings.ReplaceAll(current, "\r\n", "\n")
	lines := []string{}
	if strings.TrimSpace(normalized) != "" {
		lines = strings.Split(strings.TrimRight(normalized, "\n"), "\n")
	}

	updatedLines := make([]string, 0, len(lines)+1)

	containsAny := func(line string) bool {
		if strings.Contains(line, "proxmox-backup.sh") {
			return true
		}
		if strings.Contains(line, "proxmox-backup") {
			return true
		}
		for _, p := range legacyPaths {
			if strings.Contains(line, p) {
				return true
			}
		}
		return false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			updatedLines = append(updatedLines, line)
			continue
		}
		if containsAny(line) {
			// Skip all existing proxmox-backup entries (bash or go) to recreate cleanly.
			continue
		}
		updatedLines = append(updatedLines, line)
	}

	schedule := strings.TrimSpace(cronSchedule)
	if schedule == "" {
		schedule = "0 2 * * *"
	}
	// Always append a fresh default entry pointing to the Go binary (or fallback).
	defaultLine := fmt.Sprintf("%s %s", schedule, newCommandToken)
	updatedLines = append(updatedLines, defaultLine)

	newCron := strings.Join(updatedLines, "\n") + "\n"
	if err := writeCron(newCron); err != nil {
		bootstrap.Warning("WARNING: Failed to update cron entries: %v", err)
		return
	}

	bootstrap.Debug("Recreated cron entry for proxmox-backup at 02:00: %s", newCommandToken)
}

func logBootstrapWarning(bootstrap *logging.BootstrapLogger, format string, args ...interface{}) {
	if bootstrap != nil {
		bootstrap.Warning(format, args...)
		return
	}
	logging.Warning(format, args...)
}

func fetchBackupList(ctx context.Context, backend storage.Storage) []*types.BackupMetadata {
	listable, ok := backend.(interface {
		List(context.Context) ([]*types.BackupMetadata, error)
	})
	if !ok {
		return nil
	}

	backups, err := listable.List(ctx)
	if err != nil {
		return nil
	}
	return backups
}

func buildSignature() string {
	hash := executableHash()
	formattedTime := ""

	actualBuildTime := executableBuildTime()
	if !actualBuildTime.IsZero() {
		formattedTime = actualBuildTime.Local().Format(time.RFC3339)
	}

	var revision string
	modified := ""

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" {
					modified = "*"
				}
			}
		}
		if revision != "" {
			shortRev := revision
			if len(shortRev) > 9 {
				shortRev = shortRev[:9]
			}
			sig := shortRev + modified
			if formattedTime != "" {
				sig = fmt.Sprintf("%s (%s)", sig, formattedTime)
			}
			if hash != "" {
				sig = fmt.Sprintf("%s hash=%s", sig, truncateHash(hash))
			}
			return sig
		}
	}

	if formattedTime != "" && hash != "" {
		return fmt.Sprintf("%s hash=%s", formattedTime, truncateHash(hash))
	}
	if formattedTime != "" {
		return formattedTime
	}
	if hash != "" {
		return fmt.Sprintf("hash=%s", truncateHash(hash))
	}
	return ""
}

func executableBuildTime() time.Time {
	if buildTime != "" {
		if t, err := time.Parse(time.RFC3339, buildTime); err == nil {
			return t
		}
	}

	info := getExecInfo()
	if info.ExecPath == "" {
		return time.Time{}
	}
	stat, err := os.Stat(info.ExecPath)
	if err != nil {
		return time.Time{}
	}
	return stat.ModTime()
}

func executableHash() string {
	info := getExecInfo()
	if info.ExecPath == "" {
		return ""
	}
	f, err := os.Open(info.ExecPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func truncateHash(hash string) string {
	if len(hash) <= 16 {
		return hash
	}
	return hash[:16]
}

func cleanupAfterRun(logger *logging.Logger) {
	patterns := []string{
		"/tmp/backup_status_update_*.lock",
		"/tmp/backup_*_*.lock",
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			logger.Debug("Cleanup glob error for %s: %v", pattern, err)
			continue
		}

		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				continue
			}
			if info.Size() != 0 {
				continue
			}
			if err := os.Remove(match); err != nil {
				logger.Warning("Failed to remove orphaned lock file %s: %v", match, err)
			} else {
				logger.Debug("Removed orphaned lock file: %s", match)
			}
		}
	}
}

func addPathExclusion(excludes []string, path string) []string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return excludes
	}
	clean := filepath.Clean(trimmed)
	excludes = append(excludes, clean)
	excludes = append(excludes, filepath.ToSlash(filepath.Join(clean, "**")))
	return excludes
}

func isLocalPath(path string) bool {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return false
	}
	if strings.Contains(clean, ":") && !strings.HasPrefix(clean, "/") {
		return false
	}
	return filepath.IsAbs(clean)
}
