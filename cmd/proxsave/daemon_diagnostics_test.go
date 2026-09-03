package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectDaemonDiagnosticsBuildsOneSharedSnapshot(t *testing.T) {
	origInstalled := daemonStatusUnitInstalledProbe
	origActive := daemonStatusActiveStateProbe
	origPresence := daemonPresenceProbe
	origNow := daemonStatusNow
	t.Cleanup(func() {
		daemonStatusUnitInstalledProbe = origInstalled
		daemonStatusActiveStateProbe = origActive
		daemonPresenceProbe = origPresence
		daemonStatusNow = origNow
	})

	daemonStatusUnitInstalledProbe = func() bool { return true }
	daemonStatusActiveStateProbe = func(context.Context) string { return "active" }
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}
	daemonStatusNow = func() time.Time { return time.Unix(1_700_000_000, 0) }

	cfg := &config.Config{
		SchedulerMode:                "daemon",
		BaseDir:                      t.TempDir(),
		HealthcheckHeartbeatInterval: time.Minute,
	}
	got := collectDaemonDiagnostics(context.Background(), cfg, cfg.BaseDir)

	if got.Mode != "daemon" || got.Unit != "installed" || got.Active != "active" {
		t.Fatalf("incomplete shared snapshot: %+v", got)
	}
	if !got.State.Probed || !got.State.Installed || !got.State.Active {
		t.Fatalf("health state did not use the shared presence probe: %+v", got.State)
	}
	if got.Keyword == "" || got.Explanation == "" {
		t.Fatalf("shared snapshot is missing its verdict: %+v", got)
	}
}

func TestRunDaemonStatusUsesSharedDiagnosticsCollector(t *testing.T) {
	origCollector := daemonDiagnosticsCollector
	t.Cleanup(func() { daemonDiagnosticsCollector = origCollector })

	called := 0
	daemonDiagnosticsCollector = func(context.Context, *config.Config, string) daemonDiagnostics {
		called++
		return daemonDiagnostics{
			Mode:    "daemon",
			Unit:    "installed",
			Active:  "active",
			State:   health.DaemonState{HaveInfo: true, Version: "1.2.3", Commit: "abc", AlignChecked: true, Aligned: true},
			Level:   1,
			Keyword: "running",
		}
	}

	origLogger := logging.GetDefaultLogger()
	logger := logging.New(types.LogLevelDebug, false)
	logging.SetDefaultLogger(logger)
	t.Cleanup(func() { logging.SetDefaultLogger(origLogger) })

	rt := &appRuntime{
		ctx:    context.Background(),
		cfg:    &config.Config{SchedulerMode: "daemon", BaseDir: t.TempDir()},
		logger: logger,
	}
	if code := runDaemonStatus(rt); code != 0 {
		t.Fatalf("runDaemonStatus exit = %d, want 0", code)
	}
	if called != 1 {
		t.Fatalf("shared collector calls = %d, want 1", called)
	}
}
