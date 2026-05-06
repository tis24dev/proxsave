// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"os"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

type appRuntime struct {
	ctx               context.Context
	args              *cli.Args
	bootstrap         *logging.BootstrapLogger
	deps              appDeps
	cfg               *config.Config
	logger            *logging.Logger
	envInfo           *environment.EnvironmentInfo
	unprivilegedInfo  environment.UnprivilegedContainerInfo
	updateInfo        *UpdateInfo
	toolVersion       string
	hostname          string
	startTime         time.Time
	timestampStr      string
	dryRun            bool
	logLevel          types.LogLevel
	initialEnvBaseDir string
	autoBaseDirFound  bool
	sessionLogCloser  func()
	heapProfilePath   string
	cpuProfileFile    *os.File
	serverIDValue     string
	serverMACValue    string
}

type appRunState struct {
	finalExitCode      int
	showSummary        bool
	orch               *orchestrator.Orchestrator
	earlyErrorState    *orchestrator.EarlyErrorState
	pendingSupportStat *orchestrator.BackupStats
}

type modeResult struct {
	orch            *orchestrator.Orchestrator
	earlyErrorState *orchestrator.EarlyErrorState
	supportStats    *orchestrator.BackupStats
	exitCode        int
	handled         bool
}

type appDeps struct {
	now func() time.Time
}

func defaultAppDeps() appDeps {
	return appDeps{now: time.Now}
}

func newAppRunState() *appRunState {
	return &appRunState{finalExitCode: types.ExitSuccess.Int()}
}

func (state *appRunState) finalize(code int) int {
	state.finalExitCode = code
	return code
}

func (state *appRunState) applyModeResult(result modeResult) {
	state.orch = result.orch
	state.earlyErrorState = result.earlyErrorState
	state.pendingSupportStat = result.supportStats
}
