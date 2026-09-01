package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// SecondaryStorage implements the Storage interface for secondary (remote) filesystem storage
// This is typically a network mount (NFS/CIFS) or another local path
// All errors from secondary storage are NON-FATAL - they log warnings but don't abort the backup
type SecondaryStorage struct {
	config *config.Config
	logger *logging.Logger
	// hostname is this machine's name, resolved once at construction: retention
	// only prunes backups this host owns.
	hostname string
	// hostAliases are the other names this machine answers to, built from the one
	// name this run's writer stamped into the archives it produced ("hostname -f",
	// so usually the FQDN). Empty on a machine with no domain, which leaves
	// retention as strict as it has always been.
	hostAliases []string
	// serverID is this host's own server identity. See LocalStorage.serverID: it can
	// only ever confirm a claim the hostname already makes ambiguously.
	serverID   string
	basePath   string
	fsDetector *FilesystemDetector
	fsInfo     *FilesystemInfo
	lastRet    RetentionSummary
	// See the note on LocalStorage.scopeOwned: kept outside lastRet because that
	// struct is replaced wholesale on the delete paths.
	scopeOwned int
	scopeValid bool
	// See the note on LocalStorage.lastRetCompleted: no count in lastRet can say
	// whether a pass ran at all, so the answer is published beside them.
	lastRetCompleted bool
}

// NewSecondaryStorage creates a new secondary storage instance.
//
// writtenHostname is the name this run's writer stamps into the archives it
// produces (resolveHostname in package main, which prefers "hostname -f"). It is
// what lets retention recognise its own FQDN-named archives while os.Hostname only
// reports the kernel short name. Pass "" when the caller never runs retention: that
// is safe but strict, and archives written under any other spelling of this
// machine's name stop being rotated.
func NewSecondaryStorage(cfg *config.Config, logger *logging.Logger, writtenHostname string) (*SecondaryStorage, error) {
	host := resolveRetentionHostname()
	serverID := types.NormalizeServerID(cfg.ServerID)
	logRetentionServerIdentity(logger, "Secondary storage", serverID)
	return &SecondaryStorage{
		config:      cfg,
		logger:      logger,
		hostname:    host,
		hostAliases: retentionHostAliases(host, []string{writtenHostname}),
		serverID:    serverID,
		basePath:    cfg.SecondaryPath,
		fsDetector:  NewFilesystemDetector(logger, WithIOTimeout(fsIoTimeout(cfg))),
	}, nil
}

// Name returns the storage backend name
func (s *SecondaryStorage) Name() string {
	return "Secondary Storage"
}

// Location returns the backup location type
func (s *SecondaryStorage) Location() BackupLocation {
	return LocationSecondary
}

// IsEnabled returns true if secondary storage is configured
func (s *SecondaryStorage) IsEnabled() bool {
	return s.config.SecondaryEnabled && s.basePath != ""
}

// IsCritical returns false because secondary storage is non-critical
// Failures in secondary storage should NOT abort the backup
func (s *SecondaryStorage) IsCritical() bool {
	return false
}

// DetectFilesystem detects the filesystem type for the secondary path
func (s *SecondaryStorage) DetectFilesystem(ctx context.Context) (info *FilesystemInfo, err error) {
	done := logging.DebugStart(s.logger, "secondary detect filesystem", "path=%s", s.basePath)
	defer func() { done(err) }()
	// Ensure directory exists (bounded: secondary is typically an NFS/CIFS mount).
	if err := safefs.MkdirAll(ctx, s.basePath, 0700, fsIoTimeout(s.config)); err != nil {
		// Non-critical error - log warning and return
		s.logger.Warning("Cannot create secondary backup directory %s: %v", s.basePath, err)
		s.logger.Warning("Secondary backup will be skipped")
		return nil, &StorageError{
			Location:    LocationSecondary,
			Operation:   "detect_filesystem",
			Path:        s.basePath,
			Err:         fmt.Errorf("failed to create directory: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	fsInfo, err := s.fsDetector.DetectFilesystem(ctx, s.basePath)
	if err != nil {
		// Non-critical error - log warning
		s.logger.Warning("Failed to detect filesystem type for secondary storage: %v", err)
		s.logger.Warning("Copying files anyway; ownership and permissions will not be set")
		// Create minimal fsInfo with unknown type
		fsInfo = &FilesystemInfo{
			Path:              s.basePath,
			Type:              FilesystemUnknown,
			SupportsOwnership: false,
		}
	}

	s.fsInfo = fsInfo
	return fsInfo, nil
}

// Store copies a backup file to secondary storage using an atomic Go-based copy
func (s *SecondaryStorage) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) (err error) {
	done := logging.DebugStart(s.logger, "secondary store", "file=%s", filepath.Base(backupFile))
	defer func() { done(err) }()
	s.logger.Debug("Secondary storage: preparing to store %s", filepath.Base(backupFile))
	// Check context
	if err := ctx.Err(); err != nil {
		s.logger.Debug("Secondary storage: store aborted due to context cancellation")
		return err
	}

	bundleEnabled := s.config != nil && s.config.BundleAssociatedFiles
	sourceFile := backupFile
	if bundleEnabled {
		sourceFile = bundlePathFor(sourceFile)
	}

	// Verify source file exists (bounded against a dead/stale mount).
	if _, err := safefs.Stat(ctx, sourceFile, fsIoTimeout(s.config)); err != nil {
		// Bounded against a dead/stale mount: see the twin in cloud.go. A failure is
		// as likely to be a timeout as a missing file, and the error names the path.
		s.logger.Warning("Secondary storage - failed to read the backup to copy: %v", err)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        sourceFile,
			Err:         fmt.Errorf("source file could not be read: %w", err),
			IsCritical:  false,
			Recoverable: false,
		}
	}

	// Ensure destination directory exists (bounded against a dead/stale mount).
	if err := safefs.MkdirAll(ctx, s.basePath, 0700, fsIoTimeout(s.config)); err != nil {
		s.logger.Debug("Secondary storage: failed to create destination folder %s", s.basePath)
		s.logger.Warning("Secondary storage - failed to create destination directory %s: %v", s.basePath, err)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        s.basePath,
			Err:         fmt.Errorf("failed to create destination directory: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Determine destination filename
	destFile := filepath.Join(s.basePath, filepath.Base(sourceFile))

	s.logger.Debug("Secondary Storage: Start copy...")
	s.logger.Debug("Copying backup to secondary storage: %s -> %s", filepath.Base(sourceFile), s.basePath)

	if err := s.copyFile(ctx, sourceFile, destFile); err != nil {
		s.logger.Warning("Secondary Storage: File copy failed for %s: %v", filepath.Base(sourceFile), err)
		s.logger.Warning("Secondary Storage: Backup not saved to %s", s.basePath)
		return &StorageError{
			Location:    LocationSecondary,
			Operation:   "store",
			Path:        sourceFile,
			Err:         fmt.Errorf("copy failed: %w", err),
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Copy associated files if not bundled
	if !bundleEnabled {
		associatedFiles := []string{
			backupFile + ".sha256",
			backupFile + ".metadata",
			backupFile + ".metadata.sha256",
		}
		failedAssoc := make([]string, 0)

		for _, srcFile := range associatedFiles {
			if _, err := safefs.Stat(ctx, srcFile, fsIoTimeout(s.config)); err != nil {
				continue // Skip if doesn't exist
			}

			destAssocFile := filepath.Join(s.basePath, filepath.Base(srcFile))
			if err := s.copyFile(ctx, srcFile, destAssocFile); err != nil {
				s.logger.Warning("Secondary Storage: Failed to copy associated file: %v", err)
				failedAssoc = append(failedAssoc, filepath.Base(srcFile))
				// Continue with other files
			}
		}

		if len(failedAssoc) > 0 {
			s.logger.Warning("Secondary Storage: %d associated file(s) failed to copy: %s",
				len(failedAssoc), strings.Join(failedAssoc, ", "))
		}
	}

	// Set permissions on destination (best effort)
	if s.fsInfo != nil && s.fsInfo.SupportsOwnership {
		if err := s.fsDetector.SetPermissions(ctx, destFile, 0, 0, 0600, s.fsInfo); err != nil {
			s.logger.Warning("Secondary storage - failed to set permissions on %s: %v",
				filepath.Base(destFile), err)
			// Not critical - continue
		}
	}

	s.logger.Debug("✓ Secondary Storage: File copied")

	if count := s.countBackups(ctx); count >= 0 {
		s.logger.Debug("Secondary storage: current backups detected after copy: %d", count)
	} else {
		s.logger.Debug("Secondary storage: unable to count backups after copy (see previous log for details)")
	}

	return nil
}

// countBackups lists current backups on secondary storage for logging/diagnostic purposes.
func (s *SecondaryStorage) countBackups(ctx context.Context) int {
	backups, err := s.List(ctx)
	if err != nil {
		s.logger.Debug("Secondary storage: failed to list backups for recount: %v", err)
		return -1
	}
	return len(backups)
}

// secondaryCloseSourceFile closes the read source after a copy. It is a seam so
// a test can prove that a close failure on the success path (after the
// destination has been durably renamed) is treated as best-effort read-side
// cleanup and never turns a committed copy into a reported failure.
var secondaryCloseSourceFile = func(f *os.File) error { return f.Close() }

// copyFile copies a file using Go's io.Copy
func (s *SecondaryStorage) copyFile(ctx context.Context, src, dest string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Bound the leaf metadata/open/finalize syscalls AND the byte-transfer loop
	// (via safefs.CopyBounded below) so a dead/stale secondary mount cannot wedge
	// any of them in an uninterruptible syscall.
	to := fsIoTimeout(s.config)

	sourceInfo, err := safefs.Stat(ctx, src, to)
	if err != nil {
		return fmt.Errorf("failed to stat source file %s: %w", src, err)
	}

	destDir := filepath.Dir(dest)
	if err := safefs.MkdirAll(ctx, destDir, 0700, to); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", destDir, err)
	}

	tempFile, err := safefs.CreateTemp(ctx, destDir, fmt.Sprintf(".tmp-%s-", filepath.Base(dest)), to)
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", destDir, err)
	}
	tempName := tempFile.Name()
	defer func() {
		if tempFile != nil {
			if _, closeErr := safefs.Run(ctx, "secondary-close-temp", tempName, to, func() (struct{}, error) {
				return struct{}{}, tempFile.Close()
			}); closeErr != nil && err == nil {
				err = fmt.Errorf("failed to close temporary file %s: %w", tempName, closeErr)
			}
		}
		if tempName != "" {
			if removeErr := safefs.Remove(ctx, tempName, to); removeErr != nil && err == nil && !os.IsNotExist(removeErr) {
				err = fmt.Errorf("failed to remove temporary file %s: %w", tempName, removeErr)
			}
		}
	}()

	start := time.Now()
	sourceFile, err := safefs.Open(ctx, src, to)
	if err != nil {
		return fmt.Errorf("failed to open source file %s: %w", src, err)
	}
	defer func() {
		if sourceFile == nil { // abandoned copy worker may still hold this fd
			return
		}
		// Best-effort read-side cleanup: on the success path the destination has
		// already been durably renamed into place before this runs, so a failed or
		// timed-out close of the read-only source must NOT turn a committed copy
		// into a reported failure (that would drive misleading retries / duplicate
		// backups). On the error paths err is already set, so this never masks a
		// pre-commit failure either.
		if _, closeErr := safefs.Run(ctx, "secondary-close-src", src, to, func() (struct{}, error) {
			return struct{}{}, secondaryCloseSourceFile(sourceFile)
		}); closeErr != nil {
			s.logger.Debug("Secondary storage: failed to close source file %s: %v", src, closeErr)
		}
	}()

	// Stream the bytes under a per-chunk stall budget so a mount dying mid-copy
	// cannot wedge Read/Write in an uninterruptible (D-state) syscall. The copy
	// bypasses the shared safefs limiter (it is sequential, so it self-throttles
	// to one in-flight worker and must not erode the slot budget the critical
	// paths rely on). On a stalled chunk the worker is abandoned and may still
	// hold these handles, so on abandonment we drop them and skip the closes.
	written, copyErr := safefs.CopyBounded(ctx, tempFile, sourceFile, 1024*1024, to, "secondary-copy", src)
	if copyErr != nil {
		if safefs.IsAbandoned(copyErr) {
			tempFile = nil
			sourceFile = nil
		}
		return fmt.Errorf("stream copy %s -> %s: %w", src, dest, copyErr)
	}

	if _, err := safefs.Run(ctx, "secondary-sync-temp", tempName, to, func() (struct{}, error) {
		return struct{}{}, tempFile.Sync()
	}); err != nil {
		return fmt.Errorf("failed to sync temporary file %s: %w", tempName, err)
	}
	_, closeErr := safefs.Run(ctx, "secondary-close-temp", tempName, to, func() (struct{}, error) {
		return struct{}{}, tempFile.Close()
	})
	tempFile = nil
	if closeErr != nil {
		return fmt.Errorf("failed to close temporary file %s: %w", tempName, closeErr)
	}

	if err := safefs.Chmod(ctx, tempName, sourceInfo.Mode(), to); err != nil {
		s.logger.Debug("Secondary storage: unable to mirror permissions on %s: %v", tempName, err)
	}
	if _, err := safefs.Run(ctx, "secondary-chtimes", tempName, to, func() (struct{}, error) {
		return struct{}{}, os.Chtimes(tempName, sourceInfo.ModTime(), sourceInfo.ModTime())
	}); err != nil {
		s.logger.Debug("Secondary storage: unable to mirror timestamps on %s: %v", tempName, err)
	}

	if err := safefs.Rename(ctx, tempName, dest, to); err != nil {
		return fmt.Errorf("failed to finalize copy to %s: %w", dest, err)
	}
	tempName = ""

	elapsed := time.Since(start)
	var rateStr string
	if elapsed > 0 {
		rate := float64(written) / elapsed.Seconds()
		if rate < 0 {
			rate = 0
		}
		rateStr = fmt.Sprintf("%s/s", utils.FormatBytes(int64(rate)))
	} else {
		rateStr = "n/a"
	}
	s.logger.Debug("Copied %s (%s) to %s in %s (avg %s)", filepath.Base(src), utils.FormatBytes(written), dest, elapsed.Truncate(time.Millisecond), rateStr)
	return nil
}

// List returns all backups in secondary storage
func (s *SecondaryStorage) List(ctx context.Context) (backups []*types.BackupMetadata, err error) {
	done := logging.DebugStart(s.logger, "secondary list", "path=%s", s.basePath)
	defer func() { done(err) }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Find all backup files (legacy + Go naming)
	globPatterns := []string{
		filepath.Join(s.basePath, "proxmox-backup-*.tar.*"), // Legacy Bash naming
		filepath.Join(s.basePath, "*-backup-*.tar*"),        // Go pipeline naming (bundle included)
	}

	timeout := fsIoTimeout(s.config)
	var matches []string
	seen := make(map[string]struct{})
	for _, pattern := range globPatterns {
		patternMatches, err := safefs.Run(ctx, "secondary-glob", s.basePath, timeout, func() ([]string, error) {
			return filepath.Glob(pattern)
		})
		if err != nil {
			s.logger.Warning("Secondary storage - failed to list backups: %v", err)
			return nil, &StorageError{
				Location:    LocationSecondary,
				Operation:   "list",
				Path:        s.basePath,
				Err:         err,
				IsCritical:  false,
				Recoverable: true,
			}
		}
		for _, match := range patternMatches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			matches = append(matches, match)
		}
	}

	backups = nil

	// A stat that fails after the glob already matched the path is one of three very
	// different things, and the bare continue that used to be here treated them alike:
	// the entry left the slice, List returned a nil error, and nothing was written at
	// any level.
	//
	// Abandonment - the operator aborted, or the bound expired - is a property of the
	// run and not of any archive. It is reported once, by returning, because saying it
	// again for every match still to be stat'ed adds nothing and List runs at least
	// three times per backend per run.
	//
	// Gone since the glob ran is benign for a single archive: a backup deleted by hand
	// or by another run between two syscalls milliseconds apart. Debug, named. When it
	// is every archive AND the location itself has stopped answering, it is the dropped
	// mount, and that is reported after the loop.
	//
	// Anything else means the archive IS there and this run cannot see it. The list
	// then comes back SHORT with a nil error and every consumer believes it: retention
	// judges a subset, GetStats reports fewer archives than exist. The entry is still
	// skipped, because a size and a timestamp cannot be invented, but the count is
	// reported once after the loop.
	vanished := 0
	// unreadable holds one already-rendered entry per archive the listing lost, each
	// carrying its own cause, so no archive ever borrows another's.
	var unreadable []string

	// Filter and parse backup files
	for _, match := range matches {
		// Skip associated sidecars (checksum/metadata/manifest); shared predicate.
		if isBackupSidecar(match) {
			continue
		}
		// Skip in-flight temp copies (.tmp-...) and partial archives (<name>.partial).
		if isBackupTempArtifact(match) {
			continue
		}

		// When bundling is enabled, skip standalone files that have a corresponding bundle
		if s.config != nil && s.config.BundleAssociatedFiles {
			if !strings.HasSuffix(match, ".bundle.tar") {
				// This is a standalone file, check if bundle exists
				bundlePath := match + ".bundle.tar"
				if _, err := safefs.Stat(ctx, bundlePath, timeout); err == nil {
					// Bundle exists, skip the standalone file
					s.logger.Debug("Skipping standalone file %s (bundle exists at %s)",
						filepath.Base(match), filepath.Base(bundlePath))
					continue
				}
			}
		}

		// Get file info (bounded against a dead/stale mount).
		stat, err := safefs.Stat(ctx, match, timeout)
		if err != nil {
			if safefs.IsAbandoned(err) {
				return nil, err
			}
			if os.IsNotExist(err) {
				vanished++
				s.logger.Debug("Secondary storage: %s vanished between the listing and its stat", filepath.Base(match))
				continue
			}
			unreadable = append(unreadable, fmt.Sprintf("%s: %s", filepath.Base(match), listingFailureCause(err)))
			continue
		}

		backups = append(backups, &types.BackupMetadata{
			BackupFile: match,
			Timestamp:  stat.ModTime(),
			Size:       stat.Size(),
			// Hostname is deliberately NOT resolved here: attributing a backup costs a
			// stat plus a manifest open and parse, and a bundle additionally a tar
			// scan. List also backs countBackups - which runs after every Store - and
			// GetStats, neither of which needs an owner, and the secondary location is
			// typically the slowest one (a NAS mount). ApplyRetention fills it in for
			// the entries it is about to judge, the same way the cloud backend does.
			Verified: backupHasCompletionSidecar(ctx, match, timeout),
		})
	}

	// Nothing readable came back out of a directory the glob had entries for. Probe
	// the location itself: a mount that dropped between the glob and the stats answers
	// for none of its paths, and that is the one report worth making, in place of the
	// per-archive noise. An empty directory never reaches here, because the glob found
	// nothing to skip.
	if skipped := vanished + len(unreadable); skipped > 0 {
		located := true
		if len(backups) == 0 {
			if _, statErr := safefs.Stat(ctx, s.basePath, timeout); statErr != nil {
				located = false
				s.logger.Warning("Secondary storage - location stopped answering, %d archive(s) not listed: %v",
					skipped, statErr)
			}
		}
		// Named, not counted: the operator has to know WHICH archives the retention
		// pass and the stats are about to judge without. Header and items at INFO with
		// one WARNING carrying the verdict, the shape cron_indirect_refs.go:2139-2143
		// established, so one fault scores one warning instead of N. The WARNING
		// repeats the count and stands on its own, because DEBUG_LEVEL can be set to
		// "warning" (internal/cli/args.go), which hides the block above it.
		if located && len(unreadable) > 0 {
			s.logger.Info("Secondary storage - %d archive(s) could not be read:", len(unreadable))
			for _, entry := range unreadable {
				s.logger.Info("  - %s", entry)
			}
			s.logger.Warning("Secondary storage - listing is incomplete, %d archive(s) could not be read: retention and the stats run on the rest.",
				len(unreadable))
		}
	}

	// Sort by timestamp (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return backups, nil
}

// listingFailureCause reduces a stat error to the part two archives under the same
// fault have in common. A *fs.PathError prints the path it failed on, which is
// precisely what would make one broken mount look like N different causes.
func listingFailureCause(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr.Err != nil {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// Delete removes a backup file and its associated files
func (s *SecondaryStorage) Delete(ctx context.Context, backupFile string) (err error) {
	done := logging.DebugStart(s.logger, "secondary delete", "file=%s", backupFile)
	defer func() { done(err) }()
	_, err = s.deleteBackupInternal(ctx, backupFile)
	return err
}

func (s *SecondaryStorage) deleteBackupInternal(ctx context.Context, backupFile string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	s.logger.Debug("Deleting secondary backup: %s", backupFile)

	basePath, _ := trimBundleSuffix(backupFile)
	// Always include the bundle path so an orphan .bundle.tar (created while
	// bundling was enabled, then disabled) is cleaned up. Remove of an absent
	// bundle is a no-op, so this is behavior-preserving when no bundle exists.
	filesToDelete := buildBackupCandidatePaths(basePath, true)

	// Delete all files; collect real removal failures (not "already gone") and
	// track whether the data archive itself (not just a sidecar) failed, so the
	// caller never counts a backup whose archive remains on disk as deleted
	// (PS-BH-001), while a sidecar-only failure still counts (the archive IS gone).
	timeout := fsIoTimeout(s.config)
	var failedFiles []string
	dataFailed := false
	for _, f := range filesToDelete {
		if f == "" {
			continue
		}
		s.logger.Debug("Removing file: %s", f)
		if err := safefs.Remove(ctx, f, timeout); err != nil {
			if os.IsNotExist(err) {
				s.logger.Debug("Secondary storage: file already removed %s", f)
				continue
			}
			s.logger.Warning("Secondary storage - %v", err)
			failedFiles = append(failedFiles, f)
			if !isBackupSidecar(f) {
				dataFailed = true
			}
		}
	}

	// Best-effort: delete associated secondary log file for this backup
	logDeleted := s.deleteAssociatedLog(ctx, backupFile)

	if len(failedFiles) > 0 {
		if !dataFailed {
			return logDeleted, fmt.Errorf("%w: %v", errBackupSidecarDeleteOnly, failedFiles)
		}
		return logDeleted, fmt.Errorf("failed to remove %d file(s): %v", len(failedFiles), failedFiles)
	}

	s.logger.Debug("Deleted secondary backup: %s", filepath.Base(backupFile))
	return logDeleted, nil
}

// deleteAssociatedLog attempts to remove the secondary log file corresponding to a backup.
// It is best-effort and never returns an error to the caller.
func (s *SecondaryStorage) deleteAssociatedLog(ctx context.Context, backupFile string) bool {
	if s == nil || s.config == nil {
		return false
	}

	logPath := strings.TrimSpace(s.config.SecondaryLogPath)
	if logPath == "" {
		return false
	}

	host, ts, ok := extractLogKeyFromBackup(backupFile)
	if !ok {
		return false
	}

	logName := fmt.Sprintf("backup-%s-%s.log", host, ts)
	fullPath := filepath.Join(logPath, logName)

	// Bounded against a dead/stale mount: on a cancelled/expired ctx safefs
	// returns immediately without running the remove (best-effort cleanup).
	if err := safefs.Remove(ctx, fullPath, fsIoTimeout(s.config)); err != nil {
		if !os.IsNotExist(err) {
			s.logger.Debug("Secondary logs: failed to delete %s: %v", logName, err)
		}
		return false
	}

	s.logger.Debug("Secondary logs: deleted log file %s", logName)
	return true
}

func (s *SecondaryStorage) countLogFiles(ctx context.Context) int {
	if s == nil || s.config == nil {
		return -1
	}
	logPath := strings.TrimSpace(s.config.SecondaryLogPath)
	if logPath == "" {
		return 0
	}
	pattern := filepath.Join(logPath, "backup-*.log")
	// Bounded against a dead/stale mount, consistent with the main backup glob.
	matches, err := safefs.Run(ctx, "secondary-log-glob", s.basePath, fsIoTimeout(s.config), func() ([]string, error) {
		return filepath.Glob(pattern)
	})
	if err != nil {
		s.logger.Debug("Secondary logs: failed to count log files: %v", err)
		return -1
	}
	return len(matches)
}

// ApplyRetention removes old backups according to retention policy
// Supports both simple (count-based) and GFS (time-distributed) policies
func (s *SecondaryStorage) ApplyRetention(ctx context.Context, config RetentionConfig) (deleted int, err error) {
	done := logging.DebugStart(s.logger, "secondary retention", "policy=%s max=%d", config.Policy, config.MaxBackups)
	defer func() { done(err) }()

	// See LocalStorage.ApplyRetention for why this is declared before the first
	// return and why an unnamed host leaves it invalid.

	// lastRet is only assigned on the delete paths, so a pass that deletes nothing
	// used to leave the previous pass's BackupsDeleted standing beside a freshly
	// published scope: one struct, two different ages. Reset it here so every field
	// LastRetentionSummary returns describes THIS pass.
	// The same closure publishes whether the pass finished, taken from the named
	// err: see LocalStorage.ApplyRetention for why a reset alone cannot tell a
	// bailed pass from a healthy one with nothing to delete.
	// Reset together: the flag describes this struct, so leaving it set here made
	// the value report a COMPLETED pass beside counts this pass had just zeroed,
	// which is the one-struct-two-ages state the reset exists to prevent.
	s.lastRet, s.lastRetCompleted = RetentionSummary{}, false
	owned, scoped := 0, false
	defer func() {
		s.scopeOwned, s.scopeValid, s.lastRetCompleted = owned-deleted, scoped, err == nil
	}()

	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// List all backups
	s.logger.Debug("Secondary storage: listing backups for retention policy '%s'", config.Policy)
	backups, err := s.List(ctx)
	if err != nil {
		s.logger.Warning("Secondary storage - retention could not list the backups: %v", err)
		return 0, &StorageError{
			Location:    LocationSecondary,
			Operation:   "apply_retention",
			Path:        s.basePath,
			Err:         err,
			IsCritical:  false,
			Recoverable: true,
		}
	}

	// Attribute each candidate to its owning host, then drop anything this host does
	// not own before counting or deleting. This matters most here: a shared NAS mount
	// is the documented secondary layout, so several hosts routinely write into the
	// same directory, and the "*-backup-*" glob that produced this list matches every
	// hostname.
	s.resolveRetentionOwners(ctx, backups)
	backups, unmanaged := applyRetentionHostScope("Secondary storage", retentionIdentity{hostname: s.hostname, aliases: s.hostAliases, serverID: s.serverID}, backups, s.logger)

	// The shared NAS mount is the documented secondary layout, so this is the
	// location where the unscoped count was most often somebody else's
	// (discussion #292). See LocalStorage.ApplyRetention for why the archives no
	// host manages are added back rather than dropped.
	owned, scoped = len(backups)+unmanaged, strings.TrimSpace(s.hostname) != ""

	if len(backups) == 0 {
		s.logger.Debug("Secondary storage: no backups to apply retention")
		return 0, nil
	}

	// Apply appropriate retention policy
	if config.Policy == "gfs" {
		return s.applyGFSRetention(ctx, backups, config)
	}
	return s.applySimpleRetention(ctx, backups, config.MaxBackups)
}

// resolveRetentionOwners fills in each candidate's Hostname and ServerID from its
// manifest. It is the secondary twin of CloudStorage.resolveRetentionOwners and
// exists for the same reason: only retention needs to know who owns a backup, so
// only retention pays for finding out.
//
// It no longer skips an entry that already carries a Hostname. That guard was
// defensive while there was one field to resolve, since List() never fills it, but
// with two it becomes a half-resolution: a pre-filled name would leave the identity
// empty and silently disable the whole mechanism on this backend, with no other
// symptom. Both values are taken from ONE manifest read and each is written only
// where nothing was resolved yet, so the two facts can never come from two files.
func (s *SecondaryStorage) resolveRetentionOwners(ctx context.Context, backups []*types.BackupMetadata) {
	timeout := fsIoTimeout(s.config)
	for _, b := range backups {
		if b == nil {
			continue
		}
		hostname, serverID := manifestOwnerFromLocalArchive(ctx, b.BackupFile, timeout)
		if strings.TrimSpace(b.Hostname) == "" {
			b.Hostname = hostname
		}
		if strings.TrimSpace(b.ServerID) == "" {
			b.ServerID = serverID
		}
	}
}

// applyGFSRetention applies GFS (Grandfather-Father-Son) retention policy
func (s *SecondaryStorage) applyGFSRetention(ctx context.Context, backups []*types.BackupMetadata, config RetentionConfig) (int, error) {
	eligible, inert := partitionRetentionEligible(backups)
	for _, in := range inert {
		s.logger.Warning("Secondary storage: backup %s ignored by retention (%s)", in.Backup.BackupFile, in.Reason)
	}
	backups = eligible

	config = EffectiveGFSRetentionConfig(config)
	s.logger.Debug("Applying GFS retention policy (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
		config.Daily, config.Weekly, config.Monthly, config.Yearly)

	initialLogs := s.countLogFiles(ctx)
	logsDeleted := 0

	// Classify backups according to GFS scheme
	classification := ClassifyBackupsGFS(backups, config)

	// Get statistics
	stats := GetRetentionStats(classification)
	s.logger.Debug("GFS classification -> daily: %d/%d, weekly: %d/%d, monthly: %d/%d, yearly: %d/%d, to_delete: %d",
		stats[CategoryDaily], config.Daily,
		stats[CategoryWeekly], config.Weekly,
		stats[CategoryMonthly], config.Monthly,
		stats[CategoryYearly], config.Yearly,
		stats[CategoryDelete])

	// Delete backups marked for deletion
	deleted := 0
	for backup, category := range classification {
		if category != CategoryDelete {
			continue
		}

		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		s.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := s.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			if !errors.Is(err, errBackupSidecarDeleteOnly) {
				s.logger.Warning("Secondary storage - retention left %s in place: %v", filepath.Base(backup.BackupFile), err)
				continue
			}
			// Archive removed, only sidecar(s) failed: count as deleted but warn. The
			// cause says which of the two happened, so the line only names the backup.
			s.logger.Warning("Secondary storage - retention left files behind from %s: %v", filepath.Base(backup.BackupFile), err)
		}

		deleted++
		if logDeleted {
			logsDeleted++
		}
	}

	remaining := len(backups) - deleted
	if remaining < 0 {
		remaining = 0
	}

	if logsRemaining, ok := computeRemaining(initialLogs, logsDeleted); ok {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// applySimpleRetention applies simple count-based retention policy
func (s *SecondaryStorage) applySimpleRetention(ctx context.Context, backups []*types.BackupMetadata, maxBackups int) (int, error) {
	if maxBackups <= 0 {
		s.logger.Debug("Retention disabled for secondary storage (maxBackups = %d)", maxBackups)
		return 0, nil
	}

	eligible, inert := partitionRetentionEligible(backups)
	for _, in := range inert {
		s.logger.Warning("Secondary storage: backup %s ignored by retention (%s)", in.Backup.BackupFile, in.Reason)
	}
	backups = eligible

	totalBackups := len(backups)
	if totalBackups <= maxBackups {
		s.logger.Debug("Secondary storage: %d backups (within retention limit of %d)", totalBackups, maxBackups)
		return 0, nil
	}

	// Calculate how many to delete
	toDelete := totalBackups - maxBackups
	s.logger.Info("Applying simple retention policy: %d backups found, limit is %d, deleting %d oldest",
		totalBackups, maxBackups, toDelete)
	s.logger.Info("Simple retention -> current: %d, limit: %d, to_delete: %d",
		totalBackups, maxBackups, toDelete)

	// Delete oldest backups (already sorted newest first)
	initialLogs := s.countLogFiles(ctx)
	logsDeleted := 0
	deleted := 0
	for i := totalBackups - 1; i >= maxBackups; i-- {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}

		backup := backups[i]
		s.logger.Debug("Deleting old backup: %s (created: %s)",
			filepath.Base(backup.BackupFile),
			backup.Timestamp.Format("2006-01-02 15:04:05"))

		logDeleted, err := s.deleteBackupInternal(ctx, backup.BackupFile)
		if err != nil {
			if !errors.Is(err, errBackupSidecarDeleteOnly) {
				s.logger.Warning("Secondary storage - retention left %s in place: %v", filepath.Base(backup.BackupFile), err)
				continue
			}
			// Archive removed, only sidecar(s) failed: count as deleted but warn. The
			// cause says which of the two happened, so the line only names the backup.
			s.logger.Warning("Secondary storage - retention left files behind from %s: %v", filepath.Base(backup.BackupFile), err)
		}

		deleted++
		if logDeleted {
			logsDeleted++
		}
	}

	remaining := totalBackups - deleted
	if remaining < 0 {
		remaining = 0
	}

	if logsRemaining, ok := computeRemaining(initialLogs, logsDeleted); ok {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining (%d logs remaining)",
			deleted, logsDeleted, remaining, logsRemaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			LogsRemaining:    logsRemaining,
			HasLogInfo:       true,
		}
	} else {
		s.logger.Debug("Secondary storage retention applied: deleted %d backups (logs deleted: %d), %d backups remaining",
			deleted, logsDeleted, remaining)
		s.lastRet = RetentionSummary{
			BackupsDeleted:   deleted,
			BackupsRemaining: remaining,
			LogsDeleted:      logsDeleted,
			HasLogInfo:       false,
		}
	}

	return deleted, nil
}

// VerifyUpload is not applicable for secondary storage
func (s *SecondaryStorage) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	return true, nil
}

// LastRetentionSummary returns the latest retention summary.
// See RetentionReporter (storage.go) for what the value is worth and when.
func (s *SecondaryStorage) LastRetentionSummary() RetentionSummary {
	summary := s.lastRet
	summary.ScopeValid, summary.Owned, summary.PassCompleted = s.scopeValid, s.scopeOwned, s.lastRetCompleted
	return summary
}

// GetStats returns storage statistics
func (s *SecondaryStorage) GetStats(ctx context.Context) (stats *StorageStats, err error) {
	done := logging.DebugStart(s.logger, "secondary stats", "path=%s", s.basePath)
	defer func() { done(err) }()
	backups, err := s.List(ctx)
	if err != nil {
		// List has already named the fault on every path that reaches it through the
		// glob; what this adds is that the location's figures are gone with it. On the
		// abandoned path (ctx checked at :382, before the glob) this is the only line.
		s.logger.Warning("Secondary storage - statistics unavailable: %v", err)
		return nil, err
	}

	stats = &StorageStats{
		TotalBackups: len(backups),
	}

	if s.fsInfo != nil {
		stats.FilesystemType = s.fsInfo.Type
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

	// Get available/total space using statfs (bounded against a dead/stale mount).
	if stat, err := safefs.Statfs(ctx, s.basePath, fsIoTimeout(s.config)); err == nil {
		total, available, used := safefs.SpaceUsageFromStatfs(stat)
		stats.AvailableSpace = available
		stats.TotalSpace = total
		stats.UsedSpace = used
	}

	return stats, nil
}
