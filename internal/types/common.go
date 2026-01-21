package types

import "time"

// ProxmoxType represents the type of Proxmox environment.
type ProxmoxType string

const (
	// ProxmoxVE - Proxmox Virtual Environment
	ProxmoxVE ProxmoxType = "pve"

	// ProxmoxBS - Proxmox Backup Server
	ProxmoxBS ProxmoxType = "pbs"

	// ProxmoxUnknown - Unknown or undetected type
	ProxmoxUnknown ProxmoxType = "unknown"
)

// String returns the string representation of the Proxmox type.
func (p ProxmoxType) String() string {
	return string(p)
}

// CompressionType represents the compression type.
type CompressionType string

const (
	// CompressionGzip - gzip compression
	CompressionGzip CompressionType = "gz"

	// CompressionPigz - parallel gzip compression (pigz)
	CompressionPigz CompressionType = "pigz"

	// CompressionBzip2 - bzip2 compression
	CompressionBzip2 CompressionType = "bz2"

	// CompressionXZ - xz compression (LZMA)
	CompressionXZ CompressionType = "xz"

	// CompressionLZMA - classic lzma compression
	CompressionLZMA CompressionType = "lzma"

	// CompressionZstd - zstd compression
	CompressionZstd CompressionType = "zst"

	// CompressionNone - no compression
	CompressionNone CompressionType = "none"
)

// String returns the string representation of the compression type.
func (c CompressionType) String() string {
	return string(c)
}

// BackupInfo contains information about a backup.
type BackupInfo struct {
	// Backup timestamp
	Timestamp time.Time

	// Backup file name
	Filename string

	// File size in bytes
	Size int64

	// SHA256 checksum
	Checksum string

	// Compression type used
	Compression CompressionType

	// Full file path
	Path string

	// Proxmox environment type
	ProxmoxType ProxmoxType
}

// BackupMetadata contains metadata about a backup file
// Used by storage backends to track backup files
type BackupMetadata struct {
	// BackupFile is the full path to the backup file
	BackupFile string

	// Timestamp is when the backup was created
	Timestamp time.Time

	// Size is the file size in bytes
	Size int64

	// Checksum is the SHA256 checksum of the backup file
	Checksum string

	// ProxmoxType is the type of Proxmox environment (PVE/PBS)
	ProxmoxType ProxmoxType

	// Compression is the compression type used
	Compression CompressionType

	// Version is the backup format version
	Version string
}

// StorageLocation represents a storage destination.
type StorageLocation string

const (
	// StorageLocal - Local storage
	StorageLocal StorageLocation = "local"

	// StorageSecondary - Secondary storage
	StorageSecondary StorageLocation = "secondary"

	// StorageCloud - Cloud storage (rclone)
	StorageCloud StorageLocation = "cloud"
)

// String returns the string representation of the location.
func (s StorageLocation) String() string {
	return string(s)
}

// LogLevel represents the logging level.
type LogLevel int

const (
	// LogLevelDebug - Debug logs (maximum detail)
	LogLevelDebug LogLevel = 5

	// LogLevelInfo - General information
	LogLevelInfo LogLevel = 4

	// LogLevelWarning - Warnings
	LogLevelWarning LogLevel = 3

	// LogLevelError - Errors
	LogLevelError LogLevel = 2

	// LogLevelCritical - Critical errors
	LogLevelCritical LogLevel = 1

	// LogLevelNone - No logs
	LogLevelNone LogLevel = 0
)

// String returns the string representation of the log level.
func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "DEBUG"
	case LogLevelInfo:
		return "INFO"
	case LogLevelWarning:
		return "WARNING"
	case LogLevelError:
		return "ERROR"
	case LogLevelCritical:
		return "CRITICAL"
	case LogLevelNone:
		return "NONE"
	default:
		return "UNKNOWN"
	}
}
