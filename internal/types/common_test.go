package types

import "testing"

func TestProxmoxTypeString(t *testing.T) {
	tests := []struct {
		name     string
		ptype    ProxmoxType
		expected string
	}{
		{"pve", ProxmoxVE, "pve"},
		{"pbs", ProxmoxBS, "pbs"},
		{"dual", ProxmoxDual, "dual"},
		{"unknown", ProxmoxUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.ptype.String()
			if result != tt.expected {
				t.Errorf("ProxmoxType.String() = %q; want %q", result, tt.expected)
			}
		})
	}
}

func TestProxmoxTypeCapabilities(t *testing.T) {
	tests := []struct {
		name        string
		ptype       ProxmoxType
		supportsPVE bool
		supportsPBS bool
		targets     []string
	}{
		{name: "pve", ptype: ProxmoxVE, supportsPVE: true, supportsPBS: false, targets: []string{"pve"}},
		{name: "pbs", ptype: ProxmoxBS, supportsPVE: false, supportsPBS: true, targets: []string{"pbs"}},
		{name: "dual", ptype: ProxmoxDual, supportsPVE: true, supportsPBS: true, targets: []string{"pve", "pbs"}},
		{name: "unknown", ptype: ProxmoxUnknown, supportsPVE: false, supportsPBS: false, targets: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ptype.SupportsPVE(); got != tt.supportsPVE {
				t.Fatalf("SupportsPVE() = %v, want %v", got, tt.supportsPVE)
			}
			if got := tt.ptype.SupportsPBS(); got != tt.supportsPBS {
				t.Fatalf("SupportsPBS() = %v, want %v", got, tt.supportsPBS)
			}

			gotTargets := tt.ptype.Targets()
			if len(gotTargets) != len(tt.targets) {
				t.Fatalf("Targets() len = %d, want %d (%v)", len(gotTargets), len(tt.targets), gotTargets)
			}
			for i := range gotTargets {
				if gotTargets[i] != tt.targets[i] {
					t.Fatalf("Targets()[%d] = %q, want %q", i, gotTargets[i], tt.targets[i])
				}
			}
		})
	}
}

func TestCompressionTypeString(t *testing.T) {
	tests := []struct {
		name     string
		ctype    CompressionType
		expected string
	}{
		{"gzip", CompressionGzip, "gz"},
		{"bzip2", CompressionBzip2, "bz2"},
		{"xz", CompressionXZ, "xz"},
		{"zstd", CompressionZstd, "zst"},
		{"none", CompressionNone, "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.ctype.String()
			if result != tt.expected {
				t.Errorf("CompressionType.String() = %q; want %q", result, tt.expected)
			}
		})
	}
}

func TestStorageLocationString(t *testing.T) {
	tests := []struct {
		name     string
		location StorageLocation
		expected string
	}{
		{"local", StorageLocal, "local"},
		{"secondary", StorageSecondary, "secondary"},
		{"cloud", StorageCloud, "cloud"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.location.String()
			if result != tt.expected {
				t.Errorf("StorageLocation.String() = %q; want %q", result, tt.expected)
			}
		})
	}
}

func TestLogLevelString(t *testing.T) {
	tests := []struct {
		name     string
		level    LogLevel
		expected string
	}{
		{"debug", LogLevelDebug, "DEBUG"},
		{"info", LogLevelInfo, "INFO"},
		{"warning", LogLevelWarning, "WARNING"},
		{"error", LogLevelError, "ERROR"},
		{"critical", LogLevelCritical, "CRITICAL"},
		{"none", LogLevelNone, "NONE"},
		{"unknown", LogLevel(99), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.level.String()
			if result != tt.expected {
				t.Errorf("LogLevel(%d).String() = %q; want %q", tt.level, result, tt.expected)
			}
		})
	}
}

func TestLogLevelValues(t *testing.T) {
	// Test that log levels are correctly ordered
	if LogLevelNone >= LogLevelCritical {
		t.Error("LogLevelNone should be less than LogLevelCritical")
	}
	if LogLevelCritical >= LogLevelError {
		t.Error("LogLevelCritical should be less than LogLevelError")
	}
	if LogLevelError >= LogLevelWarning {
		t.Error("LogLevelError should be less than LogLevelWarning")
	}
	if LogLevelWarning >= LogLevelInfo {
		t.Error("LogLevelWarning should be less than LogLevelInfo")
	}
	if LogLevelInfo >= LogLevelDebug {
		t.Error("LogLevelInfo should be less than LogLevelDebug")
	}
}
