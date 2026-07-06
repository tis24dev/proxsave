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

	// Real operational state from the daemon status file (never fatal to the check).
	if st, derr := healthcheckSetupLoadStatus(baseDir); derr == nil {
		res.Daemon = health.Diagnose(st, heartbeatInterval, healthcheckSetupNow())
		res.DaemonRead = true
	}

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
