// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

type fstabMergeUIPrompt struct {
	analysis      FstabAnalysisResult
	remappedCount int
	defaultYes    bool
}

// fstabMergeTimeout is the default time a UI has to confirm the Smart fstab merge.
const fstabMergeTimeout = 90 * time.Second

func smartMergeFstabWithUI(ctx context.Context, logger *logging.Logger, ui RestoreWorkflowUI, currentFstabPath, backupFstabPath string, dryRun bool) error {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	logger.Info("")
	logger.Step("Smart Filesystem Configuration Merge")
	logger.Debug("[FSTAB_MERGE] Starting analysis of %s vs backup %s...", currentFstabPath, backupFstabPath)

	currentRaw, prompt, err := prepareFstabMergePrompt(logger, currentFstabPath, backupFstabPath)
	if err != nil {
		return err
	}
	if len(prompt.analysis.ProposedMounts) == 0 {
		logger.Info("No new safe mounts found to restore. Keeping current fstab.")
		return nil
	}

	confirmed, err := ui.ConfirmFstabMerge(ctx, "Smart fstab merge", prompt.message(), fstabMergeTimeout, prompt.defaultYes)
	if err != nil {
		return err
	}
	if !confirmed {
		logger.Info("Fstab merge skipped by user.")
		return nil
	}
	return applyFstabMerge(ctx, logger, currentRaw, currentFstabPath, prompt.analysis.ProposedMounts, dryRun)
}

func prepareFstabMergePrompt(logger *logging.Logger, currentFstabPath, backupFstabPath string) ([]string, fstabMergeUIPrompt, error) {
	currentEntries, currentRaw, err := parseFstab(currentFstabPath)
	if err != nil {
		return nil, fstabMergeUIPrompt{}, fmt.Errorf("failed to parse current fstab: %w", err)
	}
	backupEntries, _, err := parseFstab(backupFstabPath)
	if err != nil {
		return nil, fstabMergeUIPrompt{}, fmt.Errorf("failed to parse backup fstab: %w", err)
	}

	backupEntries, remappedCount := remapBackupFstabEntries(logger, backupEntries, backupFstabPath)
	analysis := analyzeFstabMerge(logger, currentEntries, backupEntries)
	prompt := fstabMergeUIPrompt{
		analysis:      analysis,
		remappedCount: remappedCount,
		defaultYes:    analysis.RootComparable && analysis.RootMatch && (!analysis.SwapComparable || analysis.SwapMatch),
	}
	return currentRaw, prompt, nil
}

func remapBackupFstabEntries(logger *logging.Logger, entries []FstabEntry, backupFstabPath string) ([]FstabEntry, int) {
	backupRoot := fstabBackupRootFromPath(backupFstabPath)
	if backupRoot == "" {
		return entries, 0
	}
	remapped, count := remapFstabDevicesFromInventory(logger, entries, backupRoot)
	if count > 0 {
		logger.Info("Fstab device remap: converted %d entry(ies) from /dev/* to stable UUID/PARTUUID/LABEL based on ProxSave inventory", count)
	}
	return remapped, count
}

func (p fstabMergeUIPrompt) message() string {
	var msg strings.Builder
	msg.WriteString("ProxSave found missing mounts in /etc/fstab.\n\n")
	p.writeWarnings(&msg)
	p.writeRemapSummary(&msg)
	p.writeProposedMounts(&msg)
	p.writeSkippedMounts(&msg)
	msg.WriteString("\nDo you want to add the missing mounts (NFS/CIFS and data mounts with verified UUID/LABEL)?")
	return msg.String()
}

func (p fstabMergeUIPrompt) writeWarnings(msg *strings.Builder) {
	if p.analysis.RootComparable && !p.analysis.RootMatch {
		msg.WriteString("⚠ Root UUID mismatch: the backup appears to come from a different machine.\n")
	}
	if p.analysis.SwapComparable && !p.analysis.SwapMatch {
		msg.WriteString("⚠ Swap mismatch: the current swap configuration will be kept.\n")
	}
}

func (p fstabMergeUIPrompt) writeRemapSummary(msg *strings.Builder) {
	if p.remappedCount > 0 {
		fmt.Fprintf(msg, "✓ Remapped %d fstab entry(ies) from /dev/* to stable UUID/PARTUUID/LABEL using ProxSave inventory.\n", p.remappedCount)
	}
}

func (p fstabMergeUIPrompt) writeProposedMounts(msg *strings.Builder) {
	msg.WriteString("\nProposed mounts (safe):\n")
	for _, mount := range p.analysis.ProposedMounts {
		fmt.Fprintf(msg, "  - %s -> %s (%s)\n", mount.Device, mount.MountPoint, mount.Type)
	}
}

func (p fstabMergeUIPrompt) writeSkippedMounts(msg *strings.Builder) {
	if len(p.analysis.SkippedMounts) == 0 {
		return
	}
	msg.WriteString("\nMounts found but not auto-proposed:\n")
	for _, mount := range p.analysis.SkippedMounts {
		fmt.Fprintf(msg, "  - %s -> %s (%s)\n", mount.Device, mount.MountPoint, mount.Type)
	}
	msg.WriteString("\nHint: verify disks/UUIDs and options (nofail/_netdev) before adding them.\n")
}
