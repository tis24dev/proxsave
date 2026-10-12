package main

import (
	"bufio"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// A missing callback or one run after bootstrap exposes the old runtime to the check.
func TestHealthcheckCLIInstallerPreparationPrecedesDisabledBootstrap(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	var order []string
	healthcheckSetupBuildBootstrap = func(context.Context, string, string) (orchestrator.HealthcheckSetupBootstrap, error) {
		order = append(order, "bootstrap")
		return orchestrator.HealthcheckSetupBootstrap{Eligibility: orchestrator.HealthcheckSetupSkipDisabled}, nil
	}
	err := runHealthcheckSetupCLI(context.Background(), bufio.NewReader(strings.NewReader("")), "/base", "/cfg", nil, installer.HealthcheckSetupOptions{BeforeBootstrap: func(context.Context) error {
		order = append(order, "prepare")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"prepare", "bootstrap"}) {
		t.Fatalf("order = %v", order)
	}
}

func TestHealthcheckCLIPreparationCancellationStopsBootstrap(t *testing.T) {
	stubHealthcheckSetupCLIDeps(t)
	healthcheckSetupBuildBootstrap = func(context.Context, string, string) (orchestrator.HealthcheckSetupBootstrap, error) {
		t.Fatal("bootstrap ran after cancellation")
		return orchestrator.HealthcheckSetupBootstrap{}, nil
	}
	err := runHealthcheckSetupCLI(context.Background(), nil, "/base", "/cfg", nil, installer.HealthcheckSetupOptions{BeforeBootstrap: func(context.Context) error { return context.Canceled }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
