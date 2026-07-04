package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func stubHealthcheckSetupCLIDeps(t *testing.T) {
	t.Helper()
	ob, oc, op := healthcheckSetupBuildBootstrap, healthcheckSetupCheck, healthcheckSetupPromptYesNo
	t.Cleanup(func() {
		healthcheckSetupBuildBootstrap = ob
		healthcheckSetupCheck = oc
		healthcheckSetupPromptYesNo = op
	})
}

func TestRunHealthcheckSetupCLI_SkipWhenNotEligible(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	called := false
	healthcheckSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupSkipDisabled}, nil
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string) orchestrator.HealthcheckCheckResult {
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
	healthcheckSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{
			Eligibility:   orchestrator.HealthcheckSetupEligibleCentralized,
			ServerID:      "123456789012",
			ServerAPIHost: "https://h",
		}, nil
	}
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		return true, nil // "Check now? yes"
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string) orchestrator.HealthcheckCheckResult {
		if host != "https://h" || id != "123456789012" {
			t.Fatalf("check got host=%q id=%q", host, id)
		}
		return orchestrator.HealthcheckCheckResult{Err: nil, Reachable: true, LoginURL: "https://hc/accounts/check_token/u/MAGIC/"}
	}
	out := captureStdout(t, func() {
		if err := runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", logging.NewBootstrapLogger()); err != nil {
			t.Fatalf("err: %v", err)
		}
	})
	if !strings.Contains(out, "https://hc/accounts/check_token/u/MAGIC/") {
		t.Fatalf("must show the magic-link, got: %q", out)
	}
	if !strings.Contains(out, "verified") {
		t.Fatalf("must confirm verified, got: %q", out)
	}
}

func TestRunHealthcheckSetupCLI_DeclineCheck(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	checkCalled := false
	healthcheckSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupEligibleCentralized, ServerID: "1", ServerAPIHost: "h"}, nil
	}
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		return false, nil // decline the check
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string) orchestrator.HealthcheckCheckResult {
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
	healthcheckSetupBuildBootstrap = func(configPath, baseDir string) (orchestrator.HealthcheckSetupBootstrap, error) {
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupEligibleCentralized, ServerID: "1", ServerAPIHost: "h"}, nil
	}
	prompts := 0
	healthcheckSetupPromptYesNo = func(ctx context.Context, r *bufio.Reader, q string, d bool) (bool, error) {
		prompts++
		return true, nil
	}
	healthcheckSetupCheck = func(ctx context.Context, host, id, baseDir string) orchestrator.HealthcheckCheckResult {
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
