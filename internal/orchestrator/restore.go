// Package orchestrator coordinates backup, restore, decrypt, and related workflows.
package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// ErrRestoreAborted is returned when a restore workflow is intentionally aborted by the user.
var ErrRestoreAborted = errors.New("restore workflow aborted by user")

// RestoreAbortInfo contains information about an aborted restore with network rollback.
type RestoreAbortInfo struct {
	NetworkRollbackArmed  bool
	NetworkRollbackLog    string
	NetworkRollbackMarker string
	OriginalIP            string    // IP from backup file (will be restored by rollback)
	CurrentIP             string    // IP after apply (before rollback)
	RollbackDeadline      time.Time // when rollback will execute
}

var lastRestoreAbortInfo *RestoreAbortInfo

// GetLastRestoreAbortInfo returns info about the last restore abort, if any.
func GetLastRestoreAbortInfo() *RestoreAbortInfo {
	return lastRestoreAbortInfo
}

// ClearRestoreAbortInfo clears the stored abort info.
func ClearRestoreAbortInfo() {
	lastRestoreAbortInfo = nil
}

// RunRestoreWorkflow runs the CLI restore workflow using stdin prompts and the provided configuration.
func RunRestoreWorkflow(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string) (err error) {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}

	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	done := logging.DebugStart(logger, "restore workflow (cli)", "version=%s", version)
	defer func() { done(err) }()

	ui := newCLIWorkflowUI(bufio.NewReader(os.Stdin), logger)
	return runRestoreWorkflowWithUI(ctx, cfg, logger, version, ui)
}
