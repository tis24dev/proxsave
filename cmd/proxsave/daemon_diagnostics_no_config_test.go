package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// runDashboardDaemonStatus leaves cfg nil when the config file cannot be loaded,
// and the whole diagnostic then stated two falsehoods about a daemon that was
// fine: the current side read NOT CONFIGURED (which asserts the settings were
// empty), and the comparison measured the daemon's real config path against ""
// and told the operator to restart the daemon. The sibling screen twenty lines
// away already refuses to gloss this - runDashboardDaemonAdmin renders "CONFIG
// UNREADABLE" - so the status screen names it too.
func TestInspectPersonalScriptsWithoutAConfigIsUnknownNotEmpty(t *testing.T) {
	got := inspectPersonalScripts(nil, 4242)
	for _, tc := range []struct {
		label      string
		diagnostic personalScriptDiagnostic
		wantKey    string
	}{
		{"pre", got.Pre, "PERSONAL_SCRIPT_PRE_RUN"},
		{"post", got.Post, "PERSONAL_SCRIPT_POST_RUN"},
	} {
		if tc.diagnostic.State == personalScriptNotConfigured {
			t.Fatalf("%s: an unreadable config must not claim the setting was empty: %+v", tc.label, tc.diagnostic)
		}
		if tc.diagnostic.State != personalScriptUnknown || tc.diagnostic.Reason != personalScriptConfigUnreadable {
			t.Fatalf("%s: state/reason = %q/%q, want unknown with the shared reason", tc.label, tc.diagnostic.State, tc.diagnostic.Reason)
		}
		if tc.diagnostic.Key != tc.wantKey || tc.diagnostic.DaemonUID != 4242 || tc.diagnostic.Path != "" {
			t.Fatalf("%s: evidence lost: %+v", tc.label, tc.diagnostic)
		}
	}
}

// The unknown current side wins over every runtime verdict, including the healthy
// one: with nothing to compare against, no synchronization statement is available,
// and the config-path arm would otherwise blame the daemon.
func TestComparePersonalScriptWithoutACurrentConfigNeverBlamesTheDaemon(t *testing.T) {
	current := unknownPersonalScript("PERSONAL_SCRIPT_PRE_RUN", 0, nil)
	running := personalScriptDiagnostic{Path: "/pre", State: personalScriptReady}
	for _, availability := range []daemonRuntimeAvailability{
		daemonRuntimeAvailable, daemonRuntimeMissing, daemonRuntimeStale,
		daemonRuntimeInvalid, daemonRuntimeUnsupported, daemonRuntimeNotApplicable,
	} {
		t.Run(string(availability), func(t *testing.T) {
			runtime := daemonRuntimeDiagnostic{Availability: availability, ConfigPath: "/daemon.env", Reason: "runtime reason"}
			got := comparePersonalScript(runtime, "", running, current)
			if got.Synchronization == personalScriptConfigurationDrift {
				t.Fatalf("an unreadable config was reported as a daemon restart being due: %+v", got)
			}
			if got.Synchronization != personalScriptCurrentUnavailable {
				t.Fatalf("synchronization = %q, want %q", got.Synchronization, personalScriptCurrentUnavailable)
			}
			if got.SyncReason != personalScriptConfigUnreadable {
				t.Fatalf("sync reason = %q, want the unreadable-config reason", got.SyncReason)
			}
		})
	}
}

// Both renderers share the state vocabulary; a state neither of them names falls
// through to the NOT CONFIGURED arm, which is the lie this fix removes.
func TestDaemonDiagnosticsRenderersNameTheUnknownCurrentState(t *testing.T) {
	diagnostics := daemonDiagnostics{
		Mode:    "unknown",
		Unit:    "installed",
		Active:  "active",
		Keyword: "running",
		Runtime: daemonRuntimeDiagnostic{Availability: daemonRuntimeNotApplicable, Reason: "daemon process is not live"},
		ScriptComparisons: personalScriptComparisons{
			Pre:  comparePersonalScript(daemonRuntimeDiagnostic{Availability: daemonRuntimeNotApplicable}, "", personalScriptDiagnostic{}, unknownPersonalScript("PERSONAL_SCRIPT_PRE_RUN", 0, nil)),
			Post: comparePersonalScript(daemonRuntimeDiagnostic{Availability: daemonRuntimeNotApplicable}, "", personalScriptDiagnostic{}, unknownPersonalScript("PERSONAL_SCRIPT_POST_RUN", 0, nil)),
		},
	}

	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logDaemonDiagnostics(logger, diagnostics)
	out := buf.String()

	if strings.Contains(out, "Current configuration: NOT CONFIGURED") {
		t.Fatalf("the CLI still claims the settings were empty:\n%s", out)
	}
	for _, want := range []string{
		"Current configuration: UNKNOWN: " + personalScriptConfigUnreadable,
		"Synchronization: UNKNOWN (" + personalScriptConfigUnreadable + ")",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}

	prompt := buildDaemonStatusPrompt(diagnostics)
	if strings.Contains(prompt, "NOT CONFIGURED") {
		t.Fatalf("the dashboard still claims the settings were empty:\n%s", prompt)
	}
	if strings.Count(prompt, personalScriptConfigUnreadable) != 4 {
		t.Fatalf("reason count = %d, want 4 (state + synchronization, per script):\n%s",
			strings.Count(prompt, personalScriptConfigUnreadable), prompt)
	}
}

// Saying only that the configuration could not be read leaves the operator with
// no file to go and fix, so the loader's own words ride along. The dashboard is
// the one caller that reaches the collector without a config, and it is the one
// that holds the error.
func TestCollectDaemonDiagnosticsCarriesTheConfigLoadCause(t *testing.T) {
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

	cause := errors.New("open /etc/proxsave/backup.env: permission denied")
	got := collectDaemonDiagnostics(context.Background(), nil, cause, t.TempDir())

	for _, tc := range []struct {
		label      string
		comparison personalScriptComparison
	}{
		{"pre", got.ScriptComparisons.Pre},
		{"post", got.ScriptComparisons.Post},
	} {
		if !strings.Contains(tc.comparison.Current.Reason, cause.Error()) {
			t.Fatalf("%s: the current state drops the load cause: %q", tc.label, tc.comparison.Current.Reason)
		}
		if !strings.Contains(tc.comparison.SyncReason, cause.Error()) {
			t.Fatalf("%s: the synchronization verdict drops the load cause: %q", tc.label, tc.comparison.SyncReason)
		}
		if tc.comparison.Synchronization != personalScriptCurrentUnavailable {
			t.Fatalf("%s: synchronization = %q", tc.label, tc.comparison.Synchronization)
		}
	}

	// A caller with a config, or one that never attempted a load, must be unchanged.
	clean := collectDaemonDiagnostics(context.Background(), nil, nil, t.TempDir())
	if clean.ScriptComparisons.Pre.Current.Reason != personalScriptConfigUnreadable {
		t.Fatalf("a nil cause invented one: %q", clean.ScriptComparisons.Pre.Current.Reason)
	}
}

// The daemon must never publish the unknown state: it is the reader's verdict
// about a missing config, not a state a running daemon can be in. A record
// carrying it is an invalid record.
func TestRuntimeRecordsMayNotCarryTheUnknownState(t *testing.T) {
	record := func(state personalScriptState) health.DaemonRuntimeScript {
		return health.DaemonRuntimeScript{Path: "/pre", State: string(state)}
	}
	if _, ok := personalScriptDiagnosticFromRuntime("PERSONAL_SCRIPT_PRE_RUN", 0, record(personalScriptUnknown)); ok {
		t.Fatal("a runtime record claiming the unknown state was accepted")
	}
	for _, state := range []personalScriptState{
		personalScriptNotConfigured, personalScriptReady, personalScriptReadyWithWarning, personalScriptRefused,
	} {
		if _, ok := personalScriptDiagnosticFromRuntime("PERSONAL_SCRIPT_PRE_RUN", 0, record(state)); !ok {
			t.Fatalf("the published state %q is no longer accepted", state)
		}
	}
}
