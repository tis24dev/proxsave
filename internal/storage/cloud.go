package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// CloudStorage implements the Storage interface for cloud storage using rclone
// All errors from cloud storage are NON-FATAL - they log warnings but don't abort the backup
// Uses comprehensible timeout names: CONNECTION (for remote check) and OPERATION (for upload/download)
const (
	cloudUploadModeSequential = "sequential"
	cloudUploadModeParallel   = "parallel"
)

type CloudStorage struct {
	config         *config.Config
	logger         *logging.Logger
	remote         string // rclone remote name (e.g. "gdrive")
	remotePrefix   string // combined path inside remote (base path from CLOUD_REMOTE + CLOUD_REMOTE_PATH)
	uploadMode     string
	parallelJobs   int
	parallelVerify bool
	execCommand    func(ctx context.Context, name string, args ...string) ([]byte, error)
	lookPath       func(string) (string, error)
	sleep          func(time.Duration)
	lastRet        RetentionSummary
	remoteFilesMu  sync.RWMutex
	remoteFiles    map[string]struct{}
	logPathMu      sync.Mutex
	logPathMissing bool
}

func (c *CloudStorage) remoteLabel() string {
	if c.remotePrefix != "" {
		return fmt.Sprintf("%s:%s", c.remote, c.remotePrefix)
	}
	return c.remote + ":"
}

func (c *CloudStorage) remoteBase() string {
	return c.remoteLabel()
}

func (c *CloudStorage) remoteRoot() string {
	return strings.TrimSpace(c.remote) + ":"
}

func (c *CloudStorage) remotePathFor(name string) string {
	clean := path.Clean(name)
	if strings.HasPrefix(clean, "..") {
		clean = filepath.Base(clean)
	}
	if c.remotePrefix != "" {
		clean = path.Join(c.remotePrefix, clean)
	}
	return fmt.Sprintf("%s:%s", c.remote, clean)
}

func (c *CloudStorage) buildRcloneArgs(subcommand string) []string {
	args := []string{"rclone", subcommand}
	if len(c.config.RcloneFlags) > 0 {
		args = append(args, c.config.RcloneFlags...)
	}
	return args
}

func splitRemoteRef(ref string) (remoteName, relPath string) {
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) < 2 {
		return ref, ""
	}
	return parts[0], parts[1]
}

func remoteDirRef(ref string) string {
	remoteName, relPath := splitRemoteRef(ref)
	if relPath == "" {
		return remoteName + ":"
	}
	dir := path.Dir(relPath)
	if dir == "." || dir == "/" || dir == "" {
		return remoteName + ":"
	}
	return fmt.Sprintf("%s:%s", remoteName, dir)
}

func remoteBaseName(ref string) string {
	_, relPath := splitRemoteRef(ref)
	if relPath == "" {
		return ""
	}
	trimmed := strings.Trim(relPath, "/")
	if trimmed == "" {
		return ""
	}
	return path.Base(trimmed)
}

// NewCloudStorage creates a new cloud storage instance
func NewCloudStorage(cfg *config.Config, logger *logging.Logger) (*CloudStorage, error) {
	// Normalize CloudRemote and CloudRemotePath into:
	//   - remote: rclone remote name (e.g. "gdrive")
	//   - remotePrefix: full path inside the remote where backups live
	//     (base path from CLOUD_REMOTE plus optional CLOUD_REMOTE_PATH)
	rawRemote := strings.TrimSpace(cfg.CloudRemote)
	remoteName, basePath := splitRemoteRef(rawRemote)
	basePath = strings.Trim(strings.TrimSpace(basePath), "/")

	userPrefix := strings.Trim(strings.TrimSpace(cfg.CloudRemotePath), "/")

	combinedPrefix := strings.Trim(path.Join(basePath, userPrefix), "/")

	mode := strings.ToLower(strings.TrimSpace(cfg.CloudUploadMode))
	if mode != cloudUploadModeParallel {
		mode = cloudUploadModeSequential
	}
	parallelJobs := cfg.CloudParallelJobs
	if parallelJobs <= 0 {
		parallelJobs = 1
	}
	return &CloudStorage{
		config:         cfg,
		logger:         logger,
		remote:         remoteName,
		remotePrefix:   combinedPrefix,
		uploadMode:     mode,
		parallelJobs:   parallelJobs,
		parallelVerify: cfg.CloudParallelVerify,
		execCommand:    defaultExecCommand,
		lookPath:       exec.LookPath,
		sleep:          time.Sleep,
	}, nil
}

// Name returns the storage backend name
func (c *CloudStorage) Name() string {
	return "Cloud Storage (rclone)"
}

// Location returns the backup location type
func (c *CloudStorage) Location() BackupLocation {
	return LocationCloud
}

// IsEnabled returns true if cloud storage is configured
func (c *CloudStorage) IsEnabled() bool {
	return c.config.CloudEnabled && c.remote != ""
}

// IsCritical returns false because cloud storage is non-critical
// Failures in cloud storage should NOT abort the backup
func (c *CloudStorage) IsCritical() bool {
	return false
}

// DetectFilesystem checks if rclone is available and the remote is accessible
func (c *CloudStorage) DetectFilesystem(ctx context.Context) (*FilesystemInfo, error) {
	// Check if rclone is available
	if !c.hasRclone() {
		c.logger.Warning("WARNING: rclone not found in PATH - cloud backup will be skipped")
		c.logger.Warning("WARNING: Install rclone to enable cloud backups")
		return nil, &StorageError{
			Location:    LocationCloud,
			Operation:   "detect_filesystem",
			Path:        c.remoteLabel(),
			Err:         fmt.Errorf("rclone command not found in PATH"),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Check if remote is configured and accessible
	// Use CONNECTION timeout for this check (short timeout)
	c.logger.Info("Checking cloud remote accessibility: %s (timeout: max %ds)",
		c.remoteLabel(),
		c.config.RcloneTimeoutConnection)

	if err := c.checkRemoteAccessible(ctx); err != nil {
		var rcErr *remoteCheckError
		if errors.As(err, &rcErr) {
			switch rcErr.kind {
			case remoteErrorTimeout:
				c.logger.Debug("WARNING: Cloud remote %s did not respond within %ds: %v",
					c.remoteLabel(), c.config.RcloneTimeoutConnection, rcErr)
				c.logger.Debug("HINT: Consider increasing RCLONE_TIMEOUT_CONNECTION for slow remotes")
			case remoteErrorAuth:
				c.logger.Debug("WARNING: Cloud remote %s authentication/config failed: %v", c.remoteLabel(), rcErr)
				c.logger.Debug("HINT: Check your rclone configuration with: rclone config show %s", c.remote)
			case remoteErrorPath:
				c.logger.Debug("WARNING: Cloud remote path %s is not accessible: %v", c.remoteLabel(), rcErr)
				c.logger.Debug("HINT: Verify CLOUD_REMOTE_PATH or create the path using: rclone mkdir %s", c.remoteLabel())
			case remoteErrorNetwork:
				c.logger.Debug("WARNING: Cloud remote %s is not reachable: %v", c.remoteLabel(), rcErr)
				c.logger.Debug("HINT: Check network connection, DNS and firewall rules")
			default:
				c.logger.Debug("WARNING: Cloud remote %s is not accessible: %v", c.remoteLabel(), rcErr)
				c.logger.Debug("HINT: Check your rclone configuration and network connectivity")
			}
		} else {
			c.logger.Debug("WARNING: Cloud remote %s is not accessible: %v", c.remoteLabel(), err)
			c.logger.Debug("HINT: Check your rclone configuration with: rclone config show %s", c.remote)
		}
		c.logger.Debug("WARNING: Cloud backup will be skipped")

		return nil, &StorageError{
			Location:    LocationCloud,
			Operation:   "check_remote",
			Path:        c.remoteLabel(),
			Err:         fmt.Errorf("remote not accessible: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	c.logger.Info("Cloud remote %s is accessible", c.remoteLabel())

	// Return minimal filesystem info (cloud doesn't have a real filesystem type)
	return &FilesystemInfo{
		Path:              c.remoteLabel(),
		Type:              FilesystemType("rclone-" + c.remote),
		SupportsOwnership: false,
		IsNetworkFS:       true,
		MountPoint:        c.remoteLabel(),
		Device:            "cloud",
	}, nil
}

// hasRclone checks if rclone command is available
func (c *CloudStorage) hasRclone() bool {
	lookPath := c.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath("rclone")
	return err == nil
}

type remoteErrorKind string

const (
	remoteErrorTimeout remoteErrorKind = "timeout"
	remoteErrorAuth    remoteErrorKind = "auth"
	remoteErrorPath    remoteErrorKind = "path"
	remoteErrorNetwork remoteErrorKind = "network"
	remoteErrorOther   remoteErrorKind = "other"
)

type remoteCheckError struct {
	kind remoteErrorKind
	msg  string
	err  error
}

func (e *remoteCheckError) Error() string {
	if e == nil {
		return ""
	}
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.err)
	}
	return e.msg
}

func (e *remoteCheckError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// checkRemoteAccessible checks if the rclone remote is accessible
// Uses RCLONE_TIMEOUT_CONNECTION (short timeout for connection check)
func (c *CloudStorage) checkRemoteAccessible(ctx context.Context) error {
	// Create a context with connection timeout for the whole check
	timeoutSeconds := c.config.RcloneTimeoutConnection
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	const maxAttempts = 3

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if timeoutCtx.Err() != nil {
			break
		}

		err := c.checkRemoteOnce(timeoutCtx)
		if err == nil {
			return nil
		}

		lastErr = err

		// If the context timed out, wrap as timeout error
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return &remoteCheckError{
				kind: remoteErrorTimeout,
				msg:  fmt.Sprintf("connection timeout (%ds) - remote did not respond in time", timeoutSeconds),
				err:  err,
			}
		}

		if attempt < maxAttempts {
			// Exponential backoff: 2s, 4s, 8s, ...
			waitTime := time.Duration(1<<uint(attempt)) * time.Second
			c.logger.Debug("Cloud remote check attempt %d/%d failed: %v (retrying in %v)",
				attempt, maxAttempts, err, waitTime)
			c.sleep(waitTime)
		}
	}

	if lastErr == nil {
		return &remoteCheckError{
			kind: remoteErrorOther,
			msg:  "cloud remote check failed for unknown reasons",
			err:  nil,
		}
	}

	return lastErr
}

func (c *CloudStorage) checkRemoteOnce(ctx context.Context) error {
	remoteRoot := c.remoteRoot()
	if strings.TrimSpace(remoteRoot) == ":" {
		return &remoteCheckError{
			kind: remoteErrorOther,
			msg:  "rclone remote name is empty",
			err:  nil,
		}
	}

	remoteBase := c.remoteBase()

	// If user explicitly enabled write healthcheck, skip list check entirely
	if c.config.CloudWriteHealthCheck {
		c.logger.Debug("CLOUD_WRITE_HEALTHCHECK=true, using write test only")
		return c.tryWriteTest(ctx)
	}

	// PHASE 1: Try list-based check (default, faster)
	listErr := c.tryListCheck(ctx, remoteRoot, remoteBase)
	if listErr == nil {
		return nil // Success via list check
	}

	// PHASE 2: Analyze error to determine if fallback is appropriate
	var rcErr *remoteCheckError
	if !errors.As(listErr, &rcErr) {
		return listErr // Unknown error type, can't fallback
	}

	if !c.shouldFallbackToWriteTest(rcErr, ctx) {
		return listErr // Error type doesn't benefit from fallback
	}

	// PHASE 3: Attempt write test fallback
	c.logger.Debug("List check failed with permission issue, attempting write test fallback...")
	writeErr := c.tryWriteTest(ctx)
	if writeErr == nil {
		c.logger.Warning("Cloud remote accessible via write test (list permissions unavailable)")
		c.logger.Debug("HINT: Consider setting CLOUD_WRITE_HEALTHCHECK=true for faster connectivity checks")
		return nil // Success via fallback
	}

	// PHASE 4: Both methods failed - return original list error
	c.logger.Debug("Write test fallback also failed: %v", writeErr)
	return listErr
}

// tryListCheck performs list-based connectivity check using rclone lsf
func (c *CloudStorage) tryListCheck(ctx context.Context, remoteRoot, remoteBase string) error {
	// Step 1: check remote root (remote:)
	argsRoot := c.buildRcloneArgs("lsf")
	argsRoot = append(argsRoot, remoteRoot, "--max-depth", "1")
	c.logger.Debug("Running (remote root check): %s", strings.Join(argsRoot, " "))

	output, err := c.exec(ctx, argsRoot[0], argsRoot[1:]...)
	if err != nil {
		return classifyRemoteError("remote", remoteRoot, err, output)
	}

	// Step 2: check specific path (remote:path) if configured
	if remoteBase != remoteRoot {
		// Ensure backup path exists (mkdir is idempotent)
		argsMkdir := c.buildRcloneArgs("mkdir")
		argsMkdir = append(argsMkdir, remoteBase)
		c.logger.Debug("Running (remote path ensure): %s", strings.Join(argsMkdir, " "))

		output, err = c.exec(ctx, argsMkdir[0], argsMkdir[1:]...)
		if err != nil {
			return classifyRemoteError("path", remoteBase, err, output)
		}

		// Verify backup path accessibility with a listing
		argsPath := c.buildRcloneArgs("lsf")
		argsPath = append(argsPath, remoteBase, "--max-depth", "1")
		c.logger.Debug("Running (remote path check): %s", strings.Join(argsPath, " "))

		output, err = c.exec(ctx, argsPath[0], argsPath[1:]...)
		if err != nil {
			return classifyRemoteError("path", remoteBase, err, output)
		}
	}

	return nil
}

// tryWriteTest performs write-based connectivity check using rclone touch/deletefile
func (c *CloudStorage) tryWriteTest(ctx context.Context) error {
	testName := fmt.Sprintf(".pbs-backup-healthcheck-%d", time.Now().UnixNano())
	testRemote := c.remotePathFor(testName)

	// Try to create the test file
	argsTouch := c.buildRcloneArgs("touch")
	argsTouch = append(argsTouch, testRemote)
	c.logger.Debug("Running (remote write test): %s", strings.Join(argsTouch, " "))
	output, err := c.exec(ctx, argsTouch[0], argsTouch[1:]...)
	if err != nil {
		return classifyRemoteError("write", testRemote, err, output)
	}

	// Try to delete the test file (cleanup)
	// Don't fail the check if cleanup fails - write succeeded which is what matters
	argsDelete := c.buildRcloneArgs("deletefile")
	argsDelete = append(argsDelete, testRemote)
	c.logger.Debug("Running (remote write cleanup): %s", strings.Join(argsDelete, " "))
	_, err = c.exec(ctx, argsDelete[0], argsDelete[1:]...)
	if err != nil {
		c.logger.Debug("Warning: write test file cleanup failed (may lack delete permissions): %v", err)
		// Don't return error - write succeeded which confirms write access
	}

	return nil
}

// shouldFallbackToWriteTest determines if automatic fallback to write test is appropriate
func (c *CloudStorage) shouldFallbackToWriteTest(err *remoteCheckError, ctx context.Context) bool {
	// Only fallback for authentication/permission errors
	if err.kind != remoteErrorAuth {
		return false
	}

	// Don't fallback if the error is from write test itself (prevent infinite loop)
	if strings.Contains(err.msg, "write check") || strings.Contains(err.msg, "cleanup check") {
		return false
	}

	// Check if there's enough time left in the context for fallback
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < 5*time.Second {
			c.logger.Debug("Skipping fallback: insufficient time remaining (%v)", remaining)
			return false
		}
	}

	return true
}

func classifyRemoteError(stage, target string, err error, output []byte) error {
	text := strings.ToLower(strings.TrimSpace(string(output)))
	msg := fmt.Sprintf("rclone %s check failed for %s", stage, target)

	kind := detectRemoteErrorKind(text)

	return &remoteCheckError{
		kind: kind,
		msg:  fmt.Sprintf("%s: %s", msg, strings.TrimSpace(string(output))),
		err:  err,
	}
}

func detectRemoteErrorKind(text string) remoteErrorKind {
	// Try to classify based on typical rclone/network messages
	switch {
	case containsAny(text,
		"directory not found",
		"file not found",
		"couldn't find root",
		"path not found"):
		return remoteErrorPath
	case containsAny(text,
		"failed to create file system",
		"couldn't find configuration section",
		"not found in config file",
		"error reading section",
		"401 unauthorized",
		"403 forbidden",
		"access denied",
		"permission denied"):
		return remoteErrorAuth
	case containsAny(text,
		"dial tcp",
		"connection refused",
		"network is unreachable",
		"host is down",
		"no such host"):
		return remoteErrorNetwork
	default:
		return remoteErrorOther
	}
}

func containsAny(text string, substrings ...string) bool {
	for _, s := range substrings {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// Store uploads a backup file to cloud storage using rclone
func (c *CloudStorage) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	c.logger.Debug("Cloud storage: preparing to upload %s", filepath.Base(backupFile))
	// Check context
	if err := ctx.Err(); err != nil {
		c.logger.Debug("Cloud storage: store aborted due to context cancellation")
		return err
	}

	// Verify source file exists
	stat, err := os.Stat(backupFile)
	if err != nil {
		c.logger.Debug("Cloud storage: source file %s not found", backupFile)
		c.logger.Warning("WARNING: Cloud storage - backup file not found: %s: %v", backupFile, err)
		return &StorageError{
			Location:    LocationCloud,
			Operation:   "store",
			Path:        backupFile,
			Err:         fmt.Errorf("source file not found: %w", err),
			IsCritical:  false,
			Recoverable: false,
		}
	}

	filename := filepath.Base(backupFile)
	remoteFile := c.remotePathFor(filename)

	c.logger.Info("Uploading backup to cloud storage: %s (%s) -> %s (timeout: %ds)",
		filename,
		utils.FormatBytes(stat.Size()),
		c.remoteLabel(),
		c.config.RcloneTimeoutOperation)
	c.logger.Debug("Cloud storage: upload retries=%d threads=%d bwlimit=%s",
		c.config.RcloneRetries, c.config.RcloneTransfers, c.config.RcloneBandwidthLimit)

	// Use OPERATION timeout for upload (long timeout)
	uploadCtx, cancel := context.WithTimeout(ctx, time.Duration(c.config.RcloneTimeoutOperation)*time.Second)
	defer cancel()

	tasks := make([]uploadTask, 0, 4)
	tasks = append(tasks, uploadTask{
		local:  backupFile,
		remote: remoteFile,
		verify: true,
	})
	if !c.config.BundleAssociatedFiles {
		associatedFiles := []string{
			backupFile + ".sha256",
			backupFile + ".metadata",
			backupFile + ".metadata.sha256",
		}

		for _, srcFile := range associatedFiles {
			if _, err := os.Stat(srcFile); err != nil {
				continue // Skip if doesn't exist
			}

			tasks = append(tasks, uploadTask{
				local:  srcFile,
				remote: c.remotePathFor(filepath.Base(srcFile)),
				verify: c.parallelVerify,
			})
		}
	} else {
		// Upload bundle file
		bundleFile := backupFile + ".bundle.tar"
		if _, err := os.Stat(bundleFile); err == nil {
			tasks = append(tasks, uploadTask{
				local:  bundleFile,
				remote: c.remotePathFor(filepath.Base(bundleFile)),
				verify: c.parallelVerify,
			})
		}
	}

	primaryFailed, err := c.uploadTasks(uploadCtx, tasks)
	if err != nil {
		op := "upload_associated"
		target := "associated files"
		if primaryFailed {
			op = "upload"
			target = "primary backup"
			c.logger.Warning("WARNING: Cloud Storage: Backup not saved to %s", c.remoteLabel())
		}
		c.logger.Warning("WARNING: Cloud Storage: Failed to upload %s: %v", target, err)
		return &StorageError{
			Location:    LocationCloud,
			Operation:   op,
			Path:        backupFile,
			Err:         err,
			IsCritical:  false,
			Recoverable: true,
		}
	}

	c.logger.Info("Cloud storage: upload and verification completed for %s", filename)
	c.logger.Debug("✓ Cloud Storage: File uploaded")

	if count := c.countBackups(ctx); count >= 0 {
		c.logger.Debug("Cloud storage: current backups detected after upload: %d", count)
	} else {
		c.logger.Debug("Cloud storage: unable to count backups after upload (see previous log for details)")
	}

	return nil
}

func (c *CloudStorage) countBackups(ctx context.Context) int {
	backups, err := c.List(ctx)
	if err != nil {
		c.logger.Debug("Cloud storage: failed to list backups for recount: %v", err)
		return -1
	}
	return len(backups)
}

// uploadWithRetry uploads a file with automatic retry on failure
func (c *CloudStorage) uploadWithRetry(ctx context.Context, localFile, remoteFile string) error {
	var lastErr error

	for attempt := 1; attempt <= c.config.RcloneRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		if attempt > 1 {
			c.logger.Info("Upload retry attempt %d/%d for %s",
				attempt,
				c.config.RcloneRetries,
				filepath.Base(localFile))
		}

		err := c.rcloneCopy(ctx, localFile, remoteFile)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is due to timeout
		if ctx.Err() == context.DeadlineExceeded {
			c.logger.Warning("Upload attempt %d/%d failed: operation timeout (%ds exceeded)",
				attempt,
				c.config.RcloneRetries,
				c.config.RcloneTimeoutOperation)
		} else {
			c.logger.Warning("Upload attempt %d/%d failed: %v",
				attempt,
				c.config.RcloneRetries,
				err)
		}

		// Don't retry if we've run out of time
		if ctx.Err() != nil {
			break
		}

		// Wait before retry (exponential backoff)
		if attempt < c.config.RcloneRetries {
			waitTime := time.Duration(1<<uint(attempt)) * time.Second
			c.logger.Debug("Waiting %v before retry...", waitTime)
			c.sleep(waitTime)
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("upload failed: operation timeout (%ds exceeded) after %d attempts",
			c.config.RcloneTimeoutOperation,
			c.config.RcloneRetries)
	}

	return fmt.Errorf("upload failed after %d attempts: %w",
		c.config.RcloneRetries,
		lastErr)
}

type uploadTask struct {
	local  string
	remote string
	verify bool
}

func (c *CloudStorage) uploadTasks(ctx context.Context, tasks []uploadTask) (bool, error) {
	if len(tasks) == 0 {
		return false, nil
	}

	first := tasks[0]
	if err := c.runUploadTask(ctx, first); err != nil {
		return true, c.wrapUploadError(first.local, err)
	}

	remaining := tasks[1:]
	if len(remaining) == 0 {
		return false, nil
	}

	if !c.shouldUseParallelUpload() {
		return c.uploadTasksSequential(ctx, remaining)
	}

	return c.uploadTasksParallel(ctx, remaining)
}

func (c *CloudStorage) shouldUseParallelUpload() bool {
	return c.uploadMode == cloudUploadModeParallel && c.parallelJobs > 1
}

func (c *CloudStorage) runUploadTask(parentCtx context.Context, task uploadTask) error {
	taskCtx, cancel := context.WithTimeout(parentCtx, time.Duration(c.config.RcloneTimeoutOperation)*time.Second)
	defer cancel()

	if err := c.uploadWithRetry(taskCtx, task.local, task.remote); err != nil {
		return err
	}

	if !task.verify {
		return nil
	}

	ok, err := c.VerifyUpload(parentCtx, task.local, task.remote)
	if err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("verification failed")
		}
		return err
	}

	return nil
}

func (c *CloudStorage) uploadTasksSequential(ctx context.Context, tasks []uploadTask) (bool, error) {
	for _, task := range tasks {
		if err := c.runUploadTask(ctx, task); err != nil {
			return false, c.wrapUploadError(task.local, err)
		}
	}
	return false, nil
}

func (c *CloudStorage) uploadTasksParallel(ctx context.Context, tasks []uploadTask) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, c.parallelJobs)

	for _, task := range tasks {
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			if err := c.runUploadTask(ctx, task); err != nil {
				select {
				case errCh <- c.wrapUploadError(task.local, err):
				default:
				}
				cancel()
			}
		}()
	}

	wg.Wait()
	close(errCh)
	if err, ok := <-errCh; ok {
		return false, err
	}
	return false, nil
}

func (c *CloudStorage) wrapUploadError(localPath string, err error) error {
	return fmt.Errorf("%s: %w", filepath.Base(localPath), err)
}

// UploadToRemotePath uploads an arbitrary file to the provided remote path using
// the same retry and verification logic used for backups.
func (c *CloudStorage) UploadToRemotePath(ctx context.Context, localFile, remoteFile string, verify bool) error {
	if err := c.uploadWithRetry(ctx, localFile, remoteFile); err != nil {
		return err
	}
	if verify {
		if ok, err := c.VerifyUpload(ctx, localFile, remoteFile); err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("verification failed")
			}
			return err
		}
	}
	return nil
}

// rcloneCopy executes rclone copy command
func (c *CloudStorage) rcloneCopy(ctx context.Context, localFile, remoteFile string) error {
	args := c.buildRcloneArgs("copyto")

	// Add bandwidth limit if configured
	if c.config.RcloneBandwidthLimit != "" {
		args = append(args, "--bwlimit", c.config.RcloneBandwidthLimit)
	}

	// Add parallel transfers
	if c.config.RcloneTransfers > 0 {
		args = append(args, "--transfers", fmt.Sprintf("%d", c.config.RcloneTransfers))
	}

	// Add progress and stats
	args = append(args, "--progress", "--stats", "10s")

	// Add source and destination
	args = append(args, localFile, remoteFile)

	c.logger.Debug("Running: %s", strings.Join(args, " "))

	output, err := c.exec(ctx, args[0], args[1:]...)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("rclone operation timeout")
		}
		return fmt.Errorf("rclone copy failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// VerifyUpload verifies that a file was successfully uploaded to cloud storage
// Uses two methods: primary (rclone lsl) and alternative (rclone ls + grep)
func (c *CloudStorage) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	// Get local file info
	localStat, err := os.Stat(localFile)
	if err != nil {
		return false, fmt.Errorf("cannot stat local file: %w", err)
	}

	filename := remoteBaseName(remoteFile)

	// Use primary verification method by default
	if c.config.RcloneVerifyMethod != "alternative" {
		return c.verifyPrimary(ctx, remoteFile, localStat.Size(), filename)
	}

	// Use alternative verification method
	return c.verifyAlternative(ctx, remoteFile, localStat.Size(), filename)
}

// verifyPrimary uses 'rclone lsl' to verify upload (primary method)
func (c *CloudStorage) verifyPrimary(ctx context.Context, remoteFile string, expectedSize int64, filename string) (bool, error) {
	args := c.buildRcloneArgs("lsl")
	args = append(args, remoteFile)

	c.logger.Debug("Verification (primary): %s", strings.Join(args, " "))

	output, err := c.exec(ctx, args[0], args[1:]...)

	if err != nil {
		c.logger.Debug("Primary verification failed, trying alternative method: %v", err)
		return c.verifyAlternative(ctx, remoteFile, expectedSize, filename)
	}

	// Parse lsl output: size filename
	outputStr := strings.TrimSpace(string(output))
	if outputStr == "" {
		return false, fmt.Errorf("empty lsl output - file may not exist")
	}

	// lsl format: "SIZE DATE TIME FILENAME"
	fields := strings.Fields(outputStr)
	if len(fields) < 4 {
		return false, fmt.Errorf("unexpected lsl output format: %s", outputStr)
	}

	// First field should be size
	var remoteSize int64
	if _, err := fmt.Sscanf(fields[0], "%d", &remoteSize); err != nil {
		return false, fmt.Errorf("cannot parse remote file size: %w", err)
	}

	if remoteSize != expectedSize {
		return false, fmt.Errorf("size mismatch: local=%d remote=%d", expectedSize, remoteSize)
	}

	c.logger.Debug("Verification successful: %s (%s)", filename, utils.FormatBytes(remoteSize))
	return true, nil
}

// verifyAlternative uses 'rclone ls | grep' to verify upload (alternative method)
func (c *CloudStorage) verifyAlternative(ctx context.Context, remoteFile string, expectedSize int64, filename string) (bool, error) {
	// List files in remote directory
	remoteDir := remoteDirRef(remoteFile)
	args := c.buildRcloneArgs("ls")
	args = append(args, remoteDir)

	c.logger.Debug("Verification (alternative): %s | grep %s", strings.Join(args, " "), filename)

	output, err := c.exec(ctx, args[0], args[1:]...)

	if err != nil {
		return false, fmt.Errorf("rclone ls failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	// Search for filename in output
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// ls format: "SIZE FILENAME"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		// Check if this is our file
		if fields[1] != filename {
			continue
		}

		// Parse size
		var remoteSize int64
		if _, err := fmt.Sscanf(fields[0], "%d", &remoteSize); err != nil {
			continue
		}

		if remoteSize != expectedSize {
			return false, fmt.Errorf("size mismatch: local=%d remote=%d", expectedSize, remoteSize)
		}

		c.logger.Debug("Verification successful (alternative): %s (%s)", filename, utils.FormatBytes(remoteSize))
		return true, nil
	}

	return false, fmt.Errorf("file not found in rclone ls output")
}

type lslEntry struct {
	fields   []string
	filename string
}

// List returns all backups in cloud storage
func (c *CloudStorage) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// List files in remote
	args := c.buildRcloneArgs("lsl")
	args = append(args, c.remoteBase())

	output, err := c.exec(ctx, args[0], args[1:]...)

	if err != nil {
		c.logger.Debug("WARNING: Cloud storage - failed to list backups: %v", err)
		return nil, &StorageError{
			Location:    LocationCloud,
			Operation:   "list",
			Path:        c.remoteLabel(),
			Err:         fmt.Errorf("rclone lsl failed: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	entries := parseLslEntries(string(output))
	snapshot := buildSnapshot(entries)
	c.setRemoteSnapshot(snapshot)

	backups := c.buildBackupMetadata(entries, snapshot)

	// Sort by timestamp (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return backups, nil
}

func parseLslEntries(output string) []lslEntry {
	lines := strings.Split(output, "\n")
	entries := make([]lslEntry, 0, len(lines))

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		filename := strings.Join(fields[3:], " ")
		filename = normalizeRemoteRelativePath(filename)
		if filename == "" {
			continue
		}

		entries = append(entries, lslEntry{
			fields:   fields,
			filename: filename,
		})
	}

	return entries
}

func buildSnapshot(entries []lslEntry) map[string]struct{} {
	snapshot := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		snapshot[entry.filename] = struct{}{}
	}
	return snapshot
}

func (c *CloudStorage) buildBackupMetadata(entries []lslEntry, snapshot map[string]struct{}) []*types.BackupMetadata {
	backups := make([]*types.BackupMetadata, 0, len(entries))

	for _, entry := range entries {
		filename := entry.filename

		if !c.isBackupEntry(filename, snapshot) {
			continue
		}

		size, ok := parseEntrySize(entry.fields[0])
		if !ok {
			c.logger.Debug("Cloud storage: skipping %s (cannot parse size from %q)", filename, entry.fields[0])
			continue
		}

		timestamp, ok := parseEntryTimestamp(entry.fields[1], entry.fields[2])
		if !ok {
			timeStr := entry.fields[1] + " " + entry.fields[2]
			c.logger.Debug("Cloud storage: skipping %s (cannot parse timestamp %q)", filename, timeStr)
			continue
		}

		backups = append(backups, &types.BackupMetadata{
			BackupFile: filename,
			Timestamp:  timestamp,
			Size:       size,
		})
	}

	return backups
}

func (c *CloudStorage) isBackupEntry(filename string, snapshot map[string]struct{}) bool {
	// Only include backup files (legacy `proxmox-backup-*` or Go `*-backup-*`)
	isNewName := strings.Contains(filename, "-backup-")
	isLegacy := strings.HasPrefix(filename, "proxmox-backup-")
	if !(isLegacy || isNewName) {
		return false
	}

	if !strings.Contains(filename, ".tar") {
		return false
	}

	// Skip associated files
	if strings.HasSuffix(filename, ".sha256") ||
		strings.HasSuffix(filename, ".metadata") {
		return false
	}

	// When bundling is enabled, skip standalone files that have a corresponding bundle
	if c.config != nil && c.config.BundleAssociatedFiles {
		if !strings.HasSuffix(filename, ".bundle.tar") {
			bundleFilename := filename + ".bundle.tar"
			if _, ok := snapshot[bundleFilename]; ok {
				c.logger.Debug("Skipping standalone file %s (bundle exists)", filename)
				return false
			}
		}
	}

	return true
}

func parseEntrySize(sizeField string) (int64, bool) {
	var size int64
	if _, err := fmt.Sscanf(sizeField, "%d", &size); err != nil {
		return 0, false
	}
	return size, true
}

func parseEntryTimestamp(dateField, timeField string) (time.Time, bool) {
	timeStr := dateField + " " + timeField
	timestamp, err := time.Parse("2006-01-02 15:04:05", timeStr)
	if err != nil {
		return time.Time{}, false
	}
	return timestamp, true
}

// Delete removes a backup file from cloud storage
func (c *CloudStorage) Delete(ctx context.Context, backupFile string) error {
	_, err := c.deleteBackupInternal(ctx, backupFile)
	return err
}

func (c *CloudStorage) deleteBackupInternal(ctx context.Context, backupFile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	c.ensureRemoteSnapshot(ctx)

	filename := strings.TrimSpace(backupFile)
	if filename == "" {
		return false, nil
	}
	baseName, _ := trimBundleSuffix(filename)

	c.logger.Debug("Deleting cloud backup: %s", backupFile)

	candidateNames := buildBackupCandidatePaths(baseName, c.config.BundleAssociatedFiles)
	relativeNames := make([]string, 0, len(candidateNames))
	for _, name := range candidateNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		relativeNames = append(relativeNames, name)
	}

	failedFiles := make([]string, 0)

	// Delete all files
	for _, rel := range relativeNames {
		exists, snapshotReady := c.remoteFileExists(rel)
		if snapshotReady && !exists {
			c.logger.Debug("Cloud storage: skipping delete for %s (not present on remote snapshot)", rel)
			continue
		}

		f := c.remotePathFor(rel)
		args := c.buildRcloneArgs("deletefile")
		args = append(args, f)

		c.logger.Debug("Running: %s", strings.Join(args, " "))

		output, err := c.exec(ctx, args[0], args[1:]...)

		if err != nil {
			msg := strings.TrimSpace(string(output))
			if isRcloneObjectNotFound(msg) {
				c.logger.Debug("Cloud storage: file already removed %s (%s)", filepath.Base(f), msg)
				c.removeRemoteSnapshotEntry(rel)
				continue
			}
			c.logger.Warning("WARNING: Cloud storage - failed to delete %s: %v: %s",
				filepath.Base(f), err, msg)
			failedFiles = append(failedFiles, filepath.Base(f))
			// Continue with other files
			continue
		}
		c.removeRemoteSnapshotEntry(rel)
	}

	// Best-effort: delete associated cloud log file for this backup
	logDeleted := c.deleteAssociatedLog(ctx, backupFile)

	if len(failedFiles) > 0 {
		return logDeleted, fmt.Errorf("failed to delete %d file(s): %v", len(failedFiles), failedFiles)
	}

	c.logger.Debug("Deleted cloud backup: %s", backupFile)
	return logDeleted, nil
}

// deleteAssociatedLog attempts to remove the cloud log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (c *CloudStorage) deleteAssociatedLog(ctx context.Context, backupFile string) bool {
	if c == nil || c.config == nil {
		return false
	}

	base := strings.TrimSpace(c.config.CloudLogPath)
	if base == "" {
		return false
	}

	host, ts, ok := extractLogKeyFromBackup(backupFile)
	if !ok {
		return false
	}

	logName := fmt.Sprintf("backup-%s-%s.log", host, ts)
	cloudPath := c.cloudLogPath(base, logName)
	if strings.TrimSpace(cloudPath) == "" {
		return false
	}

	if c.isCloudLogPathUnavailable() {
		c.logger.Debug("Cloud logs: skipping delete for %s (log path unavailable)", cloudPath)
		return false
	}

	args := c.buildRcloneArgs("delete")
	args = append(args, cloudPath)
	c.logger.Debug("Cloud logs: deleting log %s", cloudPath)
	output, err := c.exec(ctx, args[0], args[1:]...)
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if isRcloneObjectNotFound(msg) || isRcloneObjectNotFound(err.Error()) {
			if strings.Contains(strings.ToLower(msg), "directory") {
				c.markCloudLogPathMissing(base, msg)
			} else {
				c.logger.Debug("Cloud logs: log already removed %s (%s)", cloudPath, msg)
			}
			return false
		}
		if msg != "" {
			c.logger.Debug("Cloud logs: delete output for %s: %s", cloudPath, msg)
		}
		c.logger.Warning("WARNING: Cloud logs - failed to delete %s: %v", cloudPath, err)
		return false
	}

	c.logger.Debug("Cloud logs: deleted log %s", cloudPath)
	return true
}

func (c *CloudStorage) countLogFiles(ctx context.Context) int {
	if c == nil || c.config == nil {
		return -1
	}
	base := strings.TrimSpace(c.config.CloudLogPath)
	if base == "" {
		return 0
	}

	if c.isCloudLogPathUnavailable() {
		return -1
	}

	args := c.buildRcloneArgs("lsf")
	args = append(args, base, "--files-only")
	output, err := c.exec(ctx, args[0], args[1:]...)
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if isRcloneObjectNotFound(msg) || isRcloneObjectNotFound(err.Error()) {
			c.markCloudLogPathMissing(base, msg)
			return -1
		}
		if msg != "" {
			c.logger.Debug("Cloud logs: lsf output for %s: %s", base, msg)
		}
		c.logger.Debug("Cloud logs: failed to count log files at %s: %v", base, err)
		return -1
	}
	c.markCloudLogPathAvailable()

	count := 0
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		if strings.HasPrefix(name, "backup-") && strings.HasSuffix(name, ".log") {
			count++
		}
	}
	return count
}

// cloudLogPath builds the full cloud log path, mirroring buildCloudLogDestination logic.
// Supports both new style (/path) and legacy style (remote:/path).
// If basePath doesn't contain ":", uses c.remote as the remote name.
func (c *CloudStorage) cloudLogPath(basePath, fileName string) string {
	base := strings.TrimSpace(basePath)
	if base == "" {
		return ""
	}
	// If basePath doesn't contain ":", prepend c.remote
	if !strings.Contains(base, ":") && c.remote != "" {
		base = c.remote + ":" + base
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, ":") {
		return base + fileName
	}
	if strings.Contains(base, ":") {
		return base + "/" + fileName
	}
	return filepath.Join(base, fileName)
}

// ApplyRetention removes old backups according to retention policy
// Supports both simple (count-based) and GFS (time-distributed) policies
// Uses batched deletion to avoid API rate limits
func (c *CloudStorage) ApplyRetention(ctx context.Context, config RetentionConfig) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// List all backups
	c.logger.Debug("Cloud storage: listing backups for retention policy '%s'", config.Policy)
	backups, err := c.List(ctx)
	if err != nil {
		c.logger.Debug("WARNING: Cloud storage - failed to list backups for retention: %v", err)
		return 0, &StorageError{
			Location:    LocationCloud,
			Operation:   "apply_retention",
			Path:        c.remoteLabel(),
			Err:         err,
			IsCritical:  false,
			Recoverable: true,
		}
	}

	c.logger.Debug("Cloud storage: current backups detected: %d", len(backups))

	if len(backups) == 0 {
		c.logger.Debug("Cloud storage: no backups to apply retention")
		return 0, nil
	}

	// Apply appropriate retention policy
	if config.Policy == "gfs" {
		return c.applyGFSRetention(ctx, backups, config)
	}
	return c.applySimpleRetention(ctx, backups, config.MaxBackups)
}

// applyGFSRetention applies GFS (Grandfather-Father-Son) retention policy
func (c *CloudStorage) applyGFSRetention(ctx context.Context, backups []*types.BackupMetadata, config RetentionConfig) (int, error) {
	c.logger.Debug("Applying GFS retention policy (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
		config.Daily, config.Weekly, config.Monthly, config.Yearly)

	// Classify backups according to GFS scheme
	classification := ClassifyBackupsGFS(backups, config)

	// Get statistics
	stats := GetRetentionStats(classification)
	kept := len(backups) - stats[CategoryDelete]
	c.logger.Debug("GFS classification → daily: %d/%d, weekly: %d/%d, monthly: %d/%d, yearly: %d/%d, kept: %d, to_delete: %d",
		stats[CategoryDaily], config.Daily,
		stats[CategoryWeekly], config.Weekly,
		stats[CategoryMonthly], config.Monthly,
		stats[CategoryYearly], config.Yearly,
		kept,
		stats[CategoryDelete])

	// Collect backups marked for deletion
	toDelete := make([]*types.BackupMetadata, 0)
	for backup, category := range classification {
		if category == CategoryDelete {
			toDelete = append(toDelete, backup)
		}
	}

	// Ensure deterministic deletion order (sorted by filename)
	sort.Slice(toDelete, func(i, j int) bool {
		return toDelete[i].BackupFile < toDelete[j].BackupFile
	})

	// Delete in batches to avoid API rate limits
	return c.deleteBatched(ctx, toDelete, len(backups))
}

// applySimpleRetention applies simple count-based retention policy
func (c *CloudStorage) applySimpleRetention(ctx context.Context, backups []*types.BackupMetadata, maxBackups int) (int, error) {
	if maxBackups <= 0 {
		c.logger.Debug("Retention disabled for cloud storage (maxBackups = %d)", maxBackups)
		return 0, nil
	}

	totalBackups := len(backups)
	if totalBackups <= maxBackups {
		c.logger.Debug("Cloud storage: %d backups (within retention limit of %d)", totalBackups, maxBackups)
		return 0, nil
	}

	// Calculate how many to delete
	toDelete := totalBackups - maxBackups
	c.logger.Info("Applying simple retention policy: %d backups found, limit is %d, deleting %d oldest",
		totalBackups, maxBackups, toDelete)
	c.logger.Info("Simple retention → current: %d, limit: %d, to_delete: %d",
		totalBackups, maxBackups, toDelete)

	// Collect oldest backups (already sorted newest first)
	oldBackups := backups[maxBackups:]

	// Delete in batches to avoid API rate limits
	return c.deleteBatched(ctx, oldBackups, totalBackups)
}

// deleteBatched deletes backups in batches to avoid API rate limits
func (c *CloudStorage) deleteBatched(ctx context.Context, backups []*types.BackupMetadata, totalBackups int) (int, error) {
	deleted := 0
	logsDeleted := 0
	batchSize := c.config.CloudBatchSize
	batchPause := time.Duration(c.config.CloudBatchPause) * time.Second
	initialLogs := c.countLogFiles(ctx)

	for i, backup := range backups {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		c.logger.Debug("Deleting old backup: %s (created: %s)",
			backup.BackupFile,
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := c.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			c.logger.Warning("WARNING: Cloud storage - failed to delete %s: %v", backup.BackupFile, err)
			continue
		}

		deleted++
		if logDeleted {
			logsDeleted++
		}

		// Pause after each batch to avoid API rate limits
		if batchSize > 0 && deleted%batchSize == 0 && i < len(backups)-1 {
			c.logger.Debug("Batch of %d deletions completed, pausing for %v to avoid API rate limits",
				batchSize, batchPause)
			c.sleep(batchPause)
		}
	}

	remaining := totalBackups - deleted
	if remaining < 0 {
		remaining = 0
	}

	if logsRemaining, ok := computeRemaining(initialLogs, logsDeleted); ok {
		c.logger.Debug("Cloud storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		c.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		c.logger.Debug("Cloud storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		c.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// LastRetentionSummary returns the latest retention summary.
func (c *CloudStorage) LastRetentionSummary() RetentionSummary {
	return c.lastRet
}

// GetStats returns storage statistics
func (c *CloudStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	backups, err := c.List(ctx)
	if err != nil {
		c.logger.Debug("WARNING: Cloud storage - failed to get stats: %v", err)
		return nil, err
	}

	stats := &StorageStats{
		TotalBackups:   len(backups),
		FilesystemType: FilesystemType("rclone-" + c.remote),
	}

	var totalSize int64
	var oldest, newest *time.Time

	for _, backup := range backups {
		totalSize += backup.Size

		if oldest == nil || backup.Timestamp.Before(*oldest) {
			t := backup.Timestamp
			oldest = &t
		}
		if newest == nil || backup.Timestamp.After(*newest) {
			t := backup.Timestamp
			newest = &t
		}
	}

	stats.TotalSize = totalSize
	stats.OldestBackup = oldest
	stats.NewestBackup = newest

	return stats, nil
}

func (c *CloudStorage) ensureRemoteSnapshot(ctx context.Context) {
	c.remoteFilesMu.RLock()
	hasSnapshot := c.remoteFiles != nil
	c.remoteFilesMu.RUnlock()
	if hasSnapshot {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := c.List(ctx); err != nil && c.logger != nil {
		c.logger.Debug("Cloud storage: unable to refresh remote snapshot: %v", err)
	}
}

func (c *CloudStorage) setRemoteSnapshot(entries map[string]struct{}) {
	c.remoteFilesMu.Lock()
	c.remoteFiles = entries
	c.remoteFilesMu.Unlock()
}

func (c *CloudStorage) remoteFileExists(name string) (exists bool, snapshotReady bool) {
	normalized := normalizeRemoteRelativePath(name)
	if normalized == "" {
		return false, false
	}
	c.remoteFilesMu.RLock()
	defer c.remoteFilesMu.RUnlock()
	if c.remoteFiles == nil {
		return true, false
	}
	_, ok := c.remoteFiles[normalized]
	return ok, true
}

func (c *CloudStorage) removeRemoteSnapshotEntry(name string) {
	normalized := normalizeRemoteRelativePath(name)
	if normalized == "" {
		return
	}
	c.remoteFilesMu.Lock()
	defer c.remoteFilesMu.Unlock()
	if c.remoteFiles != nil {
		delete(c.remoteFiles, normalized)
	}
}

func (c *CloudStorage) isCloudLogPathUnavailable() bool {
	c.logPathMu.Lock()
	defer c.logPathMu.Unlock()
	return c.logPathMissing
}

func (c *CloudStorage) markCloudLogPathMissing(base, msg string) {
	c.logPathMu.Lock()
	alreadyMissing := c.logPathMissing
	if !alreadyMissing {
		c.logPathMissing = true
	}
	c.logPathMu.Unlock()
	if alreadyMissing || c.logger == nil {
		return
	}
	message := strings.TrimSpace(msg)
	if message != "" {
		c.logger.Warning("WARNING: Cloud logs path %s not found (%s) - skipping cloud log cleanup", base, message)
	} else {
		c.logger.Warning("WARNING: Cloud logs path %s not found - skipping cloud log cleanup", base)
	}
}

func (c *CloudStorage) markCloudLogPathAvailable() {
	c.logPathMu.Lock()
	c.logPathMissing = false
	c.logPathMu.Unlock()
}

func (c *CloudStorage) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.execCommand != nil {
		return c.execCommand(ctx, name, args...)
	}
	return defaultExecCommand(ctx, name, args...)
}

func defaultExecCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func normalizeRemoteRelativePath(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "." {
		return ""
	}
	clean := path.Clean(name)
	for strings.HasPrefix(clean, "./") {
		clean = strings.TrimPrefix(clean, "./")
	}
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." || clean == "" {
		return ""
	}
	if strings.HasPrefix(clean, "..") {
		return filepath.Base(clean)
	}
	return clean
}

func isRcloneObjectNotFound(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "object not found") ||
		strings.Contains(lower, "file not found") ||
		strings.Contains(lower, "directory not found") ||
		strings.Contains(lower, "doesn't exist")
}
