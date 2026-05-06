// Package main contains the proxsave command entrypoint.
package main

import (
	"fmt"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/security"
	"github.com/tis24dev/proxsave/internal/types"
)

func runSecurityPreflight(rt *appRuntime) (int, bool) {
	execInfo := getExecInfo()
	execPath := execInfo.ExecPath
	logging.DebugStep(rt.logger, "main", "running security checks")
	if _, secErr := security.Run(rt.ctx, rt.logger, rt.cfg, rt.args.ConfigPath, execPath, rt.envInfo); secErr != nil {
		logging.Error("Security checks failed: %v", secErr)
		return types.ExitSecurityError.Int(), false
	}
	fmt.Println()
	return types.ExitSuccess.Int(), true
}
