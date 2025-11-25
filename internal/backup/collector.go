package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

// CollectionStats tracks statistics during backup collection
type CollectionStats struct {
	FilesProcessed int64
	FilesFailed    int64
	DirsCreated    int64
	BytesCollected int64
}

// FileSummary represents metadata about a sampled file
type FileSummary struct {
	RelativePath string    `json:"relative_path"`
	SizeBytes    int64     `json:"size_bytes"`
	SizeHuman    string    `json:"size_human"`
	ModTime      time.Time `json:"mod_time"`
}

// Collector handles backup data collection
type Collector struct {
	logger     *logging.Logger
	config     *CollectorConfig
	stats      *CollectionStats
	statsMu    sync.Mutex
	tempDir    string
	proxType   types.ProxmoxType
	dryRun     bool
	rootsMu    sync.RWMutex
	rootsCache map[string][]string
	deps       CollectorDeps

	// clusteredPVE records whether cluster mode was detected during PVE collection.
	clusteredPVE bool
}

func (c *Collector) incFilesProcessed() {
	atomic.AddInt64(&c.stats.FilesProcessed, 1)
}

func (c *Collector) incFilesFailed() {
	atomic.AddInt64(&c.stats.FilesFailed, 1)
}

func (c *Collector) incDirsCreated() {
	atomic.AddInt64(&c.stats.DirsCreated, 1)
}

func (c *Collector) addBytesCollected(delta int64) {
	if delta == 0 {
		return
	}
	atomic.AddInt64(&c.stats.BytesCollected, delta)
}

func (c *Collector) depLookPath(name string) (string, error) {
	if c.deps.LookPath != nil {
		return c.deps.LookPath(name)
	}
	return execLookPath(name)
}

func (c *Collector) depRunCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.deps.RunCommand != nil {
		return c.deps.RunCommand(ctx, name, args...)
	}
	return runCommand(ctx, name, args...)
}

func (c *Collector) depRunCommandWithEnv(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
	if c.deps.RunCommandWithEnv != nil {
		return c.deps.RunCommandWithEnv(ctx, extraEnv, name, args...)
	}
	return runCommandWithEnv(ctx, extraEnv, name, args...)
}

func (c *Collector) depStat(path string) (os.FileInfo, error) {
	if c.deps.Stat != nil {
		return c.deps.Stat(path)
	}
	return statFunc(path)
}

// systemPath resolves an absolute system path under an optional test prefix.
// When SystemRootPrefix is empty or "/", it returns the original path.
func (c *Collector) systemPath(path string) string {
	prefix := c.config.SystemRootPrefix
	if prefix == "" || prefix == string(filepath.Separator) {
		return path
	}
	trimmed := strings.TrimPrefix(path, string(filepath.Separator))
	return filepath.Join(prefix, trimmed)
}

// CollectorConfig holds configuration for backup collection
type CollectorConfig struct {
	// PVE-specific collection options
	BackupVMConfigs         bool
	BackupClusterConfig     bool
	BackupPVEFirewall       bool
	BackupVZDumpConfig      bool
	BackupPVEACL            bool
	BackupPVEJobs           bool
	BackupPVESchedules      bool
	BackupPVEReplication    bool
	BackupPVEBackupFiles    bool
	BackupSmallPVEBackups   bool
	MaxPVEBackupSizeBytes   int64
	PVEBackupIncludePattern string
	BackupCephConfig        bool
	CephConfigPath          string

	// PBS-specific collection options
	BackupDatastoreConfigs bool
	BackupUserConfigs      bool
	BackupRemoteConfigs    bool
	BackupSyncJobs         bool
	BackupVerificationJobs bool
	BackupTapeConfigs      bool
	BackupPruneSchedules   bool
	BackupPxarFiles        bool

	// System collection options
	BackupNetworkConfigs    bool
	BackupAptSources        bool
	BackupCronJobs          bool
	BackupSystemdServices   bool
	BackupSSLCerts          bool
	BackupSysctlConfig      bool
	BackupKernelModules     bool
	BackupFirewallRules     bool
	BackupInstalledPackages bool
	BackupScriptDir         bool
	BackupCriticalFiles     bool
	BackupSSHKeys           bool
	BackupZFSConfig         bool
	BackupRootHome          bool
	BackupScriptRepository  bool
	BackupUserHomes         bool
	BackupConfigFile        bool
	SystemRootPrefix        string

	// PXAR scanning tuning
	PxarDatastoreConcurrency int
	PxarIntraConcurrency     int
	PxarScanFanoutLevel      int
	PxarScanMaxRoots         int
	PxarStopOnCap            bool
	PxarEnumWorkers          int
	PxarEnumBudgetMs         int
	PxarFileIncludePatterns  []string
	PxarFileExcludePatterns  []string

	// Exclude patterns (glob patterns to skip)
	ExcludePatterns []string

	CustomBackupPaths []string
	BackupBlacklist   []string

	// Paths and overrides
	ScriptRepositoryPath string
	ConfigFilePath       string
	PVEConfigPath        string
	PVEClusterPath       string
	CorosyncConfigPath   string
	VzdumpConfigPath     string
	PBSConfigPath        string
	PBSDatastorePaths    []string

	// PBS Authentication (auto-detected)
	PBSRepository  string
	PBSPassword    string
	PBSFingerprint string
}

var defaultExcludePatterns = []string{
	"**/node_modules/**",
	"**/.vscode/**",
	"**/.cursor*",
	"**/.cursor-server*",
}

// Validate checks if the collector configuration is valid
func (c *CollectorConfig) Validate() error {
	// Validate exclude patterns (basic glob syntax check)
	for i, pattern := range c.ExcludePatterns {
		if pattern == "" {
			return fmt.Errorf("exclude pattern at index %d is empty", i)
		}
		// Test if pattern is valid glob syntax
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return fmt.Errorf("invalid glob pattern at index %d: %s (error: %w)", i, pattern, err)
		}
	}

	// At least one collection option should be enabled
	hasAnyEnabled := c.BackupVMConfigs || c.BackupClusterConfig ||
		c.BackupPVEFirewall || c.BackupVZDumpConfig || c.BackupPVEACL ||
		c.BackupPVEJobs || c.BackupPVESchedules || c.BackupPVEReplication ||
		c.BackupPVEBackupFiles || c.BackupCephConfig ||
		c.BackupDatastoreConfigs || c.BackupUserConfigs || c.BackupRemoteConfigs ||
		c.BackupSyncJobs || c.BackupVerificationJobs || c.BackupTapeConfigs ||
		c.BackupPruneSchedules || c.BackupPxarFiles ||
		c.BackupNetworkConfigs || c.BackupAptSources || c.BackupCronJobs ||
		c.BackupSystemdServices || c.BackupSSLCerts || c.BackupSysctlConfig ||
		c.BackupKernelModules || c.BackupFirewallRules ||
		c.BackupInstalledPackages || c.BackupScriptDir || c.BackupCriticalFiles ||
		c.BackupSSHKeys || c.BackupZFSConfig || c.BackupConfigFile

	if !hasAnyEnabled {
		return fmt.Errorf("at least one backup option must be enabled")
	}

	if c.PxarDatastoreConcurrency <= 0 {
		c.PxarDatastoreConcurrency = 3
	}
	if c.PxarIntraConcurrency <= 0 {
		c.PxarIntraConcurrency = 4
	}
	if c.PxarScanFanoutLevel <= 0 {
		c.PxarScanFanoutLevel = 1
	}
	if c.PxarScanMaxRoots <= 0 {
		c.PxarScanMaxRoots = 2048
	}
	if c.PxarEnumWorkers <= 0 {
		c.PxarEnumWorkers = 4
	}
	if c.PxarEnumBudgetMs < 0 {
		c.PxarEnumBudgetMs = 0
	}
	if c.MaxPVEBackupSizeBytes < 0 {
		return fmt.Errorf("MAX_PVE_BACKUP_SIZE must be >= 0")
	}
	if c.SystemRootPrefix != "" && !filepath.IsAbs(c.SystemRootPrefix) {
		return fmt.Errorf("system root prefix must be an absolute path")
	}

	return nil
}

// NewCollector creates a new backup collector
func NewCollector(logger *logging.Logger, config *CollectorConfig, tempDir string, proxType types.ProxmoxType, dryRun bool) *Collector {
	return NewCollectorWithDeps(logger, config, tempDir, proxType, dryRun, defaultCollectorDeps())
}

// NewCollectorWithDeps creates a collector with explicit dependency overrides (for testing).
func NewCollectorWithDeps(logger *logging.Logger, config *CollectorConfig, tempDir string, proxType types.ProxmoxType, dryRun bool, deps CollectorDeps) *Collector {
	return &Collector{
		logger:     logger,
		config:     config,
		stats:      &CollectionStats{},
		tempDir:    tempDir,
		proxType:   proxType,
		dryRun:     dryRun,
		rootsCache: make(map[string][]string),
		deps:       deps,
	}
}

// GetDefaultCollectorConfig returns default collection configuration
func GetDefaultCollectorConfig() *CollectorConfig {
	return &CollectorConfig{
		// PVE-specific (all enabled by default)
		BackupVMConfigs:         true,
		BackupClusterConfig:     true,
		BackupPVEFirewall:       true,
		BackupVZDumpConfig:      true,
		BackupPVEACL:            true,
		BackupPVEJobs:           true,
		BackupPVESchedules:      true,
		BackupPVEReplication:    true,
		BackupPVEBackupFiles:    true,
		BackupSmallPVEBackups:   false,
		MaxPVEBackupSizeBytes:   0,
		PVEBackupIncludePattern: "",
		BackupCephConfig:        true,
		CephConfigPath:          "/etc/ceph",

		// PBS-specific (all enabled by default)
		BackupDatastoreConfigs: true,
		BackupUserConfigs:      true,
		BackupRemoteConfigs:    true,
		BackupSyncJobs:         true,
		BackupVerificationJobs: true,
		BackupTapeConfigs:      true,
		BackupPruneSchedules:   true,
		BackupPxarFiles:        true,

		// System collection (all enabled by default)
		BackupNetworkConfigs:    true,
		BackupAptSources:        true,
		BackupCronJobs:          true,
		BackupSystemdServices:   true,
		BackupSSLCerts:          true,
		BackupSysctlConfig:      true,
		BackupKernelModules:     true,
		BackupFirewallRules:     true,
		BackupInstalledPackages: true,
		BackupScriptDir:         true,
		BackupCriticalFiles:     true,
		BackupSSHKeys:           true,
		BackupZFSConfig:         true,
		BackupRootHome:          true,
		BackupScriptRepository:  true,
		BackupUserHomes:         true,
		BackupConfigFile:        true,
		SystemRootPrefix:        "",

		PxarDatastoreConcurrency: 3,
		PxarIntraConcurrency:     4,
		PxarScanFanoutLevel:      2,
		PxarScanMaxRoots:         2048,
		PxarEnumWorkers:          4,
		PxarEnumBudgetMs:         0,
		PxarFileIncludePatterns:  nil,
		PxarFileExcludePatterns:  nil,

		ExcludePatterns:    append([]string(nil), defaultExcludePatterns...),
		CustomBackupPaths:  []string{},
		BackupBlacklist:    []string{},
		PVEConfigPath:      "/etc/pve",
		PVEClusterPath:     "/var/lib/pve-cluster",
		CorosyncConfigPath: "/etc/pve/corosync.conf",
		VzdumpConfigPath:   "/etc/vzdump.conf",
		PBSConfigPath:      "/etc/proxmox-backup",
		PBSDatastorePaths:  []string{},
	}
}

// CollectAll performs full backup collection based on Proxmox type
func (c *Collector) CollectAll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.logger.Info("Starting backup collection for %s", c.proxType)
	c.logger.Debug("Collector dry-run=%v tempDir=%s", c.dryRun, c.tempDir)

	switch c.proxType {
	case types.ProxmoxVE:
		c.logger.Debug("Invoking PVE-specific collectors (configs, jobs, schedules, storage metadata)")
		if err := c.CollectPVEConfigs(ctx); err != nil {
			return fmt.Errorf("PVE collection failed: %w", err)
		}
		c.logger.Debug("PVE-specific collection completed")
	case types.ProxmoxBS:
		c.logger.Debug("Invoking PBS-specific collectors (datastores, users, namespaces, pxar metadata)")
		if err := c.CollectPBSConfigs(ctx); err != nil {
			return fmt.Errorf("PBS collection failed: %w", err)
		}
		c.logger.Debug("PBS-specific collection completed")
	case types.ProxmoxUnknown:
		c.logger.Warning("Unknown Proxmox type, collecting generic system info only")
		c.logger.Debug("Skipping hypervisor-specific collection because type is unknown")
	}

	// Collect common system information (always collect)
	if err := ctx.Err(); err != nil {
		return err
	}
	c.logger.Debug("Collecting baseline system information (network/system files, commands, hardware data)")
	if err := c.CollectSystemInfo(ctx); err != nil {
		c.logger.Warning("System info collection had warnings: %v", err)
	}
	c.logger.Debug("Baseline system information collected successfully")

	stats := c.GetStats()
	c.logger.Debug("Collection completed: %d files, %d failed, %d dirs created",
		stats.FilesProcessed, stats.FilesFailed, stats.DirsCreated)

	return nil
}

// NOTE: CollectPVEConfigs, CollectPBSConfigs, and CollectSystemInfo are now in separate files:
// - collector_pve.go
// - collector_pbs.go
// - collector_system.go

// Helper functions

func (c *Collector) shouldExclude(path string) bool {
	if len(c.config.ExcludePatterns) == 0 {
		return false
	}

	candidates := uniqueCandidates(path, c.tempDir)

	for _, pattern := range c.config.ExcludePatterns {
		for _, candidate := range candidates {
			if matchesGlob(pattern, candidate) {
				c.logger.Debug("Excluding %s (matches pattern %s)", path, pattern)
				return true
			}
		}
	}
	return false
}

func uniqueCandidates(path, tempDir string) []string {
	base := filepath.Base(path)
	candidates := []string{path}
	if base != "" && base != "." && base != string(filepath.Separator) {
		candidates = append(candidates, base)
	}

	if rel, err := filepath.Rel("/", path); err == nil {
		if rel != "." && rel != "" {
			candidates = append(candidates, rel)
		}
	}

	if tempDir != "" {
		if relTemp, err := filepath.Rel(tempDir, path); err == nil {
			if relTemp != "." && relTemp != "" && relTemp != ".." {
				candidates = append(candidates, relTemp)
			}
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	unique := make([]string, 0, len(candidates))
	for _, cand := range candidates {
		if cand == "" {
			continue
		}
		if _, ok := seen[cand]; ok {
			continue
		}
		seen[cand] = struct{}{}
		unique = append(unique, cand)
	}
	return unique
}

func matchesGlob(pattern, candidate string) bool {
	normalizedPattern := filepath.ToSlash(pattern)
	normalizedCandidate := filepath.ToSlash(candidate)

	if matched, err := filepath.Match(normalizedPattern, normalizedCandidate); err == nil && matched {
		return true
	}

	if strings.Contains(normalizedPattern, "**") {
		regexPattern := globToRegex(normalizedPattern)
		matched, err := regexp.MatchString(regexPattern, normalizedCandidate)
		if err == nil && matched {
			return true
		}
	}

	return false
}

func globToRegex(pattern string) string {
	var builder strings.Builder
	builder.WriteString("^")

	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				builder.WriteString(".*")
				i++
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		case '[':
			builder.WriteByte('[')
			j := i + 1
			if j < len(runes) && (runes[j] == '!' || runes[j] == '^') {
				builder.WriteByte('^')
				j++
			}
			for ; j < len(runes) && runes[j] != ']'; j++ {
				switch runes[j] {
				case '\\':
					builder.WriteString("\\\\")
				default:
					builder.WriteRune(runes[j])
				}
			}
			if j >= len(runes) {
				builder.WriteString("\\[")
			} else {
				builder.WriteByte(']')
				i = j
			}
		case '\\':
			builder.WriteString("\\\\")
		default:
			builder.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}

	builder.WriteString("$")
	return builder.String()
}

func (c *Collector) ensureDir(path string) error {
	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would create directory: %s", path)
		return nil
	}

	created := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		created = true
	}

	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	if created {
		c.incDirsCreated()
	}
	return nil
}

func (c *Collector) safeCopyFile(ctx context.Context, src, dest, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.logger.Debug("Collecting %s: %s -> %s", description, src, dest)

	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Debug("%s not found: %s (skipping)", description, src)
			return nil
		}
		c.incFilesFailed()
		return fmt.Errorf("failed to stat %s: %w", src, err)
	}

	// Check if this file should be excluded
	if c.shouldExclude(src) {
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would copy file: %s -> %s", src, dest)
		c.incFilesProcessed()
		return nil
	}

	// Handle symbolic links by recreating the link
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			c.incFilesFailed()
			return fmt.Errorf("failed to read symlink %s: %w", src, err)
		}

		if err := c.ensureDir(filepath.Dir(dest)); err != nil {
			c.incFilesFailed()
			return err
		}

		// Remove existing file if present
		if _, err := os.Lstat(dest); err == nil {
			if err := os.Remove(dest); err != nil {
				c.incFilesFailed()
				return fmt.Errorf("failed to replace existing file %s: %w", dest, err)
			}
		}

		if err := os.Symlink(target, dest); err != nil {
			c.incFilesFailed()
			return fmt.Errorf("failed to create symlink %s -> %s: %w", dest, target, err)
		}

		c.incFilesProcessed()
		c.logger.Debug("Successfully copied symlink %s -> %s", dest, target)
		return nil
	}

	if !info.Mode().IsRegular() {
		// Skip non-regular files (devices, sockets, etc.) but count as processed
		c.logger.Debug("Skipping non-regular file: %s", src)
		return nil
	}

	// Ensure destination directory exists
	if err := c.ensureDir(filepath.Dir(dest)); err != nil {
		c.incFilesFailed()
		return err
	}

	// Open source file
	srcFile, err := os.Open(src)
	if err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to open %s: %w", src, err)
	}
	defer srcFile.Close()

	// Create destination file with restrictive permissions
	destFile, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to create %s: %w", dest, err)
	}
	defer destFile.Close()

	// Copy content
	written, err := io.Copy(destFile, srcFile)
	if err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to copy %s: %w", src, err)
	}

	c.incFilesProcessed()
	c.addBytesCollected(int64(written))
	c.logger.Debug("Successfully collected %s: %s", description, src)

	return nil
}

func (c *Collector) safeCopyDir(ctx context.Context, src, dest, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.logger.Debug("Collecting directory %s: %s -> %s", description, src, dest)

	if c.shouldExclude(src) {
		c.logger.Debug("Skipping directory %s due to exclusion pattern", src)
		return nil
	}

	if _, err := os.Stat(src); os.IsNotExist(err) {
		c.logger.Debug("%s not found: %s (skipping)", description, src)
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would copy directory: %s -> %s", src, dest)
		return nil
	}

	// Ensure destination exists
	if err := c.ensureDir(dest); err != nil {
		return err
	}

	// Walk source directory
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}

		if err != nil {
			return err
		}

		// Check if this path should be excluded
		if c.shouldExclude(path) {
			// If it's a directory, skip it entirely
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Calculate relative path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(dest, relPath)

		if info.IsDir() {
			return c.ensureDir(destPath)
		}

		return c.safeCopyFile(ctx, path, destPath, filepath.Base(path))
	})

	if err != nil {
		c.logger.Warning("Failed to copy directory %s: %v", description, err)
		return err
	}

	c.logger.Debug("Successfully collected %s: %s", description, src)
	return nil
}

func (c *Collector) safeCmdOutput(ctx context.Context, cmd, output, description string, critical bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.logger.Debug("Collecting %s via command: %s > %s", description, cmd, output)

	cmdParts := strings.Fields(cmd)
	if len(cmdParts) == 0 {
		return fmt.Errorf("empty command")
	}

	// Check if command exists
	if _, err := c.depLookPath(cmdParts[0]); err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command not available: %s", cmdParts[0])
		}
		c.logger.Debug("Command not available: %s (skipping %s)", cmdParts[0], description)
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command: %s > %s", cmd, output)
		return nil
	}

	cmdString := strings.Join(cmdParts, " ")
	out, err := c.depRunCommand(ctx, cmdParts[0], cmdParts[1:]...)
	if err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command `%s` failed for %s: %w (output: %s)", cmdString, description, err, summarizeCommandOutputText(string(out)))
		}
		c.logger.Warning("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Ensure the PBS CLI is available and has proper permissions. Output: %s",
			description,
			cmdString,
			err,
			summarizeCommandOutputText(string(out)),
		)
		return nil // Non-critical failure
	}

	if err := c.ensureDir(filepath.Dir(output)); err != nil {
		return err
	}
	if err := os.WriteFile(output, out, 0640); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to write output %s: %w", output, err)
	}

	c.incFilesProcessed()
	c.logger.Debug("Successfully collected %s via command: %s", description, cmdString)

	return nil
}

// safeCmdOutputWithPBSAuth executes a command with PBS authentication environment variables
// This enables automatic authentication for proxmox-backup-client commands
func (c *Collector) safeCmdOutputWithPBSAuth(ctx context.Context, cmd, output, description string, critical bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cmdParts := strings.Fields(cmd)
	if len(cmdParts) == 0 {
		return fmt.Errorf("empty command")
	}

	// Check if command exists
	if _, err := c.depLookPath(cmdParts[0]); err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command not available: %s", cmdParts[0])
		}
		c.logger.Debug("Command not available: %s (skipping %s)", cmdParts[0], description)
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command with PBS auth: %s > %s", cmd, output)
		return nil
	}

	// Set PBS authentication environment variables (if available)
	var extraEnv []string
	pbsAuthUsed := false
	if c.config.PBSRepository != "" {
		extraEnv = append(extraEnv, fmt.Sprintf("PBS_REPOSITORY=%s", c.config.PBSRepository))
		pbsAuthUsed = true
	}
	if c.config.PBSPassword != "" {
		extraEnv = append(extraEnv, fmt.Sprintf("PBS_PASSWORD=%s", c.config.PBSPassword))
		pbsAuthUsed = true
	}
	if c.config.PBSFingerprint != "" {
		extraEnv = append(extraEnv, fmt.Sprintf("PBS_FINGERPRINT=%s", c.config.PBSFingerprint))
		pbsAuthUsed = true
	}

	if pbsAuthUsed {
		c.logger.Debug("Using PBS authentication for command: %s", cmdParts[0])
	}

	cmdString := strings.Join(cmdParts, " ")
	out, err := c.depRunCommandWithEnv(ctx, extraEnv, cmdParts[0], cmdParts[1:]...)
	if err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command `%s` failed for %s: %w (output: %s)", cmdString, description, err, summarizeCommandOutputText(string(out)))
		}
		c.logger.Warning("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Ensure the PBS CLI is available and has proper permissions. Output: %s",
			description,
			cmdString,
			err,
			summarizeCommandOutputText(string(out)),
		)
		return nil // Non-critical failure
	}

	if err := c.ensureDir(filepath.Dir(output)); err != nil {
		return err
	}

	if err := os.WriteFile(output, out, 0640); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to write output %s: %w", output, err)
	}

	c.incFilesProcessed()
	c.logger.Debug("Successfully collected %s via PBS-authenticated command: %s", description, cmdString)

	return nil
}

// safeCmdOutputWithPBSAuthForDatastore executes a command with PBS authentication for a specific datastore
// This function appends the datastore name to the PBS_REPOSITORY environment variable
func (c *Collector) safeCmdOutputWithPBSAuthForDatastore(ctx context.Context, cmd, output, description, datastoreName string, critical bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cmdParts := strings.Fields(cmd)
	if len(cmdParts) == 0 {
		return fmt.Errorf("empty command")
	}

	// Check if command exists
	if _, err := c.depLookPath(cmdParts[0]); err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command not available: %s", cmdParts[0])
		}
		c.logger.Debug("Command not available: %s (skipping %s)", cmdParts[0], description)
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command with PBS auth for datastore %s: %s > %s", datastoreName, cmd, output)
		return nil
	}

	// Check if PBS credentials are configured
	if c.config.PBSRepository == "" && c.config.PBSPassword == "" {
		// No PBS credentials configured - skip this command gracefully
		c.logger.Warning("Skipping %s: PBS credentials not configured. Set PBS_REPOSITORY and PBS_PASSWORD in config or environment to collect namespace information.", description)
		return nil
	}

	// Build PBS_REPOSITORY with datastore
	repoWithDatastore := ""
	if c.config.PBSRepository != "" {
		// If repository already has a datastore (contains :), replace it
		// Otherwise append the datastore name
		repoWithDatastore = c.config.PBSRepository
		if strings.Contains(repoWithDatastore, ":") {
			// Replace existing datastore: "user@host:oldds" -> "user@host:newds"
			parts := strings.SplitN(repoWithDatastore, ":", 2)
			repoWithDatastore = fmt.Sprintf("%s:%s", parts[0], datastoreName)
		} else {
			// Append datastore: "user@host" -> "user@host:datastore"
			repoWithDatastore = fmt.Sprintf("%s:%s", repoWithDatastore, datastoreName)
		}
	} else {
		// No repository configured but we have password - use root@pam as default user
		repoWithDatastore = fmt.Sprintf("root@pam@localhost:%s", datastoreName)
		c.logger.Debug("Using default user root@pam for PBS repository")
	}

	var extraEnv []string
	extraEnv = append(extraEnv, fmt.Sprintf("PBS_REPOSITORY=%s", repoWithDatastore))
	c.logger.Debug("Using PBS_REPOSITORY=%s", repoWithDatastore)

	if c.config.PBSPassword != "" {
		extraEnv = append(extraEnv, fmt.Sprintf("PBS_PASSWORD=%s", c.config.PBSPassword))
		c.logger.Debug("Using PBS_PASSWORD (hidden)")
	}
	if c.config.PBSFingerprint != "" {
		extraEnv = append(extraEnv, fmt.Sprintf("PBS_FINGERPRINT=%s", c.config.PBSFingerprint))
		c.logger.Debug("Using PBS_FINGERPRINT=%s", c.config.PBSFingerprint)
	}

	cmdString := strings.Join(cmdParts, " ")
	out, err := c.depRunCommandWithEnv(ctx, extraEnv, cmdParts[0], cmdParts[1:]...)
	if err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command `%s` failed for %s: %w (output: %s)", cmdString, description, err, summarizeCommandOutputText(string(out)))
		}
		c.logger.Warning("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Ensure the PBS CLI is available and has proper permissions. Output: %s",
			description,
			cmdString,
			err,
			summarizeCommandOutputText(string(out)),
		)
		return nil // Non-critical failure
	}

	if err := c.ensureDir(filepath.Dir(output)); err != nil {
		return err
	}
	if err := os.WriteFile(output, out, 0640); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to write output %s: %w", output, err)
	}

	c.incFilesProcessed()
	c.logger.Debug("Successfully collected %s via PBS-authenticated command for datastore %s: %s", description, datastoreName, cmdString)

	return nil
}

func summarizeCommandOutput(buf *bytes.Buffer) string {
	return summarizeCommandOutputText(buf.String())
}

func summarizeCommandOutputText(text string) string {
	const maxLen = 2048
	output := strings.TrimSpace(text)
	if output == "" {
		return "(no stdout/stderr)"
	}
	output = strings.ReplaceAll(output, "\n", " | ")
	runes := []rune(output)
	if len(runes) > maxLen {
		runes = append(runes[:maxLen], '…')
	}
	return string(runes)
}

func sanitizeFilename(name string) string {
	if name == "" {
		return "entry"
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		"@", "_",
		":", "_",
	)
	clean := replacer.Replace(name)
	clean = strings.ReplaceAll(clean, "..", "_")
	if clean == "" {
		clean = "entry"
	}
	return clean
}

// GetStats returns current collection statistics
func (c *Collector) GetStats() *CollectionStats {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	snapshot := *c.stats
	return &snapshot
}

// IsClusteredPVE returns true if the current PVE collection detected a cluster.
func (c *Collector) IsClusteredPVE() bool {
	return c.clusteredPVE
}

func (c *Collector) writeReportFile(path string, data []byte) error {
	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would write report file: %s (%d bytes)", path, len(data))
		return nil
	}

	if err := c.ensureDir(filepath.Dir(path)); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to create report directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0640); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to write report %s: %w", path, err)
	}

	c.incFilesProcessed()
	c.addBytesCollected(int64(len(data)))
	c.logger.Debug("Successfully wrote report file: %s", path)
	return nil
}

func (c *Collector) captureCommandOutput(ctx context.Context, cmd, output, description string, critical bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	if _, err := c.depLookPath(parts[0]); err != nil {
		if critical {
			c.incFilesFailed()
			return nil, fmt.Errorf("critical command not available: %s", parts[0])
		}
		c.logger.Debug("Command not available: %s (skipping %s)", parts[0], description)
		return nil, nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command: %s > %s", cmd, output)
		return nil, nil
	}

	out, err := c.depRunCommand(ctx, parts[0], parts[1:]...)
	if err != nil {
		cmdString := strings.Join(parts, " ")
		if critical {
			c.incFilesFailed()
			return nil, fmt.Errorf("critical command `%s` failed for %s: %w (output: %s)", cmdString, description, err, summarizeCommandOutputText(string(out)))
		}
		c.logger.Warning("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Output: %s",
			description,
			cmdString,
			err,
			summarizeCommandOutputText(string(out)),
		)
		return nil, nil
	}

	if err := c.writeReportFile(output, out); err != nil {
		return nil, err
	}

	return out, nil
}

func (c *Collector) collectCommandMulti(ctx context.Context, cmd, output, description string, critical bool, mirrors ...string) error {
	if output == "" {
		return fmt.Errorf("primary output path cannot be empty for %s", description)
	}

	data, err := c.captureCommandOutput(ctx, cmd, output, description, critical)
	if err != nil {
		return err
	}
	if data == nil {
		return nil
	}

	for _, mirror := range mirrors {
		if mirror == "" {
			continue
		}
		if err := c.writeReportFile(mirror, data); err != nil {
			return err
		}
	}

	return nil
}

func (c *Collector) collectCommandOptional(ctx context.Context, cmd, output, description string, mirrors ...string) {
	if output == "" {
		c.logger.Debug("Optional command %s skipped: no primary output path", description)
		return
	}

	data, err := c.captureCommandOutput(ctx, cmd, output, description, false)
	if err != nil {
		c.logger.Debug("Optional command %s skipped: %v", description, err)
		return
	}
	if len(data) == 0 {
		return
	}

	for _, mirror := range mirrors {
		if mirror == "" {
			continue
		}
		if err := c.writeReportFile(mirror, data); err != nil {
			c.logger.Debug("Failed to mirror %s to %s: %v", description, mirror, err)
		}
	}
}

func (c *Collector) sampleDirectories(ctx context.Context, root string, maxDepth, limit int) ([]string, error) {
	results := make([]string, 0, limit)
	if limit <= 0 {
		return results, nil
	}

	startDirs, err := c.computePxarWorkerRoots(ctx, root, "directories")
	if err != nil {
		return results, err
	}

	if len(startDirs) == 0 {
		c.logger.Debug("PXAR sampleDirectories: root=%s completed (selected=0 visited=0 duration=0s)", root)
		return results, nil
	}

	stopErr := errors.New("directory sample limit reached")
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerLimit := c.config.PxarIntraConcurrency
	if workerLimit <= 0 {
		workerLimit = 1
	}

	var (
		wg           sync.WaitGroup
		sem          = make(chan struct{}, workerLimit)
		resMu        sync.Mutex
		progressMu   sync.Mutex
		errMu        sync.Mutex
		visited      int
		lastLog      = start
		firstErr     error
		limitReached bool
	)

	appendResult := func(rel string) (bool, bool) {
		resMu.Lock()
		defer resMu.Unlock()
		if limitReached {
			return false, true
		}
		results = append(results, filepath.ToSlash(rel))
		if len(results) >= limit {
			limitReached = true
			cancel()
			return true, true
		}
		return true, false
	}

	logProgress := func() {
		progressMu.Lock()
		defer progressMu.Unlock()
		visited++
		if time.Since(lastLog) > 2*time.Second {
			resMu.Lock()
			selected := len(results)
			resMu.Unlock()
			c.logger.Debug("PXAR sampleDirectories: root=%s visited=%d selected=%d", root, visited, selected)
			lastLog = time.Now()
		}
	}

	recordError := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}

	for _, startPath := range startDirs {
		if err := ctx.Err(); err != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(startDir string) {
			defer func() {
				<-sem
				wg.Done()
			}()

			walkErr := filepath.WalkDir(startDir, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}

				if err := ctx.Err(); err != nil {
					return err
				}

				if path == root {
					return nil
				}

				if c.shouldExclude(path) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}

				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}

				if d.IsDir() {
					logProgress()
					depth := strings.Count(rel, string(filepath.Separator))
					if depth >= maxDepth {
						return filepath.SkipDir
					}
					if _, hitLimit := appendResult(rel); hitLimit {
						return stopErr
					}
				}
				return nil
			})

			if walkErr != nil && !errors.Is(walkErr, stopErr) && !errors.Is(walkErr, context.Canceled) {
				recordError(walkErr)
			}
		}(startPath)
	}

	wg.Wait()

	if firstErr != nil {
		return results, firstErr
	}
	resMu.Lock()
	limitWasReached := limitReached
	resMu.Unlock()

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !limitWasReached {
		return results, err
	}

	resMu.Lock()
	selected := len(results)
	resMu.Unlock()

	progressMu.Lock()
	totalVisited := visited
	progressMu.Unlock()

	c.logger.Debug("PXAR sampleDirectories: root=%s completed (selected=%d visited=%d duration=%s)",
		root, selected, totalVisited, time.Since(start).Truncate(time.Millisecond))
	return results, nil
}

func (c *Collector) sampleFiles(ctx context.Context, root string, includePatterns, excludePatterns []string, maxDepth, limit int) ([]FileSummary, error) {
	results := make([]FileSummary, 0, limit)
	if limit <= 0 {
		return results, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return results, err
	}

	stopErr := errors.New("file sample limit reached")
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerLimit := c.config.PxarIntraConcurrency
	if workerLimit <= 0 {
		workerLimit = 1
	}

	var (
		wg           sync.WaitGroup
		sem          = make(chan struct{}, workerLimit)
		resMu        sync.Mutex
		progressMu   sync.Mutex
		errMu        sync.Mutex
		visited      int
		matched      int
		lastLog      = start
		firstErr     error
		limitReached bool
	)

	appendResult := func(summary FileSummary) (bool, bool) {
		resMu.Lock()
		defer resMu.Unlock()
		if limitReached {
			return false, true
		}
		results = append(results, summary)
		if len(results) >= limit {
			limitReached = true
			cancel()
			return true, true
		}
		return true, false
	}

	logProgress := func() {
		progressMu.Lock()
		defer progressMu.Unlock()
		visited++
		if time.Since(lastLog) > 2*time.Second {
			resMu.Lock()
			selected := len(results)
			resMu.Unlock()
			c.logger.Debug("PXAR sampleFiles: root=%s visited=%d matched=%d selected=%d", root, visited, matched, selected)
			lastLog = time.Now()
		}
	}

	incMatched := func() {
		progressMu.Lock()
		matched++
		progressMu.Unlock()
	}

	recordError := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}

	processFile := func(path string, info fs.FileInfo) error {
		if c.shouldExclude(path) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		logProgress()

		if len(excludePatterns) > 0 && matchAnyPattern(excludePatterns, filepath.Base(path), rel) {
			return nil
		}
		if len(includePatterns) > 0 && !matchAnyPattern(includePatterns, filepath.Base(path), rel) {
			return nil
		}
		incMatched()

		summary := FileSummary{
			RelativePath: filepath.ToSlash(rel),
			SizeBytes:    info.Size(),
			SizeHuman:    FormatBytes(info.Size()),
			ModTime:      info.ModTime(),
		}
		if _, hitLimit := appendResult(summary); hitLimit {
			return stopErr
		}
		return nil
	}

	limitTriggered := false

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			continue
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if err := processFile(path, info); err != nil {
			if errors.Is(err, stopErr) {
				limitTriggered = true
				break
			}
			return results, err
		}
	}

	if limitTriggered {
		resMu.Lock()
		selected := len(results)
		resMu.Unlock()
		progressMu.Lock()
		totalVisited := visited
		totalMatched := matched
		progressMu.Unlock()
		c.logger.Debug("PXAR sampleFiles: root=%s completed (selected=%d matched=%d visited=%d duration=%s)",
			root, selected, totalMatched, totalVisited, time.Since(start).Truncate(time.Millisecond))
		return results, nil
	}

	startDirs, err := c.computePxarWorkerRoots(ctx, root, "files")
	if err != nil {
		return results, err
	}

	if len(startDirs) == 0 {
		resMu.Lock()
		selected := len(results)
		resMu.Unlock()
		progressMu.Lock()
		totalVisited := visited
		totalMatched := matched
		progressMu.Unlock()
		c.logger.Debug("PXAR sampleFiles: root=%s completed (selected=%d matched=%d visited=%d duration=%s)",
			root, selected, totalMatched, totalVisited, time.Since(start).Truncate(time.Millisecond))
		return results, nil
	}

	for _, startPath := range startDirs {
		if err := ctx.Err(); err != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(startDir string) {
			defer func() {
				<-sem
				wg.Done()
			}()

			walkErr := filepath.WalkDir(startDir, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}

				if err := ctx.Err(); err != nil {
					return err
				}

				if d.IsDir() {
					if c.shouldExclude(path) {
						return filepath.SkipDir
					}
					rel, relErr := filepath.Rel(root, path)
					if relErr != nil {
						return relErr
					}
					depth := strings.Count(rel, string(filepath.Separator))
					if depth >= maxDepth {
						return filepath.SkipDir
					}
					return nil
				}

				info, infoErr := d.Info()
				if infoErr != nil {
					return nil
				}
				return processFile(path, info)
			})

			if walkErr != nil && !errors.Is(walkErr, stopErr) && !errors.Is(walkErr, context.Canceled) {
				recordError(walkErr)
			}
		}(startPath)
	}

	wg.Wait()

	if firstErr != nil {
		return results, firstErr
	}

	resMu.Lock()
	limitWasReached := limitReached
	selected := len(results)
	resMu.Unlock()

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !limitWasReached {
		return results, err
	}

	progressMu.Lock()
	totalVisited := visited
	totalMatched := matched
	progressMu.Unlock()

	c.logger.Debug("PXAR sampleFiles: root=%s completed (selected=%d matched=%d visited=%d duration=%s)",
		root, selected, totalMatched, totalVisited, time.Since(start).Truncate(time.Millisecond))
	return results, nil
}

func (c *Collector) computePxarWorkerRoots(ctx context.Context, root, purpose string) ([]string, error) {
	cacheKey := fmt.Sprintf("%s|fanout=%d|max=%d", root, c.config.PxarScanFanoutLevel, c.config.PxarScanMaxRoots)
	c.rootsMu.RLock()
	if cached, ok := c.rootsCache[cacheKey]; ok && len(cached) > 0 {
		result := append([]string(nil), cached...)
		c.rootsMu.RUnlock()
		c.logger.Debug("PXAR worker roots cache hit (%s): root=%s count=%d", purpose, root, len(result))
		return result, nil
	}
	c.rootsMu.RUnlock()

	fanout := c.config.PxarScanFanoutLevel
	if fanout < 1 {
		fanout = 1
	}
	maxRoots := c.config.PxarScanMaxRoots
	if maxRoots <= 0 {
		maxRoots = 2048
	}
	enumWorkers := c.config.PxarEnumWorkers
	if enumWorkers <= 0 {
		enumWorkers = 1
	}
	budgetMs := c.config.PxarEnumBudgetMs

	baseCtx, baseCancel := context.WithCancel(ctx)
	defer baseCancel()
	ctxFanout := baseCtx
	if budgetMs > 0 {
		ctxBudget, cancel := context.WithTimeout(baseCtx, time.Duration(budgetMs)*time.Millisecond)
		ctxFanout = ctxBudget
		defer cancel()
	}

	start := time.Now()
	c.logger.Debug("PXAR fanout enumeration (%s): root=%s fanout=%d max_roots=%d workers=%d budget_ms=%d",
		purpose, root, fanout, maxRoots, enumWorkers, budgetMs)

	levels := make(map[int][]string, fanout)
	selector := newPxarRootSelector(maxRoots)
	var selectorMu sync.Mutex
	queue := []string{root}
	var foundAny atomic.Bool
	stopOnCap := c.config.PxarStopOnCap

	const (
		pxarStopReasonNone int32 = iota
		pxarStopReasonCap
		pxarStopReasonBudget
	)
	var stopReason atomic.Int32

	var progressVisited atomic.Int64
	var progressScanned atomic.Int64
	var progressExcluded atomic.Int64
	var progressLeaves atomic.Int64
	var progressReadErr atomic.Int64
	var progressDepth atomic.Int64
	var progressCandidates atomic.Int64
	var progressCapped atomic.Bool

	var progressStop chan struct{}
	if c.logger.GetLevel() >= types.LogLevelDebug {
		progressStop = make(chan struct{})
		ticker := time.NewTicker(5 * time.Second)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					c.logger.Debug("PXAR progress (%s): depth=%d visited=%d scanned=%d excluded=%d leaves=%d candidates=%d capped=%v elapsed=%s",
						purpose,
						progressDepth.Load(),
						progressVisited.Load(),
						progressScanned.Load(),
						progressExcluded.Load(),
						progressLeaves.Load(),
						progressCandidates.Load(),
						progressCapped.Load(),
						time.Since(start).Truncate(time.Millisecond))
				case <-progressStop:
					return
				case <-ctxFanout.Done():
					return
				}
			}
		}()
		defer close(progressStop)
	}

fanoutLoop:
	for depth := 0; depth < fanout; depth++ {
		if len(queue) == 0 {
			break
		}
		if err := ctxFanout.Err(); err != nil {
			break
		}

		progressDepth.Store(int64(depth + 1))
		next := make([]string, 0, len(queue))
		var nextMu sync.Mutex

		jobCh := make(chan string)
		var wg sync.WaitGroup

		workerCount := enumWorkers
		if workerCount > len(queue) {
			workerCount = len(queue)
		}
		if workerCount < 1 {
			workerCount = 1
		}

		shuffledBases := append([]string(nil), queue...)
		shuffleStringsDeterministic(shuffledBases, deterministicSeed(root, purpose, fmt.Sprintf("depth-%d", depth)))

		for w := 0; w < workerCount; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for basePath := range jobCh {
					if err := ctxFanout.Err(); err != nil {
						return
					}

					progressVisited.Add(1)
					entries, err := os.ReadDir(basePath)
					if err != nil {
						progressReadErr.Add(1)
						continue
					}
					progressScanned.Add(int64(len(entries)))
					shuffleDirEntriesDeterministic(entries, deterministicSeed(basePath, purpose, fmt.Sprintf("depth-%d", depth)))

					for _, entry := range entries {
						if err := ctxFanout.Err(); err != nil {
							return
						}

						if !entry.IsDir() {
							continue
						}
						child := filepath.Join(basePath, entry.Name())
						if c.shouldExclude(child) {
							progressExcluded.Add(1)
							continue
						}
						foundAny.Store(true)

						level := depth + 1
						if level < fanout {
							nextMu.Lock()
							levels[level] = append(levels[level], child)
							next = append(next, child)
							nextMu.Unlock()
							continue
						}

						selectorMu.Lock()
						prevCapped := selector.capped
						selector.consider(child)
						progressLeaves.Add(1)
						progressCandidates.Store(int64(selector.total))
						currentCapped := selector.capped
						selectorMu.Unlock()

						if !prevCapped && currentCapped {
							progressCapped.Store(true)
							c.logger.Debug("PXAR progress (%s): candidate cap reached (limit=%d) at total=%d",
								purpose, maxRoots, selector.total)
							if stopOnCap {
								if stopReason.CompareAndSwap(pxarStopReasonNone, pxarStopReasonCap) {
									c.logger.Debug("PXAR early termination (%s): stop_on_cap=true limit=%d candidates=%d depth=%d elapsed=%s",
										purpose,
										maxRoots,
										selector.total,
										depth+1,
										time.Since(start).Truncate(time.Millisecond))
								}
								baseCancel()
								return
							}
						}
					}
				}
			}()
		}

		for _, base := range shuffledBases {
			select {
			case <-ctxFanout.Done():
				break fanoutLoop
			default:
				jobCh <- base
			}
		}
		close(jobCh)
		wg.Wait()

		if err := ctxFanout.Err(); err != nil {
			break
		}

		c.logger.Debug("PXAR depth %d/%d done: bases=%d next_bases=%d leaves=%d excluded=%d readErrs=%d elapsed=%s",
			depth+1,
			fanout,
			len(queue),
			len(next),
			progressLeaves.Load(),
			progressExcluded.Load(),
			progressReadErr.Load(),
			time.Since(start).Truncate(time.Millisecond))
		queue = next
	}

	if budgetMs > 0 && errors.Is(ctxFanout.Err(), context.DeadlineExceeded) {
		stopReason.CompareAndSwap(pxarStopReasonNone, pxarStopReasonBudget)
		c.logger.Debug("PXAR early termination (%s): enumeration budget exceeded (%dms)", purpose, budgetMs)
	}

	if !foundAny.Load() {
		return nil, nil
	}

	roots := selector.results()
	capped := selector.capped
	totalCandidates := selector.total
	if len(roots) == 0 {
		for level := fanout - 1; level >= 1; level-- {
			if dirs := levels[level]; len(dirs) > 0 {
				c.logger.Debug("PXAR fallback to level=%d: dirs=%d (limit=%d)", level, len(dirs), maxRoots)
				roots = uniquePaths(dirs)
				totalCandidates = len(dirs)
				if maxRoots > 0 && len(roots) > maxRoots {
					c.logger.Debug("PXAR downsample: from=%d to=%d", len(roots), maxRoots)
					roots = downsampleRoots(roots, maxRoots)
					capped = true
				}
				break
			}
		}
	}

	if len(roots) == 0 {
		return nil, nil
	}

	c.logger.Debug("PXAR worker roots (%s): root=%s fanout=%d count=%d candidates=%d capped=%v duration=%s",
		purpose,
		root,
		fanout,
		len(roots),
		totalCandidates,
		capped,
		time.Since(start).Truncate(time.Millisecond))
	c.rootsMu.Lock()
	c.rootsCache[cacheKey] = append([]string(nil), roots...)
	c.rootsMu.Unlock()
	return roots, nil
}

func downsampleRoots(roots []string, limit int) []string {
	if limit <= 0 || len(roots) <= limit {
		return roots
	}
	step := len(roots) / limit
	if step <= 1 {
		return roots[:limit]
	}
	result := make([]string, 0, limit)
	for i := 0; i < len(roots) && len(result) < limit; i += step {
		result = append(result, roots[i])
	}
	if len(result) < limit {
		for i := len(roots) - 1; i >= 0 && len(result) < limit; i-- {
			result = append(result, roots[i])
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func shuffleStringsDeterministic(items []string, seed int64) {
	if len(items) <= 1 {
		return
	}
	r := rand.New(rand.NewSource(seed))
	for i := len(items) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		items[i], items[j] = items[j], items[i]
	}
}

func shuffleDirEntriesDeterministic(entries []fs.DirEntry, seed int64) {
	if len(entries) <= 1 {
		return
	}
	r := rand.New(rand.NewSource(seed))
	for i := len(entries) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		entries[i], entries[j] = entries[j], entries[i]
	}
}

func deterministicSeed(parts ...string) int64 {
	hasher := fnv.New64a()
	for _, p := range parts {
		_, _ = hasher.Write([]byte(p))
		_, _ = hasher.Write([]byte{0})
	}
	return int64(hasher.Sum64())
}

type pxarRootCandidate struct {
	path   string
	weight uint32
}

type pxarRootSelector struct {
	limit     int
	items     []pxarRootCandidate
	total     int
	capped    bool
	maxIdx    int
	maxWeight uint32
}

func newPxarRootSelector(limit int) *pxarRootSelector {
	return &pxarRootSelector{
		limit:  limit,
		maxIdx: -1,
	}
}

func (s *pxarRootSelector) consider(path string) {
	s.total++
	if s.limit <= 0 {
		s.items = append(s.items, pxarRootCandidate{path: path})
		return
	}
	weight := hashPath(path)
	if len(s.items) < s.limit {
		s.items = append(s.items, pxarRootCandidate{path: path, weight: weight})
		if weight > s.maxWeight || s.maxIdx == -1 {
			s.maxWeight = weight
			s.maxIdx = len(s.items) - 1
		}
		return
	}
	s.capped = true
	if weight >= s.maxWeight {
		return
	}
	s.items[s.maxIdx] = pxarRootCandidate{path: path, weight: weight}
	s.recomputeMax()
}

func (s *pxarRootSelector) recomputeMax() {
	if len(s.items) == 0 {
		s.maxIdx = -1
		s.maxWeight = 0
		return
	}
	maxIdx := 0
	maxWeight := s.items[0].weight
	for i := 1; i < len(s.items); i++ {
		if s.items[i].weight > maxWeight {
			maxWeight = s.items[i].weight
			maxIdx = i
		}
	}
	s.maxIdx = maxIdx
	s.maxWeight = maxWeight
}

func (s *pxarRootSelector) results() []string {
	if len(s.items) == 0 {
		return nil
	}
	roots := make([]string, len(s.items))
	for i, item := range s.items {
		roots[i] = item.path
	}
	return uniquePaths(roots)
}

func hashPath(path string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(path))
	return h.Sum32()
}

func uniquePaths(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		unique = append(unique, path)
	}
	return unique
}

func matchAnyPattern(patterns []string, name, relative string) bool {
	if len(patterns) == 0 {
		return true
	}
	normalizedRel := filepath.ToSlash(relative)
	for _, pattern := range patterns {
		p := filepath.ToSlash(pattern)
		if ok, _ := filepath.Match(p, normalizedRel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, filepath.ToSlash(name)); ok {
			return true
		}
	}
	return false
}
