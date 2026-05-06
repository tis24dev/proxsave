// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
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
}

func runFullRestoreWithUI(ctx context.Context, ui RestoreWorkflowUI, candidate *backupCandidate, prepared *preparedBundle, destRoot string, logger *logging.Logger, dryRun bool) error {
	flow := &fullRestoreUIFlow{
		ctx:       ctx,
		ui:        ui,
		candidate: candidate,
		prepared:  prepared,
		destRoot:  destRoot,
		logger:    logger,
		dryRun:    dryRun,
	}
	return flow.run()
}

func (f *fullRestoreUIFlow) run() error {
	if err := f.validate(); err != nil {
		return err
	}
	if err := f.confirm(); err != nil {
		return err
	}
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

func (f *fullRestoreUIFlow) validate() error {
	if f.candidate == nil || f.prepared == nil || f.prepared.Manifest.ArchivePath == "" {
		return fmt.Errorf("invalid restore candidate")
	}
	return nil
}

func (f *fullRestoreUIFlow) confirm() error {
	if err := f.ui.ShowMessage(f.ctx, "Full restore", "Backup category analysis failed; ProxSave will run a full restore (no selective modes)."); err != nil {
		return err
	}
	confirmed, err := f.ui.ConfirmRestore(f.ctx)
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrRestoreAborted
	}
	return nil
}

func (f *fullRestoreUIFlow) skipPath(name string) bool {
	if !f.safeFstabMerge() {
		return false
	}
	clean := strings.TrimPrefix(strings.TrimSpace(name), "./")
	clean = strings.TrimPrefix(clean, "/")
	return clean == "etc/fstab"
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
	defer restoreFS.RemoveAll(fsTempDir)
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
