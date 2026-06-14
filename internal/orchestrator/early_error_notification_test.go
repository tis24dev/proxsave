package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newEarlyErrorState() *EarlyErrorState {
	return &EarlyErrorState{
		Phase:     "storage_init",
		Error:     errors.New("primary storage mount unavailable"),
		ExitCode:  types.ExitStorageError,
		Timestamp: time.Now(),
	}
}

// TestDispatchEarlyErrorNotification_SendsToRegisteredChannel covers H12: an
// early-init failure must reach the registered notification channels exactly
// once (channels are now registered before the fallible init phases).
func TestDispatchEarlyErrorNotification_SendsToRegisteredChannel(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	stub := &stubNotifierChannel{name: "FakeEarly"}
	o := &Orchestrator{
		logger:               logger,
		cfg:                  &config.Config{},
		notificationChannels: []NotificationChannel{stub},
	}

	stats := o.DispatchEarlyErrorNotification(context.Background(), newEarlyErrorState())
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stub.count != 1 {
		t.Fatalf("expected the registered channel to be notified exactly once, got %d", stub.count)
	}
	if stats.LocalStatus != "error" || stats.ErrorCount != 1 {
		t.Fatalf("expected error stats, got LocalStatus=%q ErrorCount=%d", stats.LocalStatus, stats.ErrorCount)
	}
	if stub.errorCount != 1 {
		t.Fatalf("channel received ErrorCount=%d, want 1", stub.errorCount)
	}
}

// TestDispatchEarlyErrorNotification_DryRunDoesNotSend covers the mandatory
// guardrail: in dry-run no real notification is sent (the normal path already
// gates on !dryRun), but stats are still returned for support/log handling.
func TestDispatchEarlyErrorNotification_DryRunDoesNotSend(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	stub := &stubNotifierChannel{name: "FakeEarly"}
	o := &Orchestrator{
		logger:               logger,
		cfg:                  &config.Config{},
		dryRun:               true,
		notificationChannels: []NotificationChannel{stub},
	}

	stats := o.DispatchEarlyErrorNotification(context.Background(), newEarlyErrorState())
	if stats == nil {
		t.Fatal("dry-run should still return stats")
	}
	if stub.called {
		t.Fatal("dry-run must not send notifications")
	}
}
