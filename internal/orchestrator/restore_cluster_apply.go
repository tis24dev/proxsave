// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
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
	done := logging.DebugStart(logger, "pve guest configs apply", "entries=%d current_node=%s", len(entries), node)
	var traceErr error
	defer func() {
		logging.DebugStep(logger, "pve guest configs apply", "Result: ok=%d failed=%d", applied, failed)
		done(traceErr)
	}()
	if len(entries) == 0 {
		return 0, 0
	}
	if err := ctx.Err(); err != nil {
		traceErr = err
		logger.Warning("VM apply aborted: %v", err)
		return applied, failed
	}

	inventory, err := loadPVEGuestInventory(ctx)
	if err != nil {
		traceErr = err
		failed = len(entries)
		logger.Warning("Failed to load the cluster-wide VM/CT inventory; refusing to apply %d guest config(s): %v", len(entries), err)
		return applied, failed
	}
	logging.DebugStep(logger, "pve guest configs apply", "Loaded cluster-wide inventory: guests=%d", len(inventory))

	for _, vm := range entries {
		if err := ctx.Err(); err != nil {
			traceErr = err
			logger.Warning("VM apply aborted: %v", err)
			return applied, failed
		}
		target := fmt.Sprintf("/nodes/%s/%s/%s/config", node, vm.Kind, vm.VMID)
		display := vm.VMID
		if vm.Name != "" {
			display = fmt.Sprintf("%s (%s)", vm.VMID, vm.Name)
		}

		resource, exists := inventory[vm.VMID]
		if exists && resource.Node != node {
			logging.DebugStep(logger, "pve guest configs apply", "vmid=%s classification=remote owner_node=%s current_node=%s", vm.VMID, resource.Node, node)
			logger.Warning("Skipping VM/CT config %s: vmid=%s already belongs to remote node %s (current node: %s)", display, vm.VMID, resource.Node, node)
			failed++
			continue
		}
		if exists && resource.Kind != vm.Kind {
			logging.DebugStep(logger, "pve guest configs apply", "vmid=%s classification=type-mismatch inventory_kind=%s staged_kind=%s", vm.VMID, resource.Kind, vm.Kind)
			logger.Warning("Skipping VM/CT config %s: vmid=%s is %s in the cluster inventory but the staged config is %s", display, vm.VMID, resource.Kind, vm.Kind)
			failed++
			continue
		}

		if !exists {
			logging.DebugStep(logger, "pve guest configs apply", "vmid=%s classification=absent action=register-pmxcfs", vm.VMID)
			// pvesh create is a dead end here: ostemplate is create-time-only and
			// never persists into a CT conf, so an LXC could NEVER be created this
			// way (fable-check bug 3), and for qemu the conf itself is the
			// registration. Writing the staged conf into pmxcfs IS how a guest
			// comes into existence on PVE; the native guest locks below repeat
			// the cluster-wide absence check before doing so. Its disks are not
			// part of a config restore and the log says so.
			if err := writeGuestConfToPmxcfs(ctx, logger, node, vm, guestMustBeAbsent); err != nil {
				logger.Warning("Failed to register VM/CT config %s (vmid=%s kind=%s): %v", target, vm.VMID, vm.Kind, err)
				failed++
				continue
			}
			logger.Info("Registered VM/CT config %s via pmxcfs (config only: disks are not part of a config restore)", display)
			applied++
			continue
		}

		logging.DebugStep(logger, "pve guest configs apply", "vmid=%s classification=local kind=%s action=pvesh-set", vm.VMID, vm.Kind)
		configArgs, err := pveshArgsFromColonConfigFile(vm.Path)
		if err != nil {
			logger.Warning("Failed to read %s (vmid=%s kind=%s): %v", vm.Path, vm.VMID, vm.Kind, err)
			failed++
			continue
		}

		// Existing guest: the API first, minus the create-only keys the update
		// schema refuses (`meta` on qemu, `ostemplate` on lxc - one rejected key
		// fails the WHOLE set, which is how no modern guest ever got its config
		// applied, fable-check bug 2). The staged conf file is the fidelity net,
		// guarded off a RUNNING guest both before and inside PVE's native guest
		// locks: a config race with a live guest is the one case where the file
		// must not win (maintainer's call, 2026-09-02).
		args := append([]string{"set", target}, filterGuestCreateOnlyArgs(configArgs)...)
		if out, err := runPveshWithOutput(ctx, logger, args); err != nil {
			// The file fallback answers ONE failure: the update schema refused a key
			// filterGuestCreateOnlyArgs did not know to drop. Everything else - no
			// quorum, permission denied, a 500 from a payload the API accepted - is
			// the API failing to apply a config it understood, and overwriting the
			// conf verbatim is not a remedy for it: the API's rejection is the only
			// validation the staged bytes ever get, so a fallback on every error
			// writes an invalid conf into pmxcfs cluster-wide. This narrows a
			// deliberate "fails for any reason" decision on the maintainer's
			// instruction (2026-09-05).
			if !pveshSchemaRefusal(err, out) {
				logging.DebugStep(logger, "pve guest configs apply", "vmid=%s classification=api-failure action=refuse-file-fallback", vm.VMID)
				logger.Warning("Failed to apply %s (vmid=%s kind=%s): %v - the API refused the operation, not the payload, so the staged conf file is not a remedy", target, vm.VMID, vm.Kind, err)
				failed++
				continue
			}
			status, statusErr := pveshGuestStatus(ctx, node, vm)
			if statusErr != nil {
				logger.Warning("Failed to apply %s (vmid=%s kind=%s) and could not verify that the guest is stopped, so the staged conf file cannot be used: %v (status probe: %v)", target, vm.VMID, vm.Kind, err, statusErr)
				if applyGuestConfigDroppingRefusedKeys(ctx, logger, target, display, vm.VMID, configArgs) {
					applied++
				} else {
					failed++
				}
				continue
			}
			if status != "stopped" {
				logger.Warning("Failed to apply %s (vmid=%s kind=%s) and guest status is %q, not explicitly stopped, so the staged conf file cannot be used: %v", target, vm.VMID, vm.Kind, status, err)
				if applyGuestConfigDroppingRefusedKeys(ctx, logger, target, display, vm.VMID, configArgs) {
					applied++
				} else {
					failed++
				}
				continue
			}
			if wErr := writeGuestConfToPmxcfs(ctx, logger, node, vm, guestMustBeStopped); wErr != nil {
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

// writeGuestConfToPmxcfs applies the staged conf byte-for-byte while PVE's
// native guest locks protect the required cluster and runtime precondition.
func writeGuestConfToPmxcfs(ctx context.Context, logger *logging.Logger, node string, vm vmEntry, precondition guestApplyPrecondition) error {
	data, err := restoreFS.ReadFile(vm.Path)
	if err != nil {
		return fmt.Errorf("read staged conf %s: %w", vm.Path, err)
	}
	return pveGuestLockedWriter(ctx, logger, node, vm, precondition, data)
}

// applyGuestConfigDroppingRefusedKeys is the arm for a guest the staged conf file
// cannot be written for - it is running, or its state could not be established -
// after the update schema refused a key.
//
// Failing outright throws away every key the API WOULD have accepted, and for a
// running guest that is the entire config apply: one create-only key the prefilter
// does not know about used to cost the whole config. Dropping the refused key and
// retrying costs that key alone. The dropped names are logged at WARNING because
// their staged values are NOT applied - a partial apply reported as a plain
// success would be the same silence this whole arm exists to remove.
func applyGuestConfigDroppingRefusedKeys(ctx context.Context, logger *logging.Logger, target, display, vmid string, configArgs []string) bool {
	changed, dropped, err := pveshSetDroppingRefusedKeys(ctx, logger, "vmid "+vmid, target, filterGuestCreateOnlyArgs(configArgs))
	if err != nil {
		logger.Warning("Failed to apply VM/CT config %s through the API without the refused keys: %v", display, err)
		return false
	}
	if !changed {
		logger.Warning("Applied nothing for VM/CT config %s: the update schema refuses every staged key (%s)", display, strings.Join(dropped, ", "))
		return false
	}
	if len(dropped) > 0 {
		logger.Warning("Applied VM/CT config %s through the API without %s: the update schema refuses those keys, and the guest is not confirmed stopped so the staged conf file could not supply them", display, strings.Join(dropped, ", "))
		return true
	}
	logger.Info("Applied VM/CT config %s", display)
	return true
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

// pveshGuestStatus returns the explicit status/current state. Callers must
// positively observe "stopped" before a direct pmxcfs fallback; a command or
// parse error is never equivalent to a stopped guest.
func pveshGuestStatus(ctx context.Context, node string, vm vmEntry) (string, error) {
	statusPath := fmt.Sprintf("/nodes/%s/%s/%s/status/current", node, vm.Kind, vm.VMID)
	out, err := runCommandStdout(ctx, "pvesh", "get", statusPath, "--output-format=json")
	if err != nil {
		return "", fmt.Errorf("pvesh get %s: %w", statusPath, err)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", fmt.Errorf("parse guest status %s: %w", statusPath, err)
	}
	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if status == "" {
		return "", fmt.Errorf("guest status %s is empty", statusPath)
	}
	return status, nil
}

func localNodeName() string {
	host, _ := os.Hostname()
	host = shortHost(host)
	if host != "" {
		return host
	}
	return "localhost"
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
//
// changed reports whether a set actually reached the node. It is false in the two
// shapes where there was nothing to send: a staged block whose only keys are the
// create-only header ones (--storage/--type, stripped by the caller), and a block
// whose every remaining key came back refused. Both mean the existing definition
// already matches everything this restore could change, which is a success - but
// not an update, and the caller must not announce one.
func pveshSetStorageDroppingCreateOnly(ctx context.Context, logger *logging.Logger, id string, args []string) (changed bool, err error) {
	changed, _, err = pveshSetDroppingRefusedKeys(ctx, logger, "storage "+id, "/storage/"+id, args)
	return changed, err
}

// pveshSetDroppingRefusedKeys runs `pvesh set <path>` and, on a refusal that NAMES
// a key, drops that key and retries. It is shared by the storage and guest arms so
// there is ONE implementation of "the refusal is the authoritative list": the set
// schema varies by endpoint, storage type and PVE version, and a hardcoded list
// drifts silently against every one of them.
//
// changed reports whether a set actually reached the node; dropped names the keys
// that were refused, because their staged values were NOT applied and a caller
// that stays silent about that loses the fact. An empty args list is nothing to do,
// not a failure: `pvesh set <path>` with no option is refused by the live node, and
// reporting that blamed the restore for a definition that already matched.
func pveshSetDroppingRefusedKeys(ctx context.Context, logger *logging.Logger, subject, path string, args []string) (changed bool, dropped []string, err error) {
	if len(args) == 0 {
		logger.Debug("%s: no settable key to send", subject)
		return false, nil, nil
	}
	// The bound is taken ONCE, before the loop. Reading len(args) in the condition
	// measured the shrinking slice, so the attempts ran out before convergence as
	// soon as more than half the keys were refused - `nfs: id / server / export /
	// content` is enough (3 settable keys, 2 refused: 2 attempts allowed, 3 needed).
	// The result was "did not converge" in place of pvesh's real error, on a
	// definition the retry would have applied.
	for attempt, total := 0, len(args); attempt <= total; attempt++ {
		full := append([]string{"set", path}, args...)
		out, runErr := restoreCmd.Run(ctx, "pvesh", full...)
		if len(out) > 0 {
			logger.Debug("pvesh %v output: %s", full, strings.TrimSpace(string(out)))
		}
		if runErr == nil {
			return true, dropped, nil
		}
		key := pveshRefusedKeyFrom(runErr, out)
		if key == "" {
			return false, dropped, fmt.Errorf("pvesh %v failed: %w", full, runErr)
		}
		trimmed := args[:0:0]
		for _, arg := range args {
			if strings.HasPrefix(arg, "--"+key+"=") {
				logger.Debug("%s: dropping key --%s refused by the set schema", subject, key)
				continue
			}
			trimmed = append(trimmed, arg)
		}
		if len(trimmed) == len(args) {
			return false, dropped, fmt.Errorf("pvesh %v failed: %w", full, runErr)
		}
		dropped = append(dropped, key)
		if len(trimmed) == 0 {
			logger.Debug("%s: every staged key is refused by the set schema; nothing left to update", subject)
			return false, dropped, nil
		}
		args = trimmed
	}
	return false, dropped, fmt.Errorf("%s: the set fallback did not converge", subject)
}

// pveshRefusedKeyFrom names the key a refusal is about, across the two shapes
// measured on PVE 9.1.9 (2026-09-02): "Unknown option: <key>" on the storage
// endpoint, and "400 Parameter verification failed. <key>: property is not defined
// in schema ..." on the guest one.
//
// The second is only read when the message says the property is ABSENT from the
// schema. PVE opens value errors with the same sentence - "Parameter verification
// failed. cores: value does not match the regex pattern" - and dropping the key
// there would silently discard a staged value the operator asked to restore, which
// is worse than the failure it would be hiding.
func pveshRefusedKeyFrom(err error, out []byte) string {
	text := string(out)
	if err != nil {
		text += "\n" + err.Error()
	}
	if key := afterMarkerUpTo(text, "Unknown option: ", " \t\r\n"); key != "" {
		return key
	}
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "property is not defined in schema") &&
		!strings.Contains(lower, "does not allow additional properties") {
		return ""
	}
	return afterMarkerUpTo(text, "Parameter verification failed.", ":")
}

func afterMarkerUpTo(text, marker, stop string) string {
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(text[idx+len(marker):])
	if end := strings.IndexAny(rest, stop); end >= 0 {
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
		// Same rule as the jobs arm: a cancelled restore propagates instead of being
		// counted as failures. The caller turns failed>0 into "storage.cfg applied
		// with N failure(s)", which errors.Is cannot match against context.Canceled,
		// so an abort would be reported as storage definitions that failed to apply.
		// The counts so far are returned with it: they say what really landed.
		if cerr := ctx.Err(); cerr != nil {
			return applied, failed, cerr
		}
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
			changed, setErr := pveshSetStorageDroppingCreateOnly(ctx, logger, blk.ID, setArgs)
			switch {
			case setErr != nil:
				logger.Warning("Failed to apply storage %s: %v (create: %v)", blk.ID, setErr, runErr)
				failed++
			case changed:
				logger.Info("Updated existing storage definition %s", blk.ID)
				applied++
			default:
				// Nothing was sent, so saying "Updated" would claim a write that
				// never happened; the definition is nonetheless in the staged state.
				logger.Info("Storage definition %s already matches every settable key", blk.ID)
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
	_, err := runPveshWithOutput(ctx, logger, args)
	return err
}

// runPveshWithOutput is runPvesh for the callers that must CLASSIFY the failure,
// not just report it. The reason usually lands on the output with a bare exit
// status as the Go error (measured on PVE 9.1.9), so an error alone cannot tell
// a refused payload from a refused operation.
func runPveshWithOutput(ctx context.Context, logger *logging.Logger, args []string) ([]byte, error) {
	output, err := restoreCmd.Run(ctx, "pvesh", args...)
	if len(output) > 0 {
		logger.Debug("pvesh %v output: %s", args, strings.TrimSpace(string(output)))
	}
	if err != nil {
		return output, fmt.Errorf("pvesh %v failed: %w", args, err)
	}
	return output, nil
}

// pveshSchemaRefusal reports whether pvesh refused the PAYLOAD - a key the
// endpoint's schema does not accept - as opposed to failing to apply a payload it
// accepted. Measured shapes on PVE 9.1.9 (2026-09-02, recorded in
// diagnostics/design-staged-apply-pmxcfs-2026-09-02.md): "400 Parameter
// verification failed. meta: property is not defined in schema and the schema does
// not allow additional properties", and "Unknown option: <key>" alongside "400
// unable to parse option". Both err and out are searched because the reason lands
// on either one depending on the endpoint.
//
// "Parameter verification failed" alone is deliberately NOT a marker. PVE opens
// VALUE errors with the same sentence - "400 Parameter verification failed. cores:
// value does not match the regex pattern" - and that is the staged conf carrying a
// value the API rejects. The API's rejection is the only validation those bytes
// get, so treating a value error as a schema refusal would write exactly the conf
// PVE just refused straight into pmxcfs, cluster-wide. The key-absent phrasing is
// what the fallback answers, so the key-absent phrasing is what is matched.
func pveshSchemaRefusal(err error, out []byte) bool {
	text := strings.ToLower(string(out))
	if err != nil {
		text += "\n" + strings.ToLower(err.Error())
	}
	for _, marker := range []string{
		"property is not defined in schema",
		"does not allow additional properties",
		"unknown option:",
		"unable to parse option",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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
