// Package storage provides interfaces and implementations for managing backup storage
// across primary (local), secondary (remote filesystem), and cloud (rclone) destinations.
package storage

import (
	"context"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

// FilesystemType represents the detected filesystem type
type FilesystemType string

const (
	// Filesystems that support Unix ownership
	FilesystemExt4     FilesystemType = "ext4"
	FilesystemExt3     FilesystemType = "ext3"
	FilesystemExt2     FilesystemType = "ext2"
	FilesystemXFS      FilesystemType = "xfs"
	FilesystemBtrfs    FilesystemType = "btrfs"
	FilesystemZFS      FilesystemType = "zfs"
	FilesystemJFS      FilesystemType = "jfs"
	FilesystemReiserFS FilesystemType = "reiserfs"
	FilesystemOverlay  FilesystemType = "overlay"
	FilesystemTmpfs    FilesystemType = "tmpfs"

	// Filesystems that do NOT support Unix ownership
	FilesystemFAT32 FilesystemType = "vfat"
	FilesystemFAT   FilesystemType = "fat"
	FilesystemExFAT FilesystemType = "exfat"
	FilesystemNTFS  FilesystemType = "ntfs"
	FilesystemFUSE  FilesystemType = "fuse"

	// Network filesystems (need testing)
	FilesystemNFS  FilesystemType = "nfs"
	FilesystemNFS4 FilesystemType = "nfs4"
	FilesystemCIFS FilesystemType = "cifs"
	FilesystemSMB  FilesystemType = "smb"

	// Unknown or unsupported
	FilesystemUnknown FilesystemType = "unknown"
)

// FilesystemInfo contains information about a filesystem
type FilesystemInfo struct {
	Path              string
	Type              FilesystemType
	SupportsOwnership bool
	IsNetworkFS       bool
	MountPoint        string
	Device            string
}

// BackupLocation represents a location where backups are stored
type BackupLocation string

const (
	LocationPrimary   BackupLocation = "primary"
	LocationSecondary BackupLocation = "secondary"
	LocationCloud     BackupLocation = "cloud"
)

// Storage defines the interface for backup storage operations
type Storage interface {
	// Name returns the human-readable name of this storage backend
	Name() string

	// Location returns the backup location type (primary/secondary/cloud)
	Location() BackupLocation

	// IsEnabled returns true if this storage backend is configured and enabled
	IsEnabled() bool

	// IsCritical returns true if failures in this storage should abort the backup
	// Primary storage is critical, secondary and cloud are non-critical
	IsCritical() bool

	// DetectFilesystem detects the filesystem type for the destination path
	// This should be called BEFORE any operations and logged in real-time
	DetectFilesystem(ctx context.Context) (*FilesystemInfo, error)

	// Store stores a backup file to this storage destination
	// For cloud storage, this includes verification and retry logic
	// Returns error only if IsCritical() is true, otherwise logs warnings
	Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error

	// List returns all backups stored in this location
	List(ctx context.Context) ([]*types.BackupMetadata, error)

	// Delete removes a backup file and its associated files
	Delete(ctx context.Context, backupFile string) error

	// ApplyRetention removes old backups according to retention policy
	// Supports both simple (count-based) and GFS (time-distributed) policies
	// For cloud storage, uses batched deletion to avoid API rate limits
	ApplyRetention(ctx context.Context, config RetentionConfig) (int, error)

	// VerifyUpload verifies that a file was successfully uploaded (cloud only)
	VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error)

	// GetStats returns storage statistics (space used, file count, etc.)
	GetStats(ctx context.Context) (*StorageStats, error)
}

// RetentionSummary captures what happened during the last retention run.
type RetentionSummary struct {
	BackupsDeleted   int
	BackupsRemaining int
	LogsDeleted      int
	LogsRemaining    int
	HasLogInfo       bool

	// ScopeValid separates "this host owns nothing here" from "retention never ran",
	// which a count alone cannot express. Retention is skipped entirely when no limit
	// is configured, and reporting 0 backups on a healthy host would be a worse lie
	// than the unscoped total it replaces, so a consumer falls back to that total
	// unless this is set.
	ScopeValid bool
	// Owned is how many archives at this location this host is answerable for, taken
	// after applyRetentionHostScope and net of whatever the pass then deleted. It is
	// the only number that may be printed next to a retention limit. GetStats counts
	// every archive at the location, so on a path shared with another ProxSave host
	// it counts that host's too, which is how the summary came to read "40/7" on a
	// machine that owns five (discussion #292).
	//
	// "Answerable for" is wider than "will prune", and deliberately so. It adds the
	// archives no host manages at all: pre-Go "proxmox-backup-*" files that name
	// nobody, and this machine's own work written under a spelling of its name it can
	// no longer resolve. Those grow without bound and nothing else counts them, so
	// leaving them out turns the one number an operator watches into a false
	// all-clear. Archives carrying another machine's name are excluded, because that
	// machine prunes them and reports them.
	Owned int

	// PassCompleted reports whether a retention pass RAN AND RETURNED WITHOUT ERROR.
	// It does NOT certify the counts, and reading it that way would produce exactly
	// the false report discussion #292 opened with.
	//
	// BackupsDeleted, BackupsRemaining, LogsDeleted, LogsRemaining and HasLogInfo are
	// assigned only on the paths that actually DELETE something. The most common
	// healthy pass, the steady state where everything is already within the limit,
	// deletes nothing and therefore publishes zeros, while the location still holds
	// however many archives this host owns. A caller printing BackupsRemaining on the
	// strength of this flag would tell the operator the location is empty when it is
	// full. Owned is the field that answers "how many are there", and ScopeValid is
	// the field that says whether Owned is worth anything.
	//
	// What false means: no pass has run, or the last one bailed. The counts are then
	// the zero value even if the pass deleted archives before it failed, because
	// ApplyRetention clears them on entry and only fills them once the delete loop
	// finishes.
	//
	// What true does NOT mean: that anything was examined. A pass with no limit
	// configured, and a pass over a location whose directory has vanished, both
	// return without error and both stamp this true. It answers one question only,
	// and it is the question a reader of this struct can act on.
	//
	// It exists because nothing else here can answer "has a pass run". The counts
	// cannot: a healthy pass that finds everything within the limit publishes zeros,
	// which is byte for byte what a backend that has never run reports. ScopeValid
	// cannot either, and reusing it would be wrong rather than merely imprecise. It
	// answers "did the scope account for the listing", so it is deliberately false
	// after a real pass on a host that cannot name itself (applyRetentionHostScope
	// returns nothing owned there and warns), and a caller reading it as "a pass
	// ran" would mis-report exactly that machine.
	//
	// False deliberately does NOT separate "no pass has run" from "the last pass
	// failed". The only question a reader of this struct can act on is whether the
	// numbers describe a finished pass, and whoever ran the pass already knows which
	// of the two it is, because it is holding the error.
	PassCompleted bool
}

// RetentionReporter can be implemented by storage backends that expose details
// about the most recent retention run (e.g., log counts).
//
// The contract, which this interface used to leave unsaid:
//
// LastRetentionSummary describes the most recent ApplyRetention call on THIS
// backend and nothing else. It never fails and may be called at any time, which is
// precisely why the value has to say how much it is worth: read PassCompleted
// first and treat the counts as absent while it is false. Read it as a statement
// about the PASS, never about the location: see PassCompleted for why a completed
// pass routinely reports zero remaining on a location that is full. Counts do not
// accumulate and do not survive into the next pass either, because ApplyRetention
// clears them before its first return, so a pass that bails reports zeros rather
// than the previous pass's counts standing as if they were current.
//
// It carries no locking, and adding some would be pretending. Every implementation
// in this package writes these fields from ApplyRetention and reads them here, so
// calling the two concurrently on ONE backend is a data race. Nothing does today:
// StorageAdapter.Sync (internal/orchestrator/storage_adapter.go) is the only
// caller, it reads the summary a few lines below its own ApplyRetention call on the
// same goroutine, and dispatchPostBackup runs the adapters one after another, each
// over a backend of its own. An implementation that means to be read from another
// goroutine has to provide its own synchronisation.
type RetentionReporter interface {
	LastRetentionSummary() RetentionSummary
}

// StorageStats contains statistics about a storage location
type StorageStats struct {
	TotalBackups   int
	TotalSize      int64
	OldestBackup   *time.Time
	NewestBackup   *time.Time
	AvailableSpace int64
	TotalSpace     int64
	UsedSpace      int64
	FilesystemType FilesystemType
}

// StorageError represents an error from a storage operation
type StorageError struct {
	Location    BackupLocation
	Operation   string // "store", "delete", "verify", etc.
	Path        string
	Err         error
	IsCritical  bool
	Recoverable bool
	// PrimarySaved is true when the PRIMARY archive uploaded and verified but a later step
	// (a sidecar file) failed. It lets a caller phrase the outcome as "primary saved, sidecar
	// failed" instead of the misleading "backup was not saved" (F08-08). Default false means
	// "not applicable / primary not confirmed saved", which keeps the generic wording.
	PrimarySaved bool
}

func (e *StorageError) Error() string {
	criticality := "WARNING"
	if e.IsCritical {
		criticality = "CRITICAL"
	}

	recoverable := ""
	if e.Recoverable {
		recoverable = " (recoverable)"
	}

	return criticality + ": " + string(e.Location) + " storage " + e.Operation +
		" operation failed for " + e.Path + recoverable + ": " + e.Err.Error()
}

// Unwrap exposes the wrapped cause so callers can classify it with errors.Is/As
// (e.g. errors.Is(err, safefs.ErrTimeout)).
func (e *StorageError) Unwrap() error { return e.Err }

// SupportsUnixOwnership returns true if the filesystem supports Unix ownership (chown/chmod)
func (f FilesystemType) SupportsUnixOwnership() bool {
	switch f {
	case FilesystemExt4, FilesystemExt3, FilesystemExt2,
		FilesystemXFS, FilesystemBtrfs, FilesystemZFS,
		FilesystemJFS, FilesystemReiserFS, FilesystemOverlay, FilesystemTmpfs:
		return true
	case FilesystemFAT32, FilesystemFAT, FilesystemExFAT,
		FilesystemNTFS, FilesystemFUSE:
		return false
	case FilesystemNFS, FilesystemNFS4, FilesystemCIFS, FilesystemSMB:
		// Network filesystems need runtime testing
		return false
	default:
		return false
	}
}

// IsNetworkFilesystem returns true if the filesystem is network-based
func (f FilesystemType) IsNetworkFilesystem() bool {
	switch f {
	case FilesystemNFS, FilesystemNFS4, FilesystemCIFS, FilesystemSMB:
		return true
	default:
		return false
	}
}

// ShouldAutoExclude returns true if this filesystem should be automatically excluded
// from ownership operations (incompatible filesystems like FAT32/CIFS)
func (f FilesystemType) ShouldAutoExclude() bool {
	switch f {
	case FilesystemFAT32, FilesystemFAT, FilesystemExFAT, FilesystemNTFS, FilesystemFUSE, FilesystemCIFS, FilesystemSMB:
		return true
	default:
		return false
	}
}

// String returns a human-readable description of the filesystem type
func (f FilesystemType) String() string {
	return string(f)
}
