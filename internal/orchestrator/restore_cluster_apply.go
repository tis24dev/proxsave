// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/components"
)

// runSafeClusterApply applies selected cluster configs via pvesh without touching config.db.
// It operates on files extracted to exportRoot (e.g. exportDestRoot).
func runSafeClusterApply(ctx context.Context, reader *bufio.Reader, exportRoot string, logger *logging.Logger) (err error) {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	ui := newCLIWorkflowUI(reader, logger)
	return runSafeClusterApplyWithUI(ctx, ui, exportRoot, logger, nil)
}

type vmEntry struct {
	VMID string
	Kind string // qemu | lxc
	Name string
	Path string
}

func scanVMConfigs(exportRoot, node string) ([]vmEntry, error) {
	var entries []vmEntry
	base := filepath.Join(exportRoot, "etc/pve/nodes", node)

	type dirSpec struct {
		kind string
		path string
	}

	dirs := []dirSpec{
		{kind: "qemu", path: filepath.Join(base, "qemu-server")},
		{kind: "lxc", path: filepath.Join(base, "lxc")},
	}

	for _, spec := range dirs {
		infos, err := restoreFS.ReadDir(spec.path)
		if err != nil {
			continue
		}
		for _, entry := range infos {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".conf") {
				continue
			}
			vmid := strings.TrimSuffix(name, ".conf")
			vmPath := filepath.Join(spec.path, name)
			vmName := readVMName(vmPath)
			entries = append(entries, vmEntry{
				VMID: vmid,
				Kind: spec.kind,
				Name: vmName,
				Path: vmPath,
			})
		}
	}

	return entries, nil
}

func listExportNodeDirs(exportRoot string) ([]string, error) {
	nodesRoot := filepath.Join(exportRoot, "etc/pve/nodes")
	entries, err := restoreFS.ReadDir(nodesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var nodes []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		nodes = append(nodes, name)
	}
	sort.Strings(nodes)
	return nodes, nil
}

func countVMConfigsForNode(exportRoot, node string) (qemuCount, lxcCount int) {
	base := filepath.Join(exportRoot, "etc/pve/nodes", node)

	countInDir := func(dir string) int {
		entries, err := restoreFS.ReadDir(dir)
		if err != nil {
			return 0
		}
		n := 0
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.HasSuffix(entry.Name(), ".conf") {
				n++
			}
		}
		return n
	}

	qemuCount = countInDir(filepath.Join(base, "qemu-server"))
	lxcCount = countInDir(filepath.Join(base, "lxc"))
	return qemuCount, lxcCount
}

func promptExportNodeSelection(ctx context.Context, reader *bufio.Reader, exportRoot, currentNode string, exportNodes []string) (string, error) {
	for {
		fmt.Println()
		fmt.Printf("WARNING: VM/CT configs in this backup are stored under different node names.\n")
		// Sanitize backup-derived node names before printing (terminal-escape injection guard).
		fmt.Printf("Current node: %s\n", components.SanitizeLine(currentNode))
		fmt.Println("Select which exported node to import VM/CT configs from (they will be applied to the current node):")
		for idx, node := range exportNodes {
			qemuCount, lxcCount := countVMConfigsForNode(exportRoot, node)
			fmt.Printf("  [%d] %s (qemu=%d, lxc=%d)\n", idx+1, components.SanitizeLine(node), qemuCount, lxcCount)
		}
		fmt.Println("  [0] Skip VM/CT apply")

		fmt.Print("Choice: ")
		line, err := input.ReadLineWithIdle(ctx, reader, cliIdleTimeout)
		if err != nil {
			return "", err
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "0" {
			return "", nil
		}
		if trimmed == "" {
			continue
		}
		idx, err := parseMenuIndex(trimmed, len(exportNodes))
		if err != nil {
			fmt.Println(err)
			continue
		}
		return exportNodes[idx], nil
	}
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func readVMName(confPath string) string {
	data, err := restoreFS.ReadFile(confPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "name:"))
		}
		if strings.HasPrefix(t, "hostname:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "hostname:"))
		}
	}
	return ""
}

func applyVMConfigs(ctx context.Context, entries []vmEntry, logger *logging.Logger) (applied, failed int) {
	node := localNodeName()
	for _, vm := range entries {
		if err := ctx.Err(); err != nil {
			logger.Warning("VM apply aborted: %v", err)
			return applied, failed
		}
		target := fmt.Sprintf("/nodes/%s/%s/%s/config", node, vm.Kind, vm.VMID)
		configArgs, err := pveshArgsFromColonConfigFile(vm.Path)
		if err != nil {
			logger.Warning("Failed to read %s (vmid=%s kind=%s): %v", vm.Path, vm.VMID, vm.Kind, err)
			failed++
			continue
		}

		exists, err := pveshGuestExists(ctx, logger, target)
		if err != nil {
			logger.Warning("Failed to check existing VM/CT config %s (vmid=%s kind=%s): %v", target, vm.VMID, vm.Kind, err)
			failed++
			continue
		}
		display := vm.VMID
		if vm.Name != "" {
			display = fmt.Sprintf("%s (%s)", vm.VMID, vm.Name)
		}

		if !exists {
			// pvesh create is a dead end here: ostemplate is create-time-only and
			// never persists into a CT conf, so an LXC could NEVER be created this
			// way (fable-check bug 3), and for qemu the conf itself is the
			// registration. Writing the staged conf into pmxcfs IS how a guest
			// comes into existence on PVE; its disks are not part of a config
			// restore and the log says so.
			if err := writeGuestConfToPmxcfs(logger, node, vm); err != nil {
				logger.Warning("Failed to register VM/CT config %s (vmid=%s kind=%s): %v", target, vm.VMID, vm.Kind, err)
				failed++
				continue
			}
			logger.Info("Registered VM/CT config %s via pmxcfs (config only: disks are not part of a config restore)", display)
			applied++
			continue
		}

		// Existing guest: the API first, minus the create-only keys the update
		// schema refuses (`meta` on qemu, `ostemplate` on lxc - one rejected key
		// fails the WHOLE set, which is how no modern guest ever got its config
		// applied, fable-check bug 2). The staged conf file is the fidelity net,
		// guarded off a RUNNING guest: a config race with a live guest is the one
		// case where the file must not win (maintainer's call, 2026-09-02).
		args := append([]string{"set", target}, filterGuestCreateOnlyArgs(configArgs)...)
		if err := runPvesh(ctx, logger, args); err != nil {
			if running := pveshGuestRunning(ctx, logger, node, vm); running {
				logger.Warning("Failed to apply %s (vmid=%s kind=%s) and the guest is running; skipping the file fallback (config race with a live guest): %v", target, vm.VMID, vm.Kind, err)
				failed++
				continue
			}
			if wErr := writeGuestConfToPmxcfs(logger, node, vm); wErr != nil {
				logger.Warning("Failed to apply %s (vmid=%s kind=%s): %v (pmxcfs fallback: %v)", target, vm.VMID, vm.Kind, err, wErr)
				failed++
				continue
			}
			logger.Info("Applied VM/CT config %s via pmxcfs after the API refused: %v", display, err)
			applied++
			continue
		}
		logger.Info("Applied VM/CT config %s", display)
		applied++
	}
	return applied, failed
}

// guestConfDir maps a guest kind to its pmxcfs config directory.
func guestConfDir(kind string) string {
	if kind == "qemu" {
		return "qemu-server"
	}
	return kind
}

// writeGuestConfToPmxcfs writes the staged conf byte-for-byte to
// /etc/pve/nodes/<node>/<kind-dir>/<vmid>.conf.
func writeGuestConfToPmxcfs(logger *logging.Logger, node string, vm vmEntry) error {
	data, err := restoreFS.ReadFile(vm.Path)
	if err != nil {
		return fmt.Errorf("read staged conf %s: %w", vm.Path, err)
	}
	rel := filepath.Join("nodes", node, guestConfDir(vm.Kind), vm.VMID+".conf")
	return pmxcfsWriteFile(logger, rel, data)
}

// filterGuestCreateOnlyArgs drops the keys the live update schemas refuse
// (probed 2026-09-02 on PVE 9.1.9): --meta is absent from the qemu set schema
// and --ostemplate from the lxc one; either fails the whole set.
func filterGuestCreateOnlyArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "--meta=") || strings.HasPrefix(arg, "--ostemplate=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// pveshGuestRunning reports whether status/current answers "running". Any
// error or unparsable answer counts as NOT running: in restore context the
// guard exists only to avoid racing a demonstrably live guest.
func pveshGuestRunning(ctx context.Context, logger *logging.Logger, node string, vm vmEntry) bool {
	statusPath := fmt.Sprintf("/nodes/%s/%s/%s/status/current", node, vm.Kind, vm.VMID)
	out, err := restoreCmd.Run(ctx, "pvesh", "get", statusPath, "--output-format=json")
	if err != nil {
		logger.Debug("guest status probe %s failed (treated as not running): %v", statusPath, err)
		return false
	}
	return strings.Contains(string(out), `"status":"running"`)
}

func localNodeName() string {
	host, _ := os.Hostname()
	host = shortHost(host)
	if host != "" {
		return host
	}
	return "localhost"
}

func pveshGuestExists(ctx context.Context, logger *logging.Logger, target string) (bool, error) {
	// The reason for a missing guest lands on pvesh's OUTPUT ("Configuration
	// file '...' does not exist", measured live on PVE 9.1.9); the Go error is a
	// bare "exit status 2". Matching the error alone never fired on a real node,
	// so a missing guest read as a failed check instead of as absent - which is
	// why the not-found match must read the output too.
	out, err := restoreCmd.Run(ctx, "pvesh", "get", target)
	if len(out) > 0 {
		logger.Debug("pvesh [get %s] output: %s", target, strings.TrimSpace(string(out)))
	}
	if err == nil {
		return true, nil
	}
	if isPveshNotFoundError(err) || isPveshNotFoundText(string(out)) {
		return false, nil
	}
	return false, fmt.Errorf("pvesh [get %s] failed: %w", target, err)
}

// isPveshNotFoundText matches the not-found reasons pvesh prints on its output.
func isPveshNotFoundText(out string) bool {
	lower := strings.ToLower(out)
	for _, marker := range []string{"does not exist", "not found", "no such"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isPveshNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"not found", "does not exist", "no such", "unable to find", "404"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

type storageBlock struct {
	ID      string
	Type    string
	entries []proxmoxNotificationEntry
}

func pveshArgsFromColonConfigFile(path string) ([]string, error) {
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pveshArgsFromColonConfigLines(strings.Split(string(data), "\n")), nil
}

func pveshArgsFromColonConfigLines(lines []string) []string {
	args := make([]string, 0, len(lines)*2)
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			break
		}
		key, value, ok := parseColonConfigLine(line)
		if !ok {
			continue
		}
		args = append(args, fmt.Sprintf("--%s=%s", key, value))
	}
	return args
}

func pveshArgsFromProxmoxEntries(entries []proxmoxNotificationEntry) []string {
	args := make([]string, 0, len(entries)*2)
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		value := strings.TrimSpace(entry.Value)
		if key == "" || value == "" {
			continue
		}
		args = append(args, fmt.Sprintf("--%s=%s", key, value))
	}
	return args
}

// pveshSetStorageDroppingCreateOnly runs `pvesh set /storage/<id>` and, on the
// live "Unknown option: <key>" refusal (create-only keys: `path` on a dir
// storage, measured on PVE 9.1.9; server/export on nfs are the same family),
// drops the named key and retries. The set schema varies by storage type and
// PVE version, so the refusal itself is the authoritative list - a hardcoded
// whitelist would drift.
func pveshSetStorageDroppingCreateOnly(ctx context.Context, logger *logging.Logger, id string, args []string) error {
	for attempt := 0; attempt <= len(args); attempt++ {
		full := append([]string{"set", "/storage/" + id}, args...)
		out, err := restoreCmd.Run(ctx, "pvesh", full...)
		if len(out) > 0 {
			logger.Debug("pvesh %v output: %s", full, strings.TrimSpace(string(out)))
		}
		if err == nil {
			return nil
		}
		key := pveshUnknownOptionFrom(string(out))
		if key == "" {
			return fmt.Errorf("pvesh %v failed: %w", full, err)
		}
		trimmed := args[:0:0]
		for _, arg := range args {
			if strings.HasPrefix(arg, "--"+key+"=") {
				logger.Debug("storage %s: dropping create-only key --%s from the set fallback", id, key)
				continue
			}
			trimmed = append(trimmed, arg)
		}
		if len(trimmed) == len(args) {
			return fmt.Errorf("pvesh %v failed: %w", full, err)
		}
		if len(trimmed) == 0 {
			// Nothing left to update: the definition already matches on every
			// settable key, which is a success, not a failure.
			return nil
		}
		args = trimmed
	}
	return fmt.Errorf("storage %s: the set fallback did not converge", id)
}

// pveshUnknownOptionFrom extracts the key from pvesh's "Unknown option: <key>" output.
func pveshUnknownOptionFrom(out string) string {
	const marker = "Unknown option: "
	idx := strings.Index(out, marker)
	if idx < 0 {
		return ""
	}
	rest := out[idx+len(marker):]
	if end := strings.IndexAny(rest, " \t\r\n"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func storageBlockPveshArgs(block storageBlock) ([]string, bool) {
	storageType := strings.TrimSpace(block.Type)
	if storageType == "" {
		storageType = storageEntryValue(block.entries, "type")
	}
	if storageType == "" {
		return nil, false
	}

	args := []string{
		fmt.Sprintf("--storage=%s", block.ID),
		fmt.Sprintf("--type=%s", storageType),
	}
	for _, entry := range block.entries {
		if strings.EqualFold(strings.TrimSpace(entry.Key), "type") {
			continue
		}
		args = append(args, pveshArgsFromProxmoxEntries([]proxmoxNotificationEntry{entry})...)
	}
	return args, true
}

func storageEntryValue(entries []proxmoxNotificationEntry, want string) string {
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.Key), want) {
			return strings.TrimSpace(entry.Value)
		}
	}
	return ""
}

func parseColonConfigLine(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(trimmed[:idx])
	value = strings.TrimSpace(trimmed[idx+1:])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func applyStorageCfg(ctx context.Context, cfgPath string, logger *logging.Logger) (applied, failed int, err error) {
	blocks, perr := parseStorageBlocks(cfgPath)
	if perr != nil {
		return 0, 0, perr
	}
	if len(blocks) == 0 {
		logger.Info("No storage definitions detected in storage.cfg")
		return 0, 0, nil
	}

	for _, blk := range blocks {
		createArgs, ok := storageBlockPveshArgs(blk)
		if !ok {
			logger.Warning("Skipping storage %s: storage type missing", blk.ID)
			failed++
			continue
		}
		args := append([]string{"create", "/storage"}, createArgs...)

		if runErr := runPvesh(ctx, logger, args); runErr != nil {
			// Fallback: the definition already exists (`dir: local` does on every
			// node, probed live 2026-09-02: "storage ID 'local' already defined"),
			// so update it - the same create-then-set shape the backup-jobs apply
			// has always had. --storage and --type are create-only and stay out.
			setArgs := make([]string, 0, len(createArgs))
			for _, arg := range createArgs {
				if strings.HasPrefix(arg, "--storage=") || strings.HasPrefix(arg, "--type=") {
					continue
				}
				setArgs = append(setArgs, arg)
			}
			if setErr := pveshSetStorageDroppingCreateOnly(ctx, logger, blk.ID, setArgs); setErr != nil {
				logger.Warning("Failed to apply storage %s: %v (create: %v)", blk.ID, setErr, runErr)
				failed++
			} else {
				logger.Info("Updated existing storage definition %s", blk.ID)
				applied++
			}
		} else {
			logger.Info("Applied storage definition %s", blk.ID)
			applied++
		}

		if err := ctx.Err(); err != nil {
			return applied, failed, err
		}
	}

	return applied, failed, nil
}

func parseStorageBlocks(cfgPath string) ([]storageBlock, error) {
	data, err := restoreFS.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}

	var blocks []storageBlock
	var current *storageBlock

	flush := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}

		// storage.cfg blocks use `<type>: <id>` (e.g. `dir: local`, `nfs: backup`).
		// Older exports may still use `storage: <id>` blocks.
		typ, name, ok := parseSectionHeader(trimmed)
		if ok {
			flush()
			storageType := ""
			if !strings.EqualFold(typ, "storage") {
				storageType = typ
			}
			current = &storageBlock{ID: name, Type: storageType}
			continue
		}
		if current != nil {
			key, value := parseProxmoxNotificationKV(trimmed)
			if strings.TrimSpace(key) == "" {
				continue
			}
			current.entries = append(current.entries, proxmoxNotificationEntry{Key: key, Value: value})
		}
	}
	flush()

	return blocks, nil
}

func runPvesh(ctx context.Context, logger *logging.Logger, args []string) error {
	output, err := restoreCmd.Run(ctx, "pvesh", args...)
	if len(output) > 0 {
		logger.Debug("pvesh %v output: %s", args, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return fmt.Errorf("pvesh %v failed: %w", args, err)
	}
	return nil
}

func shortHost(host string) string {
	if idx := strings.Index(host, "."); idx > 0 {
		return host[:idx]
	}
	return host
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if isSafeIDRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func isSafeIDRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
}

// promptClusterRestoreMode asks how to handle cluster database restore (safe export vs full recovery).
func promptClusterRestoreMode(ctx context.Context, reader *bufio.Reader) (int, error) {
	fmt.Println()
	fmt.Println("Cluster backup detected. Choose how to restore the cluster database:")
	fmt.Println("  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).")
	fmt.Println("  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.")
	fmt.Println("  [0] Exit")

	for {
		fmt.Print("Choice: ")
		choiceLine, err := input.ReadLineWithIdle(ctx, reader, cliIdleTimeout)
		if err != nil {
			return 0, err
		}
		switch strings.TrimSpace(choiceLine) {
		case "1":
			return 1, nil
		case "2":
			return 2, nil
		case "0":
			return 0, nil
		default:
			fmt.Println("Please enter 1, 2, or 0.")
		}
	}
}
