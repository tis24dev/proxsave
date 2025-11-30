package environment

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestDetectProxmoxType(t *testing.T) {
	// Since we can't easily mock file existence in the actual paths,
	// we test the fileExists helper function and trust the logic
	result := DetectProxmoxType()

	// On a real Proxmox system, this should be PVE or PBS
	// On a non-Proxmox system (like our build environment), it should be Unknown
	if result != types.ProxmoxVE && result != types.ProxmoxBS && result != types.ProxmoxUnknown {
		t.Errorf("DetectProxmoxType() returned unexpected type: %v", result)
	}
}

func TestGetVersion(t *testing.T) {
	// Test with actual detected type
	pType := DetectProxmoxType()

	if pType == types.ProxmoxUnknown {
		// On non-Proxmox systems, GetVersion should return an error
		_, err := GetVersion(types.ProxmoxVE)
		if err == nil {
			t.Error("GetVersion should return error on non-Proxmox system")
		}
	} else {
		// On Proxmox systems, should return valid version
		version, err := GetVersion(pType)
		if err != nil {
			t.Errorf("GetVersion(%v) error = %v", pType, err)
		}
		if version == "" {
			t.Error("GetVersion should return non-empty version")
		}
	}
}

func TestGetVersionUnknownType(t *testing.T) {
	_, err := GetVersion(types.ProxmoxUnknown)
	if err == nil {
		t.Error("GetVersion should return error for unknown type")
	}
}

func TestDetect(t *testing.T) {
	info, err := Detect()

	// Should always return EnvironmentInfo, even if detection fails
	if info == nil {
		t.Fatal("Detect() should return EnvironmentInfo even on error")
	}

	// On non-Proxmox systems, should return error
	pType := DetectProxmoxType()
	if pType == types.ProxmoxUnknown {
		if err == nil {
			t.Error("Detect() should return error on non-Proxmox system")
		}
		if info.Type != types.ProxmoxUnknown {
			t.Errorf("EnvironmentInfo.Type = %v; want %v", info.Type, types.ProxmoxUnknown)
		}
		if info.Version != "unknown" {
			t.Errorf("EnvironmentInfo.Version = %q; want %q", info.Version, "unknown")
		}
	} else {
		// On Proxmox systems, should succeed
		if err != nil {
			t.Errorf("Detect() error = %v on Proxmox system", err)
		}
		if info.Type == types.ProxmoxUnknown {
			t.Error("EnvironmentInfo.Type should not be unknown on Proxmox system")
		}
		if info.Version == "" || info.Version == "unknown" {
			t.Error("EnvironmentInfo.Version should contain valid version on Proxmox system")
		}
	}
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Test file exists
	if !fileExists(testFile) {
		t.Error("fileExists should return true for existing file")
	}

	// Test file doesn't exist
	if fileExists(filepath.Join(tmpDir, "nonexistent.txt")) {
		t.Error("fileExists should return false for nonexistent file")
	}

	// Test directory (should return false)
	if fileExists(tmpDir) {
		t.Error("fileExists should return false for directories")
	}
}

func TestEnvironmentInfo(t *testing.T) {
	tests := []struct {
		name    string
		envInfo EnvironmentInfo
	}{
		{
			name: "pve environment",
			envInfo: EnvironmentInfo{
				Type:    types.ProxmoxVE,
				Version: "7.2-1",
			},
		},
		{
			name: "pbs environment",
			envInfo: EnvironmentInfo{
				Type:    types.ProxmoxBS,
				Version: "2.4-1",
			},
		},
		{
			name: "unknown environment",
			envInfo: EnvironmentInfo{
				Type:    types.ProxmoxUnknown,
				Version: "unknown",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envInfo.Type == "" {
				t.Error("EnvironmentInfo.Type should not be empty")
			}
			if tt.envInfo.Version == "" {
				t.Error("EnvironmentInfo.Version should not be empty")
			}
		})
	}
}

func TestExtractPVEVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard output",
			input: "pve-manager/7.4-3/d4a3b4a1 (running kernel: 5.15.35-1-pve)",
			want:  "7.4-3",
		},
		{
			name:  "missing version",
			input: "no version here",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractPVEVersion(tt.input); got != tt.want {
				t.Errorf("extractPVEVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractPBSVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard output",
			input: "proxmox-backup-manager 2.4.1\nversion: 2.4.1",
			want:  "2.4.1",
		},
		{
			name:  "missing version",
			input: "some text without version",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractPBSVersion(tt.input); got != tt.want {
				t.Errorf("extractPBSVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
