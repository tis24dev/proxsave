package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestDeferredSupportEmailGates(t *testing.T) {
	for _, tc := range []struct {
		name        string
		supportMode bool
		hasStats    bool
		alreadySent bool
		wantCalls   int
	}{
		{"normal_run", false, true, false, 0},
		{"no_stats", true, false, false, 0},
		{"already_attempted_in_dashboard", true, true, true, 0},
		{"pending_support_run", true, true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := emitSupportEmail
			t.Cleanup(func() { emitSupportEmail = original })
			calls := 0
			emitSupportEmail = func(context.Context, *config.Config, *logging.Logger, types.ProxmoxType, *orchestrator.BackupStats, support.Meta) {
				calls++
			}
			rt := &appRuntime{args: &cli.Args{Support: tc.supportMode}, envInfo: &environment.EnvironmentInfo{}}
			state := &appRunState{supportEmailSent: tc.alreadySent}
			if tc.hasStats {
				state.pendingSupportStat = &orchestrator.BackupStats{ExitCode: 2}
			}
			sendDeferredSupportEmail(rt, state)
			if calls != tc.wantCalls {
				t.Errorf("support email attempts = %d; want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestDeferredSupportEmailPayloadAndLifetime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		timeout time.Duration
	}{
		{"nil_config", nil, 30 * time.Second},
		{"default_timeout", &config.Config{}, 30 * time.Second},
		{"negative_timeout", &config.Config{WorkerTimeout: -1}, 30 * time.Second},
		{"configured_timeout", &config.Config{WorkerTimeout: 7}, 7 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := emitSupportEmail
			t.Cleanup(func() { emitSupportEmail = original })
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(io.Discard)
			stats := &orchestrator.BackupStats{Hostname: "pbs-test", ExitCode: 2, LogFilePath: "/tmp/support-test.log"}
			meta := support.Meta{GitHubUser: "alice", IssueID: "#42"}
			runCtx, cancel := context.WithCancel(context.Background())
			cancel()
			rt := &appRuntime{
				ctx: runCtx, cfg: tc.cfg, logger: logger,
				args:    &cli.Args{Support: true, SupportGitHubUser: meta.GitHubUser, SupportIssueID: meta.IssueID},
				envInfo: &environment.EnvironmentInfo{Type: types.ProxmoxBS},
			}
			var finalCtx context.Context
			var deadline time.Time
			calls := 0
			emitSupportEmail = func(ctx context.Context, cfg *config.Config, gotLogger *logging.Logger, platform types.ProxmoxType, gotStats *orchestrator.BackupStats, gotMeta support.Meta) {
				calls++
				finalCtx = ctx
				if ctx.Err() != nil {
					t.Errorf("support finalization inherited the cancelled run context: %v", ctx.Err())
				}
				var ok bool
				deadline, ok = ctx.Deadline()
				if !ok {
					t.Error("support finalization has no timeout")
				}
				if cfg != tc.cfg || gotLogger != logger || platform != types.ProxmoxBS || gotStats != stats || gotMeta != meta {
					t.Error("deferred support email lost configuration, log, host identity, stats or issue metadata")
				}
			}
			before := time.Now()
			sendDeferredSupportEmail(rt, &appRunState{pendingSupportStat: stats})
			after := time.Now()
			if calls != 1 || finalCtx == nil {
				t.Fatal("deferred support email was not attempted exactly once")
			}
			if deadline.Before(before.Add(tc.timeout)) || deadline.After(after.Add(tc.timeout)) {
				t.Errorf("finalization deadline = %v; want configured timeout %v", deadline, tc.timeout)
			}
			if finalCtx.Err() != context.Canceled {
				t.Errorf("finalization context was not released after sending: %v", finalCtx.Err())
			}
		})
	}
}
