package orchestrator

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/backup"
)

var compatFS FS = osFS{}

// SystemType represents the type of Proxmox system
type SystemType string

const (
	SystemTypePVE     SystemType = "pve"
	SystemTypePBS     SystemType = "pbs"
	SystemTypeUnknown SystemType = "unknown"
)

// DetectCurrentSystem detects the type of the current system (PVE or PBS)
func DetectCurrentSystem() SystemType {
	// Check for PVE indicators
	if fileExists("/etc/pve") || fileExists("/usr/bin/qm") || fileExists("/usr/bin/pct") {
		return SystemTypePVE
	}

	// Check for PBS indicators
	if fileExists("/etc/proxmox-backup") || fileExists("/usr/sbin/proxmox-backup-proxy") {
		return SystemTypePBS
	}

	return SystemTypeUnknown
}

// DetectBackupType detects the type of backup from manifest
func DetectBackupType(manifest *backup.Manifest) SystemType {
	if manifest == nil {
		return SystemTypeUnknown
	}

	// Check ProxmoxType field if present
	if manifest.ProxmoxType != "" {
		proxmoxType := strings.ToLower(manifest.ProxmoxType)
		if strings.Contains(proxmoxType, "pve") || strings.Contains(proxmoxType, "proxmox-ve") {
			return SystemTypePVE
		}
		if strings.Contains(proxmoxType, "pbs") || strings.Contains(proxmoxType, "proxmox-backup") {
			return SystemTypePBS
		}
	}

	// Fallback: check hostname patterns
	if manifest.Hostname != "" {
		hostname := strings.ToLower(manifest.Hostname)
		if strings.Contains(hostname, "pve") {
			return SystemTypePVE
		}
		if strings.Contains(hostname, "pbs") {
			return SystemTypePBS
		}
	}

	// If we can't determine from manifest, return unknown
	return SystemTypeUnknown
}

// ValidateCompatibility checks if a backup is compatible with the current system
func ValidateCompatibility(manifest *backup.Manifest) error {
	currentSystem := DetectCurrentSystem()
	backupType := DetectBackupType(manifest)

	// If we can't detect either, issue a warning but allow
	if currentSystem == SystemTypeUnknown {
		return fmt.Errorf("warning: cannot detect current system type - restoration may fail")
	}

	if backupType == SystemTypeUnknown {
		// If backup type is unknown, we can't validate - issue warning
		return nil // Allow but warn in calling code
	}

	// Check for incompatibility
	if currentSystem != backupType {
		return fmt.Errorf(
			"incompatible backup: this is a %s backup but you are running on a %s system. "+
				"Restoring a %s backup to a %s system will likely cause system instability. "+
				"Please restore this backup only on a %s system",
			strings.ToUpper(string(backupType)),
			strings.ToUpper(string(currentSystem)),
			strings.ToUpper(string(backupType)),
			strings.ToUpper(string(currentSystem)),
			strings.ToUpper(string(backupType)),
		)
	}

	return nil
}

// GetSystemTypeString returns a human-readable system type string
func GetSystemTypeString(st SystemType) string {
	switch st {
	case SystemTypePVE:
		return "Proxmox Virtual Environment (PVE)"
	case SystemTypePBS:
		return "Proxmox Backup Server (PBS)"
	default:
		return "Unknown System"
	}
}

// fileExists checks if a file or directory exists
func fileExists(path string) bool {
	_, err := compatFS.Stat(path)
	return err == nil
}

// GetSystemInfo returns detailed information about the current system
func GetSystemInfo() map[string]string {
	info := make(map[string]string)

	systemType := DetectCurrentSystem()
	info["type"] = string(systemType)
	info["type_name"] = GetSystemTypeString(systemType)

	// Get version information
	if systemType == SystemTypePVE {
		if content, err := compatFS.ReadFile("/etc/pve-release"); err == nil {
			info["version"] = strings.TrimSpace(string(content))
		}
	} else if systemType == SystemTypePBS {
		if content, err := compatFS.ReadFile("/etc/proxmox-backup-release"); err == nil {
			info["version"] = strings.TrimSpace(string(content))
		}
	}

	// Get hostname
	if content, err := compatFS.ReadFile("/etc/hostname"); err == nil {
		info["hostname"] = strings.TrimSpace(string(content))
	}

	return info
}

// CheckSystemRequirements checks if the system meets requirements for restore
func CheckSystemRequirements(manifest *backup.Manifest) []string {
	var warnings []string

	currentSystem := DetectCurrentSystem()
	backupType := DetectBackupType(manifest)

	// Check system type compatibility
	if currentSystem != SystemTypeUnknown && backupType != SystemTypeUnknown {
		if currentSystem != backupType {
			warnings = append(warnings, fmt.Sprintf(
				"System type mismatch: backup is from %s but current system is %s",
				strings.ToUpper(string(backupType)),
				strings.ToUpper(string(currentSystem)),
			))
		}
	}

	// Check for required directories
	requiredDirs := []string{"/etc", "/var", "/usr"}
	for _, dir := range requiredDirs {
		if !fileExists(dir) {
			warnings = append(warnings, fmt.Sprintf("Required directory missing: %s", dir))
		}
	}

	// Check disk space (basic check)
	// This is a simplified check - in production you'd want more sophisticated checks
	if _, err := compatFS.Stat("/"); err != nil {
		warnings = append(warnings, "Cannot access root filesystem - may lack permissions")
	}

	return warnings
}
