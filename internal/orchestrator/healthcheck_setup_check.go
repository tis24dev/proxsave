package orchestrator

import (
	"context"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
)

// HealthcheckCheckResult is the outcome of one install-time connection check.
// A nil Err with Reachable=true means provisioning is ready AND the monitor is
// reachable from this host. LoginURL carries the portal magic-link whenever the
// server minted one (even if the subsequent reachability ping then failed).
//
// Daemon* carry the REAL operational state read from the on-disk status file (the
// SAME source the run-start init check and the Phase-7 section use): the check thus
// reports whether monitoring is actually running, not just one-shot reachability.
type HealthcheckCheckResult struct {
	Err       error
	LoginURL  string
	Reachable bool

	Daemon     health.Diagnosis // daemon liveness/transmission diagnosed from the status file
	DaemonRead bool             // false only if the status file existed but could not be read

	// RawStatus is the on-disk status snapshot Daemon was diagnosed from, exposed so the
	// install check screen can render the per-sensor list with no second read. HaveStatus
	// is false when the status file was absent/unreadable (RawStatus is then the zero value).
	RawStatus  health.Status
	HaveStatus bool
}

// Seams for tests.
var (
	healthcheckSetupFetch = health.FetchCentralizedConfig
	healthcheckSetupPing  = func(ctx context.Context, aliveURL string) error {
		return health.NewReporter(health.Config{}).TestPing(ctx, aliveURL)
	}
	healthcheckSetupLoadStatus = health.LoadStatus
	healthcheckSetupNow        = time.Now
)

// DaemonPresenceProbe reports the daemon's authoritative systemd-level existence
// (installed + active). cmd/proxsave sets it at startup (the systemd unit helpers live
// there); when nil the checks fall back to a heartbeat-only diagnosis. This is what lets
// the checks tell "daemon not installed" / "not running" / "running but not reporting"
// apart instead of flattening everything to a heartbeat-derived "daemon not running".
var DaemonPresenceProbe func(context.Context) health.DaemonPresence

// probeDaemonPresence returns the injected systemd verdict, or an unprobed presence when
// no probe is wired (health.RefineWithPresence then leaves the diagnosis untouched).
func probeDaemonPresence(ctx context.Context) health.DaemonPresence {
	if DaemonPresenceProbe == nil {
		return health.DaemonPresence{}
	}
	return DaemonPresenceProbe(ctx)
}

// CheckHealthcheckConnection runs one install-time check: fetch the centralized
// config (asking for a fresh magic-link) to confirm provisioning is ready, then a
// /log ping to the alive URL to confirm the monitor is reachable from this host. It
// then reads the daemon status file and diagnoses the REAL operational state (is the
// daemon running and actually transmitting?), so the reported status mirrors what the
// run says instead of a one-shot "verified". The /log ping records a ping on the
// MONITOR side WITHOUT touching the local status file (that is daemon-only). It loads
// the relay secret from baseDir itself, reusing the exact identity/secret/base-URL
// plumbing the daemon uses.
func CheckHealthcheckConnection(ctx context.Context, serverAPIHost, serverID, baseDir string, heartbeatInterval time.Duration) HealthcheckCheckResult {
	res := HealthcheckCheckResult{}

	// Real operational state: the status file answers "is it transmitting?", systemd
	// answers "does the process exist and run?". Probe both so a running-but-silent daemon
	// is not misreported as down. A readable file OR a probed systemd state yields a
	// verdict; only when BOTH are unavailable is the daemon state genuinely unknown.
	presence := probeDaemonPresence(ctx)
	base := health.Diagnosis{State: health.TxNoHeartbeat}
	fileOK := false
	if st, derr := healthcheckSetupLoadStatus(baseDir); derr == nil {
		base = health.Diagnose(st, heartbeatInterval, healthcheckSetupNow())
		res.RawStatus = st
		res.HaveStatus = true
		fileOK = true
	}
	res.Daemon = health.RefineWithPresence(base, presence)
	res.DaemonRead = fileOK || presence.Probed

	secret := healthcheckSetupLoadSecret(baseDir)
	cfg, err := healthcheckSetupFetch(ctx, nil, serverAPIHost, serverID, secret, true)
	if err != nil {
		res.Err = err
		return res
	}
	res.LoginURL = cfg.LoginURL
	if cfg.AliveURL != "" {
		if perr := healthcheckSetupPing(ctx, cfg.AliveURL); perr != nil {
			res.Err = perr
			return res
		}
		res.Reachable = true
	}
	return res
}
