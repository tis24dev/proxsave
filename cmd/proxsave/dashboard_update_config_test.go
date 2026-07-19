package main

import (
	"fmt"
	"strings"
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
			"Found 2 missing key(s) to add.\nKeys to add:\n  A\n  B\nApply updates the config file (a backup is saved first)."},
		{"missing + custom + case", &config.UpgradeResult{MissingKeys: []string{"A"}, ExtraKeys: []string{"X"}, CaseConflictKeys: []string{"y"}},
			"Found 1 missing key(s) to add, 1 custom key(s) to keep, 1 case-only key(s) to keep.\nKeys to add:\n  A\nApply updates the config file (a backup is saved first)."},
		{"rewrite only", &config.UpgradeResult{},
			"Found the file would be rewritten from the template.\nApply updates the config file (a backup is saved first)."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeConfigPlan(tc.r); got != tc.want {
				t.Fatalf("describeConfigPlan =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestConfigPlanDescriptionCaps: a large set of missing keys lists up to the cap and
// summarizes the rest as "… and N more", so the check screen can never overflow.
func TestConfigPlanDescriptionCaps(t *testing.T) {
	keys := make([]string, 0, maxConfigKeysShown+2)
	for i := 1; i <= maxConfigKeysShown+2; i++ {
		keys = append(keys, fmt.Sprintf("K%d", i))
	}
	got := describeConfigPlan(&config.UpgradeResult{MissingKeys: keys})
	if !strings.Contains(got, fmt.Sprintf("\n  K%d", maxConfigKeysShown)) {
		t.Fatalf("should list up to the cap: %q", got)
	}
	if strings.Contains(got, fmt.Sprintf("K%d", maxConfigKeysShown+1)) {
		t.Fatalf("must not list past the cap: %q", got)
	}
	if !strings.Contains(got, "… and 2 more") {
		t.Fatalf("should summarize the overflow: %q", got)
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

// runUpdateConfigDriver reaches the Update config flow the way the dashboard now exposes
// it: it lives UNDER "Upgrade" as the "Check config" button, not as a top-level menu row.
// So it navigates to Upgrade (5 downs) then selects Check config (2nd item of the Upgrade
// screen), and returns the driver positioned on the Update config check screen.
func runUpdateConfigDriver(t *testing.T, args *cli.Args) (*newkeyUIDriver, <-chan dashboardResult) {
	t.Helper()
	installDashboardGates(t, true, true) // cron state -> Upgrade is the 6th selectable
	driver := installDashboardSessionSeam(t)
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down enter") // Upgrade (5 downs) -> Upgrade chooser
	driver.waitScreen("Upgrade")
	driver.keys("down enter") // Check config (2nd item) -> Update config flow
	return driver, res
}

func waitUpdateConfigResolved(t *testing.T, res <-chan dashboardResult) {
	t.Helper()
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
}

// escOutOfUpgrade escapes the Update config flow's return point (the Upgrade screen) back
// to the menu and exits, so every driver test ends the same way.
func escOutOfUpgrade(t *testing.T, driver *newkeyUIDriver, res <-chan dashboardResult) {
	t.Helper()
	driver.waitScreen("Upgrade") // Update config returned to the Upgrade screen
	driver.keys("esc")           // Back from Upgrade -> menu
	driver.waitScreen("Dashboard")
	driver.keys("esc") // exit
	waitUpdateConfigResolved(t, res)
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
	driver.waitScreen("Upgrade config") // Update available screen
	driver.keys("enter")                // Apply (primary)
	driver.waitScreen("Upgrade config") // result screen
	driver.keys("esc")                  // Back from result -> Upgrade screen
	escOutOfUpgrade(t, driver, resCh)

	if len(*calls) != 2 || (*calls)[0] != "plan" || (*calls)[1] != "apply" {
		t.Fatalf("expected plan then apply, got %v", *calls)
	}
	if args.UpgradeConfig {
		t.Fatal("Update config is an in-session action; it must not set the --upgrade-config flag")
	}
}

// TestDashboardUpdateConfigUpToDateBack: nothing to add shows the Up to date screen (no
// Apply); Back returns WITHOUT a real run.
func TestDashboardUpdateConfigUpToDateBack(t *testing.T) {
	calls := stubUpdateConfig(t, &config.UpgradeResult{Changed: false}, &config.UpgradeResult{})
	driver, resCh := runUpdateConfigDriver(t, &cli.Args{})
	driver.waitScreen("Upgrade config") // Up to date screen
	driver.keys("down enter")           // Back (secondary) -> Upgrade screen
	escOutOfUpgrade(t, driver, resCh)

	if len(*calls) != 1 || (*calls)[0] != "plan" {
		t.Fatalf("Up to date must run the check only, got %v", *calls)
	}
}

// TestDashboardUpdateConfigUpToDateRecheck: the primary action on the Up to date screen is
// Check (re-run), which re-plans and never applies.
func TestDashboardUpdateConfigUpToDateRecheck(t *testing.T) {
	calls := stubUpdateConfig(t, &config.UpgradeResult{Changed: false}, &config.UpgradeResult{})
	driver, resCh := runUpdateConfigDriver(t, &cli.Args{})
	driver.waitScreen("Upgrade config") // Up to date (1)
	driver.keys("enter")                // Check (primary) -> re-plan
	driver.waitScreen("Upgrade config") // Up to date (2)
	driver.keys("down enter")           // Back -> Upgrade screen
	escOutOfUpgrade(t, driver, resCh)

	if len(*calls) != 2 || (*calls)[0] != "plan" || (*calls)[1] != "plan" {
		t.Fatalf("re-check must run two plans and no apply, got %v", *calls)
	}
}

// TestDashboardUpdateConfigAvailableBack: Back on the Update available screen returns
// WITHOUT the real run.
func TestDashboardUpdateConfigAvailableBack(t *testing.T) {
	calls := stubUpdateConfig(t, &config.UpgradeResult{Changed: true, MissingKeys: []string{"A"}}, &config.UpgradeResult{})
	driver, resCh := runUpdateConfigDriver(t, &cli.Args{})
	driver.waitScreen("Upgrade config") // Update available screen
	driver.keys("down enter")           // Back (secondary) -> Upgrade screen
	escOutOfUpgrade(t, driver, resCh)

	if len(*calls) != 1 || (*calls)[0] != "plan" {
		t.Fatalf("Back must run the check only, got %v", *calls)
	}
}
