// Package types defines shared application data types.
package types

// ExitCode represents the application's exit codes.
type ExitCode int

const (
	// ExitSuccess - Execution completed successfully.
	ExitSuccess ExitCode = 0

	// ExitGenericError - Unspecified generic error.
	ExitGenericError ExitCode = 1

	// ExitConfigError - Configuration error.
	ExitConfigError ExitCode = 2

	// ExitEnvironmentError - Invalid or unsupported Proxmox environment.
	ExitEnvironmentError ExitCode = 3

	// ExitBackupError - Error during the backup operation (generic).
	ExitBackupError ExitCode = 4

	// ExitStorageError - Error during storage operations.
	ExitStorageError ExitCode = 5

	// ExitNetworkError - Network error (upload, notifications, etc.).
	ExitNetworkError ExitCode = 6

	// ExitPermissionError - Permission error.
	ExitPermissionError ExitCode = 7

	// ExitVerificationError - Error during integrity verification.
	ExitVerificationError ExitCode = 8

	// ExitCollectionError - Error during collection of configuration files.
	ExitCollectionError ExitCode = 9

	// ExitArchiveError - Error while creating the archive.
	ExitArchiveError ExitCode = 10

	// ExitCompressionError - Error during compression.
	ExitCompressionError ExitCode = 11

	// ExitDiskSpaceError - Insufficient disk space.
	ExitDiskSpaceError ExitCode = 12

	// ExitPanicError - Unhandled panic caught.
	ExitPanicError ExitCode = 13

	// ExitSecurityError - Errors detected by the security check.
	ExitSecurityError ExitCode = 14

	// ExitEncryptionError - Error during encryption setup or processing.
	ExitEncryptionError ExitCode = 15

	// ExitBackupSkipped - No backup was performed for a benign reason (another backup was already
	// running, or BACKUP_ENABLED=false). NOT a failure: it is a distinct sentinel so the daemon
	// does not ping a false-green finish for a child that never backed up, and the CLI footer
	// colors it as a benign skip rather than success or error (F09-03).
	ExitBackupSkipped ExitCode = 16

	// ExitGuardsPending - A guard cleanup ran without error but the storage is still
	// locked: guard mounts or immutable flags are left behind (typically hidden under a
	// live mount), or the remaining count could not be confirmed. Like ExitBackupSkipped
	// this is a STATE, not a failure — nothing went wrong, the work simply cannot finish
	// until the datastore is offline — so it is kept distinct from ExitGenericError,
	// which for this mode means the cleanup itself failed. A script gating on the exit
	// code needs the two apart: one is a bug to report, the other is "unmount and retry".
	// Also returned by the read-only --dry-run check when guards are found.
	ExitGuardsPending ExitCode = 17
)

// String returns a human-readable description of the exit code.
func (e ExitCode) String() string {
	switch e {
	case ExitSuccess:
		return "success"
	case ExitGenericError:
		return "generic error"
	case ExitConfigError:
		return "configuration error"
	case ExitEnvironmentError:
		return "environment error"
	case ExitBackupError:
		return "backup error"
	case ExitStorageError:
		return "storage error"
	case ExitNetworkError:
		return "network error"
	case ExitPermissionError:
		return "permission error"
	case ExitVerificationError:
		return "verification error"
	case ExitCollectionError:
		return "collection error"
	case ExitArchiveError:
		return "archive error"
	case ExitCompressionError:
		return "compression error"
	case ExitDiskSpaceError:
		return "disk space error"
	case ExitPanicError:
		return "panic error"
	case ExitSecurityError:
		return "security error"
	case ExitEncryptionError:
		return "encryption error"
	case ExitBackupSkipped:
		return "backup skipped"
	case ExitGuardsPending:
		return "guards still in place"
	default:
		return "unknown error"
	}
}

// Int returns the exit code as an integer.
func (e ExitCode) Int() int {
	return int(e)
}
