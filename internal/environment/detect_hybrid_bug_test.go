package environment

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func isolateDetectionFallbacks(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()

	setValue(t, &additionalPaths, []string{})
	setValue(t, &pveSourceFiles, []string{})
	setValue(t, &pbsSourceFiles, []string{})
	setValue(t, &pveDirCandidates, []string{})
	setValue(t, &pbsDirCandidates, []string{})
	setValue(t, &pveVersionFile, filepath.Join(tmpDir, "missing-pve-version"))
	setValue(t, &pveLegacyFile, filepath.Join(tmpDir, "missing-pve-legacy"))
	setValue(t, &pbsVersionFile, filepath.Join(tmpDir, "missing-pbs-version"))
}

func TestDetectHybridInstallationDual(t *testing.T) {
	isolateDetectionFallbacks(t)

	// Setup: Mock both PVE and PBS as available
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		// Both commands exist (hybrid installation)
		switch cmd {
		case "pveversion":
			return "/usr/bin/pveversion", nil
		case "proxmox-backup-manager":
			return "/usr/sbin/proxmox-backup-manager", nil
		default:
			return "", errors.New("not found")
		}
	})

	setValue(t, &runCommandFunc, func(cmd string, args ...string) (string, error) {
		// Both commands return valid output
		if cmd == "/usr/bin/pveversion" {
			return "pve-manager/9.1.5/80cf92a64bef6889 (running kernel: 6.17.4-2-pve)", nil
		}
		if cmd == "/usr/sbin/proxmox-backup-manager" && len(args) > 0 && args[0] == "version" {
			return "proxmox-backup-server 4.1.6-1 running version: 4.1.6", nil
		}
		return "", nil
	})

	// Act: Run detection
	envInfo, err := Detect()

	if err != nil {
		t.Fatalf("Detect() returned unexpected error: %v", err)
	}

	if envInfo.Type != types.ProxmoxDual {
		t.Errorf("Expected Type=ProxmoxDual, got Type=%s", envInfo.Type)
	}

	if envInfo.PVEVersion != "9.1.5" {
		t.Errorf("Expected PVE Version=9.1.5, got Version=%s", envInfo.PVEVersion)
	}
	if envInfo.PBSVersion != "4.1.6" {
		t.Errorf("Expected PBS Version=4.1.6, got Version=%s", envInfo.PBSVersion)
	}
	if envInfo.Version != "pve=9.1.5,pbs=4.1.6" {
		t.Errorf("Expected combined Version, got Version=%s", envInfo.Version)
	}
}

func TestDetectProxmoxHybridCallsBothDetectors(t *testing.T) {
	isolateDetectionFallbacks(t)

	// Track which detection functions are called
	pveDetectCalled := false
	pbsDetectCalled := false

	// Mock both systems as available
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			pveDetectCalled = true
			return "/usr/bin/pveversion", nil
		}
		if cmd == "proxmox-backup-manager" {
			pbsDetectCalled = true
			return "/usr/sbin/proxmox-backup-manager", nil
		}
		return "", errors.New("not found")
	})

	setValue(t, &runCommandFunc, func(cmd string, args ...string) (string, error) {
		if cmd == "/usr/bin/pveversion" {
			return "pve-manager/8.1.3/b46aac3b42da5d15 (running kernel: 6.5.11-8-pve)", nil
		}
		if cmd == "/usr/sbin/proxmox-backup-manager" {
			return "proxmox-backup-server 3.1.2-1 running version: 3.1.2", nil
		}
		return "", nil
	})

	// Reset flags
	pveDetectCalled = false
	pbsDetectCalled = false

	// Run detection
	pType, version, err := detectProxmox()

	// Verify PVE was detected
	if !pveDetectCalled {
		t.Error("PVE detection was not called")
	}

	if !pbsDetectCalled {
		t.Error("PBS detection should be called on hybrid installations")
	}

	if pType != types.ProxmoxDual {
		t.Errorf("Expected pType=ProxmoxDual, got %s", pType)
	}

	if version != "pve=8.1.3,pbs=3.1.2" {
		t.Errorf("Expected combined version, got %s", version)
	}

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
}

// TestDetectPVEOnly verifies that PVE-only detection still works correctly
func TestDetectPVEOnly(t *testing.T) {
	isolateDetectionFallbacks(t)

	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			return "/usr/bin/pveversion", nil
		}
		// PBS not available
		return "", errors.New("not found")
	})

	setValue(t, &runCommandFunc, func(cmd string, args ...string) (string, error) {
		if cmd == "/usr/bin/pveversion" {
			return "pve-manager/8.1.3/b46aac3b42da5d15 (running kernel: 6.5.11-8-pve)", nil
		}
		return "", nil
	})

	envInfo, err := Detect()
	if err != nil {
		t.Fatalf("Detect() error: %v", err)
	}

	if envInfo.Type != types.ProxmoxVE {
		t.Errorf("Expected Type=ProxmoxVE, got %s", envInfo.Type)
	}

	if envInfo.Version != "8.1.3" {
		t.Errorf("Expected Version=8.1.3, got %s", envInfo.Version)
	}

	t.Logf("PVE-only detection works correctly: Type=%s, Version=%s", envInfo.Type, envInfo.Version)
}

// TestDetectPBSOnly verifies that PBS-only detection works when PVE is not present
func TestDetectPBSOnly(t *testing.T) {
	isolateDetectionFallbacks(t)

	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		// PVE not available
		if cmd == "proxmox-backup-manager" {
			return "/usr/sbin/proxmox-backup-manager", nil
		}
		return "", errors.New("not found")
	})

	setValue(t, &runCommandFunc, func(cmd string, args ...string) (string, error) {
		if cmd == "/usr/sbin/proxmox-backup-manager" {
			return "proxmox-backup-server 3.1.2-1 running version: 3.1.2", nil
		}
		return "", nil
	})

	envInfo, err := Detect()
	if err != nil {
		t.Fatalf("Detect() error: %v", err)
	}

	if envInfo.Type != types.ProxmoxBS {
		t.Errorf("Expected Type=ProxmoxBS, got %s", envInfo.Type)
	}

	if envInfo.Version != "3.1.2" {
		t.Errorf("Expected Version=3.1.2, got %s", envInfo.Version)
	}

	t.Logf("PBS-only detection works correctly: Type=%s, Version=%s", envInfo.Type, envInfo.Version)
}
