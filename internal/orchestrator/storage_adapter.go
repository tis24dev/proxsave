package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// StorageAdapter adapts a storage.Storage backend to the StorageTarget interface
type StorageAdapter struct {
	backend      storage.Storage
	logger       *logging.Logger
	config       *config.Config // Main configuration for retention policy
	fsInfo       *storage.FilesystemInfo
	initialStats *storage.StorageStats
	// initialOwned is how many archives this host owned at the location when the
	// run started, with the meaning RetentionSummary.Owned has (discussion #292).
	// It is only worth anything while initialOwnedKnown is set.
	initialOwned      int
	initialOwnedKnown bool
	// statsApplied records that this location's figures reached the run's
	// BackupStats. applyInitialStats reads it: a location Sync already described
	// keeps what Sync wrote.
	statsApplied bool
}

// NewStorageAdapter creates a new storage adapter
func NewStorageAdapter(backend storage.Storage, logger *logging.Logger, cfg *config.Config) *StorageAdapter {
	return &StorageAdapter{
		backend: backend,
		logger:  logger,
		config:  cfg,
	}
}

// SetFilesystemInfo preloads filesystem info detected earlier.
func (s *StorageAdapter) SetFilesystemInfo(info *storage.FilesystemInfo) {
	if info != nil {
		s.fsInfo = info
	}
}

// SetInitialStats caches storage stats gathered during initialization.
func (s *StorageAdapter) SetInitialStats(stats *storage.StorageStats) {
	s.initialStats = stats
}

// SetInitialOwnedBackups caches the number of archives this host owned at the
// location when the run started (storage.CountOwnedBackups over the startup
// listing). known=false keeps the unscoped total, the same fallback Sync uses when
// retention did not produce a scoped count.
func (s *StorageAdapter) SetInitialOwnedBackups(owned int, known bool) {
	s.initialOwned, s.initialOwnedKnown = owned, known
}

// startupStatsFiller is implemented by storage targets that can describe their
// location as it stood when the run started.
type startupStatsFiller interface {
	applyInitialStats(stats *BackupStats)
}

// applyInitialStats writes the figures read at startup for a location this run
// never described: a failed run reaches the notifications without Sync having run
// for it, or with Sync stopped before its statistics step (a critical local store
// failure returns there), and the zero values left behind used to read as an empty
// location ("0/7 backups", "0 B free") while it held real archives.
//
// The fields and their meaning are the ones Sync writes on a successful run:
// the count is the scoped one under the same condition Sync uses it (a retention
// limit is configured and the host could name itself), otherwise the unscoped
// total; the free/used/total space comes from the same GetStats; the retention
// policy and GFS tiers are rebuilt from the same configuration, through
// EffectiveGFSRetentionConfig, the silent half of NormalizeGFSRetentionConfig.
// What differs is the moment: these figures are from before the run, and on a
// location the run never reached that is also what it holds now.
//
// Callers reach it only on the failure path (finalizeFailedBackupStats). A
// successful run keeps exactly what Sync wrote, including a location whose
// statistics read failed.
func (s *StorageAdapter) applyInitialStats(stats *BackupStats) {
	if s == nil || stats == nil || s.statsApplied || s.initialStats == nil || s.backend == nil || s.config == nil {
		return
	}
	if !s.backend.IsEnabled() {
		return
	}
	retentionConfig := storage.NewRetentionConfigFromConfig(s.config, s.backend.Location())
	if retentionConfig.Policy == "gfs" {
		retentionConfig = storage.EffectiveGFSRetentionConfig(retentionConfig)
	}
	scopedBackups := -1
	if s.initialOwnedKnown && (retentionConfig.MaxBackups > 0 || retentionConfig.Policy == "gfs") {
		scopedBackups = s.initialOwned
	}
	if s.logger != nil {
		s.logger.Debug("%s: run failed before this location's statistics were read; reporting the startup figures (backups=%d, owned=%d, scoped=%v)",
			s.backend.Name(), s.initialStats.TotalBackups, s.initialOwned, scopedBackups >= 0)
	}
	s.applyStorageStats(s.initialStats, retentionConfig, scopedBackups, stats)
}

// Sync implements the StorageTarget interface
// It performs filesystem detection, stores the backup, and applies retention
func (s *StorageAdapter) Sync(ctx context.Context, stats *BackupStats) error {
	// Check if backend is enabled
	if !s.backend.IsEnabled() {
		s.logger.Debug("%s is disabled, skipping", s.backend.Name())
		s.setStorageStatus(stats, "disabled")
		return nil
	}

	s.logger.Debug("Starting %s operations...", s.backend.Name())

	// Assume success unless operations report otherwise
	s.setStorageStatus(stats, "ok")

	// Step 1: Detect filesystem and log in real-time
	var err error
	fsInfo := s.fsInfo
	hasWarnings := false
	hasErrors := false
	// scopedBackups is how many archives this host owns at this location after the
	// retention pass, or -1 when retention did not run or could not finish. It is
	// read back from the backend rather than recomputed here: the host identity set
	// (the kernel name plus the aliases the writer stamped) lives on the backend and
	// is deliberately unreachable from this package, so a copy here would be a second
	// answer to "whose archive is this" (discussion #292).
	scopedBackups := -1
	// The block opens with the destination name, before anything is done there.
	name := s.backend.Name()
	s.logger.Step("%s", name)
	if fsInfo == nil {
		fsInfo, err = s.backend.DetectFilesystem(ctx)
		if err != nil {
			if s.backend.IsCritical() {
				s.setStorageStatus(stats, "error")
				return fmt.Errorf("%s filesystem detection failed (CRITICAL): %w", name, err)
			}
			// Nothing else runs at this destination: the backup did not reach it.
			s.logger.Debug("%s; %s operations will be skipped", storageFailureText(err, name, "filesystem detection failed"), name)
			s.logger.Info("  Filesystem detection failed: %s", storage.ErrorCause(err))
			s.logger.Warning("%s %s: backup not saved", theme.SymbolError, name)
			s.setStorageStatus(stats, "error")
			return nil
		}
		s.fsInfo = fsInfo
	}

	// Step 2: Prepare backup metadata
	metadata := &types.BackupMetadata{
		BackupFile:  stats.ArchivePath,
		Timestamp:   stats.StartTime,
		Size:        stats.ArchiveSize,
		Checksum:    stats.Checksum,
		ProxmoxType: stats.ProxmoxType,
		Compression: stats.Compression,
		Version:     stats.Version,
	}

	// Step 3: Store backup. The block opens with the destination name; the action
	// line says what this step does there: the Primary already holds the archive
	// (written at [3], bundled at [5]), so its Store only checks the file and sets
	// owner and mode, while the other destinations receive a copy.
	primary := s.backend.Location() == storage.LocationPrimary
	setsPermissions := s.setsBackupPermissions()
	if primary {
		if setsPermissions {
			s.logger.Info("Setting backup permissions...")
		} else {
			fsType := storage.FilesystemUnknown
			if fsInfo != nil {
				fsType = fsInfo.Type
			}
			s.logger.Debug("%s: filesystem %s does not support ownership, chown/chmod skipped", name, fsType)
			s.logger.Skip("Permissions: %s does not support ownership", fsType)
		}
	} else {
		s.logger.Info("Storing backup...")
	}
	if err := s.backend.Store(ctx, stats.ArchivePath, metadata); err != nil {
		// The backend printed the fact line with the short cause; the full chain
		// stays here, in DEBUG.
		s.logger.Debug("%s", storageFailureText(err, name, "store operation failed"))
		if s.backend.IsCritical() {
			s.setStorageStatus(stats, "error")
			var se *storage.StorageError
			if primary && errors.As(err, &se) && se != nil && se.Operation == "store" {
				s.logger.Error("%s %s: backup not accessible", theme.SymbolError, name)
				return fmt.Errorf("%s store operation failed (CRITICAL): %w", name, &outcomeLoggedError{err: err})
			}
			return fmt.Errorf("%s store operation failed (CRITICAL): %w", name, err)
		}

		// When the PRIMARY archive was saved and only a sidecar failed, do NOT claim the whole
		// backup was not saved (that reads as data loss when the archive is safe, F08-08).
		// se != nil guards the typed-nil shape: errors.As matches a nil
		// *StorageError in the chain and leaves the target nil (PR #303 review).
		// The status follows the backup: saved with a sidecar file missing is a
		// warning, not saved is an error.
		var se *storage.StorageError
		if errors.As(err, &se) && se != nil && se.PrimarySaved {
			s.logStoreIssues(name, append(s.lastStoreIssues(), storage.StoreIssueSidecarNotSaved))
			hasWarnings = true
		} else {
			s.logger.Warning("%s %s: backup not saved", theme.SymbolError, name)
			hasErrors = true
		}
		// Don't return error - continue with retention
	} else {
		s.logStoreOutcome(name, primary, setsPermissions)
		if slices.Contains(s.lastStoreIssues(), storage.StoreIssueSidecarNotSaved) {
			// The backup is saved, one of its sidecar files is not.
			hasWarnings = true
		}
	}

	// Step 4: Apply retention policy
	retentionConfig := storage.NewRetentionConfigFromConfig(s.config, s.backend.Location())
	if retentionConfig.Policy == "gfs" {
		// Enforce GFS-specific rules (e.g. minimum DAILY=1) once per backend.
		retentionConfig = storage.NormalizeGFSRetentionConfig(s.logger, name, retentionConfig)
	}
	if retentionConfig.MaxBackups > 0 || retentionConfig.Policy == "gfs" {
		if retentionConfig.Policy == "gfs" {
			s.logger.Info("Applying GFS retention policy...")
		} else {
			s.logger.Info("Applying retention policy...")
		}
		s.logRetentionPolicyDetails(retentionConfig)

		s.logCurrentBackupCount()
		deleted, err := s.backend.ApplyRetention(ctx, retentionConfig)
		if err != nil {
			// Never critical, on the Primary either: a backup retention could not
			// delete is one backup too many, never a backup lost, so the run goes on.
			// The backend printed the fact line when the listing failed; the full
			// chain stays in DEBUG.
			s.logger.Debug("%s", storageFailureText(err, name, "retention failed"))
			s.logger.Warning("%s Retention not applied", theme.SymbolWarning)
			hasWarnings = true
		} else {
			// Read on every successful pass, not only when something was deleted. The
			// steady state, nothing to delete, is the common healthy run, and it was
			// exactly the one where the owned count used to be thrown away and the
			// summary fell back to counting every host's archives (discussion #292).
			summary := storage.RetentionSummary{BackupsDeleted: deleted}
			if reporter, ok := s.backend.(storage.RetentionReporter); ok {
				summary = reporter.LastRetentionSummary()
				if summary.ScopeValid {
					scopedBackups = summary.Owned
				}
			}
			s.logRetentionOutcome(summary, deleted)
		}
	}

	// Step 5: Get and log statistics. The filesystem is not repeated here: the
	// storage initialization already showed it.
	storageStats, err := s.backend.GetStats(ctx)
	if err != nil {
		// A WARNING without a status change: the backup and the retention outcome
		// above stand; only the figures are missing.
		s.logger.Debug("%s: Failed to get statistics: %v", name, err)
		s.logger.Info("%s statistics:", name)
		s.logger.Info("  Read failed: %s", storage.ErrorCause(err))
		s.logger.Warning("%s Statistics unavailable", theme.SymbolWarning)
	} else {
		s.logger.Info("%s statistics:", name)
		s.logger.Info("  Total backups: %d", storageStats.TotalBackups)
		if storageStats.TotalSize > 0 {
			s.logger.Info("  Total size: %s", formatBytes(storageStats.TotalSize))
		}
		if fsInfo != nil {
			s.logger.Debug("%s: filesystem %s", name, fsInfo.Type)
		}

		if stats != nil {
			s.applyStorageStats(storageStats, retentionConfig, scopedBackups, stats)
		}
	}

	// No closing line: each outcome is printed where it happened (the store under
	// its action line, retention under "Applying retention policy..."), so a
	// summary at the bottom would only repeat the worst of them.
	s.logger.Debug("%s operations completed (errors=%v warnings=%v)", name, hasErrors, hasWarnings)
	s.finalizeStorageStatus(stats, hasErrors, hasWarnings)
	return nil
}

// receivesCopy implements storageCopyTarget: every enabled destination but the Primary
// receives a copy of the archive at step [6].
func (s *StorageAdapter) receivesCopy() bool {
	return s != nil && s.backend != nil && s.backend.IsEnabled() && s.backend.Location() != storage.LocationPrimary
}

// backupPermissionSetter is implemented by a backend whose Store may skip setting owner
// and mode (LocalStorage on a filesystem without ownership support).
type backupPermissionSetter interface {
	SetsBackupPermissions() bool
}

func (s *StorageAdapter) setsBackupPermissions() bool {
	if p, ok := s.backend.(backupPermissionSetter); ok {
		return p.SetsBackupPermissions()
	}
	return true
}

// lastStoreIssues is what the backend reports its last Store left undone around the
// backup, nil for a backend that cannot say.
func (s *StorageAdapter) lastStoreIssues() []storage.StoreIssue {
	if reporter, ok := s.backend.(storage.StoreReporter); ok {
		return reporter.LastStoreIssues()
	}
	return nil
}

// logStoreOutcome closes a Store that returned no error: the "✓" line, or ONE "⚠" line
// listing everything the backend reported left undone around the backup, in the order
// it happened. These are WARNING lines; the backup is at the destination, so only a
// sidecar file not saved there changes its status (warning, set by Sync).
func (s *StorageAdapter) logStoreOutcome(name string, primary, setsPermissions bool) {
	issues := s.lastStoreIssues()
	s.logger.Debug("%s: store issues=%v", name, issues)
	if primary {
		for _, issue := range issues {
			if issue == storage.StoreIssuePermissionsNotSet {
				s.logger.Warning("%s %s: permissions not set", theme.SymbolWarning, name)
				return
			}
		}
		if setsPermissions {
			s.logger.Info("%s %s: permissions set", theme.SymbolSuccess, name)
		}
		return
	}
	if len(issues) == 0 {
		s.logger.Info("%s %s: backup saved", theme.SymbolSuccess, name)
		return
	}
	s.logStoreIssues(name, issues)
}

// logStoreIssues prints the single "⚠ <Name>: backup saved, <issue>, <issue>" outcome.
func (s *StorageAdapter) logStoreIssues(name string, issues []storage.StoreIssue) {
	parts := []string{"backup saved"}
	seen := make(map[storage.StoreIssue]bool, len(issues))
	for _, issue := range issues {
		if seen[issue] {
			continue
		}
		seen[issue] = true
		switch issue {
		case storage.StoreIssueSidecarNotSaved:
			parts = append(parts, "sidecar file not saved")
		case storage.StoreIssueBundleNotSent:
			parts = append(parts, "bundle not sent")
		case storage.StoreIssueChecksumNotVerified:
			parts = append(parts, "checksum not verified")
		case storage.StoreIssuePermissionsNotSet:
			parts = append(parts, "permissions not set")
		}
	}
	s.logger.Warning("%s %s: %s", theme.SymbolWarning, name, strings.Join(parts, ", "))
}

// logRetentionOutcome prints the two autonomous outcomes of a retention pass that ran:
// what happened to the backups and what happened to their logs. Each has its own
// symbol, because one can succeed while the other does not.
func (s *StorageAdapter) logRetentionOutcome(summary storage.RetentionSummary, deleted int) {
	backupsDeleted := summary.BackupsDeleted
	if backupsDeleted == 0 {
		backupsDeleted = deleted
	}
	planned := summary.Planned
	if minimum := backupsDeleted + summary.NotDeleted; planned < minimum {
		planned = minimum
	}
	logsDeleted := summary.LogsDeleted
	logsPlanned := logsDeleted + summary.LogsNotDeleted
	s.logger.Debug("%s: retention outcome planned=%d deleted=%d not_deleted=%d left_behind=%d not_listed=%d skipped=%d no_metadata=%d logs_deleted=%d logs_not_deleted=%d",
		s.backend.Name(), planned, backupsDeleted, summary.NotDeleted, summary.LeftBehind, summary.NotListed,
		summary.Skipped, summary.NoMetadata, logsDeleted, summary.LogsNotDeleted)
	if summary.NotRotated > 0 {
		s.logger.Debug("%s: retention outcome not_rotated=%d names=%s", s.backend.Name(), summary.NotRotated, summary.NotRotatedNames)
	}
	if summary.SharedIdentityNames != "" {
		s.logger.Debug("%s: retention outcome shared_identity=%s", s.backend.Name(), summary.SharedIdentityNames)
	}

	if planned == 0 && summary.Skipped == 0 && summary.NotListed == 0 && summary.NoMetadata == 0 && summary.NotRotated == 0 && summary.SharedIdentityNames == "" && logsPlanned == 0 {
		s.logger.Info("%s Nothing to delete", theme.SymbolSuccess)
		return
	}

	switch {
	case summary.NotDeleted > 0:
		s.logger.Warning("%s Backups deleted: %d of %d", theme.SymbolWarning, backupsDeleted, planned)
	case summary.LeftBehind > 0:
		s.logger.Warning("%s Backups deleted: %d, files left behind", theme.SymbolWarning, backupsDeleted)
	case summary.NotListed > 0:
		s.logger.Warning("%s Backups deleted: %d, %d not listed", theme.SymbolWarning, backupsDeleted, summary.NotListed)
	case summary.Skipped > 0:
		s.logger.Warning("%s Backups deleted: %d, %d skipped", theme.SymbolWarning, backupsDeleted, summary.Skipped)
	case summary.NoMetadata > 0:
		s.logger.Warning("%s Backups deleted: %d, %d without metadata", theme.SymbolWarning, backupsDeleted, summary.NoMetadata)
	case summary.NotRotated > 0:
		s.logger.Warning("%s Backups deleted: %d, %d named %s not rotated", theme.SymbolWarning, backupsDeleted, summary.NotRotated, summary.NotRotatedNames)
	case summary.SharedIdentityNames != "":
		// Last: the archives were rotated, so every other outcome says more about
		// this pass. The "  Still writing here:" fact above is printed regardless.
		s.logger.Warning("%s Backups deleted: %d, server identity shared with %s", theme.SymbolWarning, backupsDeleted, summary.SharedIdentityNames)
	default:
		s.logger.Info("%s Backups deleted: %d", theme.SymbolSuccess, backupsDeleted)
	}

	switch {
	case summary.LogsNotDeleted > 0:
		s.logger.Warning("%s Logs deleted: %d of %d", theme.SymbolWarning, logsDeleted, logsPlanned)
	case logsDeleted > 0:
		s.logger.Info("%s Logs deleted: %d", theme.SymbolSuccess, logsDeleted)
	}
}

// outcomeLoggedError marks a storage failure whose outcome line ("✗ Local Storage:
// backup not accessible") is already on screen, so the caller that ends the run does
// not print the same failure a second time. Its text is the wrapped error's.
type outcomeLoggedError struct {
	err error
}

func (e *outcomeLoggedError) Error() string { return e.err.Error() }

func (e *outcomeLoggedError) Unwrap() error { return e.err }

// OutcomeLogged reports whether err carries a storage failure whose outcome line the
// storage step already printed.
func OutcomeLogged(err error) bool {
	var logged *outcomeLoggedError
	return errors.As(err, &logged)
}

func (s *StorageAdapter) logCurrentBackupCount() {
	// Called through the interface, not through a runtime type assertion.
	// storage.Storage declares List and s.backend is already that type, so the
	// assertion that used to sit here could never fail, while its !ok branch would
	// have turned any future rename or signature change on List into a silent
	// "no backups" with a clean build and no log line.

	// The cloud backend already logs the current count during ApplyRetention
	// by reusing the same list; avoid a second rclone lsl call.
	if s.backend.Location() == storage.LocationCloud {
		return
	}

	backups, err := s.backend.List(context.Background())
	if err != nil {
		s.logger.Debug("%s: Unable to count backups prior to retention: %v", s.backend.Name(), err)
		return
	}
	s.logger.Debug("%s: Current backups detected: %d", s.backend.Name(), len(backups))
}

func (s *StorageAdapter) logRetentionPolicyDetails(cfg storage.RetentionConfig) {
	if s.logger == nil {
		return
	}
	if cfg.Policy == "gfs" {
		s.logger.Debug("  Policy: GFS (daily=%d, weekly=%d, monthly=%d, yearly=%d)",
			cfg.Daily, cfg.Weekly, cfg.Monthly, cfg.Yearly)
		return
	}
	if cfg.MaxBackups > 0 {
		s.logger.Debug("  Policy: simple (keep %d newest)", cfg.MaxBackups)
	} else {
		s.logger.Debug("  Policy: simple (disabled)")
	}
}

// formatBytes formats bytes in human-readable format
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

func (s *StorageAdapter) applyStorageStats(storageStats *storage.StorageStats, retentionConfig storage.RetentionConfig, scopedBackups int, stats *BackupStats) {
	if storageStats == nil || stats == nil {
		return
	}
	s.statsApplied = true

	// storageStats.TotalBackups and the count retention arrived at are the same rule,
	// only the archives this host owns (by name or by server identity). Retention's
	// is preferred because it is net of what the pass deleted. -1 means retention did
	// not run or failed, and the statistics count is then the number there is.
	backupCount := storageStats.TotalBackups
	if scopedBackups >= 0 {
		backupCount = scopedBackups
	}

	switch s.backend.Location() {
	case storage.LocationPrimary:
		stats.LocalBackups = backupCount
		stats.LocalFreeSpace = clampInt64ToUint64(storageStats.AvailableSpace)
		stats.LocalUsedSpace = clampInt64ToUint64(storageStats.UsedSpace)
		stats.LocalTotalSpace = clampInt64ToUint64(storageStats.TotalSpace)
		// Populate retention info
		stats.LocalRetentionPolicy = retentionConfig.Policy
		if retentionConfig.Policy == "gfs" {
			stats.LocalGFSDaily = retentionConfig.Daily
			stats.LocalGFSWeekly = retentionConfig.Weekly
			stats.LocalGFSMonthly = retentionConfig.Monthly
			stats.LocalGFSYearly = retentionConfig.Yearly
		}
	case storage.LocationSecondary:
		if !stats.SecondaryEnabled {
			stats.SecondaryEnabled = true
		}
		stats.SecondaryBackups = backupCount
		stats.SecondaryFreeSpace = clampInt64ToUint64(storageStats.AvailableSpace)
		stats.SecondaryUsedSpace = clampInt64ToUint64(storageStats.UsedSpace)
		stats.SecondaryTotalSpace = clampInt64ToUint64(storageStats.TotalSpace)
		// Populate retention info
		stats.SecondaryRetentionPolicy = retentionConfig.Policy
		if retentionConfig.Policy == "gfs" {
			stats.SecondaryGFSDaily = retentionConfig.Daily
			stats.SecondaryGFSWeekly = retentionConfig.Weekly
			stats.SecondaryGFSMonthly = retentionConfig.Monthly
			stats.SecondaryGFSYearly = retentionConfig.Yearly
		}
	case storage.LocationCloud:
		if !stats.CloudEnabled {
			stats.CloudEnabled = true
		}
		stats.CloudBackups = backupCount
		// Populate retention info
		stats.CloudRetentionPolicy = retentionConfig.Policy
		if retentionConfig.Policy == "gfs" {
			stats.CloudGFSDaily = retentionConfig.Daily
			stats.CloudGFSWeekly = retentionConfig.Weekly
			stats.CloudGFSMonthly = retentionConfig.Monthly
			stats.CloudGFSYearly = retentionConfig.Yearly
		}
	}
}

func clampInt64ToUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func (s *StorageAdapter) finalizeStorageStatus(stats *BackupStats, hasErrors, hasWarnings bool) {
	if stats == nil {
		return
	}
	switch {
	case hasErrors:
		s.setStorageStatus(stats, "error")
	case hasWarnings:
		s.setStorageStatus(stats, "warning")
	default:
		s.setStorageStatus(stats, "ok")
	}
}

func (s *StorageAdapter) setStorageStatus(stats *BackupStats, status string) {
	if stats == nil || s == nil || s.backend == nil {
		return
	}
	switch s.backend.Location() {
	case storage.LocationSecondary:
		stats.SecondaryStatus = status
	case storage.LocationCloud:
		stats.CloudStatus = status
	case storage.LocationPrimary:
		stats.LocalStatus = status
	default:
		stats.LocalStatus = status
	}
}

// storageFailureText renders a backend failure for the operator.
//
// A *storage.StorageError already names the location, the operation and the path, so it
// is printed on its own: repeating "Secondary Storage store operation failed" in front of
// "secondary storage store operation failed for /path" said the same sentence twice, in
// two spellings.
//
// Anything else is printed with the backend name and the operation in front, because it
// carries no attribution of its own. That is not a theoretical branch: Store and
// ApplyRetention return a bare ctx.Err() on the non-critical backends
// (internal/storage/secondary.go:137, :689, :795, :880 and internal/storage/cloud.go:733,
// :1928, :2060), and on the retention arm there is no sibling line to say which backend
// it was - collapsing it to the error alone leaves "context canceled" and nothing else.
func storageFailureText(err error, backendName, operation string) string {
	var se *storage.StorageError
	if errors.As(err, &se) {
		// %v, not err.Error(): a typed-nil *StorageError satisfies errors.As while
		// panicking on Error(), and these two arms exist to log and carry on. fmt
		// renders it "<nil>", which is what the literal they replaced already did.
		return fmt.Sprintf("%v", err)
	}
	return fmt.Sprintf("%s %s: %v", backendName, operation, err)
}
