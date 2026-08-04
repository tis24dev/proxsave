// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"errors"
	"path/filepath"
	"strings"
)

// runFullRestore is the analysis-failure fallback: the archive could not be split
// into categories, so every entry is extracted rather than a selected subset.
//
// It runs the SAME safety steps as the selective path, by reusing that path's own
// methods on the shared receiver rather than reimplementing them here. Before, this
// fallback jumped straight to extraction: no safety backup, no service stop, no
// export-only separation, so a truncated archive could overwrite /etc/passwd,
// /etc/shadow and /etc/sudoers wholesale with nothing to roll back to.
//
// The order mirrors runSelectiveRestore deliberately - confirm, then back up, then
// stop services, then write - so a user who declines at the prompt costs nothing.
func (w *restoreUIWorkflowRun) runFullRestore() error {
	if err := w.validateFullRestoreCandidate(); err != nil {
		return err
	}
	if err := w.confirmFullRestore(); err != nil {
		return err
	}

	w.plan = w.synthesizeFullRestorePlan()

	// Only the safety backup, NOT createRollbackBackups. The network/firewall/HA/
	// access-control rollback backups exist to undo the transactional APPLY steps in
	// runPostRestoreApplyWorkflows, and this fallback runs none of them: taking them
	// here would arm four rollback timers over work that never happens.
	if err := w.createSafetyBackup(w.systemWriteCategories()); err != nil {
		return err
	}

	cleanupServices, err := w.prepareRestoreServices()
	if err != nil {
		return err
	}
	defer cleanupServices()

	// Built only now: the flow reads the plan for its export-only skip list, so it
	// must not be constructed before the plan exists.
	return newFullRestoreUIFlow(w).extract()
}

func (w *restoreUIWorkflowRun) validateFullRestoreCandidate() error {
	if w.candidate == nil || w.prepared == nil || w.prepared.ArchivePath == "" {
		return errors.New("invalid restore candidate")
	}
	return nil
}

func (w *restoreUIWorkflowRun) confirmFullRestore() error {
	if err := w.ui.ShowMessage(w.ctx, "Full restore", "Backup category analysis failed; ProxSave will run a full restore (no selective modes). A safety backup is taken first and export-only content is still kept off the live system."); err != nil {
		return err
	}
	confirmed, err := w.ui.ConfirmRestore(w.ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrRestoreAborted
	}
	return nil
}

// synthesizeFullRestorePlan builds the plan the fallback could not compute from the
// archive. Since the categories in the archive are unknown, it starts from every
// category ProxSave knows and keeps those whose paths actually exist on this system:
// the safety backup can only capture what is there, and carrying the rest would make
// it slower for no coverage.
//
// PlanRestore is reused rather than hand-building the struct, so the normal/staged/
// export split - including the ExportOnly separation this fallback was missing -
// comes from the same code the selective path uses and cannot drift from it.
func (w *restoreUIWorkflowRun) synthesizeFullRestorePlan() *RestorePlan {
	categories := categoriesPresentUnderRoot(GetAllCategories(), w.destRoot)
	plan := PlanRestore(false, categories, w.systemType, RestoreModeFull)

	// The cluster stays up: this fallback runs because the archive could not be
	// analyzed, so it cannot know whether the archive even holds usable cluster data,
	// and stopping pve-cluster plus unmounting /etc/pve on that guess is disruption
	// bought with nothing.
	//
	// That decision is only safe because the extraction skips the cluster database
	// too - see clusterDBArchivePaths in restore_workflow_ui_full.go. The two must
	// stay together: leaving this false while letting /var/lib/pve-cluster/ through
	// would write config.db under a live pmxcfs. The /etc/pve block in
	// restore_archive_entries.go is NOT that guarantee; it covers a different path.
	//
	// NeedsPBSServices is left as PlanRestore computed it, but note what that buys:
	// pbs_config is ExportOnly over the whole ./etc/proxmox-backup/ prefix, and
	// skipPath filters by path rather than by category, so every entry under it is
	// dropped no matter which category owns it. This extraction therefore writes no
	// PBS configuration at all, yet still stops the PBS services. Left alone on
	// purpose - stopping them is harmless, and narrowing it is a separate decision.
	plan.NeedsClusterRestore = false
	return plan
}

// categoriesPresentUnderRoot keeps the categories with at least one path that exists
// under destRoot.
func categoriesPresentUnderRoot(categories []Category, destRoot string) []Category {
	kept := make([]Category, 0, len(categories))
	for _, cat := range categories {
		if categoryHasExistingPath(cat, destRoot) {
			kept = append(kept, cat)
		}
	}
	return kept
}

func categoryHasExistingPath(cat Category, destRoot string) bool {
	for _, p := range cat.Paths {
		if _, err := restoreFS.Stat(resolveCategoryPath(p, destRoot)); err == nil {
			return true
		}
	}
	return false
}

// resolveCategoryPath turns a category's archive-relative path ("./etc/fstab") into
// an absolute path under destRoot.
func resolveCategoryPath(archivePath, destRoot string) string {
	clean := strings.TrimPrefix(strings.TrimSpace(archivePath), "./")
	clean = strings.TrimPrefix(clean, "/")
	if destRoot == "" {
		destRoot = "/"
	}
	return filepath.Join(destRoot, clean)
}
