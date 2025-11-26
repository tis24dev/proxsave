package main

import (
	"io"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

// startFlowSessionLog creates a session log for interactive flows (install,
// new-install, etc.) and mirrors bootstrap output into it.
func startFlowSessionLog(flowName string, bootstrap *logging.BootstrapLogger) (*logging.Logger, func()) {
	logger, logPath, closeFn, err := logging.StartSessionLogger(flowName, types.LogLevelInfo, false)
	if err != nil {
		if bootstrap != nil {
			bootstrap.Warning("WARNING: Unable to start %s log: %v", flowName, err)
		}
		return nil, func() {}
	}

	logger.SetOutput(io.Discard)
	if bootstrap != nil {
		bootstrap.Info("%s log: %s", strings.ToUpper(flowName), logPath)
		bootstrap.SetMirrorLogger(logger)
	}

	cleanup := func() {
		if bootstrap != nil {
			bootstrap.SetMirrorLogger(nil)
		}
		closeFn()
	}
	return logger, cleanup
}
