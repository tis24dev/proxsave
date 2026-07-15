// Package main contains the proxsave command entrypoint.
package main

import (
	"os"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/orchestrator"
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
	// Wire the systemd daemon-presence probe into the orchestrator so the Phase-7 section
	// and the dashboard/install check report the REAL daemon existence (installed/active),
	// not just a heartbeat-derived guess. Set once here so the running binary always has
	// it while unit tests (which never call run()) keep the unprobed heartbeat-only path.
	orchestrator.DaemonPresenceProbe = daemonPresenceProbe
	// Wire the record-independent /proc staleness fallback too, so the install/dashboard check catches
	// a record-less-but-stale daemon (predates the identity-record feature or a bootstrap first-deploy)
	// as BEHIND instead of a false WORKING. Parity with DaemonPresenceProbe: set once here, nil under
	// unit tests (which never call run()), keeping the record-based path the sole signal there.
	orchestrator.DaemonProcStale = procBinaryStaleProbe
	runInfo := startMainRun()
	defer finishMainRun(runInfo)
	defer releaseDashboardLeftovers()
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
