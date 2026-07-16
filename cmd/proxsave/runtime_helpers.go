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
	"unicode"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/serverbot"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

type ExecInfo struct {
	ExecPath string
	ExecDir  string
	BaseDir  string
	HasBase  bool
}

var (
	execInfo     = &ExecInfo{}
	execInfoOnce = &sync.Once{}
)

func getExecInfo() ExecInfo {
	execInfoOnce.Do(func() {
		info := detectExecInfo()
		execInfo = &info
	})
	return *execInfo
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
	baseDir, hasBase := resolveBaseDirFromExecutablePath(execPath)

	return ExecInfo{
		ExecPath: execPath,
		ExecDir:  execDir,
		BaseDir:  baseDir,
		HasBase:  hasBase,
	}
}

func detectBaseDir() (string, bool) {
	info := getExecInfo()
	return info.BaseDir, info.HasBase
}

func detectedBaseDirOrFallback() (string, bool) {
	baseDir, found := detectBaseDir()
	if strings.TrimSpace(baseDir) == "" {
		return fallbackBaseDir(), false
	}
	return baseDir, found
}

func resolveBaseDirFromExecutablePath(execPath string) (string, bool) {
	clean := filepath.Clean(strings.TrimSpace(execPath))
	if clean == "" || clean == "." {
		return fallbackBaseDir(), false
	}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && strings.TrimSpace(resolved) != "" {
		clean = filepath.Clean(resolved)
	}
	if baseDir, ok := baseDirFromStandardExecutableLayout(clean); ok {
		return baseDir, true
	}
	if baseDir, ok := baseDirFromInstallMarkers(filepath.Dir(clean)); ok {
		return baseDir, true
	}
	return fallbackBaseDir(), false
}

func baseDirFromStandardExecutableLayout(execPath string) (string, bool) {
	dir := filepath.Dir(execPath)
	if filepath.Base(dir) != "build" {
		return "", false
	}
	parent := filepath.Dir(dir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return "", false
	}
	return parent, true
}

func baseDirFromInstallMarkers(startDir string) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(startDir))
	for dir != "" && dir != "." {
		for _, marker := range []string{"configs", "env", "script", "identity"} {
			if info, err := os.Stat(filepath.Join(dir, marker)); err == nil && info.IsDir() {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

func fallbackBaseDir() string {
	if info, err := os.Stat("/opt/proxsave"); err == nil && info.IsDir() {
		return "/opt/proxsave"
	}
	if info, err := os.Stat("/opt/proxmox-backup"); err == nil && info.IsDir() {
		return "/opt/proxmox-backup"
	}
	return "/opt/proxsave"
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
	msg := "WARNING: Unable to determine proxsave/proxmox-backup executable path; symlink and cron setup skipped"
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

// logMonitoringPortalLink is the SOLE display boundary for the portal magic-link. The
// Healthchecks section carries the link RAW on stats.HealthcheckLink (captured this run
// or best-effort minted); this sanitizes it once with serverbot.SanitizeLoginURL and,
// only if it survives, prints it as the "Healthchecks Portal" line. It is called in the
// backup epilogue right after the Server MAC Address line so the link appears at the
// very end of the run. It never registers the link as a log secret (it must stay
// visible) and prints nothing for a nil stats, empty, or hostile link.
func logMonitoringPortalLink(stats *orchestrator.BackupStats) {
	if stats == nil {
		return
	}
	if safe := serverbot.SanitizeLoginURL(stats.HealthcheckLink); safe != "" {
		logging.Info("Healthchecks Portal: %s", safe)
	}
}

func resolveHostname() string {
	if path, err := exec.LookPath("hostname"); err == nil {
		cmdCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		cmd, cmdErr := safeexec.TrustedCommandContext(cmdCtx, path, "-f")
		if cmdErr == nil {
			if out, err := cmd.Output(); err == nil {
				if fqdn := strings.TrimSpace(string(out)); fqdn != "" {
					return fqdn
				}
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
	if cfg.SecondaryEnabled {
		if err := config.ValidateRequiredSecondaryPath(cfg.SecondaryPath); err != nil {
			return err
		}
		if err := config.ValidateOptionalSecondaryLogPath(cfg.SecondaryLogPath); err != nil {
			return err
		}
	}
	if cfg.CloudEnabled && cfg.CloudRemote == "" {
		logging.Warning("Cloud backup enabled but CLOUD_REMOTE is empty, disabling cloud storage for this run")
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
		if backend.Location() == storage.LocationCloud {
			return nil, err
		}
		return nil, nil
	}

	if !fsInfo.SupportsOwnership {
		if backend != nil && backend.Location() == storage.LocationCloud {
			logger.Debug("%s [%s] does not support ownership changes (cloud remote); chown/chmod already disabled", path, fsInfo.Type)
		} else {
			logger.Info("%s [%s] does not support ownership changes; chown/chmod will be skipped", path, fsInfo.Type)
		}
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
	if retentionConfig.Policy == "gfs" {
		retentionConfig = storage.EffectiveGFSRetentionConfig(retentionConfig)
	}

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

// cleanupGlobalProxmoxBackupEntrypoints performs a best-effort cleanup of any existing
// proxsave/proxmox-backup entrypoints on the system PATH, as well as the common global
// locations /usr/local/bin and /usr/bin, mirroring what an operator would manually
// inspect with:
//
//	type -a proxmox-backup
//	which proxmox-backup
//	ls -l /usr/local/bin/proxmox-backup /usr/bin/proxmox-backup
//
// It removes only SYMLINKS that do not resolve to the current Go binary, so the
// installer can recreate clean ones. A real (non-symlink) file is intentionally left
// in place (and logged): it may be a distro/package-managed binary — e.g. a packaged
// /usr/bin/proxsave — that the recreation step (which only writes
// /usr/local/bin/proxsave) never restores, so deleting it would permanently break the
// command.
// globalEntrypointDirs are the well-known directories always scanned for
// proxsave/proxmox-backup entrypoints (in addition to PATH). Declared as a var
// so tests can point it at temporary directories instead of the real system
// paths.
var globalEntrypointDirs = []string{"/usr/local/bin", "/usr/bin"}

func cleanupGlobalProxmoxBackupEntrypoints(execPath string, bootstrap *logging.BootstrapLogger) {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		// Without a known current binary we cannot tell our own entrypoint apart
		// from the ones to remove, and ensureGoSymlink cannot recreate a
		// replacement either. Removing here would leave the host with no working
		// proxsave/proxmox-backup command, so do nothing.
		logBootstrapWarning(bootstrap, "WARNING: current executable path is unknown; skipping proxsave/proxmox-backup entrypoint cleanup to avoid removing a working entrypoint that cannot be recreated")
		return
	}

	pathEnv := os.Getenv("PATH")
	if strings.TrimSpace(pathEnv) == "" {
		// Empty PATH: skip PATH-based scanning but still clean the well-known
		// global directories below (filepath.SplitList("") yields no PATH entries).
		bootstrap.Info("PATH is empty; scanning only the well-known global directories")
	} else {
		bootstrap.Info("Scanning for existing proxsave/proxmox-backup commands on PATH before installation")
	}

	candidates := make([]string, 0, 16)
	names := []string{"proxsave", "proxmox-backup"}
	for _, dir := range filepath.SplitList(pathEnv) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		for _, name := range names {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}

	// Ensure the common global directories are always considered, even if not in PATH.
	for _, dir := range globalEntrypointDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		for _, name := range names {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}

	seen := map[string]struct{}{}
	removed := 0
	kept := 0

	for _, cand := range candidates {
		cand = filepath.Clean(strings.TrimSpace(cand))
		if cand == "" {
			continue
		}
		if _, ok := seen[cand]; ok {
			continue
		}
		seen[cand] = struct{}{}

		info, err := os.Lstat(cand)
		if err != nil {
			continue
		}

		// If this entry *is* the current Go executable, keep it.
		if execPath != "" {
			if cand == execPath {
				bootstrap.Info("Keeping proxsave/proxmox-backup entrypoint (current Go binary): %s", cand)
				kept++
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Readlink(cand); err == nil {
					if !filepath.IsAbs(target) {
						target = filepath.Join(filepath.Dir(cand), target)
					}
					if resolved, err := filepath.EvalSymlinks(target); err == nil && resolved == execPath {
						bootstrap.Info("Keeping proxsave/proxmox-backup symlink pointing to Go binary: %s -> %s", cand, resolved)
						kept++
						continue
					}
				}
			}
		}

		// cand is an existing entrypoint that does not resolve to the current Go
		// binary. Only remove it if it is a SYMLINK (the form proxsave creates).
		// A real file may be a distro/package-managed binary (e.g. a packaged
		// /usr/bin/proxsave) that recreation never restores, so removing it would
		// permanently break the command — leave it in place and log it.
		if info.Mode()&os.ModeSymlink == 0 {
			logBootstrapInfo(bootstrap, "Leaving %s in place: not a symlink created by proxsave (a real/package-managed file is never removed)", cand)
			kept++
			continue
		}
		if err := os.Remove(cand); err != nil {
			logBootstrapWarning(bootstrap, "WARNING: Failed to remove existing proxsave/proxmox-backup symlink %s: %v", cand, err)
			continue
		}
		bootstrap.Info("Removed existing proxsave/proxmox-backup symlink: %s", cand)
		removed++
	}

	if removed == 0 && kept == 0 {
		bootstrap.Info("No existing proxsave/proxmox-backup entrypoints found on PATH or in /usr/local/bin,/usr/bin")
	} else {
		bootstrap.Info("Global proxsave/proxmox-backup entrypoint scan: removed=%d kept=%d", removed, kept)
	}
}

func ensureGoSymlink(execPath string, bootstrap *logging.BootstrapLogger) {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		logBootstrapWarning(bootstrap, "WARNING: Unable to update the proxsave entrypoint: executable path is unknown")
		return
	}

	installEntrypointSymlink(execPath, "/usr/local/bin/proxsave", bootstrap)
	// The legacy "proxmox-backup" command name is no longer a supported entrypoint:
	// drop its symlink instead of recreating it. proxsave is the only entrypoint.
	removeLegacyEntrypoint("/usr/local/bin/proxmox-backup", bootstrap)
}

// removeLegacyEntrypoint deletes the legacy "proxmox-backup" command symlink at
// dest. It only removes a symlink (the form proxsave always created); a real file
// is intentionally left in place so an unrelated file (e.g. an operator's own
// script) is never deleted. Proxmox Backup Server ships its tools as
// proxmox-backup-client/-proxy/-manager under /usr/sbin and /usr/bin, never a bare
// "proxmox-backup" symlink in /usr/local/bin, so PBS is unaffected.
// installEntrypointSymlink points dest at execPath as a symlink, replacing any
// existing file/symlink ATOMICALLY: it creates the new symlink at a temp path in
// the same directory and renames it over dest. A bare remove-then-symlink would
// leave the host with NO entrypoint if the symlink step failed after the remove
// succeeded; the atomic rename never leaves dest missing.
func installEntrypointSymlink(execPath, dest string, bootstrap *logging.BootstrapLogger) {
	if info, err := os.Lstat(dest); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if resolved, err := filepath.EvalSymlinks(dest); err == nil && resolved == execPath {
				logBootstrapInfo(bootstrap, "Keeping existing symlink %s -> %s", dest, resolved)
				return
			}
		case info.Mode().IsRegular():
			// A real (non-symlink) file occupies the entrypoint path: it may be an
			// operator wrapper or a packaged binary, which the cleanup scan also
			// refuses to delete. Back it up before replacing it with the proxsave
			// symlink so it is never lost silently (INSTALL-SYMLINK-001). If the
			// backup fails, refuse to replace it rather than clobber it.
			backup, err := backupRealFile(dest)
			if err != nil {
				logBootstrapWarning(bootstrap, "WARNING: Not replacing real file %s: failed to back it up: %v", dest, err)
				return
			}
			logBootstrapWarning(bootstrap, "WARNING: Backed up existing real file %s to %s before installing the proxsave symlink", dest, backup)
		}
	} else if !os.IsNotExist(err) {
		logBootstrapWarning(bootstrap, "WARNING: Unable to inspect %s: %v", dest, err)
		return
	}

	tmp := dest + ".proxsave-new"
	_ = os.Remove(tmp) // clear any leftover from a previous interrupted run
	if err := os.Symlink(execPath, tmp); err != nil {
		logBootstrapWarning(bootstrap, "WARNING: Failed to create symlink %s -> %s: %v", dest, execPath, err)
		return
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		logBootstrapWarning(bootstrap, "WARNING: Failed to install symlink %s -> %s: %v", dest, execPath, err)
		return
	}
	logBootstrapInfo(bootstrap, "Created symlink: %s -> %s", dest, execPath)
}

// backupRealFile copies the regular file at path to "<path>.bak", preserving its
// permission bits, and returns the backup path. It is used before proxsave
// replaces a real operator/package file at an entrypoint path with its symlink, so
// the original is never lost (INSTALL-SYMLINK-001).
func backupRealFile(path string) (string, error) {
	backup := path + ".bak"
	// Resolve the source and its ".bak" sibling through os.Root on their shared
	// directory so the variable path provably cannot escape it (gosec G304
	// containment); the entrypoint path is admin-controlled, but the structural
	// fix is preferred over a #nosec.
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()

	src, err := root.Open(filepath.Base(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return "", err
	}

	dst, err := root.OpenFile(filepath.Base(backup), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return "", err
	}
	return backup, dst.Close()
}

func removeLegacyEntrypoint(dest string, bootstrap *logging.BootstrapLogger) {
	info, err := os.Lstat(dest)
	if err != nil {
		if !os.IsNotExist(err) {
			logBootstrapWarning(bootstrap, "WARNING: Unable to inspect legacy entrypoint %s: %v", dest, err)
		}
		return
	}
	if info.Mode()&os.ModeSymlink == 0 {
		logBootstrapInfo(bootstrap, "Leaving %s in place: not a symlink created by proxsave", dest)
		return
	}
	if err := os.Remove(dest); err != nil {
		logBootstrapWarning(bootstrap, "WARNING: Failed to remove legacy entrypoint %s: %v", dest, err)
		return
	}
	logBootstrapInfo(bootstrap, "Removed legacy 'proxmox-backup' entrypoint: %s", dest)
}

func migrateLegacyCronEntries(ctx context.Context, baseDir, execPath string, bootstrap *logging.BootstrapLogger, cronSchedule string) {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		logBootstrapWarning(bootstrap, "WARNING: Unable to update cron entry: executable path is unknown")
		return
	}

	newCommandToken := "/usr/local/bin/proxsave"
	if _, err := os.Stat(newCommandToken); err != nil {
		// proxsave symlink missing: fall back to the known Go binary (execPath),
		// not /usr/local/bin/proxmox-backup — that path may resolve to an unrelated
		// PBS binary and we must never schedule that as root in cron.
		fallback := strings.TrimSpace(execPath)
		if fallback == "" {
			logBootstrapWarning(bootstrap, "WARNING: Unable to locate Go binary for cron migration")
			return
		}
		logBootstrapInfo(bootstrap, "proxsave symlink not found; falling back to %s for cron entries", fallback)
		newCommandToken = fallback
	}

	readCron := func() (string, error) {
		cmd, err := safeexec.CommandContext(ctx, "crontab", "-l")
		if err != nil {
			return "", err
		}
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
		cmd, err := safeexec.CommandContext(ctx, "crontab", "-")
		if err != nil {
			return err
		}
		cmd.Stdin = strings.NewReader(content)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("crontab update failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	current, err := readCron()
	if err != nil {
		logBootstrapWarning(bootstrap, "WARNING: Unable to inspect existing cron entries: %v", err)
		return
	}

	normalized := strings.ReplaceAll(current, "\r\n", "\n")
	lines := []string{}
	if strings.TrimSpace(normalized) != "" {
		lines = strings.Split(strings.TrimRight(normalized, "\n"), "\n")
	}

	// proxsave is the canonical entrypoint; the legacy "proxmox-backup" name is
	// intentionally NOT treated as a current target, so a cron still pointing at it
	// is migrated to proxsave below while keeping the operator's schedule.
	correctPaths := []string{strings.TrimSpace(newCommandToken)}
	if execPath != "" && execPath != newCommandToken {
		correctPaths = append(correctPaths, execPath)
	}

	schedule := strings.TrimSpace(cronSchedule)
	if schedule == "" {
		schedule = "0 2 * * *"
	}

	// (Re)install resets proxsave's schedule to the chosen one: every
	// proxsave-managed entry is dropped and a single fresh entry is written at the
	// chosen schedule, while unrelated operator cron lines are preserved. Upgrades
	// no longer call this, so it only runs on install (CRON-INSTALL-002 /
	// CRON-MIXED-001).
	updatedLines := buildReinstallCronLines(lines, baseDir, correctPaths, schedule, newCommandToken, bootstrap)

	newCron := strings.Join(updatedLines, "\n") + "\n"
	if err := writeCron(newCron); err != nil {
		logBootstrapWarning(bootstrap, "WARNING: Failed to update cron entries: %v", err)
		return
	}

	logBootstrapDebug(bootstrap, "Reinstalled proxsave cron entry at schedule %s: %s", schedule, newCommandToken)
}

// dropLegacyBashCronLines removes crontab lines whose command is the old Bash
// backup script (<root>/script/proxmox-backup.sh) under a known proxsave install
// root. It is deliberately narrow: it matches the cron command token by exact
// path (not a substring) and only the ".sh" script — so Proxmox Backup Server
// components (proxmox-backup, proxmox-backup-proxy, proxmox-backup-client, none of
// which end in .sh), comments, and lines that merely pass the path as an argument
// are never removed.
func dropLegacyBashCronLines(lines []string, baseDir string, bootstrap *logging.BootstrapLogger) []string {
	roots := []string{strings.TrimSpace(baseDir), "/opt/proxmox-backup", "/opt/proxsave"}
	legacyScripts := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		legacyScripts[filepath.Join(root, "script", "proxmox-backup.sh")] = struct{}{}
	}

	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		token := strings.Trim(cronCommandToken(line), "\"'")
		if _, isLegacy := legacyScripts[token]; isLegacy {
			logBootstrapInfo(bootstrap, "Removing legacy Bash backup cron entry: %s", token)
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// filterCronLines returns the crontab with every proxsave/proxmox-backup entry
// that points to an outdated binary removed. Blank lines, comments, unrelated
// operator jobs and any entry already targeting a canonical path are preserved.
// The previous schedule is not remembered: a (re)install rewrites the proxsave
// schedule from config (SCHEDULER_TIME) afterwards.
func filterCronLines(lines []string, correctPaths []string) []string {
	updatedLines := make([]string, 0, len(lines))

	containsCorrectPath := func(line string) bool {
		// Match the cron command token exactly, not as a substring: otherwise a
		// line like "... /bin/echo /usr/local/bin/proxsave" (proxsave as an
		// argument) or "/usr/local/bin/proxsavex" (prefix-sharing) would be mistaken
		// for the current proxsave entry.
		cmd := strings.Trim(cronCommandToken(line), "\"'")
		if cmd == "" {
			return false
		}
		for _, p := range correctPaths {
			if p != "" && cmd == p {
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
		if containsCorrectPath(line) {
			updatedLines = append(updatedLines, line)
			continue
		}
		if containsBinaryReference(line) {
			// Drop proxsave/proxmox-backup entries that point to an outdated binary.
			continue
		}
		updatedLines = append(updatedLines, line)
	}

	return updatedLines
}

// dropCanonicalCronLines removes every cron line whose command token already
// targets one of the canonical proxsave paths, so a (re)install can rewrite the
// schedule from scratch instead of preserving the previous one.
func dropCanonicalCronLines(lines, correctPaths []string) []string {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		token := strings.Trim(cronCommandToken(line), "\"'")
		// Drop any proxsave/proxmox-backup cron line by command-token basename, not
		// only the exact canonical path, so a non-canonical or hand-edited entry is
		// removed too and the "removed" log is truthful (F10-04). The correctPaths
		// exact match is kept as a belt-and-suspenders for an unusual token.
		drop := commandTokenMatchesTarget(token)
		if !drop && token != "" {
			for _, p := range correctPaths {
				if p != "" && token == p {
					drop = true
					break
				}
			}
		}
		if drop {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// repointLegacyCronLines repoints any cron line whose command token is exactly the
// legacy /usr/local/bin/proxmox-backup symlink to the canonical /usr/local/bin/proxsave
// entrypoint, preserving the schedule and args. Every other line is byte-preserved. Used
// on upgrade BEFORE the legacy symlink is removed so no cron line is left orphaned (F10-03).
func repointLegacyCronLines(lines []string) ([]string, bool) {
	const legacy = "/usr/local/bin/proxmox-backup"
	const canonical = "/usr/local/bin/proxsave"
	out := make([]string, len(lines))
	changed := false
	for i, line := range lines {
		if strings.Trim(cronCommandToken(line), "\"'") == legacy {
			// cronCommandToken already excluded comments/env lines, and a schedule field
			// is never a path, so the command is the first occurrence of legacy on the line.
			out[i] = strings.Replace(line, legacy, canonical, 1)
			changed = true
			continue
		}
		out[i] = line
	}
	return out, changed
}

// repointLegacyCronEntries reads the crontab, repoints legacy proxmox-backup entries, and
// writes back only if something changed. Best-effort: a crontab read/write failure logs and
// continues (a cosmetic repoint must never fail the upgrade).
func repointLegacyCronEntries(ctx context.Context, bootstrap *logging.BootstrapLogger) {
	lines, err := crontabReadLines(ctx)
	if err != nil {
		logBootstrapDebug(bootstrap, "upgrade: read crontab for repoint failed: %v", err)
		return
	}
	repointed, changed := repointLegacyCronLines(lines)
	if !changed {
		return
	}
	if err := crontabWriteLines(ctx, repointed); err != nil {
		logBootstrapWarning(bootstrap, "upgrade: failed to repoint legacy cron entrypoint: %v", err)
		return
	}
	logBootstrapInfo(bootstrap, "upgrade: repointed legacy proxmox-backup cron entry to the proxsave entrypoint")
}

// buildReinstallCronLines computes the crontab for a (re)install: it drops the
// legacy Bash cron entry, every outdated proxsave/proxmox-backup binary entry and
// any entry already targeting the canonical path, then appends a single fresh
// entry at the chosen schedule. Unrelated operator lines, comments and blanks are
// preserved. This deliberately resets proxsave's schedule to the chosen one
// (CRON-INSTALL-002) and removes stale/duplicate entries (CRON-MIXED-001).
func buildReinstallCronLines(lines []string, baseDir string, correctPaths []string, schedule, commandToken string, bootstrap *logging.BootstrapLogger) []string {
	lines = dropLegacyBashCronLines(lines, baseDir, bootstrap)
	updated := filterCronLines(lines, correctPaths)
	updated = dropCanonicalCronLines(updated, correctPaths)
	// --backup pins the non-interactive behavior: even if a scheduler ever
	// allocates a pty, the run can never land on the interactive dashboard.
	return append(updated, fmt.Sprintf("%s %s --backup", schedule, commandToken))
}

func logBootstrapWarning(bootstrap *logging.BootstrapLogger, format string, args ...interface{}) {
	if bootstrap != nil {
		bootstrap.Warning(format, args...)
		return
	}
	logging.Warning(format, args...)
}

func logBootstrapInfo(bootstrap *logging.BootstrapLogger, format string, args ...interface{}) {
	if bootstrap != nil {
		bootstrap.Info(format, args...)
		return
	}
	logging.Info(format, args...)
}

func logBootstrapDebug(bootstrap *logging.BootstrapLogger, format string, args ...interface{}) {
	if bootstrap != nil {
		bootstrap.Debug(format, args...)
		return
	}
	logging.Debug(format, args...)
}

// containsBinaryReference reports whether the cron line's COMMAND is a
// proxsave/proxmox-backup binary (matched by the command-token basename). It
// deliberately does not scan the rest of the line: a binary path that merely
// appears as an argument (e.g. "cp /usr/local/bin/proxsave /backup/") belongs to a
// different command and must not be treated as a proxsave entry to remove.
func containsBinaryReference(line string) bool {
	return commandTokenMatchesTarget(cronCommandToken(line))
}

func cronCommandToken(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}

	first := fields[0]
	if looksLikeEnvAssignment(first) {
		return ""
	}

	if strings.HasPrefix(first, "@") {
		if len(fields) >= 2 {
			return fields[1]
		}
		return ""
	}

	if len(fields) <= 5 {
		return ""
	}
	return fields[5]
}

func looksLikeEnvAssignment(token string) bool {
	idx := strings.Index(token, "=")
	if idx <= 0 {
		return false
	}
	key := token[:idx]
	for _, r := range key {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func commandTokenMatchesTarget(token string) bool {
	token = strings.Trim(token, "\"'")
	if token == "" {
		return false
	}
	base := filepath.Base(token)
	return base == "proxsave" || base == "proxmox-backup"
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
	defer func() { _ = f.Close() }()
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

// ensureDirectoryExists verifies that dirPath exists and attempts to create it when missing.
// All events are logged using the provided logger.
func ensureDirectoryExists(logger *logging.Logger, name, path string) {
	if logger == nil {
		return
	}
	dirPath := strings.TrimSpace(path)
	if dirPath == "" {
		return
	}
	if utils.DirExists(dirPath) {
		logger.Info("✓ %s exists: %s", name, dirPath)
		return
	}

	logger.Warning("✗ %s not found: %s", name, dirPath)
	if err := os.MkdirAll(dirPath, defaultDirPerm); err != nil {
		logger.Warning("Failed to create %s: %v", dirPath, err)
		return
	}
	logger.Info("%s created: %s", name, dirPath)
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

// extractRemoteName extracts the remote name from CLOUD_REMOTE.
// For "gdrive" returns "gdrive", for "gdrive:path" returns "gdrive".
func extractRemoteName(cloudRemote string) string {
	remote := strings.TrimSpace(cloudRemote)
	if remote == "" {
		return ""
	}
	if idx := strings.Index(remote, ":"); idx != -1 {
		return remote[:idx]
	}
	return remote
}
