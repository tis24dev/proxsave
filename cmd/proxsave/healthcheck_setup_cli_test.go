package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func stubHealthcheckSetupCLIDeps(t *testing.T) {
	t.Helper()
	ob, oc, os, op := healthcheckSetupBuildBootstrap, healthcheckSetupCheck, healthcheckSetupSelfCheck, healthcheckSetupPromptYesNo
	t.Cleanup(func() {
		healthcheckSetupBuildBootstrap = ob
		healthcheckSetupCheck = oc
		healthcheckSetupSelfCheck = os
		healthcheckSetupPromptYesNo = op
	})
}

// TestRunHealthcheckSetupCLI_SelfBranch verifies the self path uses the reachability
// seam (not the centralized check), renders REACHABLE with no magic-link, and passes
// the bootstrap alive URL through - the SAME keyword/level the TUI self branch shows.
func TestRunHealthcheckSetupCLI_SelfBranch(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	centralizedCalled := false
	var gotURL string
	healthcheckSetupBuildBootstrap = func(ctx context.Context, configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{
			Eligibility:         orchestrator.HealthcheckSetupEligibleSelf,
			HealthcheckMode:     "self",
			HealthcheckAliveURL: "https://hc-ping.com/alive-uuid",
		}, nil
	}
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		return true, nil // "Check now? yes"
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
		centralizedCalled = true
		return orchestrator.HealthcheckCheckResult{}
	}
	healthcheckSetupSelfCheck = func(ctx context.Context, aliveURL string) orchestrator.HealthcheckCheckResult {
		gotURL = aliveURL
		return orchestrator.HealthcheckCheckResult{Reachable: true}
	}
	out := captureStdout(t, func() {
		if err := runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("err: %v", err)
		}
	})
	if centralizedCalled {
		t.Fatal("self mode must not call the centralized check")
	}
	if gotURL != "https://hc-ping.com/alive-uuid" {
		t.Fatalf("self check got alive URL %q", gotURL)
	}
	if !strings.Contains(out, "REACHABLE") {
		t.Fatalf("self mode must show REACHABLE, got: %q", out)
	}
	if strings.Contains(out, "accounts/check_token") {
		t.Fatalf("self mode must not show a magic-link, got: %q", out)
	}
}

// TestRunHealthcheckSelfParamsCLI collects the ping URLs from scripted stdin
// (alive + backup required, optionals skipped with Enter) and writes them into the
// config via installer.ApplyHealthcheckSelfParams, matching the TUI screen.
func TestRunHealthcheckSelfParamsCLI(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte(config.DefaultEnvTemplate()), 0o600); err != nil {
		t.Fatal(err)
	}
	// alive, backup, then 5 empty lines for the optional URLs (updates + 4 notify).
	script := "https://hc-ping.com/alive-uuid\nhttps://hc-ping.com/backup-uuid\n\n\n\n\n\n"
	_ = captureStdout(t, func() {
		if err := runHealthcheckSelfParamsCLI(context.Background(), bufio.NewReader(strings.NewReader(script)), "/base", configPath, logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("err: %v", err)
		}
	})
	out, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	params := installer.DeriveHealthcheckSelfParams(string(out))
	if params.AliveURL != "https://hc-ping.com/alive-uuid" || params.BackupURL != "https://hc-ping.com/backup-uuid" {
		t.Fatalf("required URLs not written: %+v", params)
	}
	if params.UpdatesURL != "" || params.NotifyEmailURL != "" || params.NotifyWebhookURL != "" {
		t.Fatalf("skipped optionals must stay blank: %+v", params)
	}
}

func TestRunHealthcheckSetupCLI_SkipWhenNotEligible(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	called := false
	healthcheckSetupBuildBootstrap = func(ctx context.Context, configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupSkipDisabled}, nil
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
		called = true
		return orchestrator.HealthcheckCheckResult{}
	}
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		called = true
		return true, nil
	}
	out := captureStdout(t, func() {
		if err := runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("err: %v", err)
		}
	})
	if called {
		t.Fatal("not-eligible must not prompt or check")
	}
	if strings.Contains(out, "Backup monitoring") {
		t.Fatalf("not-eligible must render nothing, got: %q", out)
	}
}

func TestRunHealthcheckSetupCLI_VerifiedShowsMagicLink(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	healthcheckSetupBuildBootstrap = func(ctx context.Context, configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{
			Eligibility:   orchestrator.HealthcheckSetupEligibleCentralized,
			ServerID:      "123456789012",
			ServerAPIHost: "https://h",
		}, nil
	}
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		return true, nil // "Check now? yes"
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
		if host != "https://h" || id != "123456789012" {
			t.Fatalf("check got host=%q id=%q", host, id)
		}
		return orchestrator.HealthcheckCheckResult{
			Err: nil, Reachable: true, LoginURL: "https://hc.proxsave.dev/accounts/check_token/u/MAGIC/",
			DaemonRead: true, Daemon: health.Diagnosis{State: health.TxTransmitting, DaemonUp: true},
		}
	}
	out := captureStdout(t, func() {
		if err := runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("err: %v", err)
		}
	})
	if !strings.Contains(out, "https://hc.proxsave.dev/accounts/check_token/u/MAGIC/") {
		t.Fatalf("must show the magic-link, got: %q", out)
	}
	// The real state is shown, not a cosmetic "verified": a live transmitting daemon reads WORKING.
	if !strings.Contains(out, "WORKING") {
		t.Fatalf("must show the real WORKING state, got: %q", out)
	}
}

func TestRunHealthcheckSetupCLI_DeclineCheck(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	checkCalled := false
	healthcheckSetupBuildBootstrap = func(ctx context.Context, configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupEligibleCentralized, ServerID: "1", ServerAPIHost: "h"}, nil
	}
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		return false, nil // decline the check
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
		checkCalled = true
		return orchestrator.HealthcheckCheckResult{}
	}
	out := captureStdout(t, func() {
		_ = runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", logging.NewBootstrapLogger())
	})
	if checkCalled {
		t.Fatal("declining must not run the check")
	}
	if !strings.Contains(out, "Skipped") {
		t.Fatalf("expected a skip message, got: %q", out)
	}
}

func TestRunHealthcheckSetupCLI_FatalStopsRetry(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	healthcheckSetupBuildBootstrap = func(ctx context.Context, configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupEligibleCentralized, ServerID: "1", ServerAPIHost: "h"}, nil
	}
	prompts := 0
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		prompts++
		return true, nil
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string, hbInterval time.Duration) orchestrator.HealthcheckCheckResult {
		return orchestrator.HealthcheckCheckResult{Err: health.ErrHCAuth} // fatal
	}
	_ = captureStdout(t, func() {
		_ = runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", logging.NewBootstrapLogger())
	})
	// Only the initial "check now?" prompt; a fatal result never offers "check again".
	if prompts != 1 {
		t.Fatalf("fatal must not offer a re-check, prompts=%d", prompts)
	}
}
