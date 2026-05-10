// Package backup contains collection, archive, manifest, and optimization helpers.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// CollectionStats tracks statistics during backup collection
type CollectionStats struct {
	FilesProcessed int64
	FilesFailed    int64
	FilesNotFound  int64
	FilesSkipped   int64
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
	logger           *logging.Logger
	config           *CollectorConfig
	stats            *CollectionStats
	statsMu          sync.Mutex
	tempDir          string
	proxType         types.ProxmoxType
	dryRun           bool
	deps             CollectorDeps
	unprivilegedOnce sync.Once
	unprivilegedCtx  unprivilegedContainerContext

	// clusteredPVE records whether cluster mode was detected during PVE collection.
	clusteredPVE bool

	// Manifest tracking for backup contents
	pbsManifest    map[string]ManifestEntry
	pveManifest    map[string]ManifestEntry
	systemManifest map[string]ManifestEntry
}

var osSymlink = os.Symlink
var osReadlink = os.Readlink
var osOpen = os.Open
var osOpenFile = func(name string, flag int, perm os.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(name, flag, perm)
}

func (c *Collector) incFilesProcessed() {
	atomic.AddInt64(&c.stats.FilesProcessed, 1)
}

func (c *Collector) incFilesFailed() {
	atomic.AddInt64(&c.stats.FilesFailed, 1)
}

func (c *Collector) incFilesNotFound() {
	atomic.AddInt64(&c.stats.FilesNotFound, 1)
}

func (c *Collector) incFilesSkipped() {
	atomic.AddInt64(&c.stats.FilesSkipped, 1)
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

type CommandSpec struct {
	Name string
	Args []string
}

func commandSpec(name string, args ...string) CommandSpec {
	return CommandSpec{Name: strings.TrimSpace(name), Args: append([]string(nil), args...)}
}

func (s CommandSpec) validate() error {
	if s.Name == "" {
		return fmt.Errorf("empty command")
	}
	if strings.ContainsAny(s.Name, `/\`) {
		return fmt.Errorf("command name must not contain path separators: %s", s.Name)
	}
	for _, arg := range s.Args {
		for _, r := range arg {
			if r == 0 {
				return fmt.Errorf("command argument contains NUL byte")
			}
		}
	}
	return nil
}

func (s CommandSpec) String() string {
	if len(s.Args) == 0 {
		return s.Name
	}
	return s.Name + " " + strings.Join(s.Args, " ")
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
	PveshTimeoutSeconds     int
	FsIoTimeoutSeconds      int

	// PBS-specific collection options
	BackupDatastoreConfigs     bool
	BackupPBSS3Endpoints       bool
	BackupPBSNodeConfig        bool
	BackupPBSAcmeAccounts      bool
	BackupPBSAcmePlugins       bool
	BackupPBSMetricServers     bool
	BackupPBSTrafficControl    bool
	BackupPBSNotifications     bool
	BackupPBSNotificationsPriv bool
	BackupUserConfigs          bool
	BackupRemoteConfigs        bool
	BackupSyncJobs             bool
	BackupVerificationJobs     bool
	BackupTapeConfigs          bool
	BackupPBSNetworkConfig     bool
	BackupPruneSchedules       bool
	BackupPxarFiles            bool

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
	if err := c.validateExcludePatterns(); err != nil {
		return err
	}

	if !c.hasCollectionOptionEnabled() {
		return fmt.Errorf("at least one backup option must be enabled")
	}

	c.normalizePxarConcurrency()
	if c.MaxPVEBackupSizeBytes < 0 {
		return fmt.Errorf("MAX_PVE_BACKUP_SIZE must be >= 0")
	}
	c.normalizeCommandTimeouts()
	if c.SystemRootPrefix != "" && !filepath.IsAbs(c.SystemRootPrefix) {
		return fmt.Errorf("system root prefix must be an absolute path")
	}

	return nil
}

func (c *CollectorConfig) validateExcludePatterns() error {
	for i, pattern := range c.ExcludePatterns {
		if pattern == "" {
			return fmt.Errorf("exclude pattern at index %d is empty", i)
		}
		if _, err := filepath.Match(pattern, "test"); err != nil {
			return fmt.Errorf("invalid glob pattern at index %d: %s (error: %w)", i, pattern, err)
		}
	}
	return nil
}

func (c *CollectorConfig) hasCollectionOptionEnabled() bool {
	if len(c.CustomBackupPaths) > 0 {
		return true
	}
	for _, enabled := range c.collectionOptionFlags() {
		if enabled {
			return true
		}
	}
	return false
}

func (c *CollectorConfig) collectionOptionFlags() []bool {
	return []bool{
		c.BackupVMConfigs, c.BackupClusterConfig,
		c.BackupPVEFirewall, c.BackupVZDumpConfig, c.BackupPVEACL,
		c.BackupPVEJobs, c.BackupPVESchedules, c.BackupPVEReplication,
		c.BackupPVEBackupFiles, c.BackupCephConfig,
		c.BackupDatastoreConfigs, c.BackupPBSS3Endpoints, c.BackupPBSNodeConfig,
		c.BackupPBSAcmeAccounts, c.BackupPBSAcmePlugins, c.BackupPBSMetricServers,
		c.BackupPBSTrafficControl, c.BackupPBSNotifications, c.BackupPBSNotificationsPriv,
		c.BackupUserConfigs, c.BackupRemoteConfigs,
		c.BackupSyncJobs, c.BackupVerificationJobs, c.BackupTapeConfigs,
		c.BackupPBSNetworkConfig, c.BackupPruneSchedules, c.BackupPxarFiles,
		c.BackupNetworkConfigs, c.BackupAptSources, c.BackupCronJobs,
		c.BackupSystemdServices, c.BackupSSLCerts, c.BackupSysctlConfig,
		c.BackupKernelModules, c.BackupFirewallRules,
		c.BackupInstalledPackages, c.BackupScriptDir, c.BackupCriticalFiles,
		c.BackupSSHKeys, c.BackupZFSConfig, c.BackupConfigFile,
		c.BackupRootHome, c.BackupScriptRepository, c.BackupUserHomes,
	}
}

func (c *CollectorConfig) normalizePxarConcurrency() {
	if c.PxarDatastoreConcurrency <= 0 {
		c.PxarDatastoreConcurrency = 3
	}
}

func (c *CollectorConfig) normalizeCommandTimeouts() {
	if c.PveshTimeoutSeconds < 0 {
		c.PveshTimeoutSeconds = 15
	}
	if c.FsIoTimeoutSeconds < 0 {
		c.FsIoTimeoutSeconds = 30
	}
}

// NewCollector creates a new backup collector
func NewCollector(logger *logging.Logger, config *CollectorConfig, tempDir string, proxType types.ProxmoxType, dryRun bool) *Collector {
	return NewCollectorWithDeps(logger, config, tempDir, proxType, dryRun, defaultCollectorDeps())
}

// NewCollectorWithDeps creates a collector with explicit dependency overrides (for testing).
func NewCollectorWithDeps(logger *logging.Logger, config *CollectorConfig, tempDir string, proxType types.ProxmoxType, dryRun bool, deps CollectorDeps) *Collector {
	return &Collector{
		logger:   logger,
		config:   config,
		stats:    &CollectionStats{},
		tempDir:  tempDir,
		proxType: proxType,
		dryRun:   dryRun,
		deps:     deps,
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
		PveshTimeoutSeconds:     15,
		FsIoTimeoutSeconds:      30,

		// PBS-specific (all enabled by default)
		BackupDatastoreConfigs:     true,
		BackupPBSS3Endpoints:       true,
		BackupPBSNodeConfig:        true,
		BackupPBSAcmeAccounts:      true,
		BackupPBSAcmePlugins:       true,
		BackupPBSMetricServers:     true,
		BackupPBSTrafficControl:    true,
		BackupPBSNotifications:     true,
		BackupPBSNotificationsPriv: true,
		BackupUserConfigs:          true,
		BackupRemoteConfigs:        true,
		BackupSyncJobs:             true,
		BackupVerificationJobs:     true,
		BackupTapeConfigs:          true,
		BackupPBSNetworkConfig:     true,
		BackupPruneSchedules:       true,
		BackupPxarFiles:            true,

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
	case types.ProxmoxDual:
		c.logger.Debug("Invoking dual-role collectors (PVE + PBS recipes, shared system/common once)")
		if err := c.CollectDualConfigs(ctx); err != nil {
			return fmt.Errorf("dual collection failed: %w", err)
		}
		c.logger.Debug("Dual-role collection completed")
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

func FindExcludeMatch(patterns []string, path, tempDir, systemRootPrefix string) (bool, string) {
	if len(patterns) == 0 {
		return false, ""
	}

	candidates := uniqueCandidates(path, tempDir, systemRootPrefix)
	if len(candidates) == 0 {
		return false, ""
	}

	for _, pattern := range patterns {
		for _, candidate := range candidates {
			if matchesGlob(pattern, candidate) {
				return true, pattern
			}
		}
	}
	return false, ""
}

func (c *Collector) shouldExclude(path string) bool {
	if c == nil || c.config == nil {
		return false
	}
	excluded, pattern := FindExcludeMatch(c.config.ExcludePatterns, path, c.tempDir, c.config.SystemRootPrefix)
	if excluded {
		c.logger.Debug("Excluding %s (matches pattern %s)", path, pattern)
	}
	return excluded
}

func (c *Collector) withTemporaryExcludes(extra []string, fn func() error) error {
	if fn == nil {
		return nil
	}
	if c == nil || c.config == nil || len(extra) == 0 {
		return fn()
	}

	seen := make(map[string]struct{}, len(extra))
	normalized := make([]string, 0, len(extra))
	for _, pattern := range extra {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		normalized = append(normalized, pattern)
	}
	if len(normalized) == 0 {
		return fn()
	}

	original := append([]string(nil), c.config.ExcludePatterns...)
	c.config.ExcludePatterns = append(c.config.ExcludePatterns, normalized...)
	defer func() { c.config.ExcludePatterns = original }()

	return fn()
}

func uniqueCandidates(path, tempDir, systemRootPrefix string) []string {
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

	if systemRootPrefix != "" && systemRootPrefix != string(filepath.Separator) {
		prefix := filepath.Clean(systemRootPrefix)
		clean := filepath.Clean(path)
		if clean == prefix || strings.HasPrefix(clean, prefix+string(filepath.Separator)) {
			if relPrefix, err := filepath.Rel(prefix, clean); err == nil {
				if relPrefix != "." && relPrefix != "" && relPrefix != ".." && !strings.HasPrefix(relPrefix, ".."+string(filepath.Separator)) {
					candidates = append(candidates, filepath.Join(string(filepath.Separator), relPrefix))
				}
			}
		}
	}

	if tempDir != "" {
		if relTemp, err := filepath.Rel(tempDir, path); err == nil {
			if relTemp != "." && relTemp != "" && relTemp != ".." {
				candidates = append(candidates, relTemp)
				candidates = append(candidates, filepath.Join(string(filepath.Separator), relTemp))
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

func preservedMode(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

func (c *Collector) applyMetadata(dest string, info os.FileInfo) {
	if info == nil {
		return
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok {
		if err := os.Chown(dest, int(stat.Uid), int(stat.Gid)); err != nil {
			c.logger.Debug("Failed to chown %s: %v", dest, err)
		}
	}

	if err := os.Chmod(dest, preservedMode(info.Mode())); err != nil {
		c.logger.Debug("Failed to chmod %s: %v", dest, err)
	}

	if ok {
		atime := time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
		mtime := time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
		if err := os.Chtimes(dest, atime, mtime); err != nil {
			c.logger.Debug("Failed to set timestamps on %s: %v", dest, err)
		}
	}
}

func lstatOrNil(path string) os.FileInfo {
	info, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	return info
}

func (c *Collector) applyDirectoryMetadataFromSource(srcDir, destDir string) {
	if c.tempDir == "" {
		return
	}

	rel, err := filepath.Rel(c.tempDir, destDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return
	}

	c.applyMetadata(destDir, lstatOrNil(srcDir))
}

func (c *Collector) applySymlinkOwnership(dest string, info os.FileInfo) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	if err := os.Lchown(dest, int(stat.Uid), int(stat.Gid)); err != nil {
		c.logger.Debug("Failed to lchown symlink %s: %v", dest, err)
	}
}

func (c *Collector) safeCopyFile(ctx context.Context, src, dest, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.logger.Debug("Collecting %s: %s -> %s", description, src, dest)

	info, found, err := c.statCopySource(src, description)
	if err != nil || !found {
		return err
	}

	if c.shouldSkipCopy(src, dest) {
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would copy file: %s -> %s", src, dest)
		c.incFilesProcessed()
		return nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return c.copySymlinkFile(src, dest, info)
	}

	if !info.Mode().IsRegular() {
		c.logger.Debug("Skipping non-regular file: %s", src)
		return nil
	}

	return c.copyRegularFile(src, dest, description, info)
}

func (c *Collector) statCopySource(src, description string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(src)
	if err != nil {
		if os.IsNotExist(err) {
			c.logger.Debug("%s not found: %s (skipping)", description, src)
			return nil, false, nil
		}
		c.incFilesFailed()
		return nil, false, fmt.Errorf("failed to stat %s: %w", src, err)
	}
	return info, true, nil
}

func (c *Collector) shouldSkipCopy(src, dest string) bool {
	if c.shouldExclude(src) || c.shouldExclude(dest) {
		c.incFilesSkipped()
		return true
	}
	return false
}

func (c *Collector) copySymlinkFile(src, dest string, info os.FileInfo) error {
	target, err := osReadlink(src)
	if err != nil {
		c.incFilesFailed()
		return fmt.Errorf("symlink read failed - path: %s: %w", src, err)
	}

	if err := c.prepareCopyDestination(src, dest); err != nil {
		c.incFilesFailed()
		return err
	}

	if err := c.removeExistingSymlinkDestination(dest); err != nil {
		c.incFilesFailed()
		return err
	}

	if err := osSymlink(target, dest); err != nil {
		c.incFilesFailed()
		return fmt.Errorf("symlink creation failed - source: %s - target: %s - absolute: %v: %w",
			src, target, filepath.IsAbs(target), err)
	}

	c.applySymlinkOwnership(dest, info)
	c.incFilesProcessed()
	c.logger.Debug("Successfully copied symlink %s -> %s", dest, target)
	return nil
}

func (c *Collector) prepareCopyDestination(src, dest string) error {
	if err := c.ensureDir(filepath.Dir(dest)); err != nil {
		return err
	}
	c.applyDirectoryMetadataFromSource(filepath.Dir(src), filepath.Dir(dest))
	return nil
}

func (c *Collector) removeExistingSymlinkDestination(dest string) error {
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Remove(dest); err != nil {
			return fmt.Errorf("file replacement failed - path: %s: %w", dest, err)
		}
	}
	return nil
}

func (c *Collector) copyRegularFile(src, dest, description string, info os.FileInfo) (err error) {
	if err := c.prepareCopyDestination(src, dest); err != nil {
		c.incFilesFailed()
		return err
	}

	srcFile, err := osOpen(src)
	if err != nil {
		c.incFilesFailed()
		return fmt.Errorf("failed to open %s: %w", src, err)
	}
	defer closeIntoErr(&err, srcFile, "close source file")

	written, err := copyRegularFileContents(srcFile, src, dest)
	if err != nil {
		c.incFilesFailed()
		return err
	}

	c.applyMetadata(dest, info)

	c.incFilesProcessed()
	c.addBytesCollected(int64(written))
	c.logger.Debug("Successfully collected %s: %s", description, src)

	return nil
}

func copyRegularFileContents(srcFile io.Reader, src, dest string) (int64, error) {
	destFile, err := osOpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 0, fmt.Errorf("failed to create %s: %w", dest, err)
	}

	written, err := io.Copy(destFile, srcFile)
	closeErr := destFile.Close()
	if err != nil {
		return 0, fmt.Errorf("failed to copy %s: %w", src, err)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("failed to close %s: %w", dest, closeErr)
	}
	return written, nil
}

func (c *Collector) safeCopyDir(ctx context.Context, src, dest, description string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.logger.Debug("Collecting directory %s: %s -> %s", description, src, dest)

	if c.shouldExclude(src) || c.shouldExclude(dest) {
		c.logger.Debug("Skipping directory %s due to exclusion pattern", src)
		c.incFilesSkipped()
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

		// Calculate relative path and destination path for archive matching.
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(dest, relPath)

		// Check if this path should be excluded
		if c.shouldExclude(path) || c.shouldExclude(destPath) {
			// If it's a directory, skip it entirely
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			if err := c.ensureDir(destPath); err != nil {
				return err
			}
			c.applyMetadata(destPath, info)
			return nil
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

type commandRunClassification int

const (
	commandRunSucceeded commandRunClassification = iota
	commandRunSkipped
	commandRunNonCriticalFailure
	commandRunDowngradedToSkip
	commandRunCriticalFailure
)

type commandRunOptions struct {
	output                string
	description           string
	caller                string
	critical              bool
	logCollection         bool
	handleSystemctlStatus bool
	debugNonCritical      bool
}

type commandRunResult struct {
	output         []byte
	classification commandRunClassification
	exitCode       int
	outputSummary  string
	contextInfo    unprivilegedContainerContext
}

func (c *Collector) runAndClassifyCommand(ctx context.Context, spec CommandSpec, opts commandRunOptions) (commandRunResult, error) {
	result := commandRunResult{classification: commandRunSkipped, exitCode: -1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := spec.validate(); err != nil {
		return result, err
	}

	if opts.output != "" && c.shouldExclude(opts.output) {
		c.logger.Debug("Skipping %s: output %s excluded by pattern", opts.description, opts.output)
		c.incFilesSkipped()
		return result, nil
	}

	cmdString := spec.String()
	if opts.logCollection {
		c.logger.Debug("Collecting %s via command: %s > %s", opts.description, cmdString, opts.output)
	}

	if _, err := c.depLookPath(spec.Name); err != nil {
		if opts.critical {
			c.incFilesFailed()
			result.classification = commandRunCriticalFailure
			return result, fmt.Errorf("critical command not available: %s", spec.Name)
		}
		c.logger.Debug("Command not available: %s (skipping %s)", spec.Name, opts.description)
		return result, nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command: %s > %s", cmdString, opts.output)
		return result, nil
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Name == "pvesh" && c.config != nil && c.config.PveshTimeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(c.config.PveshTimeoutSeconds)*time.Second)
	}
	if cancel != nil {
		defer cancel()
	}

	out, err := c.depRunCommand(runCtx, spec.Name, spec.Args...)
	result.output = out
	if err != nil {
		result.outputSummary = summarizeCommandOutputText(string(out))
		if opts.critical {
			c.incFilesFailed()
			result.classification = commandRunCriticalFailure
			return result, fmt.Errorf("critical command `%s` failed for %s: %w (output: %s)", cmdString, opts.description, err, result.outputSummary)
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		outputText := strings.TrimSpace(string(out))
		result.exitCode = exitCode
		result.outputSummary = summarizeCommandOutputText(outputText)
		result.classification = commandRunNonCriticalFailure

		c.logger.Debug("Non-critical command failed (%s): description=%q cmd=%q exitCode=%d err=%v", opts.caller, opts.description, cmdString, exitCode, err)
		c.logger.Debug("Non-critical command output summary (%s): %s", opts.caller, result.outputSummary)

		ctxInfo := c.depDetectUnprivilegedContainer()
		result.contextInfo = ctxInfo
		c.logger.Debug("Privilege context evaluation: detected=%t details=%q", ctxInfo.Detected, strings.TrimSpace(ctxInfo.Details))

		reason := ""
		if ctxInfo.Detected {
			c.logger.Debug("Privilege-sensitive allowlist: command=%q allowlisted=%t", spec.Name, isPrivilegeSensitiveCommand(spec.Name))
			match := privilegeSensitiveFailureMatch(spec.Name, exitCode, outputText)
			reason = match.Reason
			c.logger.Debug("Privilege-sensitive classification: command=%q matched=%t match=%q reason=%q", spec.Name, reason != "", match.Match, reason)
		} else {
			c.logger.Debug("Privilege-sensitive downgrade not considered: limited-privilege context not detected (command=%q)", spec.Name)
		}

		if ctxInfo.Detected && reason != "" {
			result.classification = commandRunDowngradedToSkip
			c.logger.Debug("Downgrading WARNING->SKIP: description=%q cmd=%q exitCode=%d", opts.description, cmdString, exitCode)

			c.logger.Skip("Skipping %s: %s (Expected with limited privileges).", opts.description, reason)
			c.logger.Debug("SKIP context (privilege-sensitive): description=%q cmd=%q exitCode=%d err=%v contextDetails=%q", opts.description, cmdString, exitCode, err, strings.TrimSpace(ctxInfo.Details))
			c.logger.Debug("SKIP output summary for %s: %s", opts.description, result.outputSummary)
			return result, nil
		}

		if ctxInfo.Detected {
			if opts.handleSystemctlStatus {
				c.logger.Debug("No privilege-sensitive downgrade applied: command=%q did not match known patterns; continuing with standard handling", spec.Name)
			} else {
				c.logger.Debug("No privilege-sensitive downgrade applied: command=%q did not match known patterns; emitting WARNING", spec.Name)
			}
		}

		if opts.handleSystemctlStatus && spec.Name == "systemctl" && len(spec.Args) >= 2 && spec.Args[0] == "status" {
			unit := spec.Args[len(spec.Args)-1]
			if exitCode == 4 || strings.Contains(outputText, "could not be found") {
				c.logger.Warning("Skipping %s: %s.service not found (not installed?). Set BACKUP_FIREWALL_RULES=false to disable.",
					opts.description,
					unit,
				)
				return result, nil
			}
			if strings.Contains(outputText, "Failed to connect to system scope bus") || strings.Contains(outputText, "System has not been booted with systemd") {
				c.logger.Warning("Skipping %s: systemd is not available/accessible in this environment. Non-critical; backup continues. Output: %s",
					opts.description,
					result.outputSummary,
				)
				return result, nil
			}
		}

		if opts.handleSystemctlStatus {
			c.logger.Warning("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Output: %s",
				opts.description,
				cmdString,
				err,
				result.outputSummary,
			)
		} else if opts.debugNonCritical {
			c.logger.Debug("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Output: %s",
				opts.description,
				cmdString,
				err,
				result.outputSummary,
			)
		} else {
			c.logger.Warning("Skipping %s: command `%s` failed (%v). Non-critical; backup continues. Ensure the required CLI is available and has proper permissions. Output: %s",
				opts.description,
				cmdString,
				err,
				result.outputSummary,
			)
		}
		return result, nil
	}

	result.classification = commandRunSucceeded
	return result, nil
}

func (c *Collector) safeCmdOutput(ctx context.Context, spec CommandSpec, output, description string, critical bool) error {
	result, err := c.runAndClassifyCommand(ctx, spec, commandRunOptions{
		output:        output,
		description:   description,
		caller:        "safeCmdOutput",
		critical:      critical,
		logCollection: true,
	})
	if err != nil {
		return err
	}
	if result.classification != commandRunSucceeded {
		return nil
	}

	if err := c.writeReportFile(output, result.output); err != nil {
		return err
	}

	c.logger.Debug("Successfully collected %s via command: %s", description, spec.String())
	return nil
}

func (c *Collector) safeCmdOutputBestEffort(ctx context.Context, spec CommandSpec, output, description string) error {
	result, err := c.runAndClassifyCommand(ctx, spec, commandRunOptions{
		output:           output,
		description:      description,
		caller:           "safeCmdOutputBestEffort",
		logCollection:    true,
		debugNonCritical: true,
	})
	if err != nil {
		return err
	}
	if result.classification != commandRunSucceeded {
		return nil
	}

	if err := c.writeReportFile(output, result.output); err != nil {
		return err
	}

	c.logger.Debug("Successfully collected %s via command: %s", description, spec.String())
	return nil
}

// safeCmdOutputWithPBSAuth executes a command with PBS authentication environment variables
// This enables automatic authentication for proxmox-backup-client commands
func (c *Collector) safeCmdOutputWithPBSAuth(ctx context.Context, spec CommandSpec, output, description string, critical bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := spec.validate(); err != nil {
		return err
	}

	if output != "" && c.shouldExclude(output) {
		c.logger.Debug("Skipping %s: output %s excluded by pattern", description, output)
		c.incFilesSkipped()
		return nil
	}

	// Check if command exists
	if _, err := c.depLookPath(spec.Name); err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command not available: %s", spec.Name)
		}
		c.logger.Debug("Command not available: %s (skipping %s)", spec.Name, description)
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command with PBS auth: %s > %s", spec.String(), output)
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
		c.logger.Debug("Using PBS authentication for command: %s", spec.Name)
	}

	cmdString := spec.String()
	out, err := c.depRunCommandWithEnv(ctx, extraEnv, spec.Name, spec.Args...)
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

	if err := c.writeReportFile(output, out); err != nil {
		return err
	}
	c.logger.Debug("Successfully collected %s via PBS-authenticated command: %s", description, cmdString)

	return nil
}

func pbsRepositoryWithDatastore(repository, datastoreName string) string {
	separator := -1
	bracketDepth := 0
	for i, r := range repository {
		switch r {
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ':':
			if bracketDepth == 0 {
				separator = i
			}
		}
	}
	if separator >= 0 {
		return repository[:separator+1] + datastoreName
	}
	return repository + ":" + datastoreName
}

// safeCmdOutputWithPBSAuthForDatastore executes a command with PBS authentication for a specific datastore
// This function appends the datastore name to the PBS_REPOSITORY environment variable
func (c *Collector) safeCmdOutputWithPBSAuthForDatastore(ctx context.Context, spec CommandSpec, output, description, datastoreName string, critical bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := spec.validate(); err != nil {
		return err
	}

	if output != "" && c.shouldExclude(output) {
		c.logger.Debug("Skipping %s: output %s excluded by pattern", description, output)
		c.incFilesSkipped()
		return nil
	}

	// Check if command exists
	if _, err := c.depLookPath(spec.Name); err != nil {
		if critical {
			c.incFilesFailed()
			return fmt.Errorf("critical command not available: %s", spec.Name)
		}
		c.logger.Debug("Command not available: %s (skipping %s)", spec.Name, description)
		return nil
	}

	if c.dryRun {
		c.logger.Debug("[DRY RUN] Would execute command with PBS auth for datastore %s: %s > %s", datastoreName, spec.String(), output)
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
		repoWithDatastore = pbsRepositoryWithDatastore(c.config.PBSRepository, datastoreName)
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

	cmdString := spec.String()
	out, err := c.depRunCommandWithEnv(ctx, extraEnv, spec.Name, spec.Args...)
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

	if err := c.writeReportFile(output, out); err != nil {
		return err
	}
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

func collectorPathKey(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "entry"
	}

	safe := sanitizeFilename(trimmed)
	if safe == trimmed {
		return safe
	}

	sum := sha256.Sum256([]byte(trimmed))
	return fmt.Sprintf("%s_%s", safe, hex.EncodeToString(sum[:4]))
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
	if c.shouldExclude(path) {
		c.logger.Debug("Skipping report file %s due to exclusion pattern", path)
		c.incFilesSkipped()
		return nil
	}

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

func (c *Collector) captureCommandOutput(ctx context.Context, spec CommandSpec, output, description string, critical bool) ([]byte, error) {
	result, err := c.runAndClassifyCommand(ctx, spec, commandRunOptions{
		output:                output,
		description:           description,
		caller:                "captureCommandOutput",
		critical:              critical,
		handleSystemctlStatus: true,
	})
	if err != nil {
		return nil, err
	}
	if result.classification != commandRunSucceeded {
		return nil, nil
	}

	if err := c.writeReportFile(output, result.output); err != nil {
		return nil, err
	}

	return result.output, nil
}

func (c *Collector) collectCommandMulti(ctx context.Context, spec CommandSpec, output, description string, critical bool, mirrors ...string) error {
	if output == "" {
		return fmt.Errorf("primary output path cannot be empty for %s", description)
	}

	data, err := c.captureCommandOutput(ctx, spec, output, description, critical)
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

func (c *Collector) collectCommandOptional(ctx context.Context, spec CommandSpec, output, description string, mirrors ...string) {
	if output == "" {
		c.logger.Debug("Optional command %s skipped: no primary output path", description)
		return
	}

	data, err := c.captureCommandOutput(ctx, spec, output, description, false)
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
