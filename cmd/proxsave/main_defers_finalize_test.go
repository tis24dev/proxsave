package main

import (
	"context"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/types"
)

// The deferred support email must run with a fresh, non-cancelled ctx even when
// the run ctx (rt.ctx) was cancelled by SIGINT/SIGTERM. Otherwise an aborted run
// silently skips the support email instead of notifying the user.
func TestSendDeferredSupportEmailUsesFreshCtx(t *testing.T) {
	orig := emitSupportEmail
	t.Cleanup(func() { emitSupportEmail = orig })

	var gotErr error
	called := false
	emitSupportEmail = func(ctx context.Context, _ *config.Config, _ *logging.Logger, _ types.ProxmoxType, _ *orchestrator.BackupStats, _ support.Meta) {
		called = true
		gotErr = ctx.Err()
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // simulate SIGINT/SIGTERM cancelling the run ctx

	rt := &appRuntime{
		ctx:     cancelledCtx,
		args:    &cli.Args{Support: true},
		cfg:     &config.Config{},
		logger:  logging.New(types.LogLevelError, false),
		envInfo: &environment.EnvironmentInfo{},
	}
	state := &appRunState{pendingSupportStat: &orchestrator.BackupStats{}}

	sendDeferredSupportEmail(rt, state)

	if !called {
		t.Fatal("emitSupportEmail was not called")
	}
	if gotErr != nil {
		t.Fatalf("finalization must use a non-cancelled ctx; ctx.Err()=%v", gotErr)
	}
}
