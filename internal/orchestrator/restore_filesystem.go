package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

// FstabEntry represents a single non-comment line in /etc/fstab
type FstabEntry struct {
	Device     string
	MountPoint string
	Type       string
	Options    string
	Dump       string
	Pass       string
	RawLine    string // Preserves original formatting if needed, though we might reconstruct
	IsComment  bool
}

// FstabAnalysisResult holds the outcome of comparing two fstabs
type FstabAnalysisResult struct {
	RootComparable    bool
	RootMatch         bool
	RootDeviceCurrent string
	RootDeviceBackup  string
	SwapComparable    bool
	SwapMatch         bool
	SwapDeviceCurrent string
	SwapDeviceBackup  string
	ProposedMounts    []FstabEntry
	SkippedMounts     []FstabEntry
}

// SmartMergeFstab is the main entry point for the intelligent fstab restore workflow
func SmartMergeFstab(ctx context.Context, logger *logging.Logger, reader *bufio.Reader, currentFstabPath, backupFstabPath string, dryRun bool) error {
	logger.Info("")
	logger.Step("Smart Filesystem Configuration Merge")
	logger.Debug("[FSTAB_MERGE] Starting analysis of %s vs backup %s...", currentFstabPath, backupFstabPath)

	// 1. Parsing
	currentEntries, currentRaw, err := parseFstab(currentFstabPath)
	if err != nil {
		return fmt.Errorf("failed to parse current fstab: %w", err)
	}
	backupEntries, _, err := parseFstab(backupFstabPath)
	if err != nil {
		return fmt.Errorf("failed to parse backup fstab: %w", err)
	}

	// 2. Analysis
	analysis := analyzeFstabMerge(logger, currentEntries, backupEntries)

	// 3. User Interface & Prompt
	printFstabAnalysis(logger, analysis)

	if len(analysis.ProposedMounts) == 0 {
		logger.Info("No new safe mounts found to restore. Keeping current fstab.")
		return nil
	}

	defaultYes := analysis.RootComparable && analysis.RootMatch && (!analysis.SwapComparable || analysis.SwapMatch)
	confirmMsg := "Vuoi aggiungere i mount mancanti (NFS/CIFS e dati su UUID/LABEL verificati)?"
	confirmed, err := confirmLocal(ctx, reader, confirmMsg, defaultYes)
	if err != nil {
		return err
	}

	if !confirmed {
		logger.Info("Fstab merge skipped by user.")
		return nil
	}

	// 4. Execution
	return applyFstabMerge(ctx, logger, currentRaw, currentFstabPath, analysis.ProposedMounts, dryRun)
}

// confirmLocal prompts for yes/no
func confirmLocal(ctx context.Context, reader *bufio.Reader, prompt string, defaultYes bool) (bool, error) {
	defStr := "[Y/n]"
	if !defaultYes {
		defStr = "[y/N]"
	}
	fmt.Printf("%s %s ", prompt, defStr)

	line, err := input.ReadLineWithContext(ctx, reader)
	if err != nil {
		return false, err
	}

	trimmed := strings.TrimSpace(strings.ToLower(line))
	if trimmed == "" {
		return defaultYes, nil
	}
	return trimmed == "y" || trimmed == "yes", nil
}

func parseFstab(path string) ([]FstabEntry, []string, error) {
	content, err := restoreFS.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	var entries []FstabEntry
	var rawLines []string
	scanner := bufio.NewScanner(bytes.NewReader(content))

	for scanner.Scan() {
		line := scanner.Text()
		rawLines = append(rawLines, line)

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Strip inline comments: anything after a whitespace-prefixed '#'.
		if idx := strings.Index(trimmed, "#"); idx >= 0 {
			prefix := strings.TrimSpace(trimmed[:idx])
			// Consider this an inline comment only when there's something before it and a whitespace boundary.
			if prefix != "" && prefix != trimmed[:idx] {
				trimmed = prefix
			}
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			// Invalid or partial line, skip for structural analysis
			continue
		}

		entry := FstabEntry{
			Device:     fields[0],
			MountPoint: fields[1],
			Type:       fields[2],
			Options:    fields[3],
			RawLine:    line,
		}
		if len(fields) > 4 {
			entry.Dump = fields[4]
		}
		if len(fields) > 5 {
			entry.Pass = fields[5]
		}

		entries = append(entries, entry)
	}

	return entries, rawLines, scanner.Err()
}

func analyzeFstabMerge(logger *logging.Logger, current, backup []FstabEntry) FstabAnalysisResult {
	result := FstabAnalysisResult{
		RootMatch: true,
		SwapMatch: true,
	}

	// Map present mountpoints for quick lookup.
	currentMounts := make(map[string]FstabEntry)
	var currentRootDevice, currentSwapDevice string
	for _, e := range current {
		currentMounts[e.MountPoint] = e

		if e.MountPoint == "/" {
			currentRootDevice = e.Device
		}
		if isSwapEntry(e) && currentSwapDevice == "" {
			currentSwapDevice = e.Device
		}
	}
	result.RootDeviceCurrent = currentRootDevice
	result.SwapDeviceCurrent = currentSwapDevice

	var backupRootDevice, backupSwapDevice string
	for _, b := range backup {
		logger.Debug("[FSTAB_MERGE] Parsing backup entry: %s on %s (Type: %s)", b.Device, b.MountPoint, b.Type)

		if b.MountPoint == "/" && backupRootDevice == "" {
			backupRootDevice = b.Device
		}
		if isSwapEntry(b) && backupSwapDevice == "" {
			backupSwapDevice = b.Device
		}

		// Critical mountpoints and swap are never auto-restored.
		if isCriticalMountPoint(b.MountPoint) || isSwapEntry(b) {
			if curr, exists := currentMounts[b.MountPoint]; exists {
				if curr.Device != b.Device {
					logger.Debug("[FSTAB_MERGE] ⚠ Critical mismatch on %s: Current=%s vs Backup=%s", b.MountPoint, curr.Device, b.Device)
				} else {
					logger.Debug("[FSTAB_MERGE] ✓ Match found for %s. Keeping current.", b.MountPoint)
				}
			}
			continue
		}

		if _, exists := currentMounts[b.MountPoint]; exists {
			logger.Debug("[FSTAB_MERGE] - Mountpoint %s already exists. Ignoring backup version.", b.MountPoint)
			continue
		}

		if isSafeMountCandidate(b) {
			logger.Debug("[FSTAB_MERGE] + Safe candidate for addition: %s %s -> %s", b.Type, b.Device, b.MountPoint)
			result.ProposedMounts = append(result.ProposedMounts, b)
			continue
		}

		logger.Debug("[FSTAB_MERGE] ! Unsafe candidate (not proposed): %s %s -> %s", b.Type, b.Device, b.MountPoint)
		result.SkippedMounts = append(result.SkippedMounts, b)
	}

	result.RootDeviceBackup = backupRootDevice
	result.SwapDeviceBackup = backupSwapDevice

	if result.RootDeviceCurrent != "" && result.RootDeviceBackup != "" {
		result.RootComparable = true
		result.RootMatch = result.RootDeviceCurrent == result.RootDeviceBackup
	}
	if result.SwapDeviceCurrent != "" && result.SwapDeviceBackup != "" {
		result.SwapComparable = true
		result.SwapMatch = result.SwapDeviceCurrent == result.SwapDeviceBackup
	}

	return result
}

func isCriticalMountPoint(mp string) bool {
	switch mp {
	case "/", "/boot", "/boot/efi", "/usr":
		return true
	}
	return false
}

func isSwapEntry(e FstabEntry) bool {
	return strings.EqualFold(strings.TrimSpace(e.Type), "swap")
}

func isNetworkMountEntry(e FstabEntry) bool {
	fsType := strings.ToLower(strings.TrimSpace(e.Type))
	switch fsType {
	case "nfs", "nfs4", "cifs", "smbfs":
		return true
	}

	device := strings.TrimSpace(e.Device)
	if strings.HasPrefix(device, "//") {
		return true
	}
	if strings.Contains(device, ":/") {
		return true
	}

	return false
}

func isVerifiedStableDeviceRef(device string) bool {
	dev := strings.TrimSpace(device)
	if dev == "" {
		return false
	}

	// Absolute stable paths.
	if strings.HasPrefix(dev, "/dev/disk/by-uuid/") ||
		strings.HasPrefix(dev, "/dev/disk/by-label/") ||
		strings.HasPrefix(dev, "/dev/disk/by-partuuid/") ||
		strings.HasPrefix(dev, "/dev/mapper/") {
		_, err := restoreFS.Stat(dev)
		return err == nil
	}

	// Tokenized stable references (best-effort verification via /dev/disk).
	switch {
	case strings.HasPrefix(dev, "UUID="):
		uuid := strings.TrimPrefix(dev, "UUID=")
		_, err := restoreFS.Stat(filepath.Join("/dev/disk/by-uuid", uuid))
		return err == nil
	case strings.HasPrefix(dev, "LABEL="):
		label := strings.TrimPrefix(dev, "LABEL=")
		_, err := restoreFS.Stat(filepath.Join("/dev/disk/by-label", label))
		return err == nil
	case strings.HasPrefix(dev, "PARTUUID="):
		partuuid := strings.TrimPrefix(dev, "PARTUUID=")
		_, err := restoreFS.Stat(filepath.Join("/dev/disk/by-partuuid", partuuid))
		return err == nil
	}

	return false
}

func isSafeMountCandidate(e FstabEntry) bool {
	if isNetworkMountEntry(e) {
		return true
	}
	return isVerifiedStableDeviceRef(e.Device)
}

func printFstabAnalysis(logger *logging.Logger, res FstabAnalysisResult) {
	fmt.Println()
	logger.Info("Analisi fstab:")

	// Root Status
	if !res.RootComparable {
		logger.Warning("! Root filesystem: non determinabile (entry mancante in current/backup fstab)")
	} else if res.RootMatch {
		logger.Info("✓ Root filesystem: compatibile (UUID kept from system)")
	} else {
		// ANSI Yellow/Red might be nice, but stick to standard logger for now.
		logger.Warning("! Root UUID mismatch: Backup is from a different machine (System info preserved)")
		logger.Debug("  Details: Current=%s, Backup=%s", res.RootDeviceCurrent, res.RootDeviceBackup)
	}

	// Swap Status
	if !res.SwapComparable {
		logger.Info("Swap: non determinabile (entry mancante in current/backup fstab)")
	} else if res.SwapMatch {
		logger.Info("✓ Swap: compatibile")
	} else {
		logger.Warning("! Swap mismatch: keeping current swap configuration")
		logger.Debug("  Details: Current=%s, Backup=%s", res.SwapDeviceCurrent, res.SwapDeviceBackup)
	}

	// New Entries
	if len(res.ProposedMounts) > 0 {
		logger.Info("+ %d mount(s) sicuri trovati nel backup ma non nel sistema attuale:", len(res.ProposedMounts))
		for _, m := range res.ProposedMounts {
			logger.Info("  %s -> %s (%s)", m.Device, m.MountPoint, m.Type)
		}
	} else {
		logger.Info("✓ Nessun mount aggiuntivo trovato nel backup.")
	}

	if len(res.SkippedMounts) > 0 {
		logger.Warning("! %d mount(s) trovati ma NON proposti automaticamente (potenzialmente rischiosi):", len(res.SkippedMounts))
		for _, m := range res.SkippedMounts {
			logger.Warning("  %s -> %s (%s)", m.Device, m.MountPoint, m.Type)
		}
		logger.Info("  Suggerimento: verifica dischi/UUID e opzioni (nofail/_netdev) prima di aggiungerli a /etc/fstab.")
	}
	fmt.Println()
}

func applyFstabMerge(ctx context.Context, logger *logging.Logger, currentRaw []string, targetPath string, newEntries []FstabEntry, dryRun bool) error {
	if dryRun {
		logger.Info("DRY RUN: would merge %d fstab entry(ies) into %s", len(newEntries), targetPath)
		for _, e := range newEntries {
			logger.Info("  + %s -> %s (%s)", e.Device, e.MountPoint, e.Type)
		}
		return nil
	}

	logger.Info("Applying fstab changes...")

	// 1. Backup
	backupPath := targetPath + fmt.Sprintf(".bak-%s", nowRestore().Format("20060102-150405"))
	if err := copyFileSimple(targetPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup fstab: %w", err)
	}
	logger.Info("  Original fstab backed up to: %s", backupPath)

	// 2. Construct New Content
	var buffer bytes.Buffer
	for _, line := range currentRaw {
		buffer.WriteString(line + "\n")
	}

	buffer.WriteString("\n# --- ProxSave Restore Merge ---\n")
	for _, e := range newEntries {
		if e.RawLine != "" {
			buffer.WriteString(e.RawLine + "\n")
		} else {
			line := fmt.Sprintf("%-36s %-20s %-8s %-16s %s %s", e.Device, e.MountPoint, e.Type, e.Options, e.Dump, e.Pass)
			buffer.WriteString(line + "\n")
		}
	}

	// 3. Atomic write (temp file + rename)
	perm := os.FileMode(0o644)
	if st, err := restoreFS.Stat(targetPath); err == nil {
		perm = st.Mode().Perm()
	}
	dir := filepath.Dir(targetPath)
	tmpPath := filepath.Join(dir, fmt.Sprintf(".%s.proxsave-tmp-%s", filepath.Base(targetPath), nowRestore().Format("20060102-150405")))

	tmpFile, err := restoreFS.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("failed to open temp fstab file: %w", err)
	}
	if _, err := tmpFile.Write(buffer.Bytes()); err != nil {
		_ = tmpFile.Close()
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("failed to write temp fstab: %w", err)
	}
	_ = tmpFile.Sync()
	if err := tmpFile.Close(); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("failed to close temp fstab: %w", err)
	}
	if err := restoreFS.Rename(tmpPath, targetPath); err != nil {
		_ = restoreFS.Remove(tmpPath)
		return fmt.Errorf("failed to replace fstab: %w", err)
	}

	// 4. Reload systemd daemon (best-effort)
	if _, err := restoreCmd.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		logger.Debug("systemctl daemon-reload failed/skipped: %v", err)
	}

	logger.Info("Size: %d bytes written.", buffer.Len())
	return nil
}

func copyFileSimple(src, dst string) error {
	data, err := restoreFS.ReadFile(src)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if st, err := restoreFS.Stat(src); err == nil {
		perm = st.Mode().Perm()
	}
	return restoreFS.WriteFile(dst, data, perm)
}
