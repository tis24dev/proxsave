package orchestrator

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestDispatchEarlyErrorNotification_ReturnsNilWhenNoError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	orch := &Orchestrator{logger: logger}

	if got := orch.DispatchEarlyErrorNotification(context.Background(), nil); got != nil {
		t.Fatalf("expected nil stats for nil early error, got %+v", got)
	}

	early := &EarlyErrorState{
		Phase:     "config",
		Error:     nil,
		ExitCode:  types.ExitConfigError,
		Timestamp: time.Unix(1700000000, 0),
	}
	if got := orch.DispatchEarlyErrorNotification(context.Background(), early); got != nil {
		t.Fatalf("expected nil stats for early error without Error, got %+v", got)
	}
}

func TestDispatchEarlyErrorNotification_PopulatesMinimalStats(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	ts := time.Unix(1700000000, 0)
	orch := &Orchestrator{
		logger:         logger,
		version:        "1.2.3",
		proxmoxVersion: "8.1",
	}
	early := &EarlyErrorState{
		Phase:     "config",
		Error:     errors.New("boom"),
		ExitCode:  types.ExitConfigError,
		Timestamp: ts,
	}

	stats := orch.DispatchEarlyErrorNotification(context.Background(), early)
	if stats == nil {
		t.Fatalf("expected stats, got nil")
	}
	if stats.ExitCode != types.ExitConfigError.Int() {
		t.Fatalf("ExitCode=%d; want %d", stats.ExitCode, types.ExitConfigError.Int())
	}
	if stats.ErrorCount != 1 || stats.LocalStatus != "error" {
		t.Fatalf("ErrorCount/LocalStatus=%d/%q; want 1/%q", stats.ErrorCount, stats.LocalStatus, "error")
	}
	if stats.StartTime != ts || stats.EndTime != ts || stats.Timestamp != ts {
		t.Fatalf("timestamps not propagated: start=%v end=%v ts=%v", stats.StartTime, stats.EndTime, stats.Timestamp)
	}
	if stats.Version != "1.2.3" || stats.ProxmoxVersion != "8.1" {
		t.Fatalf("version fields=%q/%q; want %q/%q", stats.Version, stats.ProxmoxVersion, "1.2.3", "8.1")
	}
	if stats.LocalStatusSummary == "" || !strings.Contains(stats.LocalStatusSummary, "boom") {
		t.Fatalf("LocalStatusSummary=%q; want to contain error text", stats.LocalStatusSummary)
	}
	if stats.Hostname == "" {
		t.Fatalf("Hostname is empty")
	}
}
