package orchestrator

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

const maxArchiveInventoryBytes = 10 << 20 // 10 MiB

var nicRepairSequence uint64

type archivedNetworkInventory struct {
	GeneratedAt string                     `json:"generated_at,omitempty"`
	Hostname    string                     `json:"hostname,omitempty"`
	Interfaces  []archivedNetworkInterface `json:"interfaces"`
}

type archivedNetworkInterface struct {
	Name         string            `json:"name"`
	MAC          string            `json:"mac,omitempty"`
	PermanentMAC string            `json:"permanent_mac,omitempty"`
	PCIPath      string            `json:"pci_path,omitempty"`
	Driver       string            `json:"driver,omitempty"`
	IsVirtual    bool              `json:"is_virtual,omitempty"`
	UdevProps    map[string]string `json:"udev_properties,omitempty"`
}

type nicMappingMethod string

const (
	nicMatchPermanentMAC nicMappingMethod = "permanent_mac"
	nicMatchMAC          nicMappingMethod = "mac"
	nicMatchPCIPath      nicMappingMethod = "pci_path"
	nicMatchUdevIDSerial nicMappingMethod = "udev_id_serial"
	nicMatchUdevPCISlot  nicMappingMethod = "udev_pci_slot"
	nicMatchUdevIDPath   nicMappingMethod = "udev_id_path"
	nicMatchUdevNamePath nicMappingMethod = "udev_net_name_path"
	nicMatchUdevNameSlot nicMappingMethod = "udev_net_name_slot"
)

type nicMappingEntry struct {
	OldName    string
	NewName    string
	Method     nicMappingMethod
	Identifier string
}

type nicMappingResult struct {
	Entries          []nicMappingEntry
	BackupSourcePath string
}

func (r nicMappingResult) IsEmpty() bool {
	return len(r.Entries) == 0
}

func (r nicMappingResult) RenameMap() map[string]string {
	m := make(map[string]string, len(r.Entries))
	for _, e := range r.Entries {
		if e.OldName == "" || e.NewName == "" {
			continue
		}
		m[e.OldName] = e.NewName
	}
	return m
}

func (r nicMappingResult) Details() string {
	if len(r.Entries) == 0 {
		return "NIC mapping: none"
	}
	var b strings.Builder
	b.WriteString("NIC mapping (backup -> current):\n")
	entries := append([]nicMappingEntry(nil), r.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OldName < entries[j].OldName
	})
	for _, e := range entries {
		line := fmt.Sprintf("- %s -> %s (%s=%s)\n", e.OldName, e.NewName, e.Method, e.Identifier)
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

type nicNameConflict struct {
	Mapping  nicMappingEntry
	Existing archivedNetworkInterface
}

func (c nicNameConflict) Details() string {
	existingParts := []string{}
	if v := strings.TrimSpace(c.Existing.PermanentMAC); v != "" {
		existingParts = append(existingParts, "permMAC="+normalizeMAC(v))
	}
	if v := strings.TrimSpace(c.Existing.MAC); v != "" {
		existingParts = append(existingParts, "mac="+normalizeMAC(v))
	}
	if v := strings.TrimSpace(c.Existing.PCIPath); v != "" {
		existingParts = append(existingParts, "pci="+v)
	}
	existing := strings.Join(existingParts, " ")
	if existing == "" {
		existing = "no identifiers"
	}
	return fmt.Sprintf("- %s -> %s (%s=%s) but current %s exists (%s)",
		c.Mapping.OldName,
		c.Mapping.NewName,
		c.Mapping.Method,
		c.Mapping.Identifier,
		c.Mapping.OldName,
		existing,
	)
}

type nicRepairPlan struct {
	Mapping       nicMappingResult
	SafeMappings  []nicMappingEntry
	Conflicts     []nicNameConflict
	SkippedReason string
}

func (p nicRepairPlan) HasWork() bool {
	return len(p.SafeMappings) > 0 || len(p.Conflicts) > 0
}

type nicRepairResult struct {
	Mapping       nicMappingResult
	AppliedNICMap []nicMappingEntry
	ChangedFiles  []string
	BackupDir     string
	AppliedAt     time.Time
	SkippedReason string
}

func (r nicRepairResult) Applied() bool {
	return len(r.ChangedFiles) > 0
}

func (r nicRepairResult) Summary() string {
	if r.SkippedReason != "" {
		return fmt.Sprintf("NIC name repair skipped: %s", r.SkippedReason)
	}
	if len(r.ChangedFiles) == 0 {
		return "NIC name repair: no changes needed"
	}
	return fmt.Sprintf("NIC name repair applied: %d file(s) updated", len(r.ChangedFiles))
}

func (r nicRepairResult) Details() string {
	var b strings.Builder
	b.WriteString(r.Summary())
	if r.BackupDir != "" {
		b.WriteString(fmt.Sprintf("\nBackup of pre-repair files: %s", r.BackupDir))
	}
	if len(r.ChangedFiles) > 0 {
		b.WriteString("\nUpdated files:")
		for _, path := range r.ChangedFiles {
			b.WriteString("\n- " + path)
		}
	}
	if len(r.AppliedNICMap) > 0 {
		b.WriteString("\n\n")
		b.WriteString(nicMappingResult{Entries: r.AppliedNICMap}.Details())
	}
	return b.String()
}

func planNICNameRepair(ctx context.Context, archivePath string) (*nicRepairPlan, error) {
	plan := &nicRepairPlan{}
	if strings.TrimSpace(archivePath) == "" {
		plan.SkippedReason = "backup archive not available"
		return plan, nil
	}

	backupInv, source, err := loadBackupNetworkInventoryFromArchive(ctx, archivePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			plan.SkippedReason = "backup does not include network inventory (update ProxSave and create a new backup to enable NIC mapping)"
			return plan, nil
		}
		return nil, fmt.Errorf("read backup network inventory: %w", err)
	}

	currentInv, err := collectCurrentNetworkInventory(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect current network inventory: %w", err)
	}

	mapping := computeNICMapping(backupInv, currentInv)
	mapping.BackupSourcePath = source
	if mapping.IsEmpty() {
		plan.Mapping = mapping
		plan.SkippedReason = "no NIC rename mapping found (names already match or identifiers unavailable)"
		return plan, nil
	}

	currentByName := make(map[string]archivedNetworkInterface, len(currentInv.Interfaces))
	for _, iface := range currentInv.Interfaces {
		name := strings.TrimSpace(iface.Name)
		if name == "" {
			continue
		}
		currentByName[name] = iface
	}

	for _, e := range mapping.Entries {
		if e.OldName == "" || e.NewName == "" || e.OldName == e.NewName {
			continue
		}
		if existing, ok := currentByName[e.OldName]; ok {
			plan.Conflicts = append(plan.Conflicts, nicNameConflict{
				Mapping:  e,
				Existing: existing,
			})
		} else {
			plan.SafeMappings = append(plan.SafeMappings, e)
		}
	}
	plan.Mapping = mapping
	return plan, nil
}

func applyNICNameRepair(logger *logging.Logger, plan *nicRepairPlan, includeConflicts bool) (result *nicRepairResult, err error) {
	done := logging.DebugStart(logger, "NIC repair apply", "includeConflicts=%v", includeConflicts)
	defer func() { done(err) }()

	result = &nicRepairResult{
		AppliedAt: nowRestore(),
	}
	if plan == nil {
		logging.DebugStep(logger, "NIC repair apply", "Skipped: plan not available")
		result.SkippedReason = "NIC repair plan not available"
		return result, nil
	}
	result.Mapping = plan.Mapping
	logging.DebugStep(logger, "NIC repair apply", "Plan summary: mappingEntries=%d safe=%d conflicts=%d", len(plan.Mapping.Entries), len(plan.SafeMappings), len(plan.Conflicts))
	if plan.SkippedReason != "" && !plan.HasWork() {
		logging.DebugStep(logger, "NIC repair apply", "Skipped: %s", strings.TrimSpace(plan.SkippedReason))
		result.SkippedReason = plan.SkippedReason
		return result, nil
	}
	mappingsToApply := append([]nicMappingEntry{}, plan.SafeMappings...)
	if includeConflicts {
		for _, conflict := range plan.Conflicts {
			mappingsToApply = append(mappingsToApply, conflict.Mapping)
		}
	}
	if len(mappingsToApply) == 0 && len(plan.Conflicts) > 0 && !includeConflicts {
		logging.DebugStep(logger, "NIC repair apply", "Skipped: conflicts present and includeConflicts=false")
		result.SkippedReason = "conflicting NIC mappings detected; skipped by user"
		return result, nil
	}
	logging.DebugStep(logger, "NIC repair apply", "Selected mappings to apply: %d", len(mappingsToApply))
	renameMap := make(map[string]string, len(mappingsToApply))
	for _, mapping := range mappingsToApply {
		if mapping.OldName == "" || mapping.NewName == "" || mapping.OldName == mapping.NewName {
			continue
		}
		renameMap[mapping.OldName] = mapping.NewName
	}
	if len(renameMap) == 0 {
		if len(plan.Conflicts) > 0 && !includeConflicts {
			result.SkippedReason = "conflicting NIC mappings detected; skipped by user"
		} else {
			result.SkippedReason = "no NIC renames selected"
		}
		return result, nil
	}
	logging.DebugStep(logger, "NIC repair apply", "Rewrite ifupdown config files (renames=%d)", len(renameMap))

	changedFiles, backupDir, err := rewriteIfupdownConfigFiles(logger, renameMap)
	if err != nil {
		return nil, err
	}
	result.AppliedNICMap = mappingsToApply
	result.ChangedFiles = changedFiles
	result.BackupDir = backupDir
	if len(changedFiles) == 0 {
		result.SkippedReason = "no matching interface names found in /etc/network/interfaces*"
	}
	logging.DebugStep(logger, "NIC repair apply", "Result: changedFiles=%d backupDir=%s", len(changedFiles), backupDir)
	return result, nil
}

func loadBackupNetworkInventoryFromArchive(ctx context.Context, archivePath string) (*archivedNetworkInventory, string, error) {
	candidates := []string{
		"./var/lib/proxsave-info/commands/system/network_inventory.json",
		"./commands/network_inventory.json",
		"./var/lib/proxsave-info/network_inventory.json",
	}
	data, used, err := readArchiveEntry(ctx, archivePath, candidates, maxArchiveInventoryBytes)
	if err != nil {
		return nil, "", err
	}
	var inv archivedNetworkInventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, "", fmt.Errorf("parse network inventory json: %w", err)
	}
	return &inv, used, nil
}

func readArchiveEntry(ctx context.Context, archivePath string, candidates []string, maxBytes int64) ([]byte, string, error) {
	file, err := restoreFS.Open(archivePath)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	reader, err := createDecompressionReader(ctx, file, archivePath)
	if err != nil {
		return nil, "", err
	}
	if closer, ok := reader.(io.Closer); ok {
		defer closer.Close()
	}

	tr := tar.NewReader(reader)

	want := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		want[c] = struct{}{}
	}

	for {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		default:
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if hdr == nil {
			continue
		}
		if _, ok := want[hdr.Name]; !ok {
			continue
		}
		if hdr.FileInfo() == nil || !hdr.FileInfo().Mode().IsRegular() {
			return nil, "", fmt.Errorf("archive entry %s is not a regular file", hdr.Name)
		}

		limited := io.LimitReader(tr, maxBytes+1)
		data, err := io.ReadAll(limited)
		if err != nil {
			return nil, "", err
		}
		if int64(len(data)) > maxBytes {
			return nil, "", fmt.Errorf("archive entry %s too large (%d bytes)", hdr.Name, len(data))
		}
		return data, hdr.Name, nil
	}
	return nil, "", os.ErrNotExist
}

func collectCurrentNetworkInventory(ctx context.Context) (*archivedNetworkInventory, error) {
	sysNet := "/sys/class/net"
	entries, err := os.ReadDir(sysNet)
	if err != nil {
		return nil, err
	}

	inv := &archivedNetworkInventory{
		GeneratedAt: nowRestore().Format(time.RFC3339),
	}
	if host, err := os.Hostname(); err == nil {
		inv.Hostname = host
	}

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		netPath := filepath.Join(sysNet, name)

		profile := archivedNetworkInterface{
			Name: name,
			MAC:  readTrimmedLine(filepath.Join(netPath, "address"), 64),
		}
		profile.MAC = normalizeMAC(profile.MAC)

		if link, err := os.Readlink(netPath); err == nil && strings.Contains(link, "/virtual/") {
			profile.IsVirtual = true
		}
		if devPath, err := filepath.EvalSymlinks(filepath.Join(netPath, "device")); err == nil {
			profile.PCIPath = devPath
		}
		if driverPath, err := filepath.EvalSymlinks(filepath.Join(netPath, "device/driver")); err == nil {
			profile.Driver = filepath.Base(driverPath)
		}

		if commandAvailable("udevadm") {
			props, err := readUdevProperties(ctx, netPath)
			if err == nil && len(props) > 0 {
				profile.UdevProps = props
			}
		}

		if commandAvailable("ethtool") {
			perm, err := readPermanentMAC(ctx, name)
			if err == nil && perm != "" {
				profile.PermanentMAC = normalizeMAC(perm)
			}
		}

		inv.Interfaces = append(inv.Interfaces, profile)
	}

	sort.Slice(inv.Interfaces, func(i, j int) bool {
		return inv.Interfaces[i].Name < inv.Interfaces[j].Name
	})
	return inv, nil
}

func readPermanentMAC(ctx context.Context, iface string) (string, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := restoreCmd.Run(ctxTimeout, "ethtool", "-P", iface)
	if err != nil {
		return "", err
	}
	return parsePermanentMAC(string(out)), nil
}

func readUdevProperties(ctx context.Context, netPath string) (map[string]string, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "udevadm", "info", "-q", "property", "-p", netPath)
	if err != nil {
		return nil, err
	}
	props := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key != "" && val != "" {
			props[key] = val
		}
	}
	return props, nil
}

func parsePermanentMAC(output string) string {
	const prefix = "permanent address:"
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, prefix) {
			return strings.ToLower(strings.TrimSpace(line[len(prefix):]))
		}
	}
	return ""
}

func normalizeMAC(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.TrimPrefix(v, "mac:")
	return strings.TrimSpace(v)
}

func computeNICMapping(backupInv, currentInv *archivedNetworkInventory) nicMappingResult {
	result := nicMappingResult{}
	if backupInv == nil || currentInv == nil {
		return result
	}

	type matchIndex struct {
		Method    nicMappingMethod
		Extract   func(archivedNetworkInterface) string
		Normalize func(string) string
		Current   map[string]archivedNetworkInterface
		Dupes     map[string]struct{}
	}

	trim := func(v string) string {
		return strings.TrimSpace(v)
	}
	udevProp := func(key string) func(archivedNetworkInterface) string {
		return func(iface archivedNetworkInterface) string {
			if iface.UdevProps == nil {
				return ""
			}
			return iface.UdevProps[key]
		}
	}

	indices := []matchIndex{
		{
			Method:    nicMatchPermanentMAC,
			Extract:   func(iface archivedNetworkInterface) string { return iface.PermanentMAC },
			Normalize: normalizeMAC,
		},
		{
			Method:    nicMatchMAC,
			Extract:   func(iface archivedNetworkInterface) string { return iface.MAC },
			Normalize: normalizeMAC,
		},
		{
			Method:    nicMatchUdevIDSerial,
			Extract:   udevProp("ID_SERIAL"),
			Normalize: trim,
		},
		{
			Method:    nicMatchUdevPCISlot,
			Extract:   udevProp("ID_PCI_SLOT_NAME"),
			Normalize: trim,
		},
		{
			Method:    nicMatchUdevIDPath,
			Extract:   udevProp("ID_PATH"),
			Normalize: trim,
		},
		{
			Method:    nicMatchPCIPath,
			Extract:   func(iface archivedNetworkInterface) string { return iface.PCIPath },
			Normalize: trim,
		},
		{
			Method:    nicMatchUdevNamePath,
			Extract:   udevProp("ID_NET_NAME_PATH"),
			Normalize: trim,
		},
		{
			Method:    nicMatchUdevNameSlot,
			Extract:   udevProp("ID_NET_NAME_SLOT"),
			Normalize: trim,
		},
	}

	for i := range indices {
		indices[i].Current = make(map[string]archivedNetworkInterface)
		indices[i].Dupes = make(map[string]struct{})
	}

	for _, iface := range currentInv.Interfaces {
		if !isCandidatePhysicalNIC(iface) {
			continue
		}
		for i := range indices {
			key := indices[i].Normalize(indices[i].Extract(iface))
			if key == "" {
				continue
			}
			if prev, ok := indices[i].Current[key]; ok && prev.Name != iface.Name {
				indices[i].Dupes[key] = struct{}{}
			} else {
				indices[i].Current[key] = iface
			}
		}
	}

	usedCurrent := make(map[string]struct{})
	for _, iface := range backupInv.Interfaces {
		if !isCandidatePhysicalNIC(iface) {
			continue
		}

		oldName := strings.TrimSpace(iface.Name)
		if oldName == "" {
			continue
		}

		for i := range indices {
			key := indices[i].Normalize(indices[i].Extract(iface))
			if key == "" {
				continue
			}
			if _, dupe := indices[i].Dupes[key]; dupe {
				continue
			}
			match, ok := indices[i].Current[key]
			if !ok || strings.TrimSpace(match.Name) == "" {
				continue
			}
			if shouldAddMapping(oldName, match.Name, usedCurrent) {
				result.Entries = append(result.Entries, nicMappingEntry{
					OldName:    oldName,
					NewName:    match.Name,
					Method:     indices[i].Method,
					Identifier: key,
				})
				usedCurrent[match.Name] = struct{}{}
			}
			break
		}
	}

	return result
}

func isCandidatePhysicalNIC(iface archivedNetworkInterface) bool {
	name := strings.TrimSpace(iface.Name)
	if name == "" || name == "lo" {
		return false
	}
	if iface.IsVirtual {
		return false
	}
	if iface.PermanentMAC == "" && iface.MAC == "" && iface.PCIPath == "" && !hasStableUdevIdentifiers(iface.UdevProps) {
		return false
	}
	return true
}

func hasStableUdevIdentifiers(props map[string]string) bool {
	if len(props) == 0 {
		return false
	}
	keys := []string{
		"ID_SERIAL",
		"ID_PCI_SLOT_NAME",
		"ID_PATH",
		"ID_NET_NAME_PATH",
		"ID_NET_NAME_SLOT",
	}
	for _, k := range keys {
		if strings.TrimSpace(props[k]) != "" {
			return true
		}
	}
	return false
}

func shouldAddMapping(oldName, newName string, usedCurrent map[string]struct{}) bool {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" || oldName == newName {
		return false
	}
	if usedCurrent == nil {
		return true
	}
	if _, ok := usedCurrent[newName]; ok {
		return false
	}
	return true
}

func rewriteIfupdownConfigFiles(logger *logging.Logger, renameMap map[string]string) (updatedPaths []string, backupDir string, err error) {
	done := logging.DebugStart(logger, "NIC repair rewrite", "renames=%d", len(renameMap))
	defer func() { done(err) }()

	if len(renameMap) == 0 {
		return nil, "", nil
	}

	logging.DebugStep(logger, "NIC repair rewrite", "Collect ifupdown config files (/etc/network/interfaces, /etc/network/interfaces.d/*)")
	paths := []string{
		"/etc/network/interfaces",
	}

	if entries, err := restoreFS.ReadDir("/etc/network/interfaces.d"); err == nil {
		for _, entry := range entries {
			if entry == nil || entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" {
				continue
			}
			paths = append(paths, filepath.Join("/etc/network/interfaces.d", name))
		}
	} else {
		logging.DebugStep(logger, "NIC repair rewrite", "interfaces.d not readable; scanning only /etc/network/interfaces (error=%v)", err)
	}

	sort.Strings(paths)
	logging.DebugStep(logger, "NIC repair rewrite", "Scan %d file(s) for interface renames", len(paths))

	type fileSnapshot struct {
		Path string
		Mode os.FileMode
		Data []byte
	}
	var changed []fileSnapshot
	for _, p := range paths {
		info, err := restoreFS.Stat(p)
		if err != nil {
			logging.DebugStep(logger, "NIC repair rewrite", "Skip %s: stat failed: %v", p, err)
			continue
		}
		if info.Mode()&os.ModeType != 0 {
			logging.DebugStep(logger, "NIC repair rewrite", "Skip %s: not a regular file (mode=%s)", p, info.Mode())
			continue
		}
		data, err := restoreFS.ReadFile(p)
		if err != nil {
			logging.DebugStep(logger, "NIC repair rewrite", "Skip %s: read failed: %v", p, err)
			continue
		}

		updated, ok := applyInterfaceRenameMap(string(data), renameMap)
		if !ok {
			logging.DebugStep(logger, "NIC repair rewrite", "No changes needed in %s", p)
			continue
		}
		logging.DebugStep(logger, "NIC repair rewrite", "Will update %s", p)
		changed = append(changed, fileSnapshot{
			Path: p,
			Mode: info.Mode(),
			Data: []byte(updated),
		})
	}

	if len(changed) == 0 {
		logging.DebugStep(logger, "NIC repair rewrite", "No files require update")
		return nil, "", nil
	}

	baseDir := "/tmp/proxsave"
	logging.DebugStep(logger, "NIC repair rewrite", "Create backup directory under %s", baseDir)
	if err := restoreFS.MkdirAll(baseDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create nic repair base directory: %w", err)
	}

	seq := atomic.AddUint64(&nicRepairSequence, 1)
	backupDir = filepath.Join(baseDir, fmt.Sprintf("nic_repair_%s_%d", nowRestore().Format("20060102_150405"), seq))
	if err := restoreFS.MkdirAll(backupDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create nic repair backup directory: %w", err)
	}

	for _, snap := range changed {
		logging.DebugStep(logger, "NIC repair rewrite", "Backup original file: %s", snap.Path)
		orig, err := restoreFS.ReadFile(snap.Path)
		if err != nil {
			return nil, "", fmt.Errorf("read original %s for backup: %w", snap.Path, err)
		}
		backupPath := filepath.Join(backupDir, strings.TrimPrefix(filepath.Clean(snap.Path), string(filepath.Separator)))
		if err := restoreFS.MkdirAll(filepath.Dir(backupPath), 0o700); err != nil {
			return nil, "", fmt.Errorf("create backup directory for %s: %w", backupPath, err)
		}
		if err := restoreFS.WriteFile(backupPath, orig, 0o600); err != nil {
			return nil, "", fmt.Errorf("write backup file %s: %w", backupPath, err)
		}
	}

	for _, snap := range changed {
		logging.DebugStep(logger, "NIC repair rewrite", "Write updated file: %s", snap.Path)
		if err := restoreFS.WriteFile(snap.Path, snap.Data, snap.Mode); err != nil {
			return nil, "", fmt.Errorf("write updated file %s: %w", snap.Path, err)
		}
		updatedPaths = append(updatedPaths, snap.Path)
	}

	if logger != nil {
		logger.Info("NIC name repair updated %d file(s). Backup: %s", len(updatedPaths), backupDir)
		logger.Debug("NIC name repair mapping:\n%s", nicMappingResult{Entries: mapToEntries(renameMap)}.Details())
		logger.Debug("NIC name repair updated files: %s", strings.Join(updatedPaths, ", "))
	}

	return updatedPaths, backupDir, nil
}

func mapToEntries(renameMap map[string]string) []nicMappingEntry {
	if len(renameMap) == 0 {
		return nil
	}
	entries := make([]nicMappingEntry, 0, len(renameMap))
	for old, newName := range renameMap {
		entries = append(entries, nicMappingEntry{
			OldName: old,
			NewName: newName,
			Method:  "text_replace",
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OldName < entries[j].OldName
	})
	return entries
}

func applyInterfaceRenameMap(content string, renameMap map[string]string) (string, bool) {
	if content == "" || len(renameMap) == 0 {
		return content, false
	}
	updated := content
	changed := false
	keys := make([]string, 0, len(renameMap))
	for k := range renameMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, oldName := range keys {
		newName := renameMap[oldName]
		if oldName == "" || newName == "" || oldName == newName {
			continue
		}
		next, ok := replaceInterfaceToken(updated, oldName, newName)
		if ok {
			updated = next
			changed = true
		}
	}
	return updated, changed
}

func replaceInterfaceToken(input, oldName, newName string) (string, bool) {
	if input == "" || oldName == "" || oldName == newName {
		return input, false
	}
	var b strings.Builder
	b.Grow(len(input))
	changed := false

	i := 0
	for {
		idx := strings.Index(input[i:], oldName)
		if idx < 0 {
			b.WriteString(input[i:])
			break
		}
		idx += i

		if isTokenBoundary(input, idx, oldName) {
			b.WriteString(input[i:idx])
			b.WriteString(newName)
			i = idx + len(oldName)
			changed = true
			continue
		}

		b.WriteString(input[i : idx+1])
		i = idx + 1
	}

	if !changed {
		return input, false
	}
	return b.String(), true
}

func isTokenBoundary(text string, idx int, token string) bool {
	if idx < 0 || idx+len(token) > len(text) {
		return false
	}

	if idx > 0 {
		prev := text[idx-1]
		if isIfaceNameChar(prev) {
			return false
		}
	}

	end := idx + len(token)
	if end < len(text) {
		next := text[end]
		if isIfaceNameChar(next) {
			return false
		}
	}

	return true
}

func isIfaceNameChar(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z':
		return true
	case ch >= 'A' && ch <= 'Z':
		return true
	case ch >= '0' && ch <= '9':
		return true
	case ch == '_' || ch == '-':
		return true
	default:
		return false
	}
}

func readTrimmedLine(path string, max int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if max > 0 && len(line) > max {
		line = line[:max]
	}
	return line
}
