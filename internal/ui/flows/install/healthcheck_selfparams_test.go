package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// typeText types s one rune at a time into the focused text field.
func (d *driver) typeText(s string) {
	for _, r := range s {
		d.session.Send(shell.KeyMsg(string(r)))
	}
}

func TestValidateHealthcheckPingURL(t *testing.T) {
	valid := []string{
		"https://hc-ping.com/0c8e3d5e-4a2b-4c1d-9f3a-1234567890ab",
		"http://localhost:8000/ping/abc",
		"https://hc.example.org/a-slug",
	}
	for _, v := range valid {
		if err := validateHealthcheckPingURL(v); err != nil {
			t.Errorf("required: %q must be valid, got %v", v, err)
		}
		if err := validateOptionalHealthcheckPingURL(v); err != nil {
			t.Errorf("optional: %q must be valid, got %v", v, err)
		}
	}

	bad := []string{
		"",                    // empty
		"ftp://hc-ping.com/x", // wrong scheme
		"hc-ping.com/x",       // no scheme
		"https://",            // no host
		"not a url",           // junk
	}
	for _, v := range bad {
		if err := validateHealthcheckPingURL(v); err == nil {
			t.Errorf("required: %q must be rejected", v)
		}
	}

	// Optional accepts empty but still rejects a malformed non-empty value.
	if err := validateOptionalHealthcheckPingURL(""); err != nil {
		t.Errorf("optional empty must be accepted, got %v", err)
	}
	if err := validateOptionalHealthcheckPingURL("   "); err != nil {
		t.Errorf("optional blank must be accepted, got %v", err)
	}
	if err := validateOptionalHealthcheckPingURL("ftp://x/y"); err == nil {
		t.Error("optional must still reject a malformed non-empty URL")
	}
}

func TestRunHealthcheckSelfParams(t *testing.T) {
	d := newDriver(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte(config.DefaultEnvTemplate()), 0o600); err != nil {
		t.Fatal(err)
	}

	aliveURL := "https://hc-ping.com/alive-uuid"
	backupURL := "https://hc-ping.com/backup-uuid"

	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHealthcheckSelfParams(context.Background(), d.session, dir, configPath)
	}()
	d.waitScreen("Healthchecks - your own server parameters")
	d.typeText(aliveURL)
	d.keys("down")
	d.typeText(backupURL)
	// From the Backup row (index 1) skip the 5 optional rows, land on Continue, submit.
	d.keys("down down down down down down enter")

	if err := <-errCh; err != nil {
		t.Fatalf("RunHealthcheckSelfParams: %v", err)
	}

	out, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	params := installer.DeriveHealthcheckSelfParams(string(out))
	if params.AliveURL != aliveURL {
		t.Fatalf("alive URL not written: got %q", params.AliveURL)
	}
	if params.BackupURL != backupURL {
		t.Fatalf("backup URL not written: got %q", params.BackupURL)
	}
	// Optional URLs left blank stay blank.
	if params.UpdatesURL != "" || params.NotifyEmailURL != "" {
		t.Fatalf("blank optional URLs must stay empty: %+v", params)
	}
}

// TestRunHealthcheckSetupSelfBranch drives the self-mode reachability check: an
// EligibleSelf bootstrap swaps the seam to CheckHealthcheckSelfConnection and the
// self classifier, so a reachable alive URL reads REACHABLE and latches Verified.
func TestRunHealthcheckSetupSelfBranch(t *testing.T) {
	d := newDriver(t)

	origBootstrap := healthcheckBuildBootstrap
	origSelf := healthcheckSelfCheck
	t.Cleanup(func() {
		healthcheckBuildBootstrap = origBootstrap
		healthcheckSelfCheck = origSelf
	})

	healthcheckBuildBootstrap = func(ctx context.Context, configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{
			Eligibility:         orchestrator.HealthcheckSetupEligibleSelf,
			HealthcheckMode:     "self",
			HealthcheckAliveURL: "https://hc-ping.com/alive-uuid",
		}, nil
	}
	var gotURL string
	healthcheckSelfCheck = func(ctx context.Context, aliveURL string) orchestrator.HealthcheckCheckResult {
		gotURL = aliveURL
		return orchestrator.HealthcheckCheckResult{Reachable: true}
	}

	type result struct {
		res installer.HealthcheckSetupResult
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		res, err := RunHealthcheckSetup(context.Background(), d.session, t.TempDir(), "/tmp/backup.env", false)
		resCh <- result{res, err}
	}()
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("enter") // Check
	d.waitScreen("Backup monitoring (healthchecks)")
	// The self screen must show the reachability keyword, no magic-link.
	deadline := time.After(uitest.Deadline(10 * time.Second))
	for !strings.Contains(d.buf.String(), "REACHABLE") {
		select {
		case <-deadline:
			t.Fatalf("self check must render REACHABLE")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	d.keys("down enter") // Continue (verified)
	res := <-resCh
	if res.err != nil || !res.res.Verified || res.res.MagicLinkSeen {
		t.Fatalf("self verified path: %+v", res)
	}
	if gotURL != "https://hc-ping.com/alive-uuid" {
		t.Fatalf("self check must receive the bootstrap alive URL, got %q", gotURL)
	}
}
