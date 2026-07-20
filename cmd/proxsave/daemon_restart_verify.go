// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
)

// This file is the reusable RESTART+VERIFY block shared by the upgrade, the install,
// and the dashboard "Restart daemon" button. A binary upgrade replaces the file on
// disk WITHOUT restarting the resident daemon (systemd keeps the old process alive),
// so an upgrade/install that leaves the daemon running is left BEHIND the new binary.
// restartAndVerifyDaemon restarts the unit and then polls the composed daemon-state
// verdict until the daemon comes back genuinely fresh AND aligned with the on-disk
// binary. Because the daemon supervises its own backups, a restart mid-backup would
// KILL that backup, so the restart first WAITS (bounded) for any in-progress backup to
// finish; if it never frees within the budget the restart is DEFERRED, never forced.

// Tunable budgets. They are package vars (not consts) so tests can shrink them to run
// the poll/wait loops in milliseconds instead of minutes.
var (
	// backupWaitTimeout bounds how long restartAndVerifyDaemon waits for a running
	// backup to finish before deferring the restart.
	backupWaitTimeout = 4 * time.Minute
	// backupWaitPollInterval is the cadence of the backup-idle poll.
	backupWaitPollInterval = 1 * time.Second
	// restartVerifyTimeout bounds the post-restart alignment poll. It must exceed the
	// unit's RestartSec=10 so the daemon has time to come back up before we give up.
	restartVerifyTimeout = 30 * time.Second
	// restartVerifyTick is the cadence of the post-restart alignment poll.
	restartVerifyTick = 500 * time.Millisecond
)

// Seams so tests can drive restartAndVerifyDaemon without a real systemctl, a real
// backup lock, or a real /proc probe.
//   - restartVerifyBackupRunning: the non-intrusive "is a backup running" probe.
//   - restartVerifyDaemonState:   the composed daemon-state check.
//
// The restart itself reuses the existing daemonRestartService seam (dashboard.go), so a
// single override covers both the button and this block.
var (
	restartVerifyBackupRunning = defaultBackupRunning
	restartVerifyDaemonState   = health.CheckDaemonState
	// daemonInstalledProbe is the unit-file existence check, a seam so daemonIsActive can
	// be driven in tests without an on-disk /etc/systemd unit.
	daemonInstalledProbe = daemonUnitInstalled
)

// RestartVerifyResult is the outcome of a restart+verify (or a poll-only verify). The
// booleans are mutually informative rather than exclusive: a success is
// Restarted && ProcessAlive && Aligned && FreshInfo with TimedOut=false; a deferral is
// BackupWaitTimedOut=true with Restarted=false; a restart error is Err!=nil with
// Restarted=false; a restart that never confirmed alignment is Restarted=true with
// TimedOut=true. State carries the last observed composed verdict for display.
type RestartVerifyResult struct {
	Restarted          bool
	ProcessAlive       bool
	Aligned            bool
	FreshInfo          bool
	TimedOut           bool
	BackupWaitTimedOut bool
	// LockPathUnknown is set when the config could not be read, so the REAL backup lock
	// path is unknown. The restart is DEFERRED (fail-closed): restarting on the base-dir
	// default path could miss an in-progress backup on a custom LOCK_PATH and kill it.
	LockPathUnknown bool
	State           health.DaemonState
	Err             error
}

// defaultBackupRunning is the production backup-in-progress probe: it inspects the EXACT
// lock file the orchestrator/Checker acquires (lockFilePath, resolved from cfg.LockPath by
// the caller) using the same stale/live pid detection (checks.BackupInProgress). It is
// read-only -- it never creates, holds, or removes the lock -- so it can never interrupt or
// block a real backup. The caller MUST pass the real lock file path (see backupLockFilePath):
// a wrong path (e.g. the base-dir default on a custom-LOCK_PATH host) would find no lock and
// wrongly report "not running", letting a restart kill an in-progress backup.
func defaultBackupRunning(lockFilePath string) bool {
	return checks.BackupInProgress(lockFilePath, checks.DefaultMaxLockAge)
}

// backupLockFilePath resolves the REAL backup lock FILE path from a loaded config, honouring
// a custom LOCK_PATH: the orchestrator's Checker acquires <cfg.LockPath>/.backup.lock (see
// configurePreBackupChecker), so the restart backup-wait probe must inspect that same file.
// The second return, resolved, is false ONLY when cfg is nil (the config load failed): the
// real lock path cannot be known then, so a custom LOCK_PATH could be missed by the base-dir
// default. A readable config with an empty LOCK_PATH IS resolved (the base-dir default is the
// real path in that case).
func backupLockFilePath(cfg *config.Config, baseDir string) (string, bool) {
	if cfg != nil {
		if lp := strings.TrimSpace(cfg.LockPath); lp != "" {
			return filepath.Join(lp, checks.BackupLockFileName), true
		}
		// A readable config with the default LOCK_PATH: the base-dir default IS the real path.
		return checks.DefaultBackupLockPath(baseDir), true
	}
	// Config unreadable: the real lock path is unknown (a custom LOCK_PATH cannot be seen).
	return checks.DefaultBackupLockPath(baseDir), false
}

// restartAndVerifyDaemon restarts the resident daemon and verifies it comes back fresh
// and aligned with the on-disk binary. Ordering is load-bearing:
//
//  0. WAIT (bounded) for any in-progress backup to finish -- a restart would kill a
//     daemon-supervised backup. If it never frees within backupWaitTimeout the restart
//     is DEFERRED (BackupWaitTimedOut=true, Restarted=false): the caller reports that
//     the daemon stays on the old binary until the next idle restart.
//  1. snapshot the pre-restart start timestamp from the identity record, so a genuinely
//     NEW daemon (StartTS strictly greater) can be told apart from the old process.
//  2. restart the unit; a restart error returns immediately ({Err}, Restarted=false).
//  3. poll the composed daemon-state verdict until the daemon is process-alive, aligned
//     (a real comparison ran and matched), AND fresh (StartTS advanced past the snapshot),
//     or the bounded, ctx-aware budget elapses (TimedOut=true).
//
// lockFilePath is the REAL backup lock file (from backupLockFilePath, honouring a custom
// LOCK_PATH) the step-0 backup-wait probes; passing the wrong path defeats the wait and can
// kill an in-progress backup, so callers derive it from the config they load. lockPathKnown
// is that same call's resolved bool: false means the config was unreadable, so lockFilePath
// is only the base-dir default guess, not necessarily the real path -- the restart is
// DEFERRED (fail-closed) rather than risk killing a backup on a custom LOCK_PATH (F11-08).
func restartAndVerifyDaemon(ctx context.Context, baseDir, lockFilePath string, lockPathKnown bool, interval time.Duration) RestartVerifyResult {
	if ctx == nil {
		ctx = context.Background()
	}

	// Fail-closed: if the config was unreadable the REAL backup lock path is unknown, so
	// restarting on the base-dir default could miss a backup on a custom LOCK_PATH and
	// kill it. Defer the restart instead; the new binary is already on disk, so this is
	// retried at the next --upgrade or dashboard restart once the config is readable (F11-08).
	if !lockPathKnown {
		return RestartVerifyResult{LockPathUnknown: true}
	}

	// 0. Bounded backup-wait: never restart on top of a running, daemon-supervised backup.
	if waitForBackupIdle(ctx, lockFilePath) {
		return RestartVerifyResult{BackupWaitTimedOut: true}
	}

	// 1. Snapshot the pre-restart identity so a fresh daemon is distinguishable.
	var preStartTS int64
	if info, ok, _ := health.ReadDaemonInfo(baseDir); ok {
		preStartTS = info.StartTS
	}

	// 2. Restart. A failure here is terminal for this attempt (nothing was restarted).
	if err := daemonRestartService(ctx); err != nil {
		return RestartVerifyResult{Err: err}
	}

	// 3. Bounded, ctx-aware poll for a fresh + aligned + live daemon.
	res := RestartVerifyResult{Restarted: true}
	pollCtx, cancel := context.WithTimeout(ctx, restartVerifyTimeout)
	defer cancel()
	ticker := time.NewTicker(restartVerifyTick)
	defer ticker.Stop()
	for {
		st := restartVerifyDaemonState(health.DaemonStateInput{
			BaseDir:           baseDir,
			HeartbeatInterval: interval,
			Now:               time.Now(),
			Presence:          daemonPresenceProbe(ctx),
			ProcAlive:         probeProxsaveDaemonAlive,
			ProcStale:         procBinaryStaleProbe,
		})
		res.State = st
		res.ProcessAlive = st.ProcessAlive
		res.Aligned = st.Aligned
		res.FreshInfo = st.StartTS > preStartTS
		if st.ProcessAlive && st.Aligned && st.AlignChecked && st.StartTS > preStartTS {
			return res // genuinely fresh, aligned, live daemon
		}
		select {
		case <-pollCtx.Done():
			res.TimedOut = true
			return res
		case <-ticker.C:
		}
	}
}

// verifyDaemonAligned is the poll-only variant (no restart, no backup-wait): it waits for an
// ALREADY-(re)started daemon to become process-alive with an ASSESSABLE alignment, then returns
// the state (res.Aligned tells aligned vs behind). It polls until ProcessAlive && AlignChecked --
// NOT until Aligned -- so a daemon that is up but BEHIND is reported immediately (the SAME verdict
// --daemon-status gives), never as a timeout. TimedOut means the daemon never came up. There is no
// pre-restart snapshot, so FreshInfo simply reflects that an identity record exists.
func verifyDaemonAligned(ctx context.Context, baseDir string, interval time.Duration) RestartVerifyResult {
	if ctx == nil {
		ctx = context.Background()
	}
	res := RestartVerifyResult{}
	pollCtx, cancel := context.WithTimeout(ctx, restartVerifyTimeout)
	defer cancel()
	ticker := time.NewTicker(restartVerifyTick)
	defer ticker.Stop()
	for {
		st := restartVerifyDaemonState(health.DaemonStateInput{
			BaseDir:           baseDir,
			HeartbeatInterval: interval,
			Now:               time.Now(),
			Presence:          daemonPresenceProbe(ctx),
			ProcAlive:         probeProxsaveDaemonAlive,
			ProcStale:         procBinaryStaleProbe,
		})
		res.State = st
		res.ProcessAlive = st.ProcessAlive
		res.Aligned = st.Aligned
		res.FreshInfo = st.HaveInfo
		if st.ProcessAlive && st.AlignChecked {
			return res
		}
		select {
		case <-pollCtx.Done():
			res.TimedOut = true
			return res
		case <-ticker.C:
		}
	}
}

// waitForBackupIdle waits (bounded, ctx-aware) until no backup holds the lock at
// lockFilePath. It returns true only when a backup was STILL running when the budget
// elapsed (the caller then defers the restart). A host that is already idle returns false
// immediately.
func waitForBackupIdle(ctx context.Context, lockFilePath string) bool {
	if !restartVerifyBackupRunning(lockFilePath) {
		return false
	}
	waitCtx, cancel := context.WithTimeout(ctx, backupWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(backupWaitPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			// Final check: the backup may have freed the lock right at the deadline.
			return restartVerifyBackupRunning(lockFilePath)
		case <-ticker.C:
			if !restartVerifyBackupRunning(lockFilePath) {
				return false
			}
		}
	}
}

// daemonIsActive reports whether the daemon unit is installed AND currently active, so a
// caller can decide whether a restart is warranted at all (an inactive/absent daemon is
// left untouched -- install/cron handles bringing it up).
func daemonIsActive(ctx context.Context) bool {
	return daemonInstalledProbe() && daemonPresenceProbe(ctx).Active
}

// summarizeRestartVerify renders a one-line, plain-text summary of a restart+verify
// outcome for the upgrade footer (the CLI --upgrade path). version is the just-installed
// version, shown on the aligned line. Returns ("", false) when rv is nil (restart not
// attempted, e.g. the daemon was inactive). warn is true for any non-success outcome so
// the caller can style it as a warning WITHOUT ever changing the upgrade exit code.
func summarizeRestartVerify(rv *RestartVerifyResult, version string) (line string, warn bool) {
	if rv == nil {
		return "", false
	}
	switch {
	case rv.Err != nil:
		return "Daemon: WARNING - restart failed: " + rv.Err.Error() +
			" (the daemon may still run the old binary; restart it manually)", true
	case rv.LockPathUnknown:
		return "Daemon: WARNING - config unreadable; daemon restart deferred - " +
			"restart when the config is readable or it stays on the old binary", true
	case rv.BackupWaitTimedOut:
		return "Daemon: WARNING - a backup is running; daemon restart deferred - " +
			"restart when idle or it stays on the old binary", true
	case rv.TimedOut:
		return "Daemon: WARNING - restarted but alignment check timeout", true
	case rv.Restarted && rv.ProcessAlive && rv.Aligned && rv.FreshInfo:
		if v := strings.TrimSpace(version); v != "" {
			return "Daemon: restarted, now aligned (v" + v + ")", false
		}
		return "Daemon: restarted, now aligned", false
	default:
		return "Daemon: WARNING - restarted but alignment could not be confirmed", true
	}
}
