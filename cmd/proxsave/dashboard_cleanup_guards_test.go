package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestGuardCheckDescription pins the CHECK wording (no "dry run" text): nothing-to-unlock
// vs a pluralized list of what is locking the storage.
func TestGuardCheckDescription(t *testing.T) {
	cases := []struct {
		name string
		r    orchestrator.GuardCleanupReport
		want string
	}{
		{"clean", orchestrator.GuardCleanupReport{}, "No restore mount guards are present. Nothing to unlock."},
		{"bind only", orchestrator.GuardCleanupReport{BindGuards: 2},
			"Found 2 bind mount guards locking the storage. Apply removes them to unlock it."},
		{"immutable only", orchestrator.GuardCleanupReport{ImmutableGuards: 1},
			"Found 1 immutable flag locking the storage. Apply removes them to unlock it."},
		{"both", orchestrator.GuardCleanupReport{BindGuards: 1, ImmutableGuards: 2},
			"Found 1 bind mount guard and 2 immutable flags locking the storage. Apply removes them to unlock it."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeGuardCheck(tc.r); got != tc.want {
				t.Fatalf("describeGuardCheck =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestClassifyGuardApply: a fully-removed run is Ok/DONE; anything left behind (including
// the -1 "unknown" fail-closed sentinel) is Warn/PENDING.
func TestClassifyGuardApply(t *testing.T) {
	cases := []struct {
		name    string
		r       orchestrator.GuardCleanupReport
		wantLvl orchestrator.HealthcheckSetupLevel
		wantKey string
	}{
		{"done", orchestrator.GuardCleanupReport{DirRemoved: true}, orchestrator.HealthcheckSetupLevelOk, "DONE"},
		{"bind remaining", orchestrator.GuardCleanupReport{GuardsRemaining: 1}, orchestrator.HealthcheckSetupLevelWarn, "PENDING"},
		{"unknown", orchestrator.GuardCleanupReport{GuardsRemaining: -1}, orchestrator.HealthcheckSetupLevelWarn, "PENDING"},
		{"immutable pending", orchestrator.GuardCleanupReport{ImmutablePending: 1}, orchestrator.HealthcheckSetupLevelWarn, "PENDING"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lvl, key := classifyGuardApply(tc.r)
			if lvl != tc.wantLvl || key != tc.wantKey {
				t.Fatalf("got (%v, %q), want (%v, %q)", lvl, key, tc.wantLvl, tc.wantKey)
			}
		})
	}
}

// stubGuardReport swaps the report seam and records the dryRun of each call. check() is
// used for the dry-run CHECK; apply() for the real run.
func stubGuardReport(t *testing.T, check, apply orchestrator.GuardCleanupReport) *[]bool {
	t.Helper()
	orig := cleanupGuardsReport
	t.Cleanup(func() { cleanupGuardsReport = orig })
	calls := &[]bool{}
	cleanupGuardsReport = func(_ context.Context, _ *logging.Logger, dryRun bool) (orchestrator.GuardCleanupReport, error) {
		*calls = append(*calls, dryRun)
		if dryRun {
			return check, nil
		}
		return apply, nil
	}
	return calls
}

// runCleanupGuardsDriver navigates to Cleanup guards (11 downs) and returns the driver so
// the test can drive the resulting screens, plus a channel with the dashboard result.
func runCleanupGuardsDriver(t *testing.T, args *cli.Args) (*newkeyUIDriver, <-chan dashboardResult) {
	t.Helper()
	installDashboardGates(t, true, true) // cron state -> Cleanup guards is the 12th selectable
	driver := installDashboardSessionSeam(t)
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down down down enter") // Cleanup guards (11 downs)
	return driver, res
}

func waitDashboardResolved(t *testing.T, res <-chan dashboardResult) {
	t.Helper()
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
}

// TestDashboardCleanupGuardsFoundApply: a CHECK that finds guards shows the Found screen;
// Apply runs the real cleanup and shows the outcome. The report seam is called twice
// (check then apply) and no flag is set.
func TestDashboardCleanupGuardsFoundApply(t *testing.T) {
	calls := stubGuardReport(t,
		orchestrator.GuardCleanupReport{GuardDirPresent: true, BindGuards: 1}, // check -> Found
		orchestrator.GuardCleanupReport{DirRemoved: true},                     // apply -> DONE
	)
	args := &cli.Args{}
	driver, resCh := runCleanupGuardsDriver(t, args)
	driver.waitScreen("Cleanup guards") // Found screen
	driver.keys("enter")                // Apply (primary)
	driver.waitScreen("Cleanup guards") // result screen
	driver.keys("esc")                  // Back to the menu
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitDashboardResolved(t, resCh)

	if len(*calls) != 2 || (*calls)[0] != true || (*calls)[1] != false {
		t.Fatalf("expected check then apply, got %v", *calls)
	}
	if args.CleanupGuards {
		t.Fatal("Cleanup guards is an in-session action; it must not set the --cleanup-guards flag")
	}
}

// TestDashboardCleanupGuardsCleanNoApply: a CHECK with nothing to unlock shows the Clean
// screen (no Apply); Back returns to the menu WITHOUT a real run.
func TestDashboardCleanupGuardsCleanNoApply(t *testing.T) {
	calls := stubGuardReport(t,
		orchestrator.GuardCleanupReport{}, // check -> Clean (no guards)
		orchestrator.GuardCleanupReport{},
	)
	driver, resCh := runCleanupGuardsDriver(t, &cli.Args{})
	driver.waitScreen("Cleanup guards") // Clean screen
	driver.keys("down enter")           // Back (secondary)
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitDashboardResolved(t, resCh)

	if len(*calls) != 1 || (*calls)[0] != true {
		t.Fatalf("Clean must run the check only (no apply), got %v", *calls)
	}
}

// TestDashboardCleanupGuardsCleanRecheck: on the Clean screen the primary action is Check
// (re-scan), which re-runs the check and never applies.
func TestDashboardCleanupGuardsCleanRecheck(t *testing.T) {
	calls := stubGuardReport(t,
		orchestrator.GuardCleanupReport{}, // check -> Clean (both times)
		orchestrator.GuardCleanupReport{},
	)
	driver, resCh := runCleanupGuardsDriver(t, &cli.Args{})
	driver.waitScreen("Cleanup guards") // Clean screen (1)
	driver.keys("enter")                // Check (primary) -> re-scan
	driver.waitScreen("Cleanup guards") // Clean screen (2)
	driver.keys("down enter")           // Back
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitDashboardResolved(t, resCh)

	if len(*calls) != 2 || (*calls)[0] != true || (*calls)[1] != true {
		t.Fatalf("re-check must run two checks and no apply, got %v", *calls)
	}
}

// TestDashboardCleanupGuardsFoundBack: Back on the Found screen returns to the menu
// WITHOUT the real run.
func TestDashboardCleanupGuardsFoundBack(t *testing.T) {
	calls := stubGuardReport(t,
		orchestrator.GuardCleanupReport{GuardDirPresent: true, ImmutableGuards: 2}, // check -> Found
		orchestrator.GuardCleanupReport{},
	)
	driver, resCh := runCleanupGuardsDriver(t, &cli.Args{})
	driver.waitScreen("Cleanup guards") // Found screen
	driver.keys("down enter")           // Back (secondary)
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitDashboardResolved(t, resCh)

	if len(*calls) != 1 || (*calls)[0] != true {
		t.Fatalf("Back must run the check only (no apply), got %v", *calls)
	}
}
