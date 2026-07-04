package install

import (
	"context"
	"testing"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func TestRunHealthcheckSetup(t *testing.T) {
	d := newDriver(t)

	origBootstrap := healthcheckBuildBootstrap
	origCheck := healthcheckCheck
	t.Cleanup(func() {
		healthcheckBuildBootstrap = origBootstrap
		healthcheckCheck = origCheck
	})

	healthcheckBuildBootstrap = func(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{
			Eligibility:   orchestrator.HealthcheckSetupEligibleCentralized,
			ServerID:      "12345678",
			ServerAPIHost: "https://h",
		}, nil
	}

	type result struct {
		res installer.HealthcheckSetupResult
		err error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			res, err := RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env")
			resCh <- result{res, err}
		}()
	}

	// Skip path.
	ask()
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("down enter") // Skip
	res := <-resCh
	if res.err != nil || !res.res.Shown || !res.res.SkippedVerification {
		t.Fatalf("skip path: %+v", res)
	}

	// Verified path: Check succeeds + returns the magic-link, then Continue.
	healthcheckCheck = func(ctx context.Context, host, id, baseDir string) orchestrator.HealthcheckCheckResult {
		return orchestrator.HealthcheckCheckResult{Err: nil, Reachable: true, LoginURL: "https://hc/accounts/check_token/u/MAGIC/"}
	}
	ask()
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("enter") // Check
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("down enter") // Continue (verified)
	res = <-resCh
	if res.err != nil || !res.res.Verified || res.res.SkippedVerification {
		t.Fatalf("verified path: %+v", res)
	}
	if res.res.CheckAttempts != 1 || !res.res.MagicLinkSeen {
		t.Fatalf("check bookkeeping / magic-link: %+v", res.res)
	}

	// Fatal path: Check returns a fatal error -> Check item removed, only Skip.
	healthcheckCheck = func(ctx context.Context, host, id, baseDir string) orchestrator.HealthcheckCheckResult {
		return orchestrator.HealthcheckCheckResult{Err: health.ErrHCAuth}
	}
	ask()
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("enter") // Check (fatal)
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("enter") // now only Skip is offered
	res = <-resCh
	if res.err != nil || res.res.Verified || !res.res.LastFatal {
		t.Fatalf("fatal path: %+v", res)
	}

	// Not eligible: no screen, Shown=false.
	healthcheckBuildBootstrap = func(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupSkipDisabled}, nil
	}
	notShown, err := RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env")
	if err != nil || notShown.Shown {
		t.Fatalf("not-eligible must be silent: %+v err=%v", notShown, err)
	}
}
