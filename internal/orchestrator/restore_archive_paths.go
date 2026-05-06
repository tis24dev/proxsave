// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func sanitizeRestoreEntryTarget(destRoot, entryName string) (string, string, error) {
	return sanitizeRestoreEntryTargetWithFS(restoreFS, destRoot, entryName)
}

func sanitizeRestoreEntryTargetWithFS(fsys FS, destRoot, entryName string) (string, string, error) {
	absDestRoot, err := resolveRestoreDestRoot(destRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve destination root: %w", err)
	}

	sanitized, err := normalizeRestoreEntryName(entryName)
	if err != nil {
		return "", "", err
	}
	absTarget, err := resolveRestoreEntryTarget(absDestRoot, sanitized)
	if err != nil {
		return "", "", fmt.Errorf("resolve extraction target: %w", err)
	}
	if err := ensureRestoreTargetWithinRoot(absDestRoot, absTarget, entryName); err != nil {
		return "", "", err
	}
	if err := ensureRestoreTargetResolverAllows(fsys, absDestRoot, absTarget, entryName); err != nil {
		return "", "", err
	}

	return absTarget, absDestRoot, nil
}

func resolveRestoreDestRoot(destRoot string) (string, error) {
	cleanDestRoot := filepath.Clean(destRoot)
	if cleanDestRoot == "" {
		cleanDestRoot = string(os.PathSeparator)
	}
	return filepath.Abs(cleanDestRoot)
}

func normalizeRestoreEntryName(entryName string) (string, error) {
	name := strings.TrimSpace(entryName)
	if name == "" {
		return "", fmt.Errorf("empty archive entry name")
	}
	sanitized := path.Clean(name)
	for strings.HasPrefix(sanitized, string(os.PathSeparator)) {
		sanitized = strings.TrimPrefix(sanitized, string(os.PathSeparator))
	}
	if sanitized == "" || sanitized == "." {
		return "", fmt.Errorf("invalid archive entry name: %q", entryName)
	}
	if sanitized == ".." || strings.HasPrefix(sanitized, "../") || strings.Contains(sanitized, "/../") {
		return "", fmt.Errorf("illegal path: %s", entryName)
	}
	return sanitized, nil
}

func resolveRestoreEntryTarget(absDestRoot, sanitized string) (string, error) {
	target := filepath.Join(absDestRoot, filepath.FromSlash(sanitized))
	return filepath.Abs(target)
}

func ensureRestoreTargetWithinRoot(absDestRoot, absTarget, entryName string) error {
	rel, err := filepath.Rel(absDestRoot, absTarget)
	if err != nil {
		return fmt.Errorf("illegal path: %s: %w", entryName, err)
	}
	if strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("illegal path: %s", entryName)
	}
	return nil
}

func ensureRestoreTargetResolverAllows(fsys FS, absDestRoot, absTarget, entryName string) error {
	if _, err := resolvePathWithinRootFS(fsys, absDestRoot, absTarget); err != nil {
		if isPathSecurityError(err) {
			return fmt.Errorf("illegal path: %s: %w", entryName, err)
		}
		if !isPathOperationalError(err) {
			return fmt.Errorf("resolve extraction target: %w", err)
		}
	}
	return nil
}

func shouldSkipProxmoxSystemRestore(relTarget string) (bool, string) {
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relTarget)))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case "etc/proxmox-backup/domains.cfg":
		return true, "PBS auth realms must be recreated (domains.cfg is too fragile to restore raw)"
	case "etc/proxmox-backup/user.cfg":
		return true, "PBS users must be recreated (user.cfg should not be restored raw)"
	case "etc/proxmox-backup/acl.cfg":
		return true, "PBS permissions must be recreated (acl.cfg should not be restored raw)"
	case "var/lib/proxmox-backup/.clusterlock":
		return true, "PBS runtime lock files must not be restored"
	}

	if strings.HasPrefix(rel, "var/lib/proxmox-backup/lock/") {
		return true, "PBS runtime lock files must not be restored"
	}

	return false, ""
}
