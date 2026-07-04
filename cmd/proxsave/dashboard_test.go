package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// installDashboardGates fixes the two gate seams for a test.
func installDashboardGates(t *testing.T, bare, interactive bool) {
	t.Helper()
	origBare := dashboardIsBareInvocation
	origInteractive := dashboardIsInteractive
	dashboardIsBareInvocation = func() bool { return bare }
	dashboardIsInteractive = func() bool { return interactive }
	t.Cleanup(func() {
		dashboardIsBareInvocation = origBare
		dashboardIsInteractive = origInteractive
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
	case <-time.After(10 * time.Second):
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
		{"exit row", "down down down down down enter", true, nil},
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
	case <-time.After(10 * time.Second):
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
	args, _, handled := runDashboardWith(t, "down enter") // Restore
	if handled || !args.Restore {
		t.Fatalf("restore dispatch broken: handled=%v args=%+v", handled, args)
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
