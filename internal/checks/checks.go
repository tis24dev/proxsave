package checks

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// createTestFile is a small indirection over os.Create used by permission
// checks to allow tests to inject controlled failures (e.g., EIO) without
// depending on specific filesystem behavior.
var createTestFile = os.Create

var (
	osStat      = os.Stat
	osRemove    = os.Remove
	osOpenFile  = os.OpenFile
	osMkdirAll  = os.MkdirAll
	osWriteFile = os.WriteFile
	osSymlink   = os.Symlink
	syncFile    = func(f *os.File) error { return f.Sync() }

	// tempRootPath is the runtime path used by CheckTempDirectory.
	// It is a variable to allow tests to use a safe, isolated temporary directory.
	tempRootPath = filepath.Join("/tmp", "proxsave")
)

// Checker performs pre-backup validation checks
type Checker struct {
	logger *logging.Logger
	config *CheckerConfig
}

// DisableCloud globally disables cloud-related checks for this checker.
// It is used when the Go pipeline determines that cloud storage is unavailable
// and should be treated as disabled for the rest of the run.
func (c *Checker) DisableCloud() {
	if c == nil || c.config == nil {
		return
	}
	c.config.CloudEnabled = false
	c.config.CloudPath = ""
}

// CheckerConfig holds configuration for pre-backup checks
type CheckerConfig struct {
	BackupPath          string
	LogPath             string
	SecondaryPath       string
	SecondaryEnabled    bool
	CloudPath           string
	CloudEnabled        bool
	MinDiskPrimaryGB    float64
	MinDiskSecondaryGB  float64
	MinDiskCloudGB      float64
	SafetyFactor        float64 // Multiplier for estimated size (e.g., 1.5 = 50% buffer)
	LockDirPath         string
	LockFilePath        string
	MaxLockAge          time.Duration
	SkipPermissionCheck bool
	DryRun              bool
}

// Validate checks if the checker configuration is valid
func (c *CheckerConfig) Validate() error {
	if c.BackupPath == "" {
		return fmt.Errorf("backup path cannot be empty")
	}
	if c.LogPath == "" {
		return fmt.Errorf("log path cannot be empty")
	}
	if c.LockDirPath == "" {
		c.LockDirPath = c.BackupPath
	}
	if c.MinDiskPrimaryGB < 0 {
		return fmt.Errorf("primary minimum disk space cannot be negative")
	}
	if c.MinDiskSecondaryGB < 0 {
		return fmt.Errorf("secondary minimum disk space cannot be negative")
	}
	if c.MinDiskCloudGB < 0 {
		return fmt.Errorf("cloud minimum disk space cannot be negative")
	}
	if c.SafetyFactor < 1.0 {
		return fmt.Errorf("safety factor must be >= 1.0, got %.2f", c.SafetyFactor)
	}
	if c.MaxLockAge <= 0 {
		return fmt.Errorf("max lock age must be positive")
	}
	return nil
}

// CheckResult holds the result of a validation check
type CheckResult struct {
	Name    string
	Passed  bool
	Message string
	Error   error
	Code    string
}

// NewChecker creates a new pre-backup checker
func NewChecker(logger *logging.Logger, config *CheckerConfig) *Checker {
	return &Checker{
		logger: logger,
		config: config,
	}
}

// ShouldSkipPermissionCheck returns true if permission checks should be skipped
func (c *Checker) ShouldSkipPermissionCheck() bool {
	return c.config.SkipPermissionCheck
}

// RunAllChecks performs all pre-backup validation checks
// Order is important: directories must exist before we can check disk space,
// permissions, or create lock files in those directories
func (c *Checker) RunAllChecks(ctx context.Context) ([]CheckResult, error) {
	c.logger.Debug("Running pre-backup validation checks")

	var results []CheckResult

	// 1. Check directories FIRST - they must exist for all other checks
	dirResult := c.CheckDirectories()
	results = append(results, dirResult)
	if !dirResult.Passed {
		return results, fmt.Errorf("directory check failed: %s", dirResult.Message)
	}

	// 1.5. Check temp directory - verify /tmp/proxsave is usable
	tempDirResult := c.CheckTempDirectory()
	results = append(results, tempDirResult)
	if !tempDirResult.Passed {
		return results, fmt.Errorf("temp directory check failed: %s", tempDirResult.Message)
	}

	// 2. Check disk space - now that we know directories exist
	diskResult := c.CheckDiskSpace()
	results = append(results, diskResult)
	if !diskResult.Passed {
		return results, fmt.Errorf("disk space check failed: %s", diskResult.Message)
	}

	// 3. Check permissions - verify we can write to directories
	if !c.config.SkipPermissionCheck {
		permResult := c.CheckPermissions()
		results = append(results, permResult)
		if !permResult.Passed {
			return results, fmt.Errorf("permissions check failed: %s", permResult.Message)
		}
	}

	// 4. Check lock file LAST - only after all other prerequisites are met
	// This prevents creating lock files in non-existent or unwritable directories
	lockResult := c.CheckLockFile()
	results = append(results, lockResult)
	if !lockResult.Passed {
		return results, fmt.Errorf("lock file check failed: %s", lockResult.Message)
	}

	c.logger.Debug("All pre-backup checks passed")
	return results, nil
}

// CheckDiskSpace verifies sufficient disk space is available
func (c *Checker) CheckDiskSpace() CheckResult {
	result := CheckResult{
		Name:   "Disk Space",
		Passed: false,
	}
	paths := []struct {
		label    string
		path     string
		enabled  bool
		min      float64
		critical bool
	}{
		{"Primary", c.config.BackupPath, true, c.config.MinDiskPrimaryGB, true},
		{"Secondary", c.config.SecondaryPath, c.config.SecondaryEnabled, c.config.MinDiskSecondaryGB, false},
		{"Cloud", c.config.CloudPath, c.config.CloudEnabled, c.config.MinDiskCloudGB, false},
	}

	hasWarnings := false

	for _, entry := range paths {
		if !entry.enabled || entry.path == "" || entry.min <= 0 {
			continue
		}
		c.logger.Debug("Checking disk space on %s: %s", entry.label, entry.path)
		if err := c.checkSingleDisk(entry.label, entry.path, entry.min); err != nil {
			if entry.critical {
				result.Error = err
				result.Message = err.Error()
				c.logger.Error("%s", result.Message)
				return result
			}

			c.logger.Warning("%s disk space check failed (non-blocking): %v", entry.label, err)
			c.logger.Warning("Backup will continue, but %s storage may not be updated", entry.label)
			hasWarnings = true
		}
	}

	result.Passed = true
	if hasWarnings {
		result.Message = "Primary disk space OK (warnings on secondary/cloud destinations)"
	} else {
		result.Message = "Sufficient disk space on all configured destinations"
	}
	c.logger.Debug("%s", result.Message)
	return result
}

// CheckLockFile checks for stale lock files and creates a new lock
func (c *Checker) CheckLockFile() CheckResult {
	result := CheckResult{
		Name:   "Lock File",
		Passed: false,
	}

	lockPath := c.config.LockFilePath
	if lockPath == "" {
		lockPath = filepath.Join(c.config.LockDirPath, ".backup.lock")
	}
	c.logger.Debug("Lock file path: %s", lockPath)

	// Check if lock file exists
	if _, err := osStat(lockPath); err == nil {
		// Lock file exists, check its age
		info, err := osStat(lockPath)
		if err != nil {
			result.Error = fmt.Errorf("failed to stat lock file: %w", err)
			result.Message = result.Error.Error()
			return result
		}

		age := time.Since(info.ModTime())
		if age > c.config.MaxLockAge {
			// Stale lock file, remove it
			c.logger.Warning("Removing stale lock file (age: %v)", age)
			if err := osRemove(lockPath); err != nil {
				result.Error = fmt.Errorf("failed to remove stale lock: %w", err)
				result.Message = result.Error.Error()
				return result
			}
		} else {
			result.Message = fmt.Sprintf("Another backup is in progress (lock age: %v)", age)
			c.logger.Error("%s", result.Message)
			return result
		}
	}

	// Create new lock file atomically
	if !c.config.DryRun {
		c.logger.Debug("Creating lock file with PID %d", os.Getpid())
		f, err := osOpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
		if err != nil {
			if os.IsExist(err) {
				result.Message = "Another backup acquired the lock"
				c.logger.Error("%s", result.Message)
				return result
			}
			result.Error = fmt.Errorf("failed to create lock file: %w", err)
			result.Message = result.Error.Error()
			return result
		}
		defer f.Close()

		hostname, _ := os.Hostname()
		lockContent := fmt.Sprintf("pid=%d\nhost=%s\ntime=%s\n", os.Getpid(), hostname, time.Now().Format(time.RFC3339))
		if _, err := f.WriteString(lockContent); err != nil {
			result.Error = fmt.Errorf("failed to write lock file: %w", err)
			result.Message = result.Error.Error()
			return result
		}
		if err := syncFile(f); err != nil {
			c.logger.Warning("Failed to sync lock file %s: %v", lockPath, err)
		}
	} else {
		c.logger.Info("[DRY RUN] Would create lock file: %s", lockPath)
	}

	result.Passed = true
	result.Message = "Lock file acquired successfully"
	c.logger.Debug("%s", result.Message)
	return result
}

// CheckPermissions verifies write permissions on required directories
func (c *Checker) CheckPermissions() CheckResult {
	result := CheckResult{
		Name:   "Permissions",
		Passed: false,
		Code:   "PERMISSION_CHECK",
	}

	dirs := []string{c.config.BackupPath, c.config.LogPath}

	const maxAttempts = 3
	const retryDelay = 100 * time.Millisecond

	for _, dir := range dirs {
		c.logger.Debug("Checking permissions: %s", dir)
		// Check if directory is writable
		testFile := filepath.Join(dir, fmt.Sprintf(".permission_test_%d", os.Getpid()))

		if c.config.DryRun {
			c.logger.Debug("[DRY RUN] Would test write permission in: %s", dir)
			continue
		}

		var lastErr error

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			f, err := createTestFile(testFile)
			if err == nil {
				f.Close()
				lastErr = nil
				break
			}

			lastErr = err

			// Treat filesystem I/O errors as potentially transient and retry
			if errors.Is(err, syscall.EIO) && attempt < maxAttempts {
				c.logger.Warning("I/O error while testing write in %s (attempt %d/%d), will retry: %v",
					dir, attempt, maxAttempts, err)
				time.Sleep(retryDelay)
				continue
			}

			// For non-transient errors or after exhausting retries, stop retrying
			break
		}

		if lastErr != nil {
			err := lastErr
			var reason string
			code := "PERMISSION_CHECK_FAILED"

			switch {
			case errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM):
				reason = "no write permission"
				code = "PERMISSION_DENIED"
			case errors.Is(err, syscall.EROFS):
				reason = "filesystem is read-only"
				code = "FS_READONLY"
			case errors.Is(err, syscall.EIO):
				reason = "filesystem I/O error while testing write"
				code = "FS_IO_ERROR"
			default:
				reason = "failed to test write permission"
			}

			result.Code = code
			result.Error = fmt.Errorf("%s in %s: %w", reason, dir, err)
			result.Message = result.Error.Error()
			c.logger.Error("%s", result.Message)
			return result
		}

		c.logger.Debug("Directory writable: %s", dir)
		// Clean up test file
		if err := osRemove(testFile); err != nil {
			c.logger.Warning("Failed to remove test file %s: %v", testFile, err)
		}
	}

	result.Passed = true
	result.Message = "All directories are writable"
	c.logger.Debug("%s", result.Message)
	return result
}

// CheckDirectories verifies required directories exist
func (c *Checker) CheckDirectories() CheckResult {
	result := CheckResult{
		Name:   "Directories",
		Passed: false,
	}

	dirs := make(map[string]struct{})

	addDir := func(path string) {
		cleaned := filepath.Clean(path)
		if cleaned == "" || cleaned == "." || cleaned == "/" {
			return
		}
		dirs[cleaned] = struct{}{}
	}

	addDir(c.config.BackupPath)
	addDir(c.config.LogPath)
	addDir(c.config.LockDirPath)

	lockPath := c.config.LockFilePath
	if lockPath == "" {
		lockPath = filepath.Join(c.config.LockDirPath, ".backup.lock")
	}
	addDir(filepath.Dir(lockPath))

	for dir := range dirs {
		c.logger.Debug("Checking directory: %s", dir)
		info, err := osStat(dir)
		if err == nil {
			if !info.IsDir() {
				result.Error = fmt.Errorf("required path is not a directory: %s", dir)
				result.Message = result.Error.Error()
				c.logger.Error("%s", result.Message)
				return result
			}
			c.logger.Debug("Directory exists: %s", dir)
			continue
		}

		if !os.IsNotExist(err) {
			result.Error = fmt.Errorf("failed to stat directory %s: %w", dir, err)
			result.Message = result.Error.Error()
			c.logger.Error("%s", result.Message)
			return result
		}

		if c.config.DryRun {
			c.logger.Info("[DRY RUN] Would create directory: %s", dir)
			continue
		}

		if err := osMkdirAll(dir, 0755); err != nil {
			result.Error = fmt.Errorf("failed to create directory %s: %w", dir, err)
			result.Message = result.Error.Error()
			c.logger.Error("%s", result.Message)
			return result
		}
		c.logger.Info("Created missing directory: %s", dir)
	}

	result.Passed = true
	result.Message = "All required directories exist"
	c.logger.Debug("%s", result.Message)
	return result
}

// CheckTempDirectory verifies /tmp/proxsave exists, is writable, and supports symlinks
func (c *Checker) CheckTempDirectory() CheckResult {
	result := CheckResult{
		Name:   "Temp Directory",
		Passed: false,
	}

	tempRoot := tempRootPath
	c.logger.Debug("Checking temp directory: %s", tempRoot)

	info, err := osStat(tempRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			result.Code = "STAT_FAILED"
			result.Error = fmt.Errorf("Temp directory check failed - path: %s: %w", tempRoot, err)
			result.Message = result.Error.Error()
			return result
		}

		// Directory doesn't exist - create it
		c.logger.Debug("Temp directory not found, creating: %s", tempRoot)
		if err := osMkdirAll(tempRoot, 0o755); err != nil {
			result.Code = "CREATE_FAILED"
			result.Error = fmt.Errorf("Temp directory creation failed - path: %s: %w", tempRoot, err)
			result.Message = result.Error.Error()
			return result
		}
		c.logger.Debug("Temp directory created: %s", tempRoot)

		info, err = osStat(tempRoot)
		if err != nil {
			result.Code = "VERIFY_FAILED"
			result.Error = fmt.Errorf("Temp directory verification failed - path: %s: %w", tempRoot, err)
			result.Message = result.Error.Error()
			return result
		}
	} else {
		c.logger.Debug("Temp directory exists: %s", tempRoot)
	}

	if !info.IsDir() {
		result.Code = "NOT_DIRECTORY"
		result.Error = fmt.Errorf("Temp path is not a directory - path: %s", tempRoot)
		result.Message = result.Error.Error()
		return result
	}

	// Test write permission
	c.logger.Debug("Testing write permission: %s", tempRoot)
	testFile := filepath.Join(tempRoot, ".proxsave-permission-test")
	if err := osWriteFile(testFile, []byte("test"), 0o600); err != nil {
		result.Code = "NOT_WRITABLE"
		result.Error = fmt.Errorf("Temp directory not writable - path: %s: %w", tempRoot, err)
		result.Message = result.Error.Error()
		return result
	}
	defer osRemove(testFile)

	// Test symlink support
	c.logger.Debug("Testing symlink support: %s", tempRoot)
	testSymlink := filepath.Join(tempRoot, ".proxsave-symlink-test")
	if err := osSymlink(testFile, testSymlink); err != nil {
		result.Code = "NO_SYMLINK_SUPPORT"
		result.Error = fmt.Errorf("Temp directory does not support symlinks - path: %s: %w", tempRoot, err)
		result.Message = result.Error.Error()
		return result
	}
	osRemove(testSymlink)

	result.Passed = true
	result.Message = fmt.Sprintf("%s writable with symlink support", tempRoot)
	return result
}

// ReleaseLock removes the lock file
func (c *Checker) ReleaseLock() error {
	lockPath := c.config.LockFilePath
	if lockPath == "" {
		lockPath = filepath.Join(c.config.LockDirPath, ".backup.lock")
	}

	if c.config.DryRun {
		c.logger.Info("[DRY RUN] Would release lock file: %s", lockPath)
		return nil
	}

	if err := osRemove(lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	c.logger.Debug("Lock file released: %s", lockPath)
	return nil
}

// GetDefaultCheckerConfig returns a default checker configuration
func GetDefaultCheckerConfig(backupPath, logPath, lockDir string) *CheckerConfig {
	return &CheckerConfig{
		BackupPath:          backupPath,
		LogPath:             logPath,
		SecondaryPath:       "",
		SecondaryEnabled:    false,
		CloudPath:           "",
		CloudEnabled:        false,
		MinDiskPrimaryGB:    10.0,
		MinDiskSecondaryGB:  10.0,
		MinDiskCloudGB:      10.0,
		SafetyFactor:        1.5, // 50% buffer over estimated size
		LockDirPath:         lockDir,
		LockFilePath:        filepath.Join(lockDir, ".backup.lock"),
		MaxLockAge:          2 * time.Hour,
		SkipPermissionCheck: false,
		DryRun:              false,
	}
}

// CheckDiskSpaceForEstimate checks if there's enough space for an estimated backup size
func (c *Checker) CheckDiskSpaceForEstimate(estimatedSizeGB float64) CheckResult {
	result := CheckResult{
		Name:   "Disk Space (Estimated)",
		Passed: false,
	}

	paths := []struct {
		label    string
		path     string
		enabled  bool
		min      float64
		critical bool
	}{
		{"Primary", c.config.BackupPath, true, c.config.MinDiskPrimaryGB, true},
		{"Secondary", c.config.SecondaryPath, c.config.SecondaryEnabled, c.config.MinDiskSecondaryGB, false},
		{"Cloud", c.config.CloudPath, c.config.CloudEnabled, c.config.MinDiskCloudGB, false},
	}

	hasWarnings := false

	for _, entry := range paths {
		if !entry.enabled || entry.path == "" || entry.min <= 0 {
			continue
		}
		requiredGB := math.Max(entry.min, estimatedSizeGB*c.config.SafetyFactor)

		availableGB, err := diskSpaceGB(entry.path)
		if err != nil {
			errMsg := fmt.Sprintf("%s disk space check failed (%s): %v", entry.label, entry.path, err)
			wrappedErr := fmt.Errorf("%s disk space check failed (%s): %w", entry.label, entry.path, err)
			if entry.critical {
				result.Error = wrappedErr
				result.Message = errMsg
				return result
			}

			c.logger.Warning("%s (non-blocking)", errMsg)
			hasWarnings = true
			continue
		}
		if availableGB < requiredGB {
			msg := fmt.Sprintf("%s disk space insufficient on %s: %.2f GB available, %.2f GB required (max of %.2f GB min, %.2f GB estimated × %.1fx)",
				entry.label, entry.path, availableGB, requiredGB, entry.min, estimatedSizeGB, c.config.SafetyFactor)
			if entry.critical {
				result.Message = msg
				result.Error = fmt.Errorf("%s", msg)
				c.logger.Error("%s", result.Message)
				return result
			}

			c.logger.Warning("%s (non-blocking)", msg)
			c.logger.Warning("%s storage may fail due to insufficient space", entry.label)
			hasWarnings = true
		}
	}

	result.Passed = true
	if hasWarnings {
		result.Message = fmt.Sprintf("Primary has sufficient disk space for estimated %.2f GB (warnings on secondary/cloud)",
			estimatedSizeGB)
	} else {
		result.Message = fmt.Sprintf("Sufficient disk space for estimated %.2f GB (safety factor %.1fx) on all destinations",
			estimatedSizeGB, c.config.SafetyFactor)
	}
	c.logger.Debug("%s", result.Message)
	return result
}

func (c *Checker) checkSingleDisk(label, path string, minGB float64) error {
	availableGB, err := diskSpaceGB(path)
	if err != nil {
		return fmt.Errorf("%s disk space check failed (%s): %w", label, path, err)
	}
	c.logger.Debug("%s: %.2f GB available, %.2f GB required", label, availableGB, minGB)
	if availableGB < minGB {
		return fmt.Errorf("%s disk space insufficient on %s: %.2f GB available, %.2f GB required",
			label, path, availableGB, minGB)
	}
	return nil
}

func diskSpaceGB(path string) (float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return float64(stat.Bavail*uint64(stat.Bsize)) / (1024 * 1024 * 1024), nil
}
