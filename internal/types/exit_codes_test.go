package types

import "testing"

func TestExitCodeString(t *testing.T) {
	tests := []struct {
		name     string
		code     ExitCode
		expected string
	}{
		{"success", ExitSuccess, "success"},
		{"generic error", ExitGenericError, "generic error"},
		{"config error", ExitConfigError, "configuration error"},
		{"environment error", ExitEnvironmentError, "environment error"},
		{"backup error", ExitBackupError, "backup error"},
		{"storage error", ExitStorageError, "storage error"},
		{"network error", ExitNetworkError, "network error"},
		{"permission error", ExitPermissionError, "permission error"},
		{"verification error", ExitVerificationError, "verification error"},
		{"encryption error", ExitEncryptionError, "encryption error"},
		{"unknown", ExitCode(99), "unknown error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.code.String()
			if result != tt.expected {
				t.Errorf("ExitCode(%d).String() = %q; want %q", tt.code, result, tt.expected)
			}
		})
	}
}

func TestExitCodeInt(t *testing.T) {
	tests := []struct {
		name     string
		code     ExitCode
		expected int
	}{
		{"success", ExitSuccess, 0},
		{"generic error", ExitGenericError, 1},
		{"config error", ExitConfigError, 2},
		{"environment error", ExitEnvironmentError, 3},
		{"backup error", ExitBackupError, 4},
		{"storage error", ExitStorageError, 5},
		{"network error", ExitNetworkError, 6},
		{"permission error", ExitPermissionError, 7},
		{"verification error", ExitVerificationError, 8},
		{"encryption error", ExitEncryptionError, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.code.Int()
			if result != tt.expected {
				t.Errorf("ExitCode(%d).Int() = %d; want %d", tt.code, result, tt.expected)
			}
		})
	}
}
