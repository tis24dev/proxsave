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
		if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
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
		fmt.Printf("Current node: %s\n", currentNode)
		fmt.Println("Select which exported node to import VM/CT configs from (they will be applied to the current node):")
		for idx, node := range exportNodes {
			qemuCount, lxcCount := countVMConfigsForNode(exportRoot, node)
			fmt.Printf("  [%d] %s (qemu=%d, lxc=%d)\n", idx+1, node, qemuCount, lxcCount)
		}
		fmt.Println("  [0] Skip VM/CT apply")

		fmt.Print("Choice: ")
		line, err := input.ReadLineWithContext(ctx, reader)
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
	for _, vm := range entries {
		if err := ctx.Err(); err != nil {
			logger.Warning("VM apply aborted: %v", err)
			return applied, failed
		}
		target := fmt.Sprintf("/nodes/%s/%s/%s/config", detectNodeForVM(), vm.Kind, vm.VMID)
		args := []string{"set", target, "--filename", vm.Path}
		if err := runPvesh(ctx, logger, args); err != nil {
			logger.Warning("Failed to apply %s (vmid=%s kind=%s): %v", target, vm.VMID, vm.Kind, err)
			failed++
		} else {
			display := vm.VMID
			if vm.Name != "" {
				display = fmt.Sprintf("%s (%s)", vm.VMID, vm.Name)
			}
			logger.Info("Applied VM/CT config %s", display)
			applied++
		}
	}
	return applied, failed
}

func detectNodeForVM() string {
	host, _ := os.Hostname()
	host = shortHost(host)
	if host != "" {
		return host
	}
	return "localhost"
}

type storageBlock struct {
	ID   string
	data []string
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
		tmp, tmpErr := restoreFS.CreateTemp("", fmt.Sprintf("pve-storage-%s-*.cfg", sanitizeID(blk.ID)))
		if tmpErr != nil {
			failed++
			continue
		}
		tmpName := tmp.Name()
		if _, werr := tmp.WriteString(strings.Join(blk.data, "\n") + "\n"); werr != nil {
			_ = tmp.Close()
			_ = restoreFS.Remove(tmpName)
			failed++
			continue
		}
		_ = tmp.Close()

		args := []string{"set", fmt.Sprintf("/cluster/storage/%s", blk.ID), "-conf", tmpName}
		if runErr := runPvesh(ctx, logger, args); runErr != nil {
			logger.Warning("Failed to apply storage %s: %v", blk.ID, runErr)
			failed++
		} else {
			logger.Info("Applied storage definition %s", blk.ID)
			applied++
		}

		_ = restoreFS.Remove(tmpName)

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
		_, name, ok := parseSectionHeader(trimmed)
		if ok {
			flush()
			current = &storageBlock{ID: name, data: []string{line}}
			continue
		}
		if current != nil {
			current.data = append(current.data, line)
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
		choiceLine, err := input.ReadLineWithContext(ctx, reader)
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
