package environment

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// TestDetectHybridInstallation_Bug demonstrates the current bug where
// ProxSave fails to detect PBS when both PVE and PBS are co-installed.
//
// This test documents the BUG that needs to be fixed:
// - When both pveversion and proxmox-backup-manager are available
// - detectProxmox() returns immediately after finding PVE
// - PBS detection is NEVER executed (early return bug at line 119)
// - Result: PBS data collection is completely skipped
func TestDetectHybridInstallation_Bug(t *testing.T) {
	// Setup: Mock both PVE and PBS as available
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		// Both commands exist (hybrid installation)
		switch cmd {
		case "pveversion":
			return "/usr/bin/pveversion", nil
		case "proxmox-backup-manager":
			return "/usr/sbin/proxmox-backup-manager", nil
		default:
			return "", nil
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

	// Assert: Current behavior (with BUG)
	if err != nil {
		t.Fatalf("Detect() returned unexpected error: %v", err)
	}

	// BUG: Detects only PVE, ignores PBS
	if envInfo.Type != types.ProxmoxVE {
		t.Errorf("Expected Type=ProxmoxVE (due to early return bug), got Type=%s", envInfo.Type)
	}

	if envInfo.Version != "9.1.5" {
		t.Errorf("Expected PVE Version=9.1.5, got Version=%s", envInfo.Version)
	}

	// TODO: After fix, this test should verify:
	// - envInfo.HasPVE should be true
	// - envInfo.HasPBS should be true
	// - envInfo.PVEVersion should be "9.1.5"
	// - envInfo.PBSVersion should be "4.1.6"
	// - Both PVE and PBS collectors should be invoked

	// Document the bug for posterity
	t.Logf("BUG CONFIRMED: System has both PVE and PBS, but ProxSave only detected PVE")
	t.Logf("  Detected Type: %s", envInfo.Type)
	t.Logf("  Detected Version: %s", envInfo.Version)
	t.Logf("  PBS detection was SKIPPED due to early return at detect.go:119")
	t.Logf("  Expected: Should detect and collect data from BOTH systems")
}

// TestDetectProxmox_EarlyReturnBug verifies the early return bug in detectProxmox()
func TestDetectProxmox_EarlyReturnBug(t *testing.T) {
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
		return "", nil
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

	// BUG: PBS detection is never called due to early return
	// After finding PVE at line 118-120, function returns immediately
	// Line 122 (PBS check) is NEVER executed
	if pbsDetectCalled {
		t.Error("PBS detection should NOT be called with current buggy code (early return)")
	}

	if pType != types.ProxmoxVE {
		t.Errorf("Expected pType=ProxmoxVE, got %s", pType)
	}

	if version != "8.1.3" {
		t.Errorf("Expected version=8.1.3, got %s", version)
	}

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	t.Logf("BUG VERIFIED: PBS detection was never called due to early return at line 119")
	t.Logf("  pveDetectCalled: %v", pveDetectCalled)
	t.Logf("  pbsDetectCalled: %v (should be false with current buggy code)", pbsDetectCalled)
}

// TestDetectPVEOnly verifies that PVE-only detection still works correctly
func TestDetectPVEOnly(t *testing.T) {
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		if cmd == "pveversion" {
			return "/usr/bin/pveversion", nil
		}
		// PBS not available
		return "", nil
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
	setValue(t, &lookPathFunc, func(cmd string) (string, error) {
		// PVE not available
		if cmd == "proxmox-backup-manager" {
			return "/usr/sbin/proxmox-backup-manager", nil
		}
		return "", nil
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
