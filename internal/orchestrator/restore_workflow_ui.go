// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

var prepareRestoreBundleFunc = prepareRestoreBundleWithUI
var analyzeRestoreArchiveFunc = AnalyzeRestoreArchive

func fallbackRestoreDecisionInfoFromManifest(manifest *backup.Manifest) *RestoreDecisionInfo {
	info := &RestoreDecisionInfo{Source: RestoreDecisionSourceUnknown}
	if manifest == nil {
		return info
	}

	info.BackupType = DetectBackupType(manifest)
	info.ClusterPayload = strings.EqualFold(strings.TrimSpace(manifest.ClusterMode), "cluster")
	info.BackupHostname = strings.TrimSpace(manifest.Hostname)
	return info
}

func prepareRestoreBundleWithUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (*backupCandidate, *preparedBundle, error) {
	candidate, err := selectBackupCandidateWithUI(ctx, ui, cfg, logger, false)
	if err != nil {
		return nil, nil, err
	}

	prepared, err := preparePlainBundleWithUI(ctx, candidate, version, logger, ui)
	if err != nil {
		return nil, nil, err
	}
	return candidate, prepared, nil
}

func runRestoreWorkflowWithUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui RestoreWorkflowUI) (err error) {
	ClearRestoreAbortInfo()
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if ui == nil {
		return fmt.Errorf("restore UI not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	done := logging.DebugStart(logger, "restore workflow (ui)", "version=%s", version)
	defer func() { done(err) }()
	defer func() { err = normalizeRestoreWorkflowUIError(ctx, logger, err) }()

	workflow := newRestoreUIWorkflowRun(ctx, cfg, logger, version, ui)
	return workflow.run()
}

func normalizeRestoreWorkflowUIError(ctx context.Context, logger *logging.Logger, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) {
		logger.Warning("Restore input closed unexpectedly (EOF). This usually means the interactive UI lost access to stdin/TTY (e.g., SSH disconnect or non-interactive execution). Re-run with --restore --cli from an interactive shell.")
		return ErrRestoreAborted
	}
	if restoreWorkflowInputAborted(ctx, err) {
		return ErrRestoreAborted
	}
	return err
}

func restoreWorkflowInputAborted(ctx context.Context, err error) bool {
	return errors.Is(err, input.ErrInputAborted) ||
		errors.Is(err, ErrDecryptAborted) ||
		errors.Is(err, ErrAgeRecipientSetupAborted) ||
		errors.Is(err, context.Canceled) ||
		(ctx != nil && ctx.Err() != nil)
}
