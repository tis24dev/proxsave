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
	// Announced only on a PVE host: skipPath drops these entries whatever the detected
	// system type, but telling a PBS operator about a cluster database is noise.
	if f.plan != nil && f.plan.SystemType.SupportsPVE() {
		if paths := clusterDBArchivePaths(); len(paths) > 0 {
			f.logger.Warning("Full restore safety: %s will NOT be restored. This fallback never stops pve-cluster, so writing the cluster database under a live pmxcfs would corrupt it. Restore that category with a selective restore once the archive can be analyzed.", strings.Join(paths, ", "))
		}
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

// skipPath keeps three classes of entry out of a plain extraction: /etc/fstab, which
// is merged afterwards instead of overwritten; the PVE cluster database, which this
// fallback has no safe way to write; and everything belonging to an ExportOnly
// category. The selective path never writes export-only content to system paths
// (splitRestoreCategories routes it to an export directory); before this, the
// fallback wrote /etc/proxmox-backup/ and /var/lib/proxsave-info/ straight to /.
//
// The prefixes come from the plan's own ExportCategories and from categories.go, so
// there is no second list to keep in step with them.
func (f *fullRestoreUIFlow) skipPath(name string) bool {
	clean := normalizeArchiveEntryPath(name)
	if f.safeFstabMerge() && clean == "etc/fstab" {
		return true
	}
	if matchesAnyArchivePrefix(clean, clusterDBArchivePaths()) {
		return true
	}
	return f.isExportOnlyPath(clean)
}

// clusterDBArchivePaths returns the archive prefixes holding the PVE cluster
// database, read from categories.go rather than restated here.
//
// The fallback must never write them. runFullRestore leaves NeedsClusterRestore off,
// so pve-cluster keeps running and /etc/pve stays mounted; extracting config.db under
// a live pmxcfs that holds it open as SQLite corrupts it, and on a clustered node
// corosync would carry the damage to every other member. The /etc/pve block in
// restore_archive_entries.go does not cover this: the database lives under
// /var/lib/pve-cluster/, which no other guard touches.
func clusterDBArchivePaths() []string {
	cat := GetCategoryByID("pve_cluster", GetAllCategories())
	if cat == nil {
		return nil
	}
	return cat.Paths
}

func (f *fullRestoreUIFlow) isExportOnlyPath(clean string) bool {
	if f.plan == nil {
		return false
	}
	for _, cat := range f.plan.ExportCategories {
		if matchesAnyArchivePrefix(clean, cat.Paths) {
			return true
		}
	}
	return false
}

// matchesAnyArchivePrefix reports whether clean sits at or under any of the given
// category paths, normalizing both sides so a "./var/lib/x/" category path and a
// "var/lib/x/y" archive entry compare correctly.
func matchesAnyArchivePrefix(clean string, paths []string) bool {
	if clean == "" {
		return false
	}
	for _, p := range paths {
		prefix := normalizeArchiveEntryPath(p)
		if prefix == "" {
			continue
		}
		if clean == prefix || strings.HasPrefix(clean, strings.TrimSuffix(prefix, "/")+"/") {
			return true
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
