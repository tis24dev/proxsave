package install

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// The real task must own preparation and complete it before reading eligibility.
func TestHealthcheckPreparationRunsInsideBootstrapTask(t *testing.T) {
	d := newDriver(t)
	orig := healthcheckBuildBootstrap
	t.Cleanup(func() { healthcheckBuildBootstrap = orig })
	var order []string
	type key struct{}
	outer := context.WithValue(context.Background(), key{}, true)
	healthcheckBuildBootstrap = func(ctx context.Context, _, _ string) (orchestrator.HealthcheckSetupBootstrap, error) {
		order = append(order, "bootstrap")
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupSkipDisabled}, nil
	}
	result, err := RunHealthcheckSetup(outer, d.session, "/base", "/cfg", false, installer.HealthcheckSetupOptions{BeforeBootstrap: func(ctx context.Context) error {
		if ctx == outer || ctx.Value(key{}) != true {
			t.Error("preparation did not receive the task context")
		}
		order = append(order, "prepare")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Shown || !reflect.DeepEqual(order, []string{"prepare", "bootstrap"}) {
		t.Fatalf("shown=%v order=%v", result.Shown, order)
	}
}

func TestHealthcheckPreparationCancellationStopsBootstrap(t *testing.T) {
	d := newDriver(t)
	orig := healthcheckBuildBootstrap
	t.Cleanup(func() { healthcheckBuildBootstrap = orig })
	healthcheckBuildBootstrap = func(context.Context, string, string) (orchestrator.HealthcheckSetupBootstrap, error) {
		t.Error("bootstrap ran after cancellation")
		return orchestrator.HealthcheckSetupBootstrap{}, nil
	}
	_, err := RunHealthcheckSetup(context.Background(), d.session, "/base", "/cfg", false, installer.HealthcheckSetupOptions{BeforeBootstrap: func(context.Context) error { return context.Canceled }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestHealthcheckPreparationIsNotRepeatedBySelfRecheck(t *testing.T) {
	d := newDriver(t)
	originalBootstrap, originalCheck := healthcheckBuildBootstrap, healthcheckSelfCheck
	t.Cleanup(func() { healthcheckBuildBootstrap = originalBootstrap; healthcheckSelfCheck = originalCheck })
	prepared, checks := 0, 0
	healthcheckBuildBootstrap = func(context.Context, string, string) (orchestrator.HealthcheckSetupBootstrap, error) {
		if prepared != 1 {
			t.Errorf("bootstrap preparation count=%d", prepared)
		}
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupEligibleSelf, HealthcheckAliveURL: "https://monitor.invalid/alive"}, nil
	}
	healthcheckSelfCheck = func(context.Context, string) orchestrator.HealthcheckCheckResult {
		checks++
		return orchestrator.HealthcheckCheckResult{Reachable: true}
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunHealthcheckSetup(context.Background(), d.session, "/base", "/cfg", false, installer.HealthcheckSetupOptions{BeforeBootstrap: func(context.Context) error { prepared++; return nil }})
		done <- err
	}()
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("enter")
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("enter")
	d.waitScreen("Backup monitoring (healthchecks)")
	d.keys("down enter")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if prepared != 1 || checks != 2 {
		t.Fatalf("preparation=%d checks=%d", prepared, checks)
	}
}

func TestHealthcheckClosedSessionDoesNotStartPreparation(t *testing.T) {
	d := newDriver(t)
	if err := d.session.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err := RunHealthcheckSetup(context.Background(), d.session, "/base", "/cfg", false, installer.HealthcheckSetupOptions{BeforeBootstrap: func(context.Context) error { called = true; return nil }})
	if !errors.Is(err, shell.ErrClosed) {
		t.Fatalf("closed session error=%v", err)
	}
	if called {
		t.Fatal("closed session ran preparation")
	}
}
