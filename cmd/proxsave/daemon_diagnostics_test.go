package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestComparePersonalScriptActualInspectionTransitions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		startupMode   os.FileMode
		currentMode   os.FileMode
		removeSetting bool
		startupState  personalScriptState
		currentState  personalScriptState
		want          personalScriptSynchronization
	}{
		{"accepted to refused", 0o700, 0o720, false, personalScriptReady, personalScriptRefused, personalScriptPathStateChanged},
		{"refused to accepted", 0o720, 0o700, false, personalScriptRefused, personalScriptReady, personalScriptPathStateChanged},
		{"refused to empty setting", 0o720, 0o720, true, personalScriptRefused, personalScriptNotConfigured, personalScriptConfigurationDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := writePersonalScript(t, dir, "transition.sh", "exit 0")
			configured := " \t" + dir + "/./transition.sh  "
			if err := os.Chmod(script, tc.startupMode); err != nil {
				t.Fatal(err)
			}
			running := inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", configured, os.Geteuid())
			if err := os.Chmod(script, tc.currentMode); err != nil {
				t.Fatal(err)
			}
			currentPath := script
			if tc.removeSetting {
				configured, currentPath = "", ""
			}
			current := inspectPersonalScript("PERSONAL_SCRIPT_PRE_RUN", configured, os.Geteuid())
			startupMatches := running.State == tc.startupState || (tc.startupState == personalScriptReady && running.State == personalScriptReadyWithWarning)
			currentMatches := current.State == tc.currentState || (tc.currentState == personalScriptReady && current.State == personalScriptReadyWithWarning)
			if !startupMatches || !currentMatches {
				t.Fatalf("unexpected inspection states: running=%+v current=%+v", running, current)
			}
			if running.Path != script || current.Path != currentPath {
				t.Errorf("configured paths lost: running=%q current=%q; want %q and %q", running.Path, current.Path, script, currentPath)
			}
			for _, diagnostic := range []personalScriptDiagnostic{running, current} {
				if diagnostic.State == personalScriptRefused && applyPersonalScriptDiagnostic(diagnostic) != "" {
					t.Error("refused inspection survived execution gate")
				}
			}
			runtime := daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: "/daemon.env"}
			got := comparePersonalScript(runtime, "/daemon.env", running, current)
			if got.Synchronization != tc.want {
				t.Errorf("synchronization = %q, want %q", got.Synchronization, tc.want)
			}
		})
	}
}

func TestResolveDaemonRuntimeRequiresLiveMatchingIdentity(t *testing.T) {
	original := daemonRuntimeReader
	t.Cleanup(func() { daemonRuntimeReader = original })

	record := health.DaemonRuntimeState{
		SchemaVersion: health.DaemonRuntimeSchemaVersion,
		PID:           42, StartTS: 100, ConfigPath: "/daemon.env", DaemonUID: 0,
		PersonalScripts: health.DaemonRuntimeScripts{
			Pre:  health.DaemonRuntimeScript{Path: "/pre", State: "ready"},
			Post: health.DaemonRuntimeScript{State: "not-configured"},
		},
	}
	daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) {
		return record, true, nil
	}

	runtime, scripts := resolveDaemonRuntime(
		health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
		"/base",
	)
	if runtime.Availability != daemonRuntimeAvailable || scripts.Pre.Path != "/pre" {
		t.Fatalf("matching runtime rejected: runtime=%+v scripts=%+v", runtime, scripts)
	}

	stale, _ := resolveDaemonRuntime(
		health.DaemonState{ProcessAlive: true, PID: 43, HaveInfo: true, StartTS: 100},
		"/base",
	)
	if stale.Availability != daemonRuntimeStale {
		t.Fatalf("PID mismatch = %q, want stale", stale.Availability)
	}
}

func TestComparePersonalScriptDistinguishesConfigAndPathDrift(t *testing.T) {
	runtime := daemonRuntimeDiagnostic{
		Availability: daemonRuntimeAvailable,
		ConfigPath:   "/daemon.env",
	}
	running := personalScriptDiagnostic{Path: "/pre", State: personalScriptReady}
	current := running

	if got := comparePersonalScript(runtime, "/daemon.env", running, current); got.Synchronization != personalScriptInSync {
		t.Fatalf("equal comparison = %+v", got)
	}
	if got := comparePersonalScript(runtime, "/current.env", running, current); got.Synchronization != personalScriptConfigurationDrift {
		t.Fatalf("config source drift = %+v", got)
	}
	current = personalScriptDiagnostic{State: personalScriptNotConfigured}
	if got := comparePersonalScript(runtime, "/daemon.env", running, current); got.Synchronization != personalScriptConfigurationDrift {
		t.Fatalf("configured-to-empty drift = %+v", got)
	}
	current = running
	current.State = personalScriptReadyWithWarning
	current.Reason = "ownership changed"
	if got := comparePersonalScript(runtime, "/daemon.env", running, current); got.Synchronization != personalScriptPathStateChanged {
		t.Fatalf("path-state drift = %+v", got)
	}
}

func TestResolveDaemonRuntimeClassifiesDegradedStates(t *testing.T) {
	matching := health.DaemonRuntimeState{
		SchemaVersion: health.DaemonRuntimeSchemaVersion,
		PID:           42,
		StartTS:       100,
		PersonalScripts: health.DaemonRuntimeScripts{
			Pre:  health.DaemonRuntimeScript{State: "not-configured"},
			Post: health.DaemonRuntimeScript{State: "not-configured"},
		},
	}
	tests := []struct {
		name    string
		state   health.DaemonState
		record  health.DaemonRuntimeState
		found   bool
		readErr error
		want    daemonRuntimeAvailability
	}{
		{
			name: "missing", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
			found: false, want: daemonRuntimeMissing,
		},
		{
			name: "malformed", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
			readErr: errors.New("bad json"), want: daemonRuntimeInvalid,
		},
		{
			name: "unsupported", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
			record: func() health.DaemonRuntimeState { r := matching; r.SchemaVersion = 99; return r }(),
			found:  true, want: daemonRuntimeUnsupported,
		},
		{
			name: "PID mismatch", state: health.DaemonState{ProcessAlive: true, PID: 43, HaveInfo: true, StartTS: 100},
			record: matching, found: true, want: daemonRuntimeStale,
		},
		{
			name: "start mismatch", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 101},
			record: matching, found: true, want: daemonRuntimeStale,
		},
		{
			name: "missing identity", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: false},
			record: matching, found: true, want: daemonRuntimeMissing,
		},
		{
			name: "no live daemon", state: health.DaemonState{},
			record: matching, found: true, want: daemonRuntimeNotApplicable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := daemonRuntimeReader
			daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) {
				return tc.record, tc.found, tc.readErr
			}
			t.Cleanup(func() { daemonRuntimeReader = original })
			got, _ := resolveDaemonRuntime(tc.state, "/base")
			if got.Availability != tc.want {
				t.Fatalf("availability = %q, want %q; runtime=%+v", got.Availability, tc.want, got)
			}
		})
	}
}

func TestResolveDaemonRuntimeRejectsUnknownPolicyStates(t *testing.T) {
	original := daemonRuntimeReader
	t.Cleanup(func() { daemonRuntimeReader = original })
	for _, which := range []string{"pre", "post"} {
		t.Run(which, func(t *testing.T) {
			record := health.DaemonRuntimeState{
				SchemaVersion: health.DaemonRuntimeSchemaVersion, PID: 42, StartTS: 100,
				PersonalScripts: health.DaemonRuntimeScripts{
					Pre:  health.DaemonRuntimeScript{Path: "/pre", State: "ready"},
					Post: health.DaemonRuntimeScript{State: "not-configured"},
				},
			}
			if which == "pre" {
				record.PersonalScripts.Pre.State = "future-state"
			} else {
				record.PersonalScripts.Post.State = ""
			}
			daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) { return record, true, nil }
			got, scripts := resolveDaemonRuntime(health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100}, "/base")
			if got.Availability != daemonRuntimeInvalid || got.Reason == "" || !reflect.DeepEqual(scripts, personalScriptsDiagnostics{}) {
				t.Fatalf("unknown policy state exposed as runtime evidence: runtime=%+v scripts=%+v", got, scripts)
			}
		})
	}
}

func TestResolveDaemonRuntimePreservesPolicyEvidence(t *testing.T) {
	original := daemonRuntimeReader
	t.Cleanup(func() { daemonRuntimeReader = original })
	for _, state := range []string{"not-configured", "ready", "ready-with-warning", "refused"} {
		t.Run(state, func(t *testing.T) {
			record := health.DaemonRuntimeState{
				SchemaVersion: health.DaemonRuntimeSchemaVersion, PID: 42, StartTS: 100, ConfigPath: "/daemon.env", DaemonUID: 1234,
				PersonalScripts: health.DaemonRuntimeScripts{
					Pre:  health.DaemonRuntimeScript{Path: "/pre", State: state, Reason: "policy evidence", Components: []health.DaemonRuntimePathComponent{{Path: "/", UID: 0, Mode: uint32(os.ModeDir | 0o755)}}},
					Post: health.DaemonRuntimeScript{State: "not-configured"},
				},
			}
			daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) { return record, true, nil }
			got, scripts := resolveDaemonRuntime(health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100}, "/base")
			want := personalScriptDiagnostic{Key: "PERSONAL_SCRIPT_PRE_RUN", Path: "/pre", State: personalScriptState(state), Reason: "policy evidence", DaemonUID: 1234, Components: []personalScriptPathComponent{{Path: "/", UID: 0, Mode: os.ModeDir | 0o755}}}
			if got.Availability != daemonRuntimeAvailable || got.ConfigPath != "/daemon.env" || got.StartTS != 100 || got.DaemonUID != 1234 || !reflect.DeepEqual(scripts.Pre, want) || scripts.Post.Key != "PERSONAL_SCRIPT_POST_RUN" {
				t.Fatalf("runtime evidence lost: runtime=%+v scripts=%+v", got, scripts)
			}
		})
	}
}

func TestComparePersonalScriptPrioritizesConfigurationOverPathEvidence(t *testing.T) {
	runtime := daemonRuntimeDiagnostic{Availability: daemonRuntimeAvailable, ConfigPath: "/daemon.env"}
	running := personalScriptDiagnostic{Path: "/pre", State: personalScriptReady, Components: []personalScriptPathComponent{{Path: "/pre", UID: 0, Mode: 0o700}}}
	tests := []struct {
		name       string
		configPath string
		current    personalScriptDiagnostic
		want       personalScriptSynchronization
	}{
		{"clean config path", "/./daemon.env", running, personalScriptInSync},
		{"path and evidence", "/daemon.env", personalScriptDiagnostic{Path: "/other", State: personalScriptRefused}, personalScriptConfigurationDrift},
		{"source and evidence", "/other.env", personalScriptDiagnostic{Path: "/pre", State: personalScriptRefused}, personalScriptConfigurationDrift},
		{"reason only", "/daemon.env", personalScriptDiagnostic{Path: "/pre", State: personalScriptReady, Reason: "changed", Components: running.Components}, personalScriptPathStateChanged},
		{"mode only", "/daemon.env", personalScriptDiagnostic{Path: "/pre", State: personalScriptReady, Components: []personalScriptPathComponent{{Path: "/pre", UID: 0, Mode: 0o755}}}, personalScriptPathStateChanged},
		{"owner only", "/daemon.env", personalScriptDiagnostic{Path: "/pre", State: personalScriptReady, Components: []personalScriptPathComponent{{Path: "/pre", UID: 1000, Mode: 0o700}}}, personalScriptPathStateChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := comparePersonalScript(runtime, tc.configPath, running, tc.current)
			if got.Synchronization != tc.want || !reflect.DeepEqual(got.Running, running) || !reflect.DeepEqual(got.Current, tc.current) {
				t.Fatalf("comparison = %+v, want %s with both inputs preserved", got, tc.want)
			}
		})
	}
}

func TestComparePersonalScriptDegradedRuntimeKeepsCurrentProspective(t *testing.T) {
	current := personalScriptDiagnostic{Path: "/current", State: personalScriptReady}
	for _, availability := range []daemonRuntimeAvailability{daemonRuntimeMissing, daemonRuntimeStale, daemonRuntimeInvalid, daemonRuntimeUnsupported, daemonRuntimeNotApplicable} {
		t.Run(string(availability), func(t *testing.T) {
			got := comparePersonalScript(daemonRuntimeDiagnostic{Availability: availability, Reason: "runtime unavailable"}, "/current.env", personalScriptDiagnostic{}, current)
			want := personalScriptRuntimeUnavailable
			if availability == daemonRuntimeNotApplicable {
				want = personalScriptSyncNotApplicable
			}
			if got.Synchronization != want || got.SyncReason == "" || !reflect.DeepEqual(got.Current, current) || got.Running.State != "" {
				t.Fatalf("current inspection promoted to resident evidence: %+v", got)
			}
		})
	}
}

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
	got := collectDaemonDiagnostics(context.Background(), cfg, nil, cfg.BaseDir)

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
	daemonDiagnosticsCollector = func(context.Context, *config.Config, error, string) daemonDiagnostics {
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
