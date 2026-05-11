// Package main contains the proxsave command entrypoint.
package main

import (
	"os"
	"syscall"
	"time"
)

const (
	defaultLegacyEnvPath          = "/opt/proxsave/env/backup.env"
	legacyEnvFallbackPath         = "/opt/proxmox-backup/env/backup.env"
	goRuntimeMinVersion           = "1.25.10"
	networkPreflightTimeout       = 2 * time.Second
	bytesPerMegabyte        int64 = 1024 * 1024
	defaultDirPerm                = 0o755
	exitCodeInterrupted           = 128 + int(syscall.SIGINT)
)

// Build-time variables (injected via ldflags)
var (
	buildTime = "" // Will be set during compilation via -ldflags "-X main.buildTime=..."
)

func main() {
	os.Exit(run())
}

func run() int {
	runInfo := startMainRun()
	defer finishMainRun(runInfo)
	ctx, cancel := setupRunContext(runInfo.bootstrap)
	defer cancel()

	args, exitCode, handled := preparePreRuntimeArgs(ctx, runInfo.bootstrap, runInfo.toolVersion)
	if handled {
		return exitCode
	}

	rt, exitCode, ok := prepareRuntime(ctx, args, runInfo.bootstrap, runInfo.state, runInfo.toolVersion)
	if !ok {
		return exitCode
	}
	return runRuntime(rt, runInfo.state)
}
