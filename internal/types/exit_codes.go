# Package types defines shared application data types.
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
	default:
		return "unknown error"
	}
}

// Int restituisce il codice di uscita come intero
func (e ExitCode) Int() int {
	return int(e)
}
