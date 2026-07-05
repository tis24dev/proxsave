package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// installDashboardGates fixes the two gate seams for a test. It also pins the
// daemon menu state to DaemonStateOnCron (config load -> cron) so the menu layout
// is deterministic (offers "Install daemon" + "Daemon status").
func installDashboardGates(t *testing.T, bare, interactive bool) {
	t.Helper()
	origBare := dashboardIsBareInvocation
	origInteractive := dashboardIsInteractive
	origDaemonCfg := daemonStatusLoadConfig
	dashboardIsBareInvocation = func() bool { return bare }
	dashboardIsInteractive = func() bool { return interactive }
	daemonStatusLoadConfig = func(configPath, baseDir string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "cron"}, nil
	}
	t.Cleanup(func() {
		dashboardIsBareInvocation = origBare
		dashboardIsInteractive = origInteractive
		daemonStatusLoadConfig = origDaemonCfg
	})
}

// TestDashboardGateNonInteractiveNeverIntercepts is the cron-safety
// contract: without an interactive terminal (or with any flag present) the
// dashboard must never appear and the args must stay untouched, so the
// runtime path is byte-identical to today.
func TestDashboardGateNonInteractiveNeverIntercepts(t *testing.T) {
	cases := []struct {
		name        string
		bare        bool
		interactive bool
	}{
		{"cron (no tty)", true, false},
		{"flags present", false, true},
		{"flags present and no tty", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installDashboardGates(t, tc.bare, tc.interactive)
			args := &cli.Args{}
			code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
			if handled {
				t.Fatalf("dashboard must not intercept (code=%d)", code)
			}
			if args.Restore || args.Decrypt || args.ForceNewKey || args.Install || args.Backup {
				t.Fatalf("args mutated: %+v", args)
			}
		})
	}
}

func installDashboardSessionSeam(t *testing.T) *newkeyUIDriver {
	t.Helper()
	d := installNewkeySessionSeam(t)
	orig := testDashboardSession
	testDashboardSession = func(ctx context.Context) *shell.Session {
		return newAgeSetupSession(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	}
	t.Cleanup(func() {
		testDashboardSession = orig
		releaseDashboardLeftovers()
	})
	return d
}

func runDashboardWith(t *testing.T, keys string) (*cli.Args, int, bool) {
	t.Helper()
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	driver.keys(keys)
	select {
	case res := <-resCh:
		return args, res.code, res.handled
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
		return nil, 0, false
	}
}

func TestDashboardActions(t *testing.T) {
	cases := []struct {
		name    string
		keys    string
		handled bool
		check   func(t *testing.T, args *cli.Args)
	}{
		{"backup falls through", "enter", false, func(t *testing.T, args *cli.Args) {
			if args.Restore || args.Decrypt || args.ForceNewKey || args.Install {
				t.Fatalf("backup must not set mode flags: %+v", args)
			}
		}},
		{"restore", "down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.Restore {
				t.Fatal("restore flag not set")
			}
		}},
		{"decrypt", "down down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.Decrypt {
				t.Fatal("decrypt flag not set")
			}
		}},
		{"newkey", "down down down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.ForceNewKey {
				t.Fatal("newkey flag not set")
			}
		}},
		{"reconfigure", "down down down down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.Install {
				t.Fatal("install flag not set")
			}
		}},
		// Diagnostics group (telegram/hc/post-install) are rows 6-8, then the Daemon
		// group: Install daemon (row 9, 8 downs) + Daemon status (row 10). Install
		// daemon sets the flag and hands off like backup (handled=false).
		{"daemon install", "down down down down down down down down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.DaemonSetup || args.DaemonRemove {
				t.Fatalf("Install daemon must set DaemonSetup only: %+v", args)
			}
		}},
		// Exit is the last selectable (11th): 10 downs, skipping both separators.
		{"exit row", "down down down down down down down down down down enter", true, nil},
		{"esc exits", "esc", true, nil},
		{"ctrl+c exits", "ctrl+c", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, code, handled := runDashboardWith(t, tc.keys)
			if handled != tc.handled {
				t.Fatalf("handled = %v, want %v (code=%d)", handled, tc.handled, code)
			}
			if handled && code != types.ExitSuccess.Int() {
				t.Fatalf("exit path must be success, got %d", code)
			}
			if tc.check != nil {
				tc.check(t, args)
			}
		})
	}
}

// TestDashboardDaemonStatusLoopsBack: Daemon status shows a read-only notice in
// the live session and returns to the menu, setting no flag.
func TestDashboardDaemonStatusLoopsBack(t *testing.T) {
	installDashboardGates(t, true, true) // stubs cron -> Install daemon + Daemon status
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Daemon status (9 downs)
	driver.waitScreen("Daemon status")                                // the notice
	driver.keys("enter")                                              // dismiss
	driver.waitScreen("Dashboard")                                    // back at the menu
	driver.keys("esc")                                                // exit
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from menu must exit handled")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}
	if args.DaemonSetup || args.DaemonRemove {
		t.Fatalf("Daemon status must set no flag: %+v", args)
	}
}

// TestDashboardDaemonRemoveWhenActive: with the daemon active the menu offers
// "Disable daemon", which sets DaemonRemove and hands off (handled=false).
func TestDashboardDaemonRemoveWhenActive(t *testing.T) {
	installDashboardGates(t, true, true)
	orig := daemonStatusLoadConfig
	daemonStatusLoadConfig = func(configPath, baseDir string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	t.Cleanup(func() { daemonStatusLoadConfig = orig })
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	// Active state: Daemon group = "Disable daemon" (row 9, 8 downs) + "Daemon status".
	driver.keys("down down down down down down down down enter") // Disable daemon
	select {
	case res := <-resCh:
		if res.handled {
			t.Fatalf("Disable daemon must hand off (handled=false): %+v", res)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}
	if !args.DaemonRemove || args.DaemonSetup {
		t.Fatalf("Disable daemon must set DaemonRemove only: %+v", args)
	}
}

// stubDashboardDiagnostics replaces the three diagnostics screen seams so the
// loop can be driven without an on-disk config or the real Charm screens.
func stubDashboardDiagnostics(t *testing.T, telegramShown, hcShown bool, tele, hc, audit *int) {
	t.Helper()
	origT, origH, origA := dashboardRunTelegramSetup, dashboardRunHealthcheckSetup, dashboardRunPostInstallAudit
	t.Cleanup(func() {
		dashboardRunTelegramSetup = origT
		dashboardRunHealthcheckSetup = origH
		dashboardRunPostInstallAudit = origA
	})
	dashboardRunTelegramSetup = func(ctx context.Context, s *shell.Session, baseDir, configPath string) (installer.TelegramSetupResult, error) {
		*tele++
		return installer.TelegramSetupResult{Shown: telegramShown}, nil
	}
	dashboardRunHealthcheckSetup = func(ctx context.Context, s *shell.Session, baseDir, configPath string) (installer.HealthcheckSetupResult, error) {
		*hc++
		return installer.HealthcheckSetupResult{Shown: hcShown}, nil
	}
	dashboardRunPostInstallAudit = func(ctx context.Context, s *shell.Session, execPath, configPath string) (installer.PostInstallAuditResult, error) {
		*audit++
		return installer.PostInstallAuditResult{}, nil
	}
}

// TestDashboardDiagnosticsLoopBackToMenu: each diagnostics item runs its screen
// in the live session and returns to the menu (never sets a mode flag, never
// ends the dashboard); only Exit/esc ends it.
func TestDashboardDiagnosticsLoopBackToMenu(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)
	var tele, hc, audit int
	stubDashboardDiagnostics(t, true, true, &tele, &hc, &audit)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()

	driver.waitScreen("Dashboard")
	driver.keys("down down down down down enter")      // Check Telegram (6th selectable)
	driver.waitScreen("Dashboard")                     // looped back after the screen
	driver.keys("down down down down down down enter") // Check healthchecks (7th)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down enter") // Post-install check (8th)
	driver.waitScreen("Dashboard")
	driver.keys("esc") // exit

	select {
	case res := <-resCh:
		if !res.handled || res.code != types.ExitSuccess.Int() {
			t.Fatalf("esc from menu must exit cleanly, got %+v", res)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}
	if tele != 1 || hc != 1 || audit != 1 {
		t.Fatalf("each diagnostic must run once: tele=%d hc=%d audit=%d", tele, hc, audit)
	}
	if args.Restore || args.Decrypt || args.ForceNewKey || args.Install || args.Backup {
		t.Fatalf("diagnostics must not set any mode flag: %+v", args)
	}
}

// TestDashboardDiagnosticNotConfiguredShowsNotice: when a setup screen is not
// eligible (Shown=false), a dismissible notice appears instead of a blank
// flicker, then the menu returns.
func TestDashboardDiagnosticNotConfiguredShowsNotice(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)
	var tele, hc, audit int
	stubDashboardDiagnostics(t, false, true, &tele, &hc, &audit) // telegram not configured

	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()

	driver.waitScreen("Dashboard")
	driver.keys("down down down down down enter") // Check Telegram (not configured)
	driver.waitScreen("Telegram not configured")  // the notice
	driver.keys("enter")                          // dismiss
	driver.waitScreen("Dashboard")                // back at the menu
	driver.keys("esc")                            // exit

	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from menu must exit handled")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}
	if tele != 1 {
		t.Fatalf("the telegram check must have run once, got %d", tele)
	}
}

// TestDashboardUIDeathIsExitNotBackup: a dying UI must never fall through to
// a surprise backup for a human sitting at the menu.
func TestDashboardUIDeathIsExitNotBackup(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	driver.cancel() // kill the UI program
	select {
	case res := <-resCh:
		if !res.handled || res.code != types.ExitSuccess.Int() {
			t.Fatalf("UI death must exit cleanly, got %+v", res)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve after UI death")
	}
}

// TestDashboardBareInvocationGateReal exercises the REAL bare-invocation
// check by swapping os.Args (a mutation like <=2 must fail here).
func TestDashboardBareInvocationGateReal(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	os.Args = []string{"proxsave"}
	if !dashboardBareInvocationCheck() {
		t.Fatal("bare invocation must be detected")
	}
	os.Args = []string{"proxsave", "--dry-run"}
	if dashboardBareInvocationCheck() {
		t.Fatal("any flag must make the invocation non-bare")
	}
	os.Args = []string{"proxsave", "--backup"}
	if dashboardBareInvocationCheck() {
		t.Fatal("--backup must bypass the dashboard")
	}
}

// TestDashboardInteractiveGateUnderTest: under go test there is no terminal,
// so the REAL interactive gate must be false (cron-safety default).
func TestDashboardInteractiveGateUnderTest(t *testing.T) {
	if isDashboardTerminalInteractive() {
		t.Fatal("gate must be false without a real terminal")
	}
}

// TestDashboardFlowActionHandsSessionOver: choosing a flow keeps the session
// alive (stashed for adoption) and adoption consumes it exactly once,
// restoring the console mute it installed.
func TestDashboardFlowActionHandsSessionOver(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down enter") // Restore
	var res outcome
	select {
	case res = <-resCh:
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}
	if res.handled || !args.Restore {
		t.Fatalf("restore dispatch broken: handled=%v args=%+v", res.handled, args)
	}
	if !dashboardHandoffPending() {
		t.Fatal("flow action must stash the session for adoption")
	}
	s := adoptDashboardSession(shell.Config{AppName: "ProxSave", Subtitle: "Restore Backup Workflow"})
	if s == nil {
		t.Fatal("adoption must return the stashed session")
	}
	if adoptDashboardSession(shell.Config{}) != nil {
		t.Fatal("adoption must consume the stash (second call nil)")
	}

	// The adopted session must be ALIVE: a real Ask must reach the screen
	// and resolve (this is the regression that shipped once: the dashboard
	// closed the session before stashing, so every flow Ask died with
	// ErrClosed and the workflow reported "aborted by user").
	type askOut struct {
		err error
	}
	askRes := make(chan askOut, 1)
	go func() {
		_, err := shell.Ask(context.Background(), s, components.NewNotice(components.NoticeInfo, "Adopted", "still alive"))
		askRes <- askOut{err}
	}()
	driver.waitScreen("Adopted")
	driver.keys("enter")
	select {
	case r := <-askRes:
		if r.err != nil {
			t.Fatalf("Ask on the adopted session must work, got %v", r.err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Ask on the adopted session did not resolve")
	}
	_ = s.Close()
}

// TestDashboardExitStillClosesSession: exit and backup do NOT stash.
func TestDashboardExitStillClosesSession(t *testing.T) {
	_, _, handled := runDashboardWith(t, "esc")
	if !handled {
		t.Fatal("esc must exit")
	}
	if dashboardHandoffPending() {
		t.Fatal("exit must not leave a stashed session")
	}
	args, _, handled := runDashboardWith(t, "enter") // Run backup now
	if handled || args.Restore || args.Install {
		t.Fatalf("backup dispatch broken: %+v", args)
	}
	if dashboardHandoffPending() {
		t.Fatal("backup must not stash the session (plain terminal run)")
	}
}
