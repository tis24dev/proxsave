package orchestrator

import (
	"context"

	"github.com/tis24dev/proxsave/internal/health"
)

// HealthcheckCheckResult is the outcome of one install-time connection check.
// A nil Err with Reachable=true means provisioning is ready AND the monitor is
// reachable from this host. LoginURL carries the portal magic-link whenever the
// server minted one (even if the subsequent reachability ping then failed).
type HealthcheckCheckResult struct {
	Err       error
	LoginURL  string
	Reachable bool
}

// Seams for tests.
var (
	healthcheckSetupFetch = health.FetchCentralizedConfig
	healthcheckSetupPing  = func(ctx context.Context, aliveURL string) error {
		return health.NewReporter(health.Config{}).TestPing(ctx, aliveURL)
	}
)

// CheckHealthcheckConnection runs one install-time check: fetch the centralized
// config (asking for a fresh magic-link) to confirm provisioning is ready, then a
// /log ping to the alive URL to confirm the monitor is reachable from this host.
// The /log ping records a ping WITHOUT flipping the check state. It loads the relay
// secret from baseDir itself, reusing the exact identity/secret/base-URL plumbing
// the daemon uses.
func CheckHealthcheckConnection(ctx context.Context, serverAPIHost, serverID, baseDir string) HealthcheckCheckResult {
	secret := healthcheckSetupLoadSecret(baseDir)
	cfg, err := healthcheckSetupFetch(ctx, nil, serverAPIHost, serverID, secret, true)
	if err != nil {
		return HealthcheckCheckResult{Err: err}
	}
	res := HealthcheckCheckResult{LoginURL: cfg.LoginURL}
	if cfg.AliveURL != "" {
		if perr := healthcheckSetupPing(ctx, cfg.AliveURL); perr != nil {
			res.Err = perr
			return res
		}
		res.Reachable = true
	}
	return res
}
