// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type safeClusterApplyUIFlow struct {
	ctx           context.Context
	ui            RestoreWorkflowUI
	exportRoot    string
	logger        *logging.Logger
	plan          *RestorePlan
	currentNode   string
	sourceNode    string
	pools         []pvePoolSpec
	applyPools    bool
	allowPoolMove bool
}

func runSafeClusterApplyWithUI(ctx context.Context, ui RestoreWorkflowUI, exportRoot string, logger *logging.Logger, plan *RestorePlan) (err error) {
	done := logging.DebugStart(logger, "safe cluster apply (ui)", "export_root=%s", exportRoot)
	defer func() { done(err) }()

	flow := &safeClusterApplyUIFlow{
		ctx:        ctx,
		ui:         ui,
		exportRoot: exportRoot,
		logger:     logger,
		plan:       plan,
	}
	err = flow.run()
	if errors.Is(err, errSafeClusterApplySkipped) {
		return nil
	}
	return err
}

func (f *safeClusterApplyUIFlow) run() error {
	if err := f.validate(); err != nil {
		return err
	}
	f.detectCurrentNode()
	f.logger.Info("")
	f.logger.Info("SAFE cluster restore: applying configs via pvesh (node=%s)", f.currentNode)
	f.applyResourceMappings()
	if err := f.preparePools(); err != nil {
		return err
	}
	if err := f.selectVMSourceNode(); err != nil {
		return err
	}
	if err := f.applyVMConfigsFromExport(); err != nil {
		return err
	}
	if err := f.applyStorageAndDatacenter(); err != nil {
		return err
	}
	f.applyPoolMembership()
	return nil
}

func (f *safeClusterApplyUIFlow) validate() error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	if f.ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	pveshPath, err := exec.LookPath("pvesh")
	if err != nil {
		f.logger.Warning("pvesh not found in PATH; skipping SAFE cluster apply")
		return errSafeClusterApplySkipped
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "pvesh=%s", pveshPath)
	return nil
}

var errSafeClusterApplySkipped = fmt.Errorf("safe cluster apply skipped")

func (f *safeClusterApplyUIFlow) detectCurrentNode() {
	currentNode, _ := os.Hostname()
	f.currentNode = shortHost(currentNode)
	if strings.TrimSpace(f.currentNode) == "" {
		f.currentNode = "localhost"
	}
	f.sourceNode = f.currentNode
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "current_node=%s", f.currentNode)
}

func (f *safeClusterApplyUIFlow) applyResourceMappings() {
	if err := maybeApplyPVEClusterResourceMappingsWithUI(f.ctx, f.ui, f.logger, f.exportRoot); err != nil {
		f.logger.Warning("SAFE apply: resource mappings: %v", err)
	}
}

func (f *safeClusterApplyUIFlow) preparePools() error {
	if err := f.loadPools(); err != nil {
		return nil
	}
	if len(f.pools) == 0 {
		return nil
	}
	if err := f.confirmPoolDefinitions(); err != nil {
		return err
	}
	if f.applyPools {
		return f.applyPoolDefinitions()
	}
	return nil
}

func (f *safeClusterApplyUIFlow) loadPools() error {
	pools, err := readPVEPoolsFromExportUserCfg(f.exportRoot)
	if err != nil {
		f.logger.Warning("SAFE apply: failed to parse pools from export: %v", err)
		f.pools = nil
		return err
	}
	f.pools = pools
	return nil
}

func (f *safeClusterApplyUIFlow) confirmPoolDefinitions() error {
	poolNames := summarizePoolIDs(f.pools, 10)
	message := fmt.Sprintf("Found %d pool(s) in exported user.cfg.\n\nPools: %s\n\nApply pool definitions now? (Membership will be applied later in this SAFE apply flow.)", len(f.pools), poolNames)
	ok, err := f.ui.ConfirmAction(f.ctx, "Apply PVE resource pools (merge)", message, "Apply now", "Skip apply", 0, false)
	if err != nil {
		return err
	}
	f.applyPools = ok
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "User choice: apply_pools=%v (pools=%d)", f.applyPools, len(f.pools))
	return nil
}

func (f *safeClusterApplyUIFlow) applyPoolDefinitions() error {
	if anyPoolHasVMs(f.pools) {
		if err := f.confirmAllowPoolMove(); err != nil {
			return err
		}
	}
	applied, failed, err := applyPVEPoolsDefinitions(f.ctx, f.logger, f.pools)
	if err != nil {
		f.logger.Warning("Pools apply (definitions) encountered errors: %v", err)
	}
	f.logger.Info("Pools apply (definitions) completed: ok=%d failed=%d", applied, failed)
	return nil
}

func (f *safeClusterApplyUIFlow) confirmAllowPoolMove() error {
	moveMsg := "Allow moving guests from other pools to match the backup? This may change the current pool assignment of existing VMs/CTs."
	move, err := f.ui.ConfirmAction(f.ctx, "Pools: allow move (VM/CT)", moveMsg, "Allow move", "Don't move", 0, false)
	if err != nil {
		return err
	}
	f.allowPoolMove = move
	return nil
}

func (f *safeClusterApplyUIFlow) selectVMSourceNode() error {
	exportNodes, err := f.listExportNodes()
	if err != nil || len(exportNodes) == 0 || stringSliceContains(exportNodes, f.sourceNode) {
		return nil
	}
	return f.resolveSourceNodeMismatch(exportNodes)
}

func (f *safeClusterApplyUIFlow) listExportNodes() ([]string, error) {
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "List exported node directories under %s", filepath.Join(f.exportRoot, "etc/pve/nodes"))
	exportNodes, err := listExportNodeDirs(f.exportRoot)
	if err != nil {
		f.logger.Warning("Failed to inspect exported node directories: %v", err)
		return nil, err
	}
	if len(exportNodes) > 0 {
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "export_nodes=%s", strings.Join(exportNodes, ","))
	} else {
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "No exported node directories found")
	}
	return exportNodes, nil
}

func (f *safeClusterApplyUIFlow) resolveSourceNodeMismatch(exportNodes []string) error {
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "Node mismatch: current_node=%s export_nodes=%s", f.currentNode, strings.Join(exportNodes, ","))
	f.logger.Warning("SAFE cluster restore: VM/CT configs not found for current node %s in export; available nodes: %s", f.currentNode, strings.Join(exportNodes, ", "))
	if len(exportNodes) == 1 {
		f.sourceNode = exportNodes[0]
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "Auto-select source node: %s", f.sourceNode)
		f.logger.Info("SAFE cluster restore: using exported node %s as VM/CT source, applying to current node %s", f.sourceNode, f.currentNode)
		return nil
	}
	return f.promptSourceNode(exportNodes)
}

func (f *safeClusterApplyUIFlow) promptSourceNode(exportNodes []string) error {
	f.logExportNodeCandidates(exportNodes)
	selected, err := f.ui.SelectExportNode(f.ctx, f.exportRoot, f.currentNode, exportNodes)
	if err != nil {
		return err
	}
	if strings.TrimSpace(selected) == "" {
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "User selected: skip VM/CT apply (no source node)")
		f.logger.Info("Skipping VM/CT apply (no source node selected)")
		f.sourceNode = ""
		return nil
	}
	f.sourceNode = selected
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "User selected source node: %s", f.sourceNode)
	f.logger.Info("SAFE cluster restore: selected exported node %s as VM/CT source, applying to current node %s", f.sourceNode, f.currentNode)
	return nil
}

func (f *safeClusterApplyUIFlow) logExportNodeCandidates(exportNodes []string) {
	for _, node := range exportNodes {
		qemuCount, lxcCount := countVMConfigsForNode(f.exportRoot, node)
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "Export node candidate: %s (qemu=%d, lxc=%d)", node, qemuCount, lxcCount)
	}
}

func (f *safeClusterApplyUIFlow) applyVMConfigsFromExport() error {
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "Selected VM/CT source node: %q (current_node=%q)", f.sourceNode, f.currentNode)
	vmEntries := f.scanVMConfigs()
	if len(vmEntries) == 0 {
		f.logNoVMConfigs()
		return nil
	}
	applyVMs, err := f.ui.ConfirmApplyVMConfigs(f.ctx, f.sourceNode, f.currentNode, len(vmEntries))
	if err != nil {
		return err
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "User choice: apply_vms=%v (entries=%d)", applyVMs, len(vmEntries))
	if applyVMs {
		applied, failed := applyVMConfigs(f.ctx, vmEntries, f.logger)
		f.logger.Info("VM/CT apply completed: ok=%d failed=%d", applied, failed)
	} else {
		f.logger.Info("Skipping VM/CT apply")
	}
	return nil
}

func (f *safeClusterApplyUIFlow) scanVMConfigs() []vmEntry {
	if strings.TrimSpace(f.sourceNode) == "" {
		return nil
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "Scan VM/CT configs in export (source_node=%s)", f.sourceNode)
	vmEntries, err := scanVMConfigs(f.exportRoot, f.sourceNode)
	if err != nil {
		f.logger.Warning("Failed to scan VM configs: %v", err)
		return nil
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "VM/CT configs found=%d (source_node=%s)", len(vmEntries), f.sourceNode)
	return vmEntries
}

func (f *safeClusterApplyUIFlow) logNoVMConfigs() {
	if strings.TrimSpace(f.sourceNode) == "" {
		f.logger.Info("No VM/CT configs applied (no source node selected)")
		return
	}
	f.logger.Info("No VM/CT configs found for node %s in export", f.sourceNode)
}

func (f *safeClusterApplyUIFlow) applyStorageAndDatacenter() error {
	if f.plan != nil && f.plan.HasCategoryID("storage_pve") {
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "Skip storage/datacenter apply: handled by storage_pve staged restore")
		f.logger.Info("Skipping storage/datacenter apply (handled by storage_pve staged restore)")
		return nil
	}
	if err := f.maybeApplyStorageCfg(); err != nil {
		return err
	}
	return f.maybeApplyDatacenterCfg()
}

func (f *safeClusterApplyUIFlow) maybeApplyStorageCfg() error {
	storageCfg := filepath.Join(f.exportRoot, "etc/pve/storage.cfg")
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "Check export: storage.cfg (%s)", storageCfg)
	info, err := restoreFS.Stat(storageCfg)
	if err != nil || info.IsDir() {
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "storage.cfg not found (err=%v)", err)
		f.logger.Info("No storage.cfg found in export")
		return nil
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "storage.cfg found (size=%d)", info.Size())
	applyStorage, err := f.ui.ConfirmApplyStorageCfg(f.ctx, storageCfg)
	if err != nil {
		return err
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "User choice: apply_storage=%v", applyStorage)
	if applyStorage {
		applied, failed, applyErr := applyStorageCfg(f.ctx, storageCfg, f.logger)
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "Storage apply result: ok=%d failed=%d err=%v", applied, failed, applyErr)
		if applyErr != nil {
			f.logger.Warning("Storage apply encountered errors: %v", applyErr)
		}
		f.logger.Info("Storage apply completed: ok=%d failed=%d", applied, failed)
	} else {
		f.logger.Info("Skipping storage.cfg apply")
	}
	return nil
}

func (f *safeClusterApplyUIFlow) maybeApplyDatacenterCfg() error {
	dcCfg := filepath.Join(f.exportRoot, "etc/pve/datacenter.cfg")
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "Check export: datacenter.cfg (%s)", dcCfg)
	info, err := restoreFS.Stat(dcCfg)
	if err != nil || info.IsDir() {
		logging.DebugStep(f.logger, "safe cluster apply (ui)", "datacenter.cfg not found (err=%v)", err)
		f.logger.Info("No datacenter.cfg found in export")
		return nil
	}
	return f.confirmAndApplyDatacenterCfg(dcCfg, info.Size())
}

func (f *safeClusterApplyUIFlow) confirmAndApplyDatacenterCfg(dcCfg string, size int64) error {
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "datacenter.cfg found (size=%d)", size)
	applyDC, err := f.ui.ConfirmApplyDatacenterCfg(f.ctx, dcCfg)
	if err != nil {
		return err
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "User choice: apply_datacenter=%v", applyDC)
	if !applyDC {
		f.logger.Info("Skipping datacenter.cfg apply")
		return nil
	}
	logging.DebugStep(f.logger, "safe cluster apply (ui)", "Apply datacenter.cfg via pvesh")
	if err := runPvesh(f.ctx, f.logger, []string{"set", "/cluster/config", "-conf", dcCfg}); err != nil {
		f.logger.Warning("Failed to apply datacenter.cfg: %v", err)
	} else {
		f.logger.Info("datacenter.cfg applied successfully")
	}
	return nil
}

func (f *safeClusterApplyUIFlow) applyPoolMembership() {
	if !f.applyPools || len(f.pools) == 0 {
		return
	}
	applied, failed, err := applyPVEPoolsMembership(f.ctx, f.logger, f.pools, f.allowPoolMove)
	if err != nil {
		f.logger.Warning("Pools apply (membership) encountered errors: %v", err)
	}
	f.logger.Info("Pools apply (membership) completed: ok=%d failed=%d", applied, failed)
}
