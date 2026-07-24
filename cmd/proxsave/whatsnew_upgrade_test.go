package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// isErrNilCheck reports whether e is `<name> == nil`.
func isErrNilCheck(e ast.Expr, name string) bool {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.EQL {
		return false
	}
	x, ok1 := be.X.(*ast.Ident)
	y, ok2 := be.Y.(*ast.Ident)
	return ok1 && ok2 && x.Name == name && y.Name == "nil"
}

// ifGuardsUpgradeSuccessAndCallsWhatsnew reports whether ifs is
// `if upgradeErr == nil && cfgUpgradeErr == nil { ... runWhatsnewAfterUpgrade(...) ... }`:
// Screen 0 opens only after a FULLY successful upgrade (binary AND configuration), and its
// body directly calls the Screen 0 re-invocation helper. Gating on the binary result alone
// would open a celebratory notes screen even when the config upgrade failed (footer shows
// "Configuration: ERROR", nonzero exit).
func ifGuardsUpgradeSuccessAndCallsWhatsnew(ifs *ast.IfStmt) bool {
	land, ok := ifs.Cond.(*ast.BinaryExpr)
	if !ok || land.Op != token.LAND {
		return false
	}
	// Both binary-install and config-upgrade success must be required, in either order.
	if !((isErrNilCheck(land.X, "upgradeErr") && isErrNilCheck(land.Y, "cfgUpgradeErr")) ||
		(isErrNilCheck(land.X, "cfgUpgradeErr") && isErrNilCheck(land.Y, "upgradeErr"))) {
		return false
	}
	if ifs.Body == nil {
		return false
	}
	for _, s := range ifs.Body.List {
		es, ok := s.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "runWhatsnewAfterUpgrade" {
			return true
		}
	}
	return false
}

// TestUpgradeShowsWhatsnewAfterFooter is a STRUCTURAL (AST) guard for the core requirement:
// Screen 0 (what's new) must open at the END of every FULLY successful upgrade. It parses
// upgradeFinalizePhase and pins that
// `if upgradeErr == nil && cfgUpgradeErr == nil { runWhatsnewAfterUpgrade(...) }`
// exists AND appears AFTER the printUpgradeFooter call. Parsing the AST -- not scanning text
// -- catches a commented-out call, a call moved out of the success gate (which would fire
// Screen 0 even on a failed upgrade), a call relocated before the footer, and outright
// deletion, all of which break the requirement. upgradeFinalizePhase itself cannot be
// unit-invoked cheaply (it does real config-upgrade, symlink, permission, and daemon work).
func TestUpgradeShowsWhatsnewAfterFooter(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "upgrade.go", nil, 0)
	if err != nil {
		t.Fatalf("parse upgrade.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "upgradeFinalizePhase" {
			fn = fd
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("upgradeFinalizePhase not found in upgrade.go")
	}

	directCallName := func(s ast.Stmt) string {
		es, ok := s.(*ast.ExprStmt)
		if !ok {
			return ""
		}
		call, ok := es.X.(*ast.CallExpr)
		if !ok {
			return ""
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			return id.Name
		}
		return ""
	}

	idxFooter, idxHook := -1, -1
	for i, s := range fn.Body.List {
		if directCallName(s) == "printUpgradeFooter" {
			idxFooter = i
		}
		if ifs, ok := s.(*ast.IfStmt); ok && ifGuardsUpgradeSuccessAndCallsWhatsnew(ifs) {
			idxHook = i
		}
	}

	if idxFooter < 0 {
		t.Fatal("printUpgradeFooter is not a direct statement in upgradeFinalizePhase")
	}
	if idxHook < 0 {
		t.Fatal("Screen 0 hook missing: upgradeFinalizePhase must contain " +
			"`if upgradeErr == nil && cfgUpgradeErr == nil { runWhatsnewAfterUpgrade(...) }` so Screen 0 opens only at the end of a fully successful upgrade")
	}
	if idxHook <= idxFooter {
		t.Fatal("the Screen 0 hook (runWhatsnewAfterUpgrade) must come AFTER printUpgradeFooter")
	}
}

// TestShouldRunWhatsnewAfterUpgrade pins the post-upgrade gate: Screen 0 opens only on an
// interactive terminal AND when the operator did not request auto-yes. Auto-yes (`--upgrade
// y`) means non-interactive intent even under a pty (ssh -tt, Ansible, script -c), so it must
// skip the screen; otherwise a successful automated upgrade would stall until the Screen 0
// timeout waiting for a keypress nobody sends.
func TestShouldRunWhatsnewAfterUpgrade(t *testing.T) {
	orig := whatsnewAfterUpgradeInteractive
	t.Cleanup(func() { whatsnewAfterUpgradeInteractive = orig })

	cases := []struct {
		name        string
		args        *cli.Args
		interactive bool
		want        bool
	}{
		{"interactive human upgrade shows", &cli.Args{}, true, true},
		{"auto-yes under a pty skips (no stall)", &cli.Args{UpgradeAutoYes: true}, true, false},
		{"non-interactive skips", &cli.Args{}, false, false},
		{"auto-yes and non-interactive skips", &cli.Args{UpgradeAutoYes: true}, false, false},
		{"nil args skips", nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			whatsnewAfterUpgradeInteractive = func() bool { return tc.interactive }
			if got := shouldRunWhatsnewAfterUpgrade(tc.args); got != tc.want {
				t.Fatalf("shouldRunWhatsnewAfterUpgrade = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShowWhatsnewScreenSkipsWhenNonInteractive: a non-interactive re-invocation
// (--show-whatsnew piped or without a TTY) must return WITHOUT building a session or calling
// Decide, so an automated upgrade never blocks on a screen nobody can dismiss.
func TestShowWhatsnewScreenSkipsWhenNonInteractive(t *testing.T) {
	origInter := dashboardIsInteractive
	origDecide := whatsnewDecide
	dashboardIsInteractive = func() bool { return false }
	called := false
	whatsnewDecide = func(baseDir, current string) (bool, string, error) { called = true; return false, "", nil }
	t.Cleanup(func() {
		dashboardIsInteractive = origInter
		whatsnewDecide = origDecide
	})

	showWhatsnewScreen(context.Background(), &cli.Args{}, "0.30.0")
	if called {
		t.Fatal("non-interactive showWhatsnewScreen must not build a session or call Decide")
	}
}

// TestShowWhatsnewScreenSkipsUnderDryRun: a --dry-run invocation must not reach
// maybeShowWhatsnew (whose self-heal / continue write mutates the seen-flag), so Screen 0 is
// skipped even on an interactive terminal. Guards the FS-mutation-under-dry-run hole opened by
// making showWhatsnewScreen a non-bare caller of maybeShowWhatsnew.
func TestShowWhatsnewScreenSkipsUnderDryRun(t *testing.T) {
	origInter := dashboardIsInteractive
	origDecide := whatsnewDecide
	dashboardIsInteractive = func() bool { return true }
	called := false
	whatsnewDecide = func(baseDir, current string) (bool, string, error) { called = true; return false, "", nil }
	t.Cleanup(func() {
		dashboardIsInteractive = origInter
		whatsnewDecide = origDecide
	})

	showWhatsnewScreen(context.Background(), &cli.Args{DryRun: true}, "0.30.0")
	if called {
		t.Fatal("--dry-run showWhatsnewScreen must not call Decide or write the seen-flag")
	}
}

// TestShowWhatsnewScreenDelegatesWhenInteractive: on an interactive terminal showWhatsnewScreen
// builds a session and delegates to maybeShowWhatsnew, keyed on the RUNNING binary's version
// (the value passed in), so it renders from the binary's own compiled-in notes registry.
func TestShowWhatsnewScreenDelegatesWhenInteractive(t *testing.T) {
	origInter := dashboardIsInteractive
	origSess := testDashboardSession
	origDecide := whatsnewDecide
	dashboardIsInteractive = func() bool { return true }
	testDashboardSession = func(ctx context.Context) *shell.Session {
		return shell.StartForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	}
	var gotVersion string
	called := false
	whatsnewDecide = func(baseDir, current string) (bool, string, error) {
		called = true
		gotVersion = current
		return false, "", nil // no-show: maybeShowWhatsnew returns before touching the session
	}
	t.Cleanup(func() {
		dashboardIsInteractive = origInter
		testDashboardSession = origSess
		whatsnewDecide = origDecide
		releaseDashboardLeftovers()
	})

	showWhatsnewScreen(context.Background(), &cli.Args{}, "0.30.0-beta6")
	if !called {
		t.Fatal("interactive showWhatsnewScreen must delegate to maybeShowWhatsnew (Decide never called)")
	}
	if gotVersion != "0.30.0-beta6" {
		t.Fatalf("Decide called with version %q, want the running binary version 0.30.0-beta6", gotVersion)
	}
}

// TestRunShowWhatsnewMode pins the --show-whatsnew mode gate: it falls through (handled=false)
// unless args.ShowWhatsnew is set, and when set it runs Screen 0 (delegates to Decide) and
// exits success. Screen 0 is best-effort, so the mode never returns a non-success code.
func TestRunShowWhatsnewMode(t *testing.T) {
	origInter := dashboardIsInteractive
	origSess := testDashboardSession
	origDecide := whatsnewDecide
	dashboardIsInteractive = func() bool { return true }
	testDashboardSession = func(ctx context.Context) *shell.Session {
		return shell.StartForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	}
	decideCalls := 0
	whatsnewDecide = func(baseDir, current string) (bool, string, error) { decideCalls++; return false, "", nil }
	t.Cleanup(func() {
		dashboardIsInteractive = origInter
		testDashboardSession = origSess
		whatsnewDecide = origDecide
		releaseDashboardLeftovers()
	})

	if code, handled := runShowWhatsnewMode(context.Background(), &cli.Args{ShowWhatsnew: false}, nil, "0.30.0"); handled {
		t.Fatalf("mode must fall through when ShowWhatsnew=false (handled=%v, code=%d)", handled, code)
	}
	if decideCalls != 0 {
		t.Fatalf("Decide must not run when ShowWhatsnew=false (calls=%d)", decideCalls)
	}

	code, handled := runShowWhatsnewMode(context.Background(), &cli.Args{ShowWhatsnew: true}, nil, "0.30.0")
	if !handled {
		t.Fatal("mode must handle the run when ShowWhatsnew=true")
	}
	if code != types.ExitSuccess.Int() {
		t.Fatalf("mode exit = %d, want success", code)
	}
	if decideCalls != 1 {
		t.Fatalf("mode must run Screen 0 exactly once when ShowWhatsnew=true (Decide calls=%d)", decideCalls)
	}
}
