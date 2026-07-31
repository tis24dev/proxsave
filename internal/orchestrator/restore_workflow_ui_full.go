// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

type fullRestoreUIFlow struct {
	ctx       context.Context
	ui        RestoreWorkflowUI
	candidate *backupCandidate
	prepared  *preparedBundle
	destRoot  string
	logger    *logging.Logger
	dryRun    bool
	// plan is the synthesized full-restore plan; only ExportCategories is read, to
	// keep export-only content out of the live system.
	plan *RestorePlan
}

// newFullRestoreUIFlow builds the extraction half of the fallback from the shared
// workflow state. The safety half (confirm ordering, safety backup, services) lives
// in runFullRestore, which reuses the selective path's own methods.
func newFullRestoreUIFlow(w *restoreUIWorkflowRun) *fullRestoreUIFlow {
	return &fullRestoreUIFlow{
		ctx:       w.ctx,
		ui:        w.ui,
		candidate: w.candidate,
		prepared:  w.prepared,
		destRoot:  w.destRoot,
		logger:    w.logger,
		dryRun:    w.cfg.DryRun,
		plan:      w.plan,
	}
}

// extract writes the archive out, skipping what must not reach the live system, and
// then merges fstab. The plan is read here only for its ExportCategories.
func (f *fullRestoreUIFlow) extract() error {
	if f.safeFstabMerge() {
		f.logger.Warning("Full restore safety: /etc/fstab will not be overwritten; Smart Merge will be applied after extraction.")
	}
	if err := extractPlainArchive(f.ctx, f.prepared.ArchivePath, f.destRoot, f.logger, f.skipPath); err != nil {
		return err
	}
	if err := f.mergeFstabIfSafe(); err != nil {
		return err
	}
	f.logger.Info("Restore completed successfully.")
	return nil
}

// skipPath keeps two classes of entry out of a plain extraction: /etc/fstab, which
// is merged afterwards instead of overwritten, and everything belonging to an
// ExportOnly category. The selective path never writes export-only content to system
// paths (splitRestoreCategories routes it to an export directory); before this, the
// fallback wrote /etc/proxmox-backup/ and /var/lib/proxsave-info/ straight to /.
//
// The prefixes come from the plan's own ExportCategories, so there is no second list
// to keep in step with categories.go.
func (f *fullRestoreUIFlow) skipPath(name string) bool {
	clean := normalizeArchiveEntryPath(name)
	if f.safeFstabMerge() && clean == "etc/fstab" {
		return true
	}
	return f.isExportOnlyPath(clean)
}

func (f *fullRestoreUIFlow) isExportOnlyPath(clean string) bool {
	if f.plan == nil || clean == "" {
		return false
	}
	for _, cat := range f.plan.ExportCategories {
		for _, p := range cat.Paths {
			prefix := normalizeArchiveEntryPath(p)
			if prefix == "" {
				continue
			}
			if clean == prefix || strings.HasPrefix(clean, strings.TrimSuffix(prefix, "/")+"/") {
				return true
			}
		}
	}
	return false
}

// normalizeArchiveEntryPath strips the "./" and "/" prefixes tar entries and category
// paths carry, so the two can be compared.
func normalizeArchiveEntryPath(name string) string {
	clean := strings.TrimPrefix(strings.TrimSpace(name), "./")
	return strings.TrimPrefix(clean, "/")
}

func (f *fullRestoreUIFlow) safeFstabMerge() bool {
	return f.destRoot == "/" && isRealRestoreFS(restoreFS)
}

func (f *fullRestoreUIFlow) mergeFstabIfSafe() error {
	if !f.safeFstabMerge() {
		return nil
	}
	f.logger.Info("")
	fsTempDir, err := restoreFS.MkdirTemp("", "proxsave-fstab-")
	if err != nil {
		f.logger.Warning("Failed to create temp dir for fstab merge: %v", err)
		return nil
	}
	defer func() {
		if err := restoreFS.RemoveAll(fsTempDir); err != nil {
			f.logger.Debug("Failed to remove temporary fstab merge directory %s: %v", fsTempDir, err)
		}
	}()
	return f.extractAndMergeFstab(fsTempDir)
}

func (f *fullRestoreUIFlow) extractAndMergeFstab(fsTempDir string) error {
	category := []Category{{
		ID:    "filesystem",
		Name:  "Filesystem Configuration",
		Paths: []string{"./etc/fstab"},
	}}
	err := extractArchiveNative(f.ctx, restoreArchiveOptions{
		archivePath: f.prepared.ArchivePath,
		destRoot:    fsTempDir,
		logger:      f.logger,
		categories:  category,
		mode:        RestoreModeCustom,
	})
	if err != nil {
		f.logger.Warning("Failed to extract filesystem config for merge: %v", err)
		return nil
	}
	// The selective path does this too. Without it remapFstabDevicesFromInventory has
	// nothing to map against, so the merge silently proposes the backup's raw device
	// names instead of this system's UUID/LABEL.
	extractFstabInventoryInto(f.ctx, f.prepared.ArchivePath, fsTempDir, f.logger)
	currentFstab := filepath.Join(f.destRoot, "etc", "fstab")
	backupFstab := filepath.Join(fsTempDir, "etc", "fstab")
	if err := smartMergeFstabWithUI(f.ctx, f.logger, f.ui, currentFstab, backupFstab, f.dryRun); err != nil {
		return f.handleFstabMergeError(err)
	}
	return nil
}

func (f *fullRestoreUIFlow) handleFstabMergeError(err error) error {
	if errors.Is(err, ErrRestoreAborted) || input.IsAborted(err) {
		f.logger.Info("Restore aborted by user during Smart Filesystem Configuration Merge.")
		return err
	}
	f.logger.Warning("Smart Fstab Merge failed: %v", err)
	return nil
}
