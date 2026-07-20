// Package main contains the proxsave command entrypoint.
package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
)

// daemonAliveProbe reports whether pid is a LIVE proxsave --daemon process. It is a package var so
// a test can override the probe: verifying a real fork is neither portable nor safe in unit tests,
// so the decision helper is tested against a fake probe. Production uses
// probeProxsaveDaemonAlive.
var daemonAliveProbe = probeProxsaveDaemonAlive

// maybeHandoffManualBackup hands a STANDALONE backup's outcome to the resident daemon (the SOLE
// pinger) and wakes it with SIGUSR1, so the daemon pings + records the backup-outcome check
// through its own finish path -- WITHOUT this run ever building a Reporter or writing the status
// file. It hands off ONLY when:
//   - a backup was actually ATTEMPTED (supportStats or an earlyError present; the disabled and
//     benign-concurrency-skip paths leave BOTH nil, so they never hand off);
//   - this is a STANDALONE run (health.EnvRunID unset). When it is set THIS run is the daemon's
//     own supervised child and the daemon already pings that run's outcome in runOnce -- handing
//     off again would DOUBLE-ping;
//   - healthchecks AND backups are both enabled.
//
// It then finds the daemon's recorded pid and verifies it is a LIVE proxsave --daemon before
// signalling (never SIGUSR1 an unrelated process that reused the pid -- SIGUSR1's default action
// would KILL it). With no live daemon it does nothing (coherent: without the daemon, nothing
// pings anyway). Every step is best-effort: a hiccup here must never change the backup exit code,
// so failures are logged at Debug and swallowed.
func maybeHandoffManualBackup(opts backupModeOptions, res backupModeResult) {
	if opts.dryRun {
		// A dry-run is a TEST, never a real backup outcome, so it must not touch the
		// backup-outcome check. The post-install audit runs `proxsave --dry-run` as a
		// subprocess (installer.runPostInstallAuditDryRun) and exits 1 on warnings --
		// without this gate that probe would ping the monitor with a phantom exit=1.
		return
	}
	if res.supportStats == nil && res.earlyErrorState == nil {
		return // no backup attempted (disabled / benign concurrency skip): nothing to report
	}
	if strings.TrimSpace(os.Getenv(health.EnvRunID)) != "" {
		return // supervised child: the daemon already pings this run's outcome in runOnce
	}
	if opts.cfg == nil || !opts.cfg.HealthcheckEnabled || !opts.cfg.BackupEnabled {
		return
	}
	baseDir := opts.cfg.BaseDir

	pid, err := health.ReadDaemonPID(baseDir)
	if err != nil {
		logging.Debug("manual backup handoff: read daemon pid failed: %v", err)
		return
	}
	if pid <= 0 || !daemonAliveProbe(pid) {
		logging.Debug("manual backup handoff: no live proxsave daemon to signal (pid=%d)", pid)
		return
	}

	if err := health.WriteManualOutcome(baseDir, health.NewRunID(), time.Now().Unix(), res.exitCode); err != nil {
		logging.Debug("manual backup handoff: write outcome failed: %v", err)
		return
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		logging.Debug("manual backup handoff: signal daemon pid=%d failed: %v", pid, err)
		return
	}
	logging.Debug("manual backup handoff: handed outcome to daemon pid=%d (exit=%d)", pid, res.exitCode)
}

// probeProxsaveDaemonAlive reports whether pid is alive AND its /proc/<pid>/cmdline identifies a
// proxsave --daemon process. Liveness is checked with signal 0 (no signal delivered, just an errno
// probe: nil => alive; ESRCH => gone; EPERM => alive but not ours -> treated as "not signallable",
// which is also what a later Kill would hit). The cmdline match is the SAFETY gate: "is pid alive"
// alone is not enough because the OS reuses pids, and signalling a recycled pid with SIGUSR1 could
// KILL an unrelated process (SIGUSR1's default action is terminate). Requiring BOTH a "proxsave"
// token AND the exact "--daemon" arg means we only ever wake our own daemon.
func probeProxsaveDaemonAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false // ESRCH (gone) or EPERM (not ours): do not signal it
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return false
	}
	// /proc/<pid>/cmdline is NUL-separated argv; split to whole args so the match is on a real
	// argument, not a coincidental "proxsave" substring buried inside an unrelated path.
	args := strings.Split(string(data), "\x00")
	var hasProxsave, hasDaemon bool
	for _, a := range args {
		if strings.Contains(a, "proxsave") {
			hasProxsave = true
		}
		if a == "--daemon" {
			hasDaemon = true
		}
	}
	return hasProxsave && hasDaemon
}
