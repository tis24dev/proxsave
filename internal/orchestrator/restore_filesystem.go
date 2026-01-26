package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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

	backupRoot := fstabBackupRootFromPath(backupFstabPath)
	if backupRoot != "" {
		if remapped, count := remapFstabDevicesFromInventory(logger, backupEntries, backupRoot); count > 0 {
			backupEntries = remapped
			logger.Info("Fstab device remap: converted %d entry(ies) from /dev/* to stable UUID/PARTUUID/LABEL based on ProxSave inventory", count)
		} else {
			backupEntries = remapped
		}
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
	confirmMsg := "Do you want to add the missing mounts (NFS/CIFS and data mounts with verified UUID/LABEL)?"
	confirmed, err := promptYesNoWithCountdown(ctx, reader, logger, confirmMsg, 90*time.Second, defaultYes)
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

type fstabDeviceIdentity struct {
	UUID     string
	PartUUID string
	Label    string
}

type pbsDatastoreInventoryLite struct {
	Commands map[string]struct {
		Output string `json:"output"`
	} `json:"commands"`
}

type lsblkReport struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	UUID     string       `json:"uuid"`
	PartUUID string       `json:"partuuid"`
	Label    string       `json:"label"`
	Children []lsblkDevice `json:"children"`
}

func fstabBackupRootFromPath(backupFstabPath string) string {
	p := filepath.Clean(strings.TrimSpace(backupFstabPath))
	if p == "" || p == "." || p == string(os.PathSeparator) {
		return ""
	}
	return filepath.Dir(filepath.Dir(p))
}

func remapFstabDevicesFromInventory(logger *logging.Logger, entries []FstabEntry, backupRoot string) ([]FstabEntry, int) {
	inventory := loadFstabDeviceInventory(logger, backupRoot)
	if len(inventory) == 0 || len(entries) == 0 {
		return entries, 0
	}

	out := make([]FstabEntry, len(entries))
	copy(out, entries)

	remapped := 0
	for i := range out {
		device := strings.TrimSpace(out[i].Device)
		if !isLikelyUnstableDevicePath(device) {
			continue
		}

		id, ok := inventory[filepath.Clean(device)]
		if !ok {
			continue
		}

		for _, candidate := range []struct {
			prefix string
			value  string
		}{
			{prefix: "UUID=", value: id.UUID},
			{prefix: "PARTUUID=", value: id.PartUUID},
			{prefix: "LABEL=", value: id.Label},
		} {
			if strings.TrimSpace(candidate.value) == "" {
				continue
			}
			newRef := candidate.prefix + strings.TrimSpace(candidate.value)
			if isVerifiedStableDeviceRef(newRef) {
				if logger != nil {
					logger.Debug("[FSTAB_MERGE] Remap device %s -> %s", device, newRef)
				}
				out[i].Device = newRef
				out[i].RawLine = ""
				remapped++
				break
			}
		}
	}

	return out, remapped
}

func loadFstabDeviceInventory(logger *logging.Logger, backupRoot string) map[string]fstabDeviceIdentity {
	root := filepath.Clean(strings.TrimSpace(backupRoot))
	if root == "" || root == "." {
		return nil
	}

	out := make(map[string]fstabDeviceIdentity)

	merge := func(src map[string]fstabDeviceIdentity) {
		for dev, id := range src {
			dev = filepath.Clean(strings.TrimSpace(dev))
			if dev == "" || dev == "." {
				continue
			}
			existing := out[dev]
			if existing.UUID == "" {
				existing.UUID = id.UUID
			}
			if existing.PartUUID == "" {
				existing.PartUUID = id.PartUUID
			}
			if existing.Label == "" {
				existing.Label = id.Label
			}
			out[dev] = existing
		}
	}

	// Prefer structured lsblk JSON if available.
	if data, err := restoreFS.ReadFile(filepath.Join(root, "var/lib/proxsave-info/commands/system/lsblk_json.json")); err == nil && len(data) > 0 {
		merge(parseLsblkJSONInventory(string(data)))
	}
	// Then blkid output from system collection.
	if data, err := restoreFS.ReadFile(filepath.Join(root, "var/lib/proxsave-info/commands/system/blkid.txt")); err == nil && len(data) > 0 {
		merge(parseBlkidInventory(string(data)))
	}
	// Fallback for older PBS backups: datastore inventory embeds blkid/lsblk output.
	if data, err := restoreFS.ReadFile(filepath.Join(root, "var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json")); err == nil && len(data) > 0 {
		merge(parsePBSDatastoreInventoryForDevices(logger, string(data)))
	}
	// Last resort: plain-text lsblk -f (less reliable, but may still provide UUID/LABEL).
	if data, err := restoreFS.ReadFile(filepath.Join(root, "var/lib/proxsave-info/commands/system/lsblk.txt")); err == nil && len(data) > 0 {
		merge(parseLsblkTextInventory(string(data)))
	}

	return out
}

func parsePBSDatastoreInventoryForDevices(logger *logging.Logger, content string) map[string]fstabDeviceIdentity {
	out := make(map[string]fstabDeviceIdentity)
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return out
	}

	var report pbsDatastoreInventoryLite
	if err := json.Unmarshal([]byte(trimmed), &report); err != nil {
		if logger != nil {
			logger.Debug("[FSTAB_MERGE] Unable to parse pbs_datastore_inventory.json: %v", err)
		}
		return out
	}
	if report.Commands == nil {
		return out
	}

	if blkid := strings.TrimSpace(report.Commands["blkid"].Output); blkid != "" {
		for dev, id := range parseBlkidInventory(blkid) {
			out[dev] = id
		}
	}
	if lsblk := strings.TrimSpace(report.Commands["lsblk_json"].Output); lsblk != "" {
		for dev, id := range parseLsblkJSONInventory(lsblk) {
			existing := out[dev]
			if existing.UUID == "" {
				existing.UUID = id.UUID
			}
			if existing.PartUUID == "" {
				existing.PartUUID = id.PartUUID
			}
			if existing.Label == "" {
				existing.Label = id.Label
			}
			out[dev] = existing
		}
	}

	return out
}

func parseLsblkJSONInventory(content string) map[string]fstabDeviceIdentity {
	out := make(map[string]fstabDeviceIdentity)
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return out
	}

	var report lsblkReport
	if err := json.Unmarshal([]byte(trimmed), &report); err != nil {
		return out
	}

	var walk func(dev lsblkDevice)
	walk = func(dev lsblkDevice) {
		path := strings.TrimSpace(dev.Path)
		if path == "" && strings.TrimSpace(dev.Name) != "" {
			path = filepath.Join("/dev", strings.TrimSpace(dev.Name))
		}
		path = filepath.Clean(path)
		if path != "" && path != "." {
			out[path] = fstabDeviceIdentity{
				UUID:     strings.TrimSpace(dev.UUID),
				PartUUID: strings.TrimSpace(dev.PartUUID),
				Label:    strings.TrimSpace(dev.Label),
			}
		}
		for _, child := range dev.Children {
			walk(child)
		}
	}

	for _, dev := range report.BlockDevices {
		walk(dev)
	}

	return out
}

var blkidKVRe = regexp.MustCompile(`([A-Za-z0-9_]+)=\"([^\"]*)\"`)

func parseBlkidInventory(content string) map[string]fstabDeviceIdentity {
	out := make(map[string]fstabDeviceIdentity)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Example: /dev/sdb1: UUID="..." TYPE="ext4" PARTUUID="..."
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}

		device := strings.TrimSpace(line[:colon])
		rest := strings.TrimSpace(line[colon+1:])
		if device == "" || rest == "" {
			continue
		}

		id := fstabDeviceIdentity{}
		for _, match := range blkidKVRe.FindAllStringSubmatch(rest, -1) {
			if len(match) != 3 {
				continue
			}
			key := strings.ToUpper(strings.TrimSpace(match[1]))
			val := strings.TrimSpace(match[2])
			switch key {
			case "UUID":
				id.UUID = val
			case "PARTUUID":
				id.PartUUID = val
			case "LABEL":
				id.Label = val
			}
		}

		if id.UUID == "" && id.PartUUID == "" && id.Label == "" {
			continue
		}

		out[filepath.Clean(device)] = id
	}
	return out
}

func parseLsblkTextInventory(content string) map[string]fstabDeviceIdentity {
	out := make(map[string]fstabDeviceIdentity)
	lines := strings.Split(content, "\n")
	headerIdx := -1
	var headerFields []string
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		headerFields = strings.Fields(line)
		if len(headerFields) >= 2 && strings.EqualFold(headerFields[0], "NAME") {
			headerIdx = i
			break
		}
	}
	if headerIdx == -1 || len(headerFields) == 0 {
		return out
	}

	uuidCol := -1
	labelCol := -1
	for i, field := range headerFields {
		switch strings.ToUpper(strings.TrimSpace(field)) {
		case "UUID":
			uuidCol = i
		case "LABEL":
			labelCol = i
		}
	}
	if uuidCol == -1 && labelCol == -1 {
		return out
	}

	for _, raw := range lines[headerIdx+1:] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}

		name := sanitizeLsblkName(fields[0])
		if name == "" {
			continue
		}
		path := filepath.Join("/dev", name)

		id := fstabDeviceIdentity{}
		if uuidCol >= 0 && uuidCol < len(fields) {
			id.UUID = strings.TrimSpace(fields[uuidCol])
		}
		if labelCol >= 0 && labelCol < len(fields) {
			id.Label = strings.TrimSpace(fields[labelCol])
		}
		if id.UUID == "" && id.Label == "" {
			continue
		}
		out[filepath.Clean(path)] = id
	}

	return out
}

func sanitizeLsblkName(field string) string {
	s := strings.TrimSpace(field)
	if s == "" {
		return ""
	}

	// Drop any tree prefix runes (├─, └─, │, etc.) by finding the first ASCII alnum.
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return s[i:]
		}
	}
	return ""
}

func isLikelyUnstableDevicePath(device string) bool {
	dev := strings.TrimSpace(device)
	if !strings.HasPrefix(dev, "/dev/") {
		return false
	}
	if strings.HasPrefix(dev, "/dev/disk/by-") || strings.HasPrefix(dev, "/dev/mapper/") {
		return false
	}

	base := filepath.Base(dev)
	switch {
	case strings.HasPrefix(base, "sd"),
		strings.HasPrefix(base, "vd"),
		strings.HasPrefix(base, "xvd"),
		strings.HasPrefix(base, "hd"),
		strings.HasPrefix(base, "nvme"),
		strings.HasPrefix(base, "mmcblk"):
		return true
	default:
		return false
	}
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

func normalizeFstabEntryForRestore(e FstabEntry) FstabEntry {
	e.Options = normalizeFstabOptionsForRestore(e.Options)
	if isNetworkMountEntry(e) {
		e.Options = ensureFstabOption(e.Options, "_netdev")
	}
	e.Options = ensureFstabOption(e.Options, "nofail")

	if strings.TrimSpace(e.Dump) == "" {
		e.Dump = "0"
	}
	if strings.TrimSpace(e.Pass) == "" {
		e.Pass = "0"
	}
	return e
}

func normalizeFstabOptionsForRestore(options string) string {
	opts := strings.TrimSpace(options)
	if opts == "" {
		return "defaults"
	}
	return opts
}

func ensureFstabOption(options, option string) string {
	opts := strings.TrimSpace(options)
	opt := strings.TrimSpace(option)
	if opt == "" {
		return opts
	}
	if opts == "" {
		return opt
	}

	for _, part := range strings.Split(opts, ",") {
		if strings.TrimSpace(part) == opt {
			return opts
		}
	}
	return opts + "," + opt
}

func printFstabAnalysis(logger *logging.Logger, res FstabAnalysisResult) {
	fmt.Println()
	logger.Info("fstab analysis:")

	// Root Status
	if !res.RootComparable {
		logger.Warning("! Root filesystem: undetermined (missing entry in current/backup fstab)")
	} else if res.RootMatch {
		logger.Info("✓ Root filesystem: compatible (UUID kept from system)")
	} else {
		// ANSI Yellow/Red might be nice, but stick to standard logger for now.
		logger.Warning("! Root UUID mismatch: Backup is from a different machine (System info preserved)")
		logger.Debug("  Details: Current=%s, Backup=%s", res.RootDeviceCurrent, res.RootDeviceBackup)
	}

	// Swap Status
	if !res.SwapComparable {
		logger.Info("Swap: undetermined (missing entry in current/backup fstab)")
	} else if res.SwapMatch {
		logger.Info("✓ Swap: compatible")
	} else {
		logger.Warning("! Swap mismatch: keeping current swap configuration")
		logger.Debug("  Details: Current=%s, Backup=%s", res.SwapDeviceCurrent, res.SwapDeviceBackup)
	}

	// New Entries
	if len(res.ProposedMounts) > 0 {
		logger.Info("+ %d safe mount(s) found in the backup but missing from the current system:", len(res.ProposedMounts))
		for _, m := range res.ProposedMounts {
			logger.Info("  %s -> %s (%s)", m.Device, m.MountPoint, m.Type)
		}
	} else {
		logger.Info("✓ No additional mounts found in the backup.")
	}

	if len(res.SkippedMounts) > 0 {
		logger.Warning("! %d mount(s) found but NOT auto-proposed (potentially risky):", len(res.SkippedMounts))
		for _, m := range res.SkippedMounts {
			logger.Warning("  %s -> %s (%s)", m.Device, m.MountPoint, m.Type)
		}
		logger.Info("  Hint: verify disks/UUIDs and options (nofail/_netdev) before adding them to /etc/fstab.")
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
		e = normalizeFstabEntryForRestore(e)
		line := fmt.Sprintf("%-36s %-20s %-8s %-16s %s %s", e.Device, e.MountPoint, e.Type, e.Options, e.Dump, e.Pass)
		buffer.WriteString(line + "\n")
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
