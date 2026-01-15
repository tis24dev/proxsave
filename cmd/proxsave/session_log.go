package main

import (
	"io"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// startFlowSessionLog creates a session log for interactive flows (install,
// new-install, etc.) and mirrors bootstrap output into it.
func startFlowSessionLog(flowName string, logLevel types.LogLevel, bootstrap *logging.BootstrapLogger) (*logging.Logger, func()) {
	if logLevel == types.LogLevelNone {
		logLevel = types.LogLevelInfo
	}
	logger, logPath, closeFn, err := logging.StartSessionLogger(flowName, logLevel, false)
	if err != nil {
		if bootstrap != nil {
			bootstrap.Warning("WARNING: Unable to start %s log: %v", flowName, err)
		}
		return nil, func() {}
	}

	logger.SetOutput(io.Discard)
	if bootstrap != nil {
		bootstrap.Info("%s log: %s", strings.ToUpper(flowName), logPath)
		bootstrap.SetLevel(logLevel)
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
