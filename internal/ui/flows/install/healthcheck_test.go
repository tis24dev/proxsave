package install

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func healthcheckEligibleBootstrap(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
	return orchestrator.HealthcheckSetupBootstrap{
		Eligibility:   orchestrator.HealthcheckSetupEligibleCentralized,
		ServerID:      "12345678",
		ServerAPIHost: "https://h",
	}, nil
}

// In dashboard mode (backToMenu=true) the leave action is labeled "Back" (return to
// the menu) instead of the install-flow "Skip"/"Continue".
func TestRunHealthcheckSetupDashboardBack(t *testing.T) {
	d := newDriver(t)
	orig := healthcheckBuildBootstrap
	t.Cleanup(func() { healthcheckBuildBootstrap = orig })
	healthcheckBuildBootstrap = healthcheckEligibleBootstrap

	resCh := make(chan struct{}, 1)
	go func() {
		_, _ = RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", true)
		resCh <- struct{}{}
	}()
	d.waitScreen("Backup monitoring (healthchecks)")
	deadline := time.After(10 * time.Second)
	for !strings.Contains(ansi.Strip(d.buf.String()), "return to the dashboard menu") {
		select {
		case <-deadline:
			t.Fatalf("dashboard mode must render a Back item:\n%s", ansi.Strip(d.buf.String()))
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	d.keys("down enter") // Back -> returns to the menu
	<-resCh
}

// Ctrl+C on a dashboard diagnostic is a global interrupt: it terminates the session,
// so the screen resolves via shell.IsAbort and returns without an error (the dead
// session is what exits the dashboard loop).
func TestRunHealthcheckSetupCtrlCInterrupts(t *testing.T) {
	d := newDriver(t)
	orig := healthcheckBuildBootstrap
	t.Cleanup(func() { healthcheckBuildBootstrap = orig })
	healthcheckBuildBootstrap = healthcheckEligibleBootstrap

	resCh := make(chan error, 1)
	go func() {
		_, err := RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", true)
		resCh <- err
	}()
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("ctrl+c")
	if err := <-resCh; err != nil {
		t.Fatalf("ctrl+c must resolve via the abort path (nil err), got %v", err)
	}
}

func TestBuildHealthcheckPrompt(t *testing.T) {
	link := "https://hc.proxsave.dev/l/Nr4vAebz5b"

	// Ok level: green "✓ WORKING", the explanation on its own line, the link boxed.
	v := buildHealthcheckPrompt(link, "WORKING", "It is reporting.", orchestrator.HealthcheckSetupLevelOk)
	if !strings.Contains(ansi.Strip(v), "✓ WORKING") {
		t.Fatalf("working keyword missing: %q", ansi.Strip(v))
	}
	if !strings.Contains(v, "34;197;94") { // theme.Green SGR
		t.Fatalf("WORKING must be green")
	}
	if !strings.Contains(ansi.Strip(v), "It is reporting.") {
		t.Fatalf("explanation must render on the second line: %q", ansi.Strip(v))
	}
	if !strings.Contains(ansi.Strip(v), "╭") || !strings.Contains(ansi.Strip(v), link) {
		t.Fatalf("magic-link must be boxed: %q", ansi.Strip(v))
	}

	// Error level: red "✗ REJECTED".
	f := buildHealthcheckPrompt(link, "REJECTED", "bad creds", orchestrator.HealthcheckSetupLevelError)
	if !strings.Contains(ansi.Strip(f), "✗ REJECTED") {
		t.Fatalf("error keyword missing: %q", ansi.Strip(f))
	}
	if !strings.Contains(f, "239;68;68") { // theme.Red SGR
		t.Fatalf("REJECTED must be red")
	}

	// Warn level (a real post-check warning): yellow "⚠ NOT RUNNING" (with the triangle).
	w := buildHealthcheckPrompt(link, "NOT RUNNING", "daemon down", orchestrator.HealthcheckSetupLevelWarn)
	if !strings.Contains(ansi.Strip(w), "⚠ NOT RUNNING") {
		t.Fatalf("warn keyword missing: %q", ansi.Strip(w))
	}
	if !strings.Contains(w, "234;179;8") { // theme.Yellow SGR (#EAB308)
		t.Fatalf("NOT RUNNING must be yellow")
	}

	// Neutral level (pre-check): yellow "NOT CHECKED" with NO triangle - like upgrade/telegram.
	nn := buildHealthcheckPrompt(link, "NOT CHECKED", "Choose Check.", orchestrator.HealthcheckSetupLevelNeutral)
	if !strings.Contains(ansi.Strip(nn), "NOT CHECKED") {
		t.Fatalf("neutral keyword missing: %q", ansi.Strip(nn))
	}
	if strings.ContainsAny(ansi.Strip(nn), "✓✗⚠") {
		t.Fatalf("pre-check NOT CHECKED must carry NO triangle: %q", ansi.Strip(nn))
	}
	if !strings.Contains(nn, "234;179;8") { // yellow
		t.Fatalf("NOT CHECKED must be yellow")
	}

	// No link -> no box; the explanation still renders verbatim.
	n := ansi.Strip(buildHealthcheckPrompt("", "NOT CHECKED", "Choose Check.", orchestrator.HealthcheckSetupLevelNeutral))
	if strings.Contains(n, "╭") {
		t.Fatalf("no link must render no box: %q", n)
	}
	if !strings.Contains(n, "Choose Check.") {
		t.Fatalf("explanation missing: %q", n)
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
			res, err := RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", false)
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

	// Verified path: Check reaches the monitor + returns the magic-link, then Continue.
	// The daemon reads as transmitting -> the headline is WORKING.
	healthcheckCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
		return orchestrator.HealthcheckCheckResult{
			Err: nil, Reachable: true, LoginURL: "https://hc/accounts/check_token/u/MAGIC/",
			DaemonRead: true, Daemon: health.Diagnosis{State: health.TxTransmitting, DaemonUp: true},
		}
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
	healthcheckCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
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
	notShown, err := RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", false)
	if err != nil || notShown.Shown {
		t.Fatalf("not-eligible must be silent: %+v err=%v", notShown, err)
	}
}
