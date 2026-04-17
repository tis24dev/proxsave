package orchestrator

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
)

var compatFS FS = osFS{}

// SystemType represents the type of Proxmox system
type SystemType string

const (
	SystemTypePVE     SystemType = "pve"
	SystemTypePBS     SystemType = "pbs"
	SystemTypeDual    SystemType = "dual"
	SystemTypeUnknown SystemType = "unknown"
)

func (s SystemType) SupportsPVE() bool {
	return s == SystemTypePVE || s == SystemTypeDual
}

func (s SystemType) SupportsPBS() bool {
	return s == SystemTypePBS || s == SystemTypeDual
}

func (s SystemType) Targets() []string {
	targets := make([]string, 0, 2)
	if s.SupportsPVE() {
		targets = append(targets, string(SystemTypePVE))
	}
	if s.SupportsPBS() {
		targets = append(targets, string(SystemTypePBS))
	}
	return targets
}

func (s SystemType) Overlaps(other SystemType) bool {
	return (s.SupportsPVE() && other.SupportsPVE()) || (s.SupportsPBS() && other.SupportsPBS())
}

// DetectCurrentSystem detects the type of the current system (PVE or PBS)
func DetectCurrentSystem() SystemType {
	hasPVE := fileExists("/etc/pve") || fileExists("/usr/bin/qm") || fileExists("/usr/bin/pct")
	hasPBS := fileExists("/etc/proxmox-backup") || fileExists("/usr/sbin/proxmox-backup-proxy")

	// Check for PVE indicators
	if hasPVE && hasPBS {
		return SystemTypeDual
	}
	if hasPVE {
		return SystemTypePVE
	}

	// Check for PBS indicators
	if hasPBS {
		return SystemTypePBS
	}

	return SystemTypeUnknown
}

// DetectBackupType detects the type of backup from manifest
func DetectBackupType(manifest *backup.Manifest) SystemType {
	if manifest == nil {
		return SystemTypeUnknown
	}

	if len(manifest.ProxmoxTargets) > 0 {
		return parseSystemTargets(manifest.ProxmoxTargets)
	}

	// Check ProxmoxType field if present
	if manifest.ProxmoxType != "" {
		if backupType := parseSystemTypeString(manifest.ProxmoxType); backupType != SystemTypeUnknown {
			return backupType
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

func parseSystemTypeString(value string) SystemType {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case normalized == "dual",
		strings.Contains(normalized, "dual"),
		strings.Contains(normalized, "pve,pbs"),
		strings.Contains(normalized, "pbs,pve"):
		return SystemTypeDual
	case strings.Contains(normalized, "pve"),
		strings.Contains(normalized, "proxmox-ve"),
		strings.Contains(normalized, "proxmox ve"):
		return SystemTypePVE
	case strings.Contains(normalized, "pbs"),
		strings.Contains(normalized, "proxmox-backup"),
		strings.Contains(normalized, "proxmox backup"),
		strings.Contains(normalized, "proxmox backup server"):
		return SystemTypePBS
	default:
		return SystemTypeUnknown
	}
}

func parseSystemTargets(values []string) SystemType {
	var hasPVE, hasPBS bool
	for _, value := range values {
		switch parseSystemTypeString(value) {
		case SystemTypeDual:
			return SystemTypeDual
		case SystemTypePVE:
			hasPVE = true
		case SystemTypePBS:
			hasPBS = true
		}
	}
	switch {
	case hasPVE && hasPBS:
		return SystemTypeDual
	case hasPVE:
		return SystemTypePVE
	case hasPBS:
		return SystemTypePBS
	default:
		return SystemTypeUnknown
	}
}

// ValidateCompatibility checks if a backup is compatible with the current system.
func ValidateCompatibility(currentSystem, backupType SystemType) error {
	if currentSystem == SystemTypeUnknown {
		return fmt.Errorf("warning: cannot detect current system type - restoration may fail")
	}

	if backupType == SystemTypeUnknown {
		// If backup type is unknown, we can't validate - issue warning
		return nil // Allow but warn in calling code
	}

	if !currentSystem.Overlaps(backupType) {
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

	if currentSystem != backupType {
		return fmt.Errorf(
			"partial compatibility: backup targets %s, current system %s. ProxSave will continue with only the categories compatible with the current system",
			strings.ToUpper(strings.Join(backupType.Targets(), "+")),
			strings.ToUpper(strings.Join(currentSystem.Targets(), "+")),
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
	case SystemTypeDual:
		return "Proxmox VE + Proxmox Backup Server (DUAL)"
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
	switch systemType {
	case SystemTypeDual:
		if pveContent, err := compatFS.ReadFile("/etc/pve-release"); err == nil {
			info["pve_version"] = strings.TrimSpace(string(pveContent))
		}
		if pbsContent, err := compatFS.ReadFile("/etc/proxmox-backup-release"); err == nil {
			info["pbs_version"] = strings.TrimSpace(string(pbsContent))
		}
	case SystemTypePVE:
		if content, err := compatFS.ReadFile("/etc/pve-release"); err == nil {
			info["version"] = strings.TrimSpace(string(content))
		}
	case SystemTypePBS:
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
		if !currentSystem.Overlaps(backupType) {
			warnings = append(warnings, fmt.Sprintf(
				"System type mismatch: backup is from %s but current system is %s",
				strings.ToUpper(string(backupType)),
				strings.ToUpper(string(currentSystem)),
			))
		} else if currentSystem != backupType {
			warnings = append(warnings, fmt.Sprintf(
				"Partial system type match: backup targets %s while current system is %s; only compatible categories can be restored",
				strings.ToUpper(strings.Join(backupType.Targets(), "+")),
				strings.ToUpper(strings.Join(currentSystem.Targets(), "+")),
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
