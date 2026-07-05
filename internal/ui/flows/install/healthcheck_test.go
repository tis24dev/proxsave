package install

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func TestBuildHealthcheckPrompt(t *testing.T) {
	link := "https://hc.proxsave.dev/l/Nr4vAebz5b"

	// Verified: green "✓ VERIFIED", the link boxed.
	v := buildHealthcheckPrompt(link, "ready", true, false)
	if !strings.Contains(ansi.Strip(v), "\u2713 VERIFIED") {
		t.Fatalf("verified keyword missing: %q", ansi.Strip(v))
	}
	if !strings.Contains(v, "34;197;94") { // theme.Green SGR
		t.Fatalf("VERIFIED must be green")
	}
	if !strings.Contains(ansi.Strip(v), "╭") || !strings.Contains(ansi.Strip(v), link) {
		t.Fatalf("magic-link must be boxed: %q", ansi.Strip(v))
	}

	// Fatal: red "✗ FAILED".
	f := buildHealthcheckPrompt(link, "bad creds", false, true)
	if !strings.Contains(ansi.Strip(f), "\u2717 FAILED") {
		t.Fatalf("failed keyword missing: %q", ansi.Strip(f))
	}
	if !strings.Contains(f, "239;68;68") { // theme.Red SGR
		t.Fatalf("FAILED must be red")
	}

	// No link -> no box; neutral status shows the message verbatim.
	n := ansi.Strip(buildHealthcheckPrompt("", "Not checked yet.", false, false))
	if strings.Contains(n, "╭") {
		t.Fatalf("no link must render no box: %q", n)
	}
	if !strings.Contains(n, "Not checked yet.") {
		t.Fatalf("neutral status missing: %q", n)
	}
}

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
