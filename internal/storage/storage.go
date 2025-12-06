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
}

// RetentionReporter can be implemented by storage backends that expose details
// about the most recent retention run (e.g., log counts).
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

// SupportsUnixOwnership returns true if the filesystem supports Unix ownership (chown/chmod)
func (f FilesystemType) SupportsUnixOwnership() bool {
	switch f {
	case FilesystemExt4, FilesystemExt3, FilesystemExt2,
		FilesystemXFS, FilesystemBtrfs, FilesystemZFS,
		FilesystemJFS, FilesystemReiserFS:
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
	case FilesystemFAT32, FilesystemFAT, FilesystemExFAT, FilesystemNTFS, FilesystemCIFS:
		return true
	default:
		return false
	}
}

// String returns a human-readable description of the filesystem type
func (f FilesystemType) String() string {
	return string(f)
}
