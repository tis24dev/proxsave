package orchestrator

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/types"
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

// detectEnvironment is the seam that lets a test drive DetectCurrentSystem without a
// Proxmox host, the way compatFS does for the file probes in this file.
var detectEnvironment = environment.Detect

// DetectCurrentSystem reports what this host is, for the restore side.
//
// It used to carry its own rule, and the rule had rotted: hasPBS was
// `/etc/proxmox-backup` OR `/usr/sbin/proxmox-backup-proxy`, and the second path does
// not exist on PBS 3.4.9 or on 4.2.0 (the proxy is a systemd unit, not a binary on
// PATH). So the OR was never a choice between two proofs. Every PBS restore decision
// rested on one directory, which no package owns and no removal deletes: the same
// marker that turned a PVE-only host into a dual backup in issue #315.
//
// Backup and restore now read the same ladder, so a host cannot be one type while
// being collected and another while being restored onto.
func DetectCurrentSystem() SystemType {
	info, _ := detectEnvironment()
	if info == nil {
		return SystemTypeUnknown
	}
	switch info.Type {
	case types.ProxmoxDual:
		return SystemTypeDual
	case types.ProxmoxVE:
		return SystemTypePVE
	case types.ProxmoxBS:
		return SystemTypePBS
	default:
		return SystemTypeUnknown
	}
}

// DetectBackupType detects the type of backup from manifest.
//
// A role the archive was supposed to carry and does not is subtracted first. The
// targets field records what the run SET OUT to collect; incomplete_targets records
// what it failed to bring back. A dual run that lost its PBS half ships a PVE
// archive, and calling it dual would let it clear ValidateCompatibility against a
// dual host as though nothing were missing, with the PBS categories offered and
// nothing behind them.
func DetectBackupType(manifest *backup.Manifest) SystemType {
	if manifest == nil {
		return SystemTypeUnknown
	}

	if targets := completedTargets(manifest); len(targets) > 0 {
		return parseSystemTargets(targets)
	}
	if len(manifest.ProxmoxTargets) > 0 && len(manifest.IncompleteTargets) > 0 {
		// Every declared target failed. The archive carries no role payload at all,
		// so it is not a backup of either product.
		return SystemTypeUnknown
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

// completedTargets is the declared target list with every incomplete role removed.
// Matching is case-insensitive and trimmed because the two lists are written by
// different code paths: targets by ProxmoxType.Targets(), incomplete by the
// collector naming the recipe that failed.
func completedTargets(manifest *backup.Manifest) []string {
	if len(manifest.ProxmoxTargets) == 0 {
		return nil
	}
	missing := make(map[string]struct{}, len(manifest.IncompleteTargets))
	for _, target := range manifest.IncompleteTargets {
		missing[strings.ToLower(strings.TrimSpace(target))] = struct{}{}
	}
	kept := make([]string, 0, len(manifest.ProxmoxTargets))
	for _, target := range manifest.ProxmoxTargets {
		if _, gone := missing[strings.ToLower(strings.TrimSpace(target))]; gone {
			continue
		}
		kept = append(kept, target)
	}
	return kept
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
