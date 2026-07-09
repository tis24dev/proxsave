package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestConfigPlanDescription pins the CHECK wording (same "(s)" style as the CLI).
func TestConfigPlanDescription(t *testing.T) {
	cases := []struct {
		name string
		r    *config.UpgradeResult
		want string
	}{
		{"missing only", &config.UpgradeResult{MissingKeys: []string{"A", "B"}},
			"Found 2 missing key(s) to add. Apply updates the config file (a backup is saved first)."},
		{"missing + custom + case", &config.UpgradeResult{MissingKeys: []string{"A"}, ExtraKeys: []string{"X"}, CaseConflictKeys: []string{"y"}},
			"Found 1 missing key(s) to add, 1 custom key(s) to keep, 1 case-only key(s) to keep. Apply updates the config file (a backup is saved first)."},
		{"rewrite only", &config.UpgradeResult{},
			"Found the file would be rewritten from the template. Apply updates the config file (a backup is saved first)."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeConfigPlan(tc.r); got != tc.want {
				t.Fatalf("describeConfigPlan =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestConfigApplyDescription pins the real-run wording, including the backup line.
func TestConfigApplyDescription(t *testing.T) {
	got := describeConfigApply(&config.UpgradeResult{Changed: true, MissingKeys: []string{"A", "B"}, PreservedValues: 3, BackupPath: "/tmp/c.backup"})
	want := "Updated the configuration: added 2 key(s), preserved 3 value(s).\nBackup saved to /tmp/c.backup."
	if got != want {
		t.Fatalf("describeConfigApply =\n%q\nwant\n%q", got, want)
	}
	if s := describeConfigApply(&config.UpgradeResult{Changed: false}); s != "The configuration already has every key from the template." {
		t.Fatalf("no-change apply = %q", s)
	}
}

// stubUpdateConfig swaps the plan/apply seams and records each call.
func stubUpdateConfig(t *testing.T, plan, apply *config.UpgradeResult) *[]string {
	t.Helper()
	origPlan, origApply := updateConfigPlan, updateConfigApply
	t.Cleanup(func() { updateConfigPlan = origPlan; updateConfigApply = origApply })
	calls := &[]string{}
	updateConfigPlan = func(string) (*config.UpgradeResult, error) {
		*calls = append(*calls, "plan")
		return plan, nil
	}
	updateConfigApply = func(string, string) (*config.UpgradeResult, error) {
		*calls = append(*calls, "apply")
		return apply, nil
	}
	return calls
}

// runUpdateConfigDriver navigates to Update config (7 downs) and returns the driver plus
// the dashboard result channel.
func runUpdateConfigDriver(t *testing.T, args *cli.Args) (*newkeyUIDriver, chan bool) {
	t.Helper()
	installDashboardGates(t, true, true) // cron state -> Update config is the 8th selectable
	driver := installDashboardSessionSeam(t)
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down enter") // Update config (7 downs)
	return driver, resCh
}

func waitUpdateConfigResolved(t *testing.T, resCh chan bool) {
	t.Helper()
	select {
	case <-resCh:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
}

// TestDashboardUpdateConfigAvailableApply: a CHECK that finds new keys shows the Update
// available screen; Apply runs the real merge. plan then apply are called; no flag set.
func TestDashboardUpdateConfigAvailableApply(t *testing.T) {
	calls := stubUpdateConfig(t,
		&config.UpgradeResult{Changed: true, MissingKeys: []string{"A", "B"}},                          // plan -> Update available
		&config.UpgradeResult{Changed: true, MissingKeys: []string{"A", "B"}, BackupPath: "/x.backup"}, // apply -> Updated
	)
	args := &cli.Args{}
	driver, resCh := runUpdateConfigDriver(t, args)
	driver.waitScreen("Update config") // Update available screen
	driver.keys("enter")               // Apply (primary)
	driver.waitScreen("Update config") // result screen
	driver.keys("esc")                 // Back to the menu
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitUpdateConfigResolved(t, resCh)

	if len(*calls) != 2 || (*calls)[0] != "plan" || (*calls)[1] != "apply" {
		t.Fatalf("expected plan then apply, got %v", *calls)
	}
	if args.UpgradeConfig {
		t.Fatal("Update config is an in-session action; it must not set the --upgrade-config flag")
	}
}

// TestDashboardUpdateConfigUpToDateBack: nothing to add shows the Up to date screen (no
// Apply); Back returns to the menu WITHOUT a real run.
func TestDashboardUpdateConfigUpToDateBack(t *testing.T) {
	calls := stubUpdateConfig(t, &config.UpgradeResult{Changed: false}, &config.UpgradeResult{})
	driver, resCh := runUpdateConfigDriver(t, &cli.Args{})
	driver.waitScreen("Update config") // Up to date screen
	driver.keys("down enter")          // Back (secondary)
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitUpdateConfigResolved(t, resCh)

	if len(*calls) != 1 || (*calls)[0] != "plan" {
		t.Fatalf("Up to date must run the check only, got %v", *calls)
	}
}

// TestDashboardUpdateConfigUpToDateRecheck: the primary action on the Up to date screen is
// Check (re-run), which re-plans and never applies.
func TestDashboardUpdateConfigUpToDateRecheck(t *testing.T) {
	calls := stubUpdateConfig(t, &config.UpgradeResult{Changed: false}, &config.UpgradeResult{})
	driver, resCh := runUpdateConfigDriver(t, &cli.Args{})
	driver.waitScreen("Update config") // Up to date (1)
	driver.keys("enter")               // Check (primary) -> re-plan
	driver.waitScreen("Update config") // Up to date (2)
	driver.keys("down enter")          // Back
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitUpdateConfigResolved(t, resCh)

	if len(*calls) != 2 || (*calls)[0] != "plan" || (*calls)[1] != "plan" {
		t.Fatalf("re-check must run two plans and no apply, got %v", *calls)
	}
}

// TestDashboardUpdateConfigAvailableCancel: Cancel on the Update available screen returns
// to the menu WITHOUT the real run.
func TestDashboardUpdateConfigAvailableCancel(t *testing.T) {
	calls := stubUpdateConfig(t, &config.UpgradeResult{Changed: true, MissingKeys: []string{"A"}}, &config.UpgradeResult{})
	driver, resCh := runUpdateConfigDriver(t, &cli.Args{})
	driver.waitScreen("Update config") // Update available screen
	driver.keys("down enter")          // Cancel (secondary)
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	waitUpdateConfigResolved(t, resCh)

	if len(*calls) != 1 || (*calls)[0] != "plan" {
		t.Fatalf("Cancel must run the check only, got %v", *calls)
	}
}
