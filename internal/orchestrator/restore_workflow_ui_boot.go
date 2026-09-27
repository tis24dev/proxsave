// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// interceptBootCategory takes the boot category out of the system-path extraction:
// its archive path is the backed-up host's /proc/cmdline, which is read, not
// restored, and its two live paths are written only by the merge.
func (w *restoreUIWorkflowRun) interceptBootCategory() {
	if !w.plan.HasCategoryID("boot") {
		return
	}
	w.needsBootConfiguration = true
	w.plan.NormalCategories = categoriesWithoutID(w.plan.NormalCategories, "boot")
	logging.DebugStep(w.logger, "restore", "Boot category intercepted: kernel command line merge after the other categories")
}

// recordBootRebuildInput notes when the system-path extraction writes a file the
// initramfs is built from.
func (w *restoreUIWorkflowRun) recordBootRebuildInput(entryName string) {
	input, ok := bootRebuildInput(entryName)
	if !ok {
		return
	}
	for _, seen := range w.bootRebuildInputsWritten {
		if seen == input {
			return
		}
	}
	w.bootRebuildInputsWritten = append(w.bootRebuildInputsWritten, input)
}

// applyBootConfiguration runs once, after every other category: it merges the
// backed-up host's kernel parameters into this host's boot configuration, then
// rebuilds the initramfs and the bootloader when the merge changed a file or the
// restore wrote an initramfs input. Failures are warnings; only an abort stops the
// restore.
func (w *restoreUIWorkflowRun) applyBootConfiguration() error {
	if !w.needsBootConfiguration {
		return nil
	}
	w.logger.Info("")
	source, err := readBackedUpKernelCmdline(w.ctx, w.logger, w.prepared.ArchivePath)
	if err != nil {
		if restoreAbortOrInput(err) || w.ctx.Err() != nil {
			return err
		}
		w.restoreHadWarnings = true
		w.logger.Warning("Boot configuration - failed to read the kernel command line from the backup: %v", err)
	}
	target := detectBootTarget(w.ctx, w.destRoot)
	changed := false
	if err == nil {
		var warned bool
		changed, warned = mergeBootKernelCmdline(w.logger, w.destRoot, target, source)
		if warned {
			w.restoreHadWarnings = true
		}
	}

	if len(w.bootRebuildInputsWritten) > 0 {
		written := make([]string, len(w.bootRebuildInputsWritten))
		for i, p := range w.bootRebuildInputsWritten {
			written[i] = "/" + p
		}
		w.logger.Info("Boot configuration - restore wrote %s, which the initramfs copies", strings.Join(written, ", "))
	}
	if !changed && len(w.bootRebuildInputsWritten) == 0 {
		w.logger.Info("Boot configuration - initramfs and bootloader not rebuilt: no boot file changed")
		return nil
	}
	warned, err := rebuildBootAfterRestore(w.ctx, w.logger, target)
	if warned {
		w.restoreHadWarnings = true
	}
	return err
}
